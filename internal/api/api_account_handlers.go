package api

import (
	"errors"
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
				if _, err := a.bm.ShareScopeFor(r.Context(), uid, in.ShareID, in.Mode == identity.ModeEdit); err != nil {
					writeDomainError(w, err)
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
