package api_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/api"
	"github.com/atikansari-ghr/BookmarksSync/internal/backup"
)

type backupTestAdapter struct{ manager *backup.Manager }

func (b backupTestAdapter) Snapshot(ctx context.Context) (api.BackupSnapshot, error) {
	s, err := b.manager.Snapshot(ctx)
	if err != nil {
		return api.BackupSnapshot{}, err
	}
	destinations := make([]api.BackupDestination, 0, len(s.Destinations))
	for _, value := range s.Destinations {
		destinations = append(destinations, api.BackupDestination{ID: value.ID, Label: value.Label, Kind: string(value.Kind), Reference: value.Reference, Enabled: value.Enabled, Encrypted: value.Encrypted, RecoveryKeyAvailable: value.RecoveryKeyAvailable})
	}
	return api.BackupSnapshot{Schedule: testAPISchedule(s.Schedule), Destinations: destinations, Runs: []api.BackupRun{}, Running: s.Running}, nil
}
func (b backupTestAdapter) UpdateSchedule(ctx context.Context, s api.BackupSchedule) error {
	err := b.manager.UpdateSchedule(ctx, backup.Schedule{WeeklyEnabled: s.WeeklyEnabled, MonthlyEnabled: s.MonthlyEnabled, Timezone: s.Timezone, WeeklyDay: time.Weekday(s.WeeklyDay), WeeklyHour: s.WeeklyHour, WeeklyMinute: s.WeeklyMinute, MonthlyDay: s.MonthlyDay, MonthlyHour: s.MonthlyHour, MonthlyMinute: s.MonthlyMinute, WeeklyRetention: s.WeeklyRetention, MonthlyRetention: s.MonthlyRetention})
	if errors.Is(err, backup.ErrInvalidSchedule) {
		return api.ErrBackupInvalidSchedule
	}
	return err
}
func (b backupTestAdapter) SaveDestination(ctx context.Context, d api.BackupDestination) (api.BackupDestination, error) {
	saved, err := b.manager.SaveDestination(ctx, backup.Destination{ID: d.ID, Label: d.Label, Kind: backup.DestinationKind(d.Kind), Reference: d.Reference, Enabled: d.Enabled, Encrypted: d.Encrypted})
	if errors.Is(err, sql.ErrNoRows) {
		return api.BackupDestination{}, api.ErrBackupDestinationNotFound
	}
	if errors.Is(err, backup.ErrInvalidDestination) {
		return api.BackupDestination{}, api.ErrBackupInvalidDestination
	}
	if errors.Is(err, backup.ErrDestinationConflict) {
		return api.BackupDestination{}, api.ErrBackupDestinationConflict
	}
	return api.BackupDestination{ID: saved.ID, Label: saved.Label, Kind: string(saved.Kind), Reference: saved.Reference, Enabled: saved.Enabled, Encrypted: saved.Encrypted, RecoveryKeyAvailable: saved.RecoveryKeyAvailable}, err
}
func (b backupTestAdapter) DeleteDestination(ctx context.Context, id string) error {
	err := b.manager.DeleteDestination(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return api.ErrBackupDestinationNotFound
	}
	return err
}
func (b backupTestAdapter) ExportDestinationRecoveryKey(ctx context.Context, id string) (string, error) {
	key, err := b.manager.ExportDestinationRecoveryKey(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", api.ErrBackupDestinationNotFound
	}
	return key, err
}
func (b backupTestAdapter) TestDestination(ctx context.Context, d api.BackupDestination) error {
	return b.manager.TestDestination(ctx, backup.Destination{ID: d.ID, Label: d.Label, Kind: backup.DestinationKind(d.Kind), Reference: d.Reference, Enabled: d.Enabled, Encrypted: d.Encrypted})
}
func (b backupTestAdapter) StartManual() (string, error) {
	id, err := b.manager.StartManual()
	if errors.Is(err, backup.ErrAlreadyRunning) {
		return "", api.ErrBackupAlreadyRunning
	}
	if errors.Is(err, backup.ErrNoDestinations) {
		return "", api.ErrBackupNoDestinations
	}
	return id, err
}
func testAPISchedule(s backup.Schedule) api.BackupSchedule {
	return api.BackupSchedule{WeeklyEnabled: s.WeeklyEnabled, MonthlyEnabled: s.MonthlyEnabled, Timezone: s.Timezone, WeeklyDay: int(s.WeeklyDay), WeeklyHour: s.WeeklyHour, WeeklyMinute: s.WeeklyMinute, MonthlyDay: s.MonthlyDay, MonthlyHour: s.MonthlyHour, MonthlyMinute: s.MonthlyMinute, WeeklyRetention: s.WeeklyRetention, MonthlyRetention: s.MonthlyRetention}
}

func TestAdminBackupRoutesRequireAdminAndManageScheduleAndDestinations(t *testing.T) {
	e := newTEnv(t)
	if rec := e.do("GET", "/api/v1/admin/backups", nil); rec.Code != 401 {
		t.Fatalf("anonymous backup snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
	alice := e.login(e.alice)
	if rec := e.do("GET", "/api/v1/admin/backups", nil, alice.mods()...); rec.Code != 403 {
		t.Fatalf("non-admin backup snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
	root := e.login(e.root)
	if rec := e.do("GET", "/api/v1/admin/backups", nil, root.mods()...); rec.Code != 200 {
		t.Fatalf("admin backup snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
	schedule := backup.DefaultSchedule()
	schedule.WeeklyEnabled = true
	if rec := e.do("PUT", "/api/v1/admin/backups/schedule", schedule, root.mods()...); rec.Code != 200 {
		t.Fatalf("schedule update status=%d body=%s", rec.Code, rec.Body.String())
	}
	missingScheduleField := map[string]any{"weeklyEnabled": false, "monthlyEnabled": false, "timezone": "Asia/Riyadh", "weeklyHour": 3, "weeklyMinute": 0, "monthlyDay": 1, "monthlyHour": 4, "monthlyMinute": 0, "weeklyRetention": 8, "monthlyRetention": 12}
	if rec := e.do("PUT", "/api/v1/admin/backups/schedule", missingScheduleField, root.mods()...); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("schedule without weeklyDay status=%d body=%s", rec.Code, rec.Body.String())
	}
	nullScheduleField := map[string]any{"weeklyEnabled": false, "monthlyEnabled": false, "timezone": "Asia/Riyadh", "weeklyDay": nil, "weeklyHour": 3, "weeklyMinute": 0, "monthlyDay": 1, "monthlyHour": 4, "monthlyMinute": 0, "weeklyRetention": 8, "monthlyRetention": 12}
	if rec := e.do("PUT", "/api/v1/admin/backups/schedule", nullScheduleField, root.mods()...); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("schedule with null weeklyDay status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !decode(t, e.do("GET", "/api/v1/admin/backups", nil, root.mods()...))["schedule"].(map[string]any)["weeklyEnabled"].(bool) {
		t.Fatal("updated schedule was not returned")
	}
	destination := backup.Destination{Label: "Local test", Kind: backup.DestinationLocal, Reference: filepath.Join(t.TempDir(), "backup"), Enabled: true, Encrypted: true}
	if err := os.MkdirAll(destination.Reference, 0o700); err != nil {
		t.Fatal(err)
	}
	created := e.do("POST", "/api/v1/admin/backups/destinations", destination, root.mods()...)
	if created.Code != 201 {
		t.Fatalf("destination create status=%d body=%s", created.Code, created.Body.String())
	}
	createdBody := decode(t, created)
	if createdBody["encrypted"] != true || createdBody["hasRecoveryKey"] != true {
		t.Fatalf("encrypted destination response omitted safe state: %#v", createdBody)
	}
	missingEnabled := map[string]any{"label": "Local test", "kind": "local", "reference": destination.Reference}
	destinationPath := "/api/v1/admin/backups/destinations/" + createdBody["id"].(string)
	recoveryKeyPath := destinationPath + "/recovery-key"
	if rec := e.do("GET", recoveryKeyPath, nil); rec.Code != 401 {
		t.Fatalf("anonymous recovery key status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", recoveryKeyPath, nil, alice.mods()...); rec.Code != 403 {
		t.Fatalf("non-admin recovery key status=%d body=%s", rec.Code, rec.Body.String())
	}
	keyResponse := e.do("GET", recoveryKeyPath, nil, root.mods()...)
	keyBody := decode(t, keyResponse)
	key, ok := keyBody["recoveryKey"].(string)
	snapshotResponse := e.do("GET", "/api/v1/admin/backups", nil, root.mods()...)
	if keyResponse.Code != 200 || !ok || len(key) != 64 || strings.Contains(snapshotResponse.Body.String(), key) {
		t.Fatalf("recovery key export status=%d body=%s", keyResponse.Code, keyResponse.Body.String())
	}
	if rec := e.do("PUT", destinationPath, missingEnabled, root.mods()...); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("destination replacement without enabled status=%d body=%s", rec.Code, rec.Body.String())
	}
	nullEnabled := map[string]any{"label": "Local test", "kind": "local", "reference": destination.Reference, "enabled": nil}
	if rec := e.do("PUT", destinationPath, nullEnabled, root.mods()...); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("destination replacement with null enabled status=%d body=%s", rec.Code, rec.Body.String())
	}
	validMissingID := "/api/v1/admin/backups/destinations/fedcba9876543210"
	if rec := e.do("PUT", validMissingID, map[string]any{"label": "Missing", "kind": "local", "reference": destination.Reference, "enabled": true}, root.mods()...); rec.Code != 404 {
		t.Fatalf("PUT missing destination status=%d body=%s", rec.Code, rec.Body.String())
	}
	replacement := map[string]any{"label": "Local renamed", "kind": "local", "reference": destination.Reference, "enabled": true, "encrypted": true}
	if rec := e.do("PUT", destinationPath, replacement, root.mods()...); rec.Code != 200 {
		t.Fatalf("destination replacement status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/admin/backups/destinations", destination, root.mods()...); rec.Code != 409 {
		t.Fatalf("duplicate destination status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/admin/backups/destinations/test", destination, root.mods()...); rec.Code != 200 {
		t.Fatalf("destination test status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/admin/backups/run", nil, root.mods()...); rec.Code != 202 {
		t.Fatalf("manual run status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.backupManager.Wait()
	snapshot, err := e.backupManager.Snapshot(context.Background())
	if err != nil || snapshot.Running || len(snapshot.Runs) != 1 || snapshot.Runs[0].State != backup.RunSucceeded {
		t.Fatalf("manual run did not succeed through the API: %#v err=%v", snapshot, err)
	}
	activity := e.do("GET", "/api/v1/admin/activity", nil, root.mods()...)
	for _, action := range []string{"backup_schedule_updated", "backup_destination_created", "backup_destination_updated", "backup_run_started"} {
		if activity.Code != 200 || !strings.Contains(activity.Body.String(), action) {
			t.Fatalf("missing audit event %s: %d %s", action, activity.Code, activity.Body.String())
		}
	}
	if rec := e.do("DELETE", "/api/v1/admin/backups/destinations/zzzzzzzzzzzzzzzz", nil, root.mods()...); rec.Code != 400 {
		t.Fatalf("malformed destination DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", "/api/v1/admin/backups/destinations/0123456789abcdef", nil, root.mods()...); rec.Code != 404 {
		t.Fatalf("missing destination DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", destinationPath, nil, root.mods()...); rec.Code != 204 {
		t.Fatalf("successful destination DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	activity = e.do("GET", "/api/v1/admin/activity", nil, root.mods()...)
	if activity.Code != 200 || !strings.Contains(activity.Body.String(), "backup_destination_deleted") {
		t.Fatalf("destination delete audit event missing: %d %s", activity.Code, activity.Body.String())
	}
}
