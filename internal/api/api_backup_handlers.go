package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

func (a *API) serveBackups(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/admin/backups") {
		return false
	}
	if a.backups == nil {
		server.WriteError(w, http.StatusServiceUnavailable, "backup_unavailable", "backup management is unavailable")
		return true
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/v1/admin/backups/destinations/") && strings.HasSuffix(p, "/recovery-key") && r.Method == http.MethodGet:
		id := r.PathValue("id")
		if !validBackupDestinationID(id) {
			server.WriteError(w, http.StatusBadRequest, "invalid_destination", "backup destination id is invalid")
			return true
		}
		key, err := a.backups.ExportDestinationRecoveryKey(r.Context(), id)
		if err != nil {
			if errors.Is(err, ErrBackupDestinationNotFound) {
				server.WriteError(w, http.StatusNotFound, "destination_not_found", "backup destination was not found")
			} else {
				server.WriteError(w, http.StatusConflict, "recovery_key_unavailable", "backup recovery key is unavailable")
			}
		} else {
			writeJSON(w, http.StatusOK, map[string]string{"recoveryKey": key})
		}
		return true
	case p == "/api/v1/admin/backups" && r.Method == http.MethodGet:
		value, err := a.backups.Snapshot(r.Context())
		if err != nil {
			server.WriteError(w, http.StatusInternalServerError, "backup_unavailable", "backup status could not be loaded")
		} else {
			writeJSON(w, http.StatusOK, value)
		}
		return true
	case p == "/api/v1/admin/backups/schedule" && r.Method == http.MethodPut:
		var in BackupSchedule
		if !decodeRequiredJSON(w, r, &in, "weeklyEnabled", "monthlyEnabled", "timezone", "weeklyDay", "weeklyHour", "weeklyMinute", "monthlyDay", "monthlyHour", "monthlyMinute", "weeklyRetention", "monthlyRetention") {
			return true
		}
		if err := a.backups.UpdateSchedule(r.Context(), in); err != nil {
			if errors.Is(err, ErrBackupInvalidSchedule) {
				server.WriteError(w, http.StatusBadRequest, "invalid_schedule", "backup schedule is invalid")
			} else {
				server.WriteError(w, http.StatusInternalServerError, "schedule_save_failed", "backup schedule could not be saved")
			}
		} else {
			a.recordAdminActivity(r, "backup_schedule_updated", "backup", 0)
			writeJSON(w, http.StatusOK, map[string]any{"schedule": in})
		}
		return true
	case p == "/api/v1/admin/backups/destinations" && r.Method == http.MethodPost:
		var in BackupDestination
		if !decodeRequiredJSON(w, r, &in, "label", "kind", "reference", "enabled") {
			return true
		}
		created, err := a.backups.SaveDestination(r.Context(), in)
		if err != nil {
			writeBackupDestinationError(w, err)
		} else {
			a.recordAdminActivity(r, "backup_destination_created", "backup_destination", 0)
			writeJSON(w, http.StatusCreated, created)
		}
		return true
	case strings.HasPrefix(p, "/api/v1/admin/backups/destinations/") && r.Method == http.MethodPut:
		id := r.PathValue("id")
		if !validBackupDestinationID(id) {
			server.WriteError(w, http.StatusBadRequest, "invalid_destination", "backup destination id is invalid")
			return true
		}
		var in BackupDestination
		if !decodeRequiredJSON(w, r, &in, "label", "kind", "reference", "enabled") {
			return true
		}
		in.ID = id
		updated, err := a.backups.SaveDestination(r.Context(), in)
		if err != nil {
			if errors.Is(err, ErrBackupDestinationNotFound) {
				server.WriteError(w, http.StatusNotFound, "destination_not_found", "backup destination was not found")
			} else {
				writeBackupDestinationError(w, err)
			}
		} else {
			a.recordAdminActivity(r, "backup_destination_updated", "backup_destination", 0)
			writeJSON(w, http.StatusOK, updated)
		}
		return true
	case strings.HasPrefix(p, "/api/v1/admin/backups/destinations/") && r.Method == http.MethodDelete:
		id := r.PathValue("id")
		if !validBackupDestinationID(id) {
			server.WriteError(w, http.StatusBadRequest, "invalid_destination", "backup destination id is invalid")
			return true
		}
		if err := a.backups.DeleteDestination(r.Context(), id); err != nil {
			if errors.Is(err, ErrBackupDestinationNotFound) {
				server.WriteError(w, http.StatusNotFound, "destination_not_found", "backup destination was not found")
			} else {
				server.WriteError(w, http.StatusInternalServerError, "destination_delete_failed", "backup destination could not be removed")
			}
		} else {
			a.recordAdminActivity(r, "backup_destination_deleted", "backup_destination", 0)
			empty(w)
		}
		return true
	case p == "/api/v1/admin/backups/destinations/test" && r.Method == http.MethodPost:
		var in BackupDestination
		if !decodeJSON(w, r, &in) {
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := a.backups.TestDestination(ctx, in); err != nil {
			server.WriteError(w, http.StatusBadRequest, "destination_test_failed", "backup destination test failed")
		} else {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		}
		return true
	case p == "/api/v1/admin/backups/run" && r.Method == http.MethodPost:
		batch, err := a.backups.StartManual()
		if err != nil {
			if errors.Is(err, ErrBackupAlreadyRunning) {
				server.WriteError(w, http.StatusConflict, "backup_running", "a backup is already running")
			} else if errors.Is(err, ErrBackupNoDestinations) {
				server.WriteError(w, http.StatusBadRequest, "backup_destination_required", "enable at least one backup destination first")
			} else {
				server.WriteError(w, http.StatusInternalServerError, "backup_start_failed", "backup could not be started")
			}
		} else {
			a.recordAdminActivity(r, "backup_run_started", "backup", 0)
			writeJSON(w, http.StatusAccepted, map[string]string{"batchId": batch, "status": "accepted"})
		}
		return true
	default:
		return false
	}
}

func writeBackupDestinationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBackupInvalidDestination):
		server.WriteError(w, http.StatusBadRequest, "invalid_destination", "backup destination is invalid")
	case errors.Is(err, ErrBackupDestinationConflict):
		server.WriteError(w, http.StatusConflict, "destination_conflict", "backup destination conflicts with an existing destination or has backup history; add a new destination")
	default:
		server.WriteError(w, http.StatusInternalServerError, "destination_save_failed", "backup destination could not be saved")
	}
}

func validBackupDestinationID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
