package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/interchange"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

func (a *API) servePresentation(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v1/auth/presentation" || r.Method != http.MethodGet {
		return false
	}
	settings, err := a.ids.SystemSettings(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	defaults := map[string]string{
		"appTitle": "BookmarksSync", "signinTitle": "Personal Bookmarks Server",
		"tagline":        "Your bookmarks. Your browsers. Your control.",
		"supportingLine": "A self-hosted home for your bookmarks.",
		"loginLayout":    "split", "primaryColor": "#0f766e", "accentColor": "#14b8a6", "backgroundColor": "#e6f4fb",
	}
	for k, sk := range map[string]string{"appTitle": "branding.app-title", "signinTitle": "branding.signin-title", "tagline": "branding.tagline", "supportingLine": "branding.supporting-line", "loginLayout": "branding.login-layout", "primaryColor": "branding.primary-color", "accentColor": "branding.accent-color", "backgroundColor": "branding.background-color"} {
		if v, ok := settings[sk]; ok {
			defaults[k] = v
		}
	}
	for output, key := range map[string]string{"logoUrl": "branding.logo-id", "backgroundUrl": "branding.background-id"} {
		if id := settings[key]; brandingAssetReferenced(id, settings) && a.brandingAssetExists(id) {
			defaults[output] = "/api/v1/auth/presentation/assets/" + id
		}
	}
	defaults["version"] = a.system.Version
	defaults["copyright"] = fmt.Sprintf("\u00a9 %d Atik Ansari", time.Now().Year())
	writeJSON(w, 200, defaults)
	return true
}

func (a *API) recordAdminActivity(r *http.Request, action, targetType string, targetID int64) {
	if err := a.ids.RecordAdminActivity(r.Context(), principalFrom(r).user.ID, action, targetType, targetID); err != nil {
		a.log.Warn("admin activity record failed", "action", action, "error", err)
	}
}

func userJSON(u identity.UserInfo) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Username, "isAdmin": u.IsAdmin, "active": u.Active, "createdAt": u.CreatedAt, "lastContactAt": timeOrNil(u.LastContactAt)}
}
func tokenJSON(t identity.APIToken) map[string]any {
	return map[string]any{"id": t.ID, "label": t.Label, "scopes": t.Scopes, "createdAt": t.CreatedAt, "expiresAt": timeOrNil(t.ExpiresAt), "lastUsedAt": timeOrNil(t.LastUsedAt), "revoked": t.Revoked, "expired": t.Expired}
}
func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func credentialJSON(c identity.CredentialInfo) map[string]any {
	return map[string]any{"id": c.ID, "label": c.Label, "kind": c.Kind, "shareId": c.ShareID, "mode": c.Mode, "createdAt": c.CreatedAt, "lastUsedAt": timeOrNil(c.LastUsedAt), "revoked": c.Revoked}
}

// dashboardWindow is the recent-changes window of the dashboard.
const dashboardWindowDays = 7

// serveDashboard answers GET /api/v1/me/dashboard for the caller's own private collection
// only. lastContactAt comes from the identity service (newest Floccus credential use);
// the bookmarks service never sees identity data.
func (a *API) serveDashboard(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.URL.Query().Has("share") {
		server.WriteError(w, 400, "invalid_request", "the dashboard is available only for your own collection")
		return
	}
	page, e1 := queryInt(r, "page", 0)
	limit, e2 := queryInt(r, "limit", 20)
	if e1 != nil || e2 != nil || page < 0 || page > 1_000_000 || limit < 1 {
		server.WriteError(w, 400, "invalid_request", "invalid pagination")
		return
	}
	effective := min(limit, 200)
	sc, err := a.bm.PrivateScope(r.Context(), userID, false)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	since := a.now().UTC().Add(-dashboardWindowDays * 24 * time.Hour)
	sum, err := a.bm.Dashboard(r.Context(), sc, since, int(page), int(effective))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	dups, err := a.bm.ListDuplicates(r.Context(), sc, 0, 1)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	creds, err := a.ids.ListCredentials(r.Context(), userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var lastContact *time.Time
	for _, c := range creds {
		if !c.Revoked && !c.LastUsedAt.IsZero() && (lastContact == nil || c.LastUsedAt.After(*lastContact)) {
			t := c.LastUsedAt
			lastContact = &t
		}
	}
	items := make([]map[string]any, 0, len(sum.Recent.Items))
	for _, it := range sum.Recent.Items {
		items = append(items, map[string]any{"type": it.Type, "id": it.ID, "title": it.Title, "url": it.URL, "at": it.At})
	}
	writeJSON(w, 200, map[string]any{
		"totalBookmarks": sum.TotalBookmarks,
		"duplicateCount": dups.Count,
		"lastContactAt":  lastContact,
		"recentChanges": map[string]any{
			"since":      sum.Recent.Since,
			"windowDays": dashboardWindowDays,
			"counts":     map[string]any{"created": sum.Recent.Created, "updated": sum.Recent.Updated, "deleted": sum.Recent.Deleted},
			"items":      items,
			"page":       page,
			"limit":      effective,
			"hasMore":    sum.Recent.HasMore,
		},
	})
}

func (a *API) serveMe(w http.ResponseWriter, r *http.Request) bool {
	p := principalFrom(r)
	userID := p.user.ID
	switch r.URL.Path {
	case "/api/v1/me":
		if r.Method != http.MethodGet {
			return false
		}
		u, err := a.ids.GetUserByID(r.Context(), userID)
		if err != nil {
			writeDomainError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"id": u.ID, "username": u.Username, "isAdmin": u.IsAdmin, "createdAt": u.CreatedAt})
		}
		return true
	case "/api/v1/me/dashboard":
		if r.Method != http.MethodGet {
			return false
		}
		a.serveDashboard(w, r, userID)
		return true
	case "/api/v1/me/password":
		if r.Method != http.MethodPatch {
			return false
		}
		var in struct {
			Current string `json:"currentPassword"`
			New     string `json:"newPassword"`
		}
		if !decodeJSON(w, r, &in) {
			return true
		}
		if !a.auth.CheckPassword(w, r, p.user.Username, in.Current) {
			return true
		}
		rot, err := a.ids.ChangePassword(r.Context(), userID, p.session.ID, in.New)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		csrf := a.auth.ApplyRotation(w, rot)
		writeJSON(w, 200, map[string]any{"csrfToken": csrf})
		return true
	case "/api/v1/me/settings":
		switch r.Method {
		case http.MethodGet:
			s, err := a.ids.Settings(r.Context(), userID)
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, map[string]any{"settings": s})
			}
			return true
		case http.MethodPatch:
			var in map[string]*string
			if !decodeJSON(w, r, &in) {
				return true
			}
			s, err := a.ids.UpdateSettings(r.Context(), userID, in)
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, map[string]any{"settings": s})
			}
			return true
		}
	}
	return false
}

func (a *API) serveTokens(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/me/tokens") {
		return false
	}
	uid := principalFrom(r).user.ID
	if r.URL.Path == "/api/v1/me/tokens" {
		if r.Method == http.MethodGet {
			ts, err := a.ids.ListAPITokens(r.Context(), uid)
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			out := make([]map[string]any, 0, len(ts))
			for _, t := range ts {
				out = append(out, tokenJSON(t))
			}
			writeJSON(w, 200, map[string]any{"tokens": out})
			return true
		}
		if r.Method == http.MethodPost {
			var in struct {
				Label     string           `json:"label"`
				Scopes    []identity.Scope `json:"scopes"`
				ExpiresAt time.Time        `json:"expiresAt"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			tok, secret, err := a.ids.CreateAPIToken(r.Context(), identity.NewAPIToken{UserID: uid, Label: in.Label, Scopes: in.Scopes, ExpiresAt: in.ExpiresAt})
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 201, map[string]any{"token": tokenJSON(tok), "secret": secret})
			}
			return true
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/me/tokens/") && r.Method == http.MethodDelete {
		id, e := requestID(r, "id")
		if e != nil {
			writeDomainError(w, identity.ErrNotFound)
			return true
		}
		err := a.ids.RevokeAPIToken(r.Context(), uid, id)
		if err != nil {
			writeDomainError(w, err)
		} else {
			empty(w)
		}
		return true
	}
	return false
}

func (a *API) serveConnections(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/me/connections") {
		return false
	}
	uid := principalFrom(r).user.ID
	if r.URL.Path == "/api/v1/me/connections" {
		if r.Method == http.MethodGet {
			cs, err := a.ids.ListCredentials(r.Context(), uid)
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			out := make([]map[string]any, 0, len(cs))
			for _, c := range cs {
				out = append(out, credentialJSON(c))
			}
			writeJSON(w, 200, map[string]any{"connections": out})
			return true
		}
		if r.Method == http.MethodPost {
			var in struct {
				Label   string        `json:"label"`
				Mode    identity.Mode `json:"mode"`
				ShareID int64         `json:"shareId"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			kind := identity.KindPrivate
			if in.ShareID != 0 {
				kind = identity.KindShare
				sc, err := a.bm.ShareScopeFor(r.Context(), uid, in.ShareID, in.Mode == identity.ModeEdit)
				if err != nil {
					writeDomainError(w, err)
					return true
				}
				// The connection's mode may not exceed the live grant. Only the share's
				// recipient gets this far (anyone else got 404), so 403 leaks nothing.
				if in.Mode == identity.ModeEdit && !sc.Writable {
					writeDomainError(w, bookmarks.ErrForbidden)
					return true
				}
			}
			c, secret, err := a.ids.CreateCredential(r.Context(), identity.NewCredential{UserID: uid, Label: in.Label, Mode: in.Mode, Kind: kind, ShareID: in.ShareID})
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			writeJSON(w, 201, map[string]any{"connection": map[string]any{"id": c.ID, "label": c.Label, "kind": c.Kind, "shareId": c.ShareID, "mode": c.Mode}, "secret": secret})
			return true
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/me/connections/") && r.Method == http.MethodDelete {
		id, e := requestID(r, "id")
		if e != nil {
			writeDomainError(w, identity.ErrNotFound)
			return true
		}
		err := a.ids.RevokeUserCredential(r.Context(), uid, id)
		if err != nil {
			writeDomainError(w, err)
		} else {
			empty(w)
		}
		return true
	}
	return false
}

func (a *API) serveAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/admin/") {
		return false
	}
	if p == "/api/v1/admin/health" && r.Method == http.MethodGet {
		if a.health == nil {
			server.WriteError(w, http.StatusServiceUnavailable, "health_unavailable", "host health collector is unavailable")
			return true
		}
		report := a.health.Collect(r.Context())
		dbReady := a.readiness != nil && a.readiness.Ping(r.Context()) == nil
		writeJSON(w, 200, map[string]any{
			"cpuPercent": report.CPUPercent, "memory": report.Memory, "disk": report.Disk,
			"uptimeSeconds": report.UptimeSeconds, "unavailable": report.Unavailable,
			"dbReady": dbReady, "syncErrors": a.syncErrors(),
		})
		return true
	}
	if p == "/api/v1/admin/activity" && r.Method == http.MethodGet {
		limit, err := queryInt(r, "limit", 50)
		if err != nil || limit < 1 {
			server.WriteError(w, 400, "invalid_request", "invalid activity limit")
			return true
		}
		items, err := a.ids.ListAdminActivity(r.Context(), int(min(limit, 200)))
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		out := make([]map[string]any, 0, len(items))
		for _, v := range items {
			out = append(out, map[string]any{"id": v.ID, "actorId": v.ActorID, "action": v.Action, "targetType": v.TargetType, "targetId": v.TargetID, "createdAt": v.CreatedAt})
		}
		writeJSON(w, 200, map[string]any{"items": out})
		return true
	}
	if p == "/api/v1/admin/settings" {
		switch r.Method {
		case http.MethodGet:
			settings, err := a.ids.SystemSettings(r.Context())
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, map[string]any{"settings": settings})
			}
			return true
		case http.MethodPatch:
			var changes map[string]*string
			if !decodeJSON(w, r, &changes) {
				return true
			}
			a.assetMu.Lock()
			for key, value := range changes {
				if value != nil && (key == "branding.logo-id" || key == "branding.background-id") && !a.brandingAssetExists(*value) {
					a.assetMu.Unlock()
					server.WriteError(w, http.StatusBadRequest, "invalid_setting", "branding images must refer to an uploaded asset")
					return true
				}
			}
			old, oldErr := a.ids.SystemSettings(r.Context())
			if oldErr != nil {
				a.assetMu.Unlock()
				writeDomainError(w, oldErr)
				return true
			}
			settings, err := a.ids.UpdateSystemSettings(r.Context(), changes)
			if err != nil {
				a.assetMu.Unlock()
				writeDomainError(w, err)
			} else {
				a.cleanupUnreferencedBrandingAssets([]string{old["branding.logo-id"], old["branding.background-id"]}, settings)
				a.assetMu.Unlock()
				a.recordAdminActivity(r, "system_settings_updated", "system", 0)
				writeJSON(w, 200, map[string]any{"settings": settings})
			}
			return true
		}
	}
	if p == "/api/v1/admin/settings/assets" && r.Method == http.MethodPost {
		a.uploadBrandingAsset(w, r)
		return true
	}
	if p == "/api/v1/admin/users" && r.Method == http.MethodPost {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Admin    bool   `json:"isAdmin"`
		}
		if !decodeJSON(w, r, &in) {
			return true
		}
		u, err := a.ids.CreateUser(r.Context(), in.Username, in.Password, in.Admin)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		a.recordAdminActivity(r, "user_created", "user", u.ID)
		info, err := a.ids.GetUserByID(r.Context(), u.ID)
		if err != nil {
			writeDomainError(w, err)
		} else {
			writeJSON(w, 201, userJSON(info))
		}
		return true
	}
	if p == "/api/v1/admin/users" && r.Method == http.MethodGet {
		page, e1 := queryInt(r, "page", 0)
		limit, e2 := queryInt(r, "limit", 50)
		if e1 != nil || e2 != nil || page < 0 || page > 1_000_000 || limit < 1 {
			server.WriteError(w, 400, "invalid_request", "invalid pagination")
			return true
		}
		effective := min(limit, 200)
		offset := int(page * effective)
		users, err := a.ids.ListUsersPage(r.Context(), int(effective+1), offset)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		stats, err := a.bm.Stats(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		hasMore := int64(len(users)) > effective
		if hasMore {
			users = users[:effective]
		}
		themeDefaults, err := a.ids.ListUserThemeDefaults(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		out := make([]map[string]any, 0, len(users))
		for _, u := range users {
			row := userJSON(u)
			st := stats[u.ID]
			conns, err := a.ids.ListCredentials(r.Context(), u.ID)
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			activeConnections := 0
			for _, c := range conns {
				if !c.Revoked {
					activeConnections++
				}
			}
			row["bookmarkCount"] = st.Bookmarks
			row["folderCount"] = st.Folders
			row["storageBytes"] = st.StorageBytes
			row["connectionCount"] = activeConnections
			row["themeDefault"] = themeDefaults[u.ID]
			if _, ok := themeDefaults[u.ID]; !ok {
				row["themeDefault"] = nil
			}
			out = append(out, row)
		}
		writeJSON(w, 200, map[string]any{"users": out, "page": page, "limit": effective, "hasMore": hasMore})
		return true
	}
	if p == "/api/v1/admin/system" && r.Method == http.MethodGet {
		users, err := a.ids.ListUsers(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		admins, active := 0, 0
		for _, u := range users {
			if u.IsAdmin && u.Active {
				admins++
			}
			if u.Active {
				active++
			}
		}
		stats, err := a.bm.Stats(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		var bookmarkTotal int
		var storageBytes int64
		for _, st := range stats {
			bookmarkTotal += st.Bookmarks
			storageBytes += st.StorageBytes
		}
		writeJSON(w, 200, map[string]any{"version": a.system.Version, "publicOrigin": a.system.PublicOrigin, "passkeysEnabled": a.system.PasskeysEnabled, "oidcProviders": a.system.OIDCProviders, "users": map[string]any{"total": len(users), "active": active, "admins": admins}, "bookmarks": map[string]any{"total": bookmarkTotal}, "storageBytes": storageBytes})
		return true
	}
	if strings.HasPrefix(p, "/api/v1/admin/users/") {
		parts := strings.Split(strings.TrimPrefix(p, "/api/v1/admin/users/"), "/")
		id, e := strconv.ParseInt(parts[0], 10, 64)
		if e != nil || id <= 0 {
			writeDomainError(w, identity.ErrNotFound)
			return true
		}
		if len(parts) == 2 && parts[1] == "settings" {
			if _, err := a.ids.GetUserByID(r.Context(), id); err != nil {
				writeDomainError(w, err)
				return true
			}
			switch r.Method {
			case http.MethodGet:
				settings, err := a.ids.UserSettingDefaults(r.Context(), id)
				if err != nil {
					writeDomainError(w, err)
				} else {
					writeJSON(w, 200, map[string]any{"settings": settings})
				}
				return true
			case http.MethodPatch:
				var changes map[string]*string
				if !decodeJSON(w, r, &changes) {
					return true
				}
				settings, err := a.ids.UpdateUserSettingDefaults(r.Context(), id, changes)
				if err != nil {
					writeDomainError(w, err)
				} else {
					a.recordAdminActivity(r, "user_settings_updated", "user", id)
					writeJSON(w, 200, map[string]any{"settings": settings})
				}
				return true
			}
		}
		if len(parts) == 2 && parts[1] == "connections" && r.Method == http.MethodGet {
			if _, err := a.ids.GetUserByID(r.Context(), id); err != nil {
				writeDomainError(w, err)
				return true
			}
			cs, err := a.ids.ListCredentials(r.Context(), id)
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			out := make([]map[string]any, 0, len(cs))
			for _, c := range cs {
				out = append(out, credentialJSON(c))
			}
			writeJSON(w, 200, map[string]any{"connections": out})
			return true
		}
		if len(parts) == 3 && parts[1] == "connections" && r.Method == http.MethodDelete {
			cid, e := strconv.ParseInt(parts[2], 10, 64)
			if e != nil {
				writeDomainError(w, identity.ErrNotFound)
				return true
			}
			err := a.ids.RevokeUserCredential(r.Context(), id, cid)
			if err != nil {
				writeDomainError(w, err)
			} else {
				a.recordAdminActivity(r, "connection_revoked", "user", id)
				empty(w)
			}
			return true
		}
		if len(parts) == 1 && r.Method == http.MethodPatch {
			var in struct {
				Active *bool `json:"active"`
				Admin  *bool `json:"isAdmin"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			u, err := a.ids.UpdateUser(r.Context(), id, in.Active, in.Admin)
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			a.recordAdminActivity(r, "user_updated", "user", id)
			info, err := a.ids.GetUserByID(r.Context(), u.ID)
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, userJSON(info))
			}
			return true
		}
		if len(parts) == 2 && parts[1] == "password-reset" && r.Method == http.MethodPost {
			var in struct {
				Password string `json:"newPassword"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			if err := a.ids.ResetPassword(r.Context(), id, in.Password); err != nil {
				writeDomainError(w, err)
			} else {
				a.recordAdminActivity(r, "password_reset", "user", id)
				empty(w)
			}
			return true
		}
		if len(parts) == 2 && parts[1] == "factor-reset" && r.Method == http.MethodPost {
			var in struct {
				TOTP     bool `json:"totp"`
				Passkeys bool `json:"passkeys"`
				OIDC     bool `json:"oidc"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			if err := a.ids.ResetFactors(r.Context(), id, identity.FactorReset{TOTP: in.TOTP, Passkeys: in.Passkeys, OIDC: in.OIDC}); err != nil {
				writeDomainError(w, err)
			} else {
				a.recordAdminActivity(r, "factor_reset", "user", id)
				empty(w)
			}
			return true
		}
	}
	return false
}

func (a *API) serveTransfer(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/api/v1/import" && r.Method == http.MethodPost {
		sc, err := a.scope(r, true)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		folder, e := queryInt(r, "folder", bookmarks.RootID)
		if e != nil {
			server.WriteError(w, 400, "invalid_request", "invalid import folder")
			return true
		}
		if !r.URL.Query().Has("folder") && sc.RootFolderID == 0 {
			roots, err := a.bm.FolderTree(r.Context(), sc, bookmarks.RootID, 1)
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			for _, root := range roots {
				if root.Type == "folder" && root.Title == "Other Bookmarks" {
					folder = root.ID
					break
				}
			}
		}
		ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			server.WriteError(w, 415, "unsupported_media_type", "unsupported import format")
			return true
		}
		var input io.Reader = io.LimitReader(r.Body, importBodyLimit+1)
		var nodes []bookmarks.ImportNode
		if ct == "multipart/form-data" {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				var max *http.MaxBytesError
				if errors.As(err, &max) {
					server.WriteError(w, 413, "body_too_large", "request body too large")
					return true
				}
				server.WriteError(w, 400, "invalid_request", "invalid import upload")
				return true
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				server.WriteError(w, 400, "invalid_request", "file is required")
				return true
			}
			defer file.Close()
			ext := strings.ToLower(filepath.Ext(header.Filename))
			if ext == ".xbel" {
				nodes, err = interchange.ParseXBEL(file, importBodyLimit)
			} else {
				nodes, err = interchange.ParseNetscape(file, importBodyLimit, true)
			}
		} else if ct == "text/html" {
			nodes, err = interchange.ParseNetscape(input, importBodyLimit, true)
		} else if ct == "application/xbel+xml" || ct == "application/xml" || ct == "text/xml" {
			nodes, err = interchange.ParseXBEL(input, importBodyLimit)
		} else {
			server.WriteError(w, 415, "unsupported_media_type", "supported formats are Netscape HTML and XBEL")
			return true
		}
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		created, err := a.bm.ImportTree(r.Context(), sc, folder, nodes)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		count := countNodes(created)
		writeJSON(w, 201, map[string]any{"imported": count, "folders": treeJSON(created)})
		return true
	}
	if r.URL.Path == "/api/v1/export" && r.Method == http.MethodGet {
		sc, err := a.scope(r, false)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		folder, e := queryInt(r, "folder", bookmarks.RootID)
		if e != nil {
			server.WriteError(w, 400, "invalid_request", "invalid export folder")
			return true
		}
		tree, err := a.bm.ExportTree(r.Context(), sc, folder)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		var out strings.Builder
		format := r.URL.Query().Get("format")
		switch format {
		case "", "netscape":
			err = interchange.WriteNetscape(&out, tree)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="bookmarks.html"`)
		case "xbel":
			err = interchange.WriteXBEL(&out, tree)
			w.Header().Set("Content-Type", "application/xbel+xml; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="bookmarks.xbel"`)
		default:
			server.WriteError(w, 400, "invalid_request", "format must be netscape or xbel")
			return true
		}
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, out.String())
		return true
	}
	return false
}

func countNodes(nodes []bookmarks.Node) int {
	n := 0
	for _, v := range nodes {
		n++
		n += countNodes(v.Children)
	}
	return n
}
