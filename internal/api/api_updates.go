package api

import (
	"errors"
	"net/http"

	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
)

func (a *API) serveUpdates(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/api/v1/admin/updates" && r.Method == http.MethodGet {
		catalog, err := a.loadUpdateCatalog(r)
		if err != nil {
			server.WriteError(w, http.StatusServiceUnavailable, "updates_unavailable", "release catalog is temporarily unavailable")
			return true
		}
		catalog.ApplyAvailable = a.updateApply != nil
		writeJSON(w, http.StatusOK, catalog)
		return true
	}
	if r.URL.Path != "/api/v1/admin/updates/approve" || r.Method != http.MethodPost {
		return false
	}
	var in struct {
		Tag string `json:"tag"`
	}
	if !decodeJSON(w, r, &in) {
		return true
	}
	if updates.ValidateApprovedTag(in.Tag) != nil {
		server.WriteError(w, http.StatusBadRequest, "invalid_request", "a full SHA-addressed release tag is required")
		return true
	}
	if a.updateApply == nil {
		server.WriteError(w, http.StatusServiceUnavailable, "updates_unavailable", "host update runner is not installed")
		return true
	}
	catalog, err := a.loadUpdateCatalog(r)
	if err != nil {
		server.WriteError(w, http.StatusServiceUnavailable, "updates_unavailable", "release catalog is temporarily unavailable")
		return true
	}
	available := false
	for _, release := range catalog.Releases {
		if release.Tag == in.Tag {
			available = true
			break
		}
	}
	if !available {
		server.WriteError(w, http.StatusConflict, "release_unavailable", "selected release is not in the published catalog")
		return true
	}
	job, err := a.updateApply(r.Context(), in.Tag)
	if err != nil {
		if errors.Is(err, updates.ErrRunnerUnavailable) {
			server.WriteError(w, http.StatusServiceUnavailable, "updates_unavailable", "host update runner could not schedule the update")
		} else {
			server.WriteError(w, http.StatusInternalServerError, "update_schedule_failed", "selected update could not be scheduled")
		}
		return true
	}
	a.recordAdminActivity(r, "software_update_approved", "system", 0)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "tag": in.Tag, "job": job})
	return true
}

func (a *API) loadUpdateCatalog(r *http.Request) (updates.Catalog, error) {
	if a.updateCatalog == nil {
		return updates.Catalog{}, errors.New("catalog is not configured")
	}
	return a.updateCatalog(r.Context(), a.system.Commit)
}
