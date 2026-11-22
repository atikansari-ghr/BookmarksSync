// Package api implements the user and administrator JSON API. Request identity is
// always obtained from validated web sessions or personal API tokens; handlers use
// scoped domain services for bookmark data.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/health"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

const (
	APIVersion            = "1.0.0"
	jsonBodyLimit   int64 = 64 << 10
	importBodyLimit int64 = 4 << 20
)

// BodyLimit selects a larger outer request bound only for bookmark imports. JSON
// endpoints apply jsonBodyLimit while decoding their own bodies.
func BodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/import" {
		return importBodyLimit
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/settings/assets" {
		return (2 << 20) + (64 << 10)
	}
	return 0
}

// Identity and Bookmarks are domain services. The API layer does not access the DB.
type Identity interface {
	ResolveAPIToken(context.Context, string) (identity.TokenPrincipal, error)
	GetUserByID(context.Context, int64) (identity.UserInfo, error)
	ListUsers(context.Context) ([]identity.UserInfo, error)
	ListUsersPage(context.Context, int, int) ([]identity.UserInfo, error)
	ListUserThemeDefaults(context.Context) (map[int64]string, error)
	CreateUser(context.Context, string, string, bool) (identity.User, error)
	UpdateUser(context.Context, int64, *bool, *bool) (identity.User, error)
	ChangePassword(context.Context, int64, int64, string) (identity.SessionRotation, error)
	ResetPassword(context.Context, int64, string) error
	ResetFactors(context.Context, int64, identity.FactorReset) error
	CreateAPIToken(context.Context, identity.NewAPIToken) (identity.APIToken, string, error)
	ListAPITokens(context.Context, int64) ([]identity.APIToken, error)
	RevokeAPIToken(context.Context, int64, int64) error
	CreateCredential(context.Context, identity.NewCredential) (identity.Credential, string, error)
	ListCredentials(context.Context, int64) ([]identity.CredentialInfo, error)
	RevokeUserCredential(context.Context, int64, int64) error
	Settings(context.Context, int64) (map[string]string, error)
	UpdateSettings(context.Context, int64, map[string]*string) (map[string]string, error)
	UserSettingDefaults(context.Context, int64) (map[string]string, error)
	UpdateUserSettingDefaults(context.Context, int64, map[string]*string) (map[string]string, error)
	SystemSettings(context.Context) (map[string]string, error)
	UpdateSystemSettings(context.Context, map[string]*string) (map[string]string, error)
	RecordAdminActivity(context.Context, int64, string, string, int64) error
	ListAdminActivity(context.Context, int) ([]identity.AdminActivity, error)
}

type Bookmarks interface {
	PrivateScope(context.Context, int64, bool) (bookmarks.Scope, error)
	ShareScopeFor(context.Context, int64, int64, bool) (bookmarks.Scope, error)
	ListBookmarks(context.Context, bookmarks.Scope, bookmarks.ListQuery) ([]bookmarks.Bookmark, error)
	GetBookmark(context.Context, bookmarks.Scope, int64) (bookmarks.Bookmark, error)
	CreateBookmark(context.Context, bookmarks.Scope, bookmarks.BookmarkInput) (bookmarks.Bookmark, error)
	UpdateBookmark(context.Context, bookmarks.Scope, int64, bookmarks.BookmarkUpdate) (bookmarks.Bookmark, error)
	DeleteBookmark(context.Context, bookmarks.Scope, int64) error
	MoveBookmarks(context.Context, bookmarks.Scope, []int64, int64, int64) error
	RemovePlacement(context.Context, bookmarks.Scope, int64, int64) error
	SetFavorite(context.Context, bookmarks.Scope, int64, bool) error
	FolderTree(context.Context, bookmarks.Scope, int64, int) ([]bookmarks.Node, error)
	GetFolder(context.Context, bookmarks.Scope, int64) (bookmarks.Folder, error)
	CreateFolder(context.Context, bookmarks.Scope, int64, string) (bookmarks.Folder, error)
	UpdateFolder(context.Context, bookmarks.Scope, int64, *int64, *string) (bookmarks.Folder, error)
	DeleteFolder(context.Context, bookmarks.Scope, int64) error
	SetChildOrder(context.Context, bookmarks.Scope, int64, []bookmarks.ChildRef) error
	ListTags(context.Context, bookmarks.Scope) ([]bookmarks.TagCount, error)
	ListDuplicates(context.Context, bookmarks.Scope, int, int) (bookmarks.DuplicateList, error)
	Dashboard(context.Context, bookmarks.Scope, time.Time, int, int) (bookmarks.DashboardSummary, error)
	ListTrash(context.Context, bookmarks.Scope, int, int) ([]bookmarks.TrashItem, error)
	RestoreTrash(context.Context, bookmarks.Scope, string, int64) (bookmarks.Node, error)
	PurgeTrash(context.Context, bookmarks.Scope, string, int64) error
	EmptyTrash(context.Context, bookmarks.Scope) error
	PurgeExpiredTrash(context.Context) (int, error)
	Stats(context.Context) (map[int64]bookmarks.UserStats, error)
	ExportTree(context.Context, bookmarks.Scope, int64) ([]bookmarks.ExportNode, error)
	ImportTree(context.Context, bookmarks.Scope, int64, []bookmarks.ImportNode) ([]bookmarks.Node, error)
	ListShares(context.Context, int64) ([]bookmarks.Share, error)
	ListIncomingShares(context.Context, int64) ([]bookmarks.Share, error)
	ShareByID(context.Context, int64) (bookmarks.Share, error)
	GrantShare(context.Context, int64, int64, int64, identity.Mode) (bookmarks.Share, error)
	UpdateShare(context.Context, int64, int64, identity.Mode) (bookmarks.Share, error)
	RevokeShare(context.Context, int64, int64, int64) error
	LeaveShare(context.Context, int64, int64) error
}

// LinkChecks queues and reports public-only link checks (see internal/linkcheck).
type LinkChecks interface {
	SubmitCheck(context.Context, bookmarks.Scope, []int64) (linkcheck.Job, error)
	CheckStatus(context.Context, bookmarks.Scope, int64) (linkcheck.Job, error)
}

type SystemInfo struct {
	Version           string
	PublicOrigin      string
	PasskeysEnabled   bool
	OIDCProviders     []string
	GeoIPAvailable    bool
	GeoIPBuildTime    time.Time
	TrustedProxyCount int
}

type HealthCollector interface {
	Collect(context.Context) health.Report
}
type Readiness interface{ Ping(context.Context) error }

type BackupSchedule struct {
	WeeklyEnabled    bool   `json:"weeklyEnabled"`
	MonthlyEnabled   bool   `json:"monthlyEnabled"`
	Timezone         string `json:"timezone"`
	WeeklyDay        int    `json:"weeklyDay"`
	WeeklyHour       int    `json:"weeklyHour"`
	WeeklyMinute     int    `json:"weeklyMinute"`
	MonthlyDay       int    `json:"monthlyDay"`
	MonthlyHour      int    `json:"monthlyHour"`
	MonthlyMinute    int    `json:"monthlyMinute"`
	WeeklyRetention  int    `json:"weeklyRetention"`
	MonthlyRetention int    `json:"monthlyRetention"`
}

type BackupDestination struct {
	ID        string `json:"id,omitempty"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Enabled   bool   `json:"enabled"`
}

type BackupRun struct {
	ID               int64      `json:"id"`
	BatchID          string     `json:"batchId"`
	Trigger          string     `json:"trigger"`
	DestinationID    string     `json:"destinationId"`
	DestinationLabel string     `json:"destinationLabel"`
	ArchiveName      string     `json:"archiveName"`
	State            string     `json:"state"`
	SizeBytes        int64      `json:"sizeBytes"`
	ErrorCode        string     `json:"errorCode,omitempty"`
	StartedAt        time.Time  `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
}

type BackupSnapshot struct {
	Schedule     BackupSchedule      `json:"schedule"`
	Destinations []BackupDestination `json:"destinations"`
	Runs         []BackupRun         `json:"runs"`
	Running      bool                `json:"running"`
}

var (
	ErrBackupAlreadyRunning      = errors.New("backup already running")
	ErrBackupNoDestinations      = errors.New("backup destination required")
	ErrBackupDestinationNotFound = errors.New("backup destination not found")
	ErrBackupInvalidSchedule     = errors.New("backup schedule is invalid")
	ErrBackupInvalidDestination  = errors.New("backup destination is invalid")
	ErrBackupDestinationConflict = errors.New("backup destination conflicts")
)

type BackupOperations interface {
	Snapshot(context.Context) (BackupSnapshot, error)
	UpdateSchedule(context.Context, BackupSchedule) error
	SaveDestination(context.Context, BackupDestination) (BackupDestination, error)
	DeleteDestination(context.Context, string) error
	TestDestination(context.Context, BackupDestination) error
	StartManual() (string, error)
}

type Options struct {
	Auth      *auth.Handler
	Identity  Identity
	Bookmarks Bookmarks
	// LinkChecks is optional; without it the link-check routes answer 503.
	LinkChecks LinkChecks
	Now        func() time.Time
	Logger     *slog.Logger
	System     SystemInfo
	Health     HealthCollector
	Readiness  Readiness
	SyncErrors func() uint64
	AssetDir   string
	AssetMu    *sync.Mutex
	Backups    BackupOperations
}

type RouteInfo struct {
	Method     string
	Path       string
	TokenScope identity.Scope
	AnyToken   bool
	Admin      bool
	Recent     bool
	Public     bool
}

type API struct {
	auth       *auth.Handler
	ids        Identity
	bm         Bookmarks
	lc         LinkChecks
	now        func() time.Time
	log        *slog.Logger
	system     SystemInfo
	health     HealthCollector
	readiness  Readiness
	syncErrors func() uint64
	assetDir   string
	backups    BackupOperations
	routes     []RouteInfo
	assetMu    *sync.Mutex
}

var routePolicies = []RouteInfo{
	{Method: "GET", Path: "/api/v1/me", AnyToken: true},
	{Method: "PATCH", Path: "/api/v1/me/password", Recent: true},
	{Method: "GET", Path: "/api/v1/me/dashboard", TokenScope: identity.ScopeBookmarksRead},
	{Method: "GET", Path: "/api/v1/me/settings"},
	{Method: "PATCH", Path: "/api/v1/me/settings"},
	{Method: "GET", Path: "/api/v1/me/tokens"},
	{Method: "POST", Path: "/api/v1/me/tokens", Recent: true},
	{Method: "DELETE", Path: "/api/v1/me/tokens/{id}"},
	{Method: "GET", Path: "/api/v1/me/connections"},
	{Method: "POST", Path: "/api/v1/me/connections", Recent: true},
	{Method: "DELETE", Path: "/api/v1/me/connections/{id}"},
	{Method: "GET", Path: "/api/v1/bookmarks", TokenScope: identity.ScopeBookmarksRead},
	{Method: "POST", Path: "/api/v1/bookmarks", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "POST", Path: "/api/v1/bookmarks/bulk-move", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "GET", Path: "/api/v1/bookmarks/{id}", TokenScope: identity.ScopeBookmarksRead},
	{Method: "PATCH", Path: "/api/v1/bookmarks/{id}", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "DELETE", Path: "/api/v1/bookmarks/{id}", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "PUT", Path: "/api/v1/bookmarks/{id}/favorite", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "DELETE", Path: "/api/v1/bookmarks/{id}/favorite", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "DELETE", Path: "/api/v1/folders/{folderId}/bookmarks/{id}", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "GET", Path: "/api/v1/folders", TokenScope: identity.ScopeBookmarksRead},
	{Method: "POST", Path: "/api/v1/folders", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "GET", Path: "/api/v1/folders/{id}", TokenScope: identity.ScopeBookmarksRead},
	{Method: "PATCH", Path: "/api/v1/folders/{id}", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "DELETE", Path: "/api/v1/folders/{id}", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "PUT", Path: "/api/v1/folders/{id}/order", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "GET", Path: "/api/v1/tags", TokenScope: identity.ScopeBookmarksRead},
	{Method: "GET", Path: "/api/v1/duplicates", TokenScope: identity.ScopeBookmarksRead},
	{Method: "POST", Path: "/api/v1/link-checks", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "GET", Path: "/api/v1/link-checks/{id}", TokenScope: identity.ScopeBookmarksRead},
	{Method: "GET", Path: "/api/v1/trash", TokenScope: identity.ScopeBookmarksRead},
	{Method: "POST", Path: "/api/v1/trash/{type}/{id}/restore", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "DELETE", Path: "/api/v1/trash/{type}/{id}", Recent: true},
	{Method: "DELETE", Path: "/api/v1/trash", Recent: true},
	{Method: "POST", Path: "/api/v1/import", TokenScope: identity.ScopeBookmarksWrite},
	{Method: "GET", Path: "/api/v1/export", TokenScope: identity.ScopeBookmarksRead},
	{Method: "GET", Path: "/api/v1/shares/owned", TokenScope: identity.ScopeSharesRead},
	{Method: "GET", Path: "/api/v1/shares/incoming", TokenScope: identity.ScopeSharesRead},
	{Method: "POST", Path: "/api/v1/shares", TokenScope: identity.ScopeSharesManage},
	{Method: "PATCH", Path: "/api/v1/shares/{id}", TokenScope: identity.ScopeSharesManage},
	{Method: "DELETE", Path: "/api/v1/shares/{id}", TokenScope: identity.ScopeSharesManage},
	{Method: "GET", Path: "/api/v1/admin/users", Admin: true},
	{Method: "POST", Path: "/api/v1/admin/users", Admin: true, Recent: true},
	{Method: "PATCH", Path: "/api/v1/admin/users/{id}", Admin: true, Recent: true},
	{Method: "POST", Path: "/api/v1/admin/users/{id}/password-reset", Admin: true, Recent: true},
	{Method: "POST", Path: "/api/v1/admin/users/{id}/factor-reset", Admin: true, Recent: true},
	{Method: "GET", Path: "/api/v1/admin/users/{id}/connections", Admin: true},
	{Method: "DELETE", Path: "/api/v1/admin/users/{id}/connections/{cid}", Admin: true, Recent: true},
	{Method: "GET", Path: "/api/v1/admin/system", Admin: true},
	{Method: "GET", Path: "/api/v1/admin/health", Admin: true},
	{Method: "GET", Path: "/api/v1/admin/activity", Admin: true},
	{Method: "GET", Path: "/api/v1/admin/backups", Admin: true},
	{Method: "PUT", Path: "/api/v1/admin/backups/schedule", Admin: true, Recent: true},
	{Method: "POST", Path: "/api/v1/admin/backups/destinations", Admin: true, Recent: true},
	{Method: "PUT", Path: "/api/v1/admin/backups/destinations/{id}", Admin: true, Recent: true},
	{Method: "DELETE", Path: "/api/v1/admin/backups/destinations/{id}", Admin: true, Recent: true},
	{Method: "POST", Path: "/api/v1/admin/backups/destinations/test", Admin: true, Recent: true},
	{Method: "POST", Path: "/api/v1/admin/backups/run", Admin: true, Recent: true},
	{Method: "GET", Path: "/api/v1/admin/settings", Admin: true},
	{Method: "PATCH", Path: "/api/v1/admin/settings", Admin: true, Recent: true},
	{Method: "GET", Path: "/api/v1/admin/network-access", Admin: true},
	{Method: "PATCH", Path: "/api/v1/admin/network-access", Admin: true, Recent: true},
	{Method: "POST", Path: "/api/v1/admin/settings/assets", Admin: true, Recent: true},
	{Method: "GET", Path: "/api/v1/admin/users/{id}/settings", Admin: true},
	{Method: "PATCH", Path: "/api/v1/admin/users/{id}/settings", Admin: true, Recent: true},
	{Method: "GET", Path: "/api/v1/auth/presentation", Public: true},
	{Method: "GET", Path: "/api/v1/auth/presentation/assets/{id}", Public: true},
}

func New(opts Options) (*API, error) {
	if opts.Auth == nil || opts.Identity == nil || opts.Bookmarks == nil {
		return nil, errors.New("api: auth, identity and bookmarks services are required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.SyncErrors == nil {
		opts.SyncErrors = func() uint64 { return 0 }
	}
	if opts.AssetMu == nil {
		opts.AssetMu = &sync.Mutex{}
	}
	return &API{auth: opts.Auth, ids: opts.Identity, bm: opts.Bookmarks, lc: opts.LinkChecks, now: opts.Now, log: opts.Logger, system: opts.System, health: opts.Health, readiness: opts.Readiness, syncErrors: opts.SyncErrors, assetDir: opts.AssetDir, backups: opts.Backups, assetMu: opts.AssetMu, routes: append([]RouteInfo(nil), routePolicies...)}, nil
}

func (a *API) Routes() []RouteInfo { return append([]RouteInfo(nil), a.routes...) }

func (a *API) Register(r server.Registrar) {
	for _, rt := range a.routes {
		info := rt
		var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { a.serve(w, req, info) })
		if !info.Public {
			h = a.authorize(info, h)
		}
		h = noStore(h)
		r.Handle(rt.Method+" "+rt.Path, h)
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type principal struct {
	user    identity.User
	session identity.Session
	token   *identity.TokenPrincipal
}
type principalKey struct{}

func (a *API) withPrincipal(r *http.Request, p principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}
func principalFrom(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

func (a *API) authorize(rt RouteInfo, next http.Handler) http.Handler {
	var sessionHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, u, ok := auth.FromContext(r.Context())
		if !ok {
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		p := principal{user: u, session: sess}
		if rt.Admin {
			fresh, err := a.ids.GetUserByID(r.Context(), u.ID)
			if err != nil || !fresh.Active || !fresh.IsAdmin {
				server.WriteError(w, http.StatusForbidden, "admin_required", "administrator access required")
				return
			}
			p.user = fresh.User
		}
		next.ServeHTTP(w, a.withPrincipal(r, p))
	})
	if rt.Recent {
		sessionHandler = a.auth.RequireRecentAuth(sessionHandler)
	}
	sessionHandler = a.auth.RequireSession(sessionHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Values("Authorization")
		if len(h) == 0 {
			sessionHandler.ServeHTTP(w, r)
			return
		}
		if len(h) != 1 {
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		parts := strings.SplitN(h[0], " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" || strings.ContainsAny(parts[1], " \t\r\n") {
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		if rt.TokenScope == "" && !rt.AnyToken {
			server.WriteError(w, http.StatusForbidden, "token_not_allowed", "this endpoint requires a web session")
			return
		}
		tok, err := a.ids.ResolveAPIToken(r.Context(), parts[1])
		if err != nil {
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		if rt.TokenScope != "" && !tok.Allows(rt.TokenScope) {
			server.WriteError(w, http.StatusForbidden, "insufficient_scope", "token lacks the required scope")
			return
		}
		p := principal{user: tok.User, token: &tok}
		next.ServeHTTP(w, a.withPrincipal(r, p))
	})
}

func (a *API) serve(w http.ResponseWriter, r *http.Request, rt RouteInfo) {
	if a.serveBrandingAsset(w, r) || a.servePresentation(w, r) || a.serveBookmarks(w, r) || a.serveFolders(w, r) || a.serveTags(w, r) || a.serveDuplicates(w, r) || a.serveLinkChecks(w, r) || a.serveTrash(w, r) || a.serveShares(w, r) || a.serveMe(w, r) || a.serveTokens(w, r) || a.serveConnections(w, r) || a.serveBackups(w, r) || a.serveAdmin(w, r) || a.serveTransfer(w, r) {
		return
	}
	server.WriteError(w, http.StatusNotFound, "not_found", "route not found")
}

func writeJSON(w http.ResponseWriter, status int, v any) { server.WriteJSON(w, status, v) }
func empty(w http.ResponseWriter)                        { w.WriteHeader(http.StatusNoContent) }

func requestID(r *http.Request, key string) (int64, error) {
	v := r.PathValue(key)
	if v == "" {
		return 0, errors.New("missing id")
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func queryInt(r *http.Request, key string, def int64) (int64, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (a *API) scope(r *http.Request, write bool) (bookmarks.Scope, error) {
	p := principalFrom(r)
	canWrite := write
	if p.token != nil {
		canWrite = p.token.Allows(identity.ScopeBookmarksWrite)
	}
	if v := r.URL.Query().Get("share"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return bookmarks.Scope{}, bookmarks.ErrInvalid
		}
		return a.bm.ShareScopeFor(r.Context(), p.user.ID, id, canWrite)
	}
	return a.bm.PrivateScope(r.Context(), p.user.ID, canWrite)
}

func bookmarkJSON(b bookmarks.Bookmark) map[string]any {
	return map[string]any{"id": b.ID, "url": b.URL, "title": b.Title, "tags": b.Tags, "folders": b.Folders, "favorite": b.Favorite}
}
func folderJSON(f bookmarks.Folder) map[string]any {
	return map[string]any{"id": f.ID, "parentId": f.ParentID, "title": f.Title, "position": f.Position, "canonical": f.Canonical}
}
func treeJSON(nodes []bookmarks.Node) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		m := map[string]any{"id": n.ID, "type": n.Type, "title": n.Title}
		if n.Type == "bookmark" {
			m["url"] = n.URL
		}
		if n.Type == "folder" {
			m["loaded"] = n.Loaded
			m["children"] = treeJSON(n.Children)
		}
		out = append(out, m)
	}
	return out
}

func (a *API) serveBookmarks(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p != "/api/v1/bookmarks" && !(strings.HasPrefix(p, "/api/v1/bookmarks/") || strings.HasPrefix(p, "/api/v1/folders/")) {
		return false
	}
	// A favorite is the caller's own metadata, so its toggle resolves a read-only scope:
	// a view-only share recipient may favorite what it can see. Token callers still need
	// bookmarks:write (route policy), and the service revalidates actor and visibility.
	favorite := strings.HasPrefix(p, "/api/v1/bookmarks/") && strings.HasSuffix(p, "/favorite")
	sc, err := a.scope(r, r.Method != http.MethodGet && !favorite)
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	if favorite {
		id, e := requestID(r, "id")
		if e != nil || (r.Method != http.MethodPut && r.Method != http.MethodDelete) {
			writeDomainError(w, bookmarks.ErrNotFound)
			return true
		}
		if err := a.bm.SetFavorite(r.Context(), sc, id, r.Method == http.MethodPut); err != nil {
			writeDomainError(w, err)
		} else {
			empty(w)
		}
		return true
	}
	if p == "/api/v1/bookmarks" {
		if r.Method == http.MethodGet {
			q := r.URL.Query()
			page, e1 := queryInt(r, "page", 0)
			limit, e2 := queryInt(r, "limit", 50)
			if e1 != nil || e2 != nil || page < 0 || page > 1_000_000 || limit < 1 {
				server.WriteError(w, 400, "invalid_request", "invalid pagination")
				return true
			}
			effective := min(limit, 200)
			offset := int(page * effective)
			folderID, e3 := queryInt(r, "folder", 0)
			if e3 != nil || folderID < 0 {
				server.WriteError(w, 400, "invalid_request", "invalid folder filter")
				return true
			}
			var fav *bool
			if q.Has("favorite") {
				var v bool
				switch q.Get("favorite") {
				case "true":
					v = true
				case "false":
				default:
					server.WriteError(w, 400, "invalid_request", "favorite must be true or false")
					return true
				}
				fav = &v
			}
			items, err := a.bm.ListBookmarks(r.Context(), sc, bookmarks.ListQuery{Page: int(page), Limit: int(effective + 1), Offset: &offset, FolderID: folderID, URL: q.Get("url"), Search: strings.Fields(q.Get("q")), Tags: q["tag"], Favorite: fav})
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			hasMore := int64(len(items)) > effective
			if hasMore {
				items = items[:effective]
			}
			out := make([]map[string]any, 0, len(items))
			for _, b := range items {
				out = append(out, bookmarkJSON(b))
			}
			writeJSON(w, 200, map[string]any{"items": out, "page": page, "limit": effective, "hasMore": hasMore})
			return true
		}
		if r.Method == http.MethodPost {
			var in struct {
				URL     string    `json:"url"`
				Title   string    `json:"title"`
				Folders []int64   `json:"folders"`
				Tags    *[]string `json:"tags"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			b, err := a.bm.CreateBookmark(r.Context(), sc, bookmarks.BookmarkInput{URL: in.URL, Title: in.Title, Folders: in.Folders, Tags: in.Tags})
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			writeJSON(w, 201, bookmarkJSON(b))
			return true
		}
	}
	if p == "/api/v1/bookmarks/bulk-move" && r.Method == http.MethodPost {
		var in struct {
			BookmarkIDs    []int64 `json:"bookmarkIds"`
			SourceFolderID *int64  `json:"sourceFolderId"`
			FolderID       *int64  `json:"folderId"`
		}
		if !decodeJSON(w, r, &in) {
			return true
		}
		if in.SourceFolderID == nil || in.FolderID == nil {
			server.WriteError(w, 400, "invalid_request", "sourceFolderId and folderId are required")
			return true
		}
		if err := a.bm.MoveBookmarks(r.Context(), sc, in.BookmarkIDs, *in.SourceFolderID, *in.FolderID); err != nil {
			writeDomainError(w, err)
		} else {
			empty(w)
		}
		return true
	}
	if strings.HasPrefix(p, "/api/v1/bookmarks/") {
		id, e := requestID(r, "id")
		if e != nil {
			writeDomainError(w, bookmarks.ErrNotFound)
			return true
		}
		switch r.Method {
		case http.MethodGet:
			b, err := a.bm.GetBookmark(r.Context(), sc, id)
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, bookmarkJSON(b))
			}
			return true
		case http.MethodPatch:
			var in struct {
				URL     *string   `json:"url"`
				Title   *string   `json:"title"`
				Folders *[]int64  `json:"folders"`
				Tags    *[]string `json:"tags"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			b, err := a.bm.UpdateBookmark(r.Context(), sc, id, bookmarks.BookmarkUpdate{URL: in.URL, Title: in.Title, Folders: in.Folders, Tags: in.Tags})
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, bookmarkJSON(b))
			}
			return true
		case http.MethodDelete:
			err := a.bm.DeleteBookmark(r.Context(), sc, id)
			if err != nil {
				writeDomainError(w, err)
			} else {
				empty(w)
			}
			return true
		}
	}
	if r.Method == http.MethodDelete && strings.HasPrefix(p, "/api/v1/folders/") && strings.Contains(p, "/bookmarks/") {
		parts := strings.Split(strings.TrimPrefix(p, "/api/v1/folders/"), "/bookmarks/")
		if len(parts) == 2 {
			fid, e1 := strconv.ParseInt(parts[0], 10, 64)
			bid, e2 := strconv.ParseInt(parts[1], 10, 64)
			if e1 != nil || e2 != nil || fid == 0 || bid <= 0 {
				writeDomainError(w, bookmarks.ErrNotFound)
				return true
			}
			err := a.bm.RemovePlacement(r.Context(), sc, fid, bid)
			if err != nil {
				writeDomainError(w, err)
			} else {
				empty(w)
			}
			return true
		}
	}
	return false
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, jsonBodyLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, err)
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		server.WriteError(w, 400, "invalid_request", "request must contain one JSON value")
		return false
	}
	return true
}

func decodeRequiredJSON(w http.ResponseWriter, r *http.Request, dst any, required ...string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, jsonBodyLimit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, err)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		writeJSONError(w, err)
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		server.WriteError(w, 400, "invalid_request", "request must contain one JSON value")
		return false
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			server.WriteError(w, 400, "invalid_request", "request is missing a required field")
			return false
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return decodeJSON(w, r, dst)
}

func writeJSONError(w http.ResponseWriter, err error) {
	var max *http.MaxBytesError
	if errors.As(err, &max) {
		server.WriteError(w, 413, "body_too_large", "request body too large")
		return
	}
	server.WriteError(w, 400, "invalid_request", "invalid JSON request")
}

func writeDomainError(w http.ResponseWriter, err error) {
	var max *http.MaxBytesError
	if errors.As(err, &max) {
		server.WriteError(w, 413, "body_too_large", "request body too large")
		return
	}
	switch {
	case errors.Is(err, identity.ErrUnauthorized):
		server.WriteError(w, 401, "unauthenticated", "sign-in required")
	case errors.Is(err, identity.ErrNotFound), errors.Is(err, bookmarks.ErrNotFound):
		server.WriteError(w, 404, "not_found", "resource not found")
	case errors.Is(err, identity.ErrLastAdmin):
		server.WriteError(w, 409, "last_admin", "at least one active administrator must remain")
	case errors.Is(err, identity.ErrExists):
		server.WriteError(w, 409, "conflict", "resource conflicts with existing data")
	case errors.Is(err, linkcheck.ErrTooManyJobs):
		w.Header().Set("Retry-After", "30")
		server.WriteError(w, 429, "too_many_jobs", "too many link checks are already queued")
	case errors.Is(err, bookmarks.ErrConflict):
		server.WriteError(w, 409, "conflict", "resource conflicts with existing data")
	case errors.Is(err, bookmarks.ErrForbidden):
		server.WriteError(w, 403, "read_only", "operation is not permitted")
	case errors.Is(err, bookmarks.ErrLocked):
		w.Header().Set("Retry-After", "5")
		server.WriteError(w, 423, "locked", "bookmark collection is locked by a Floccus sync")
	case errors.Is(err, identity.ErrInvalid), errors.Is(err, bookmarks.ErrInvalid):
		server.WriteError(w, 400, "validation_failed", "request validation failed")
	default:
		server.WriteError(w, 500, "internal_error", "internal server error")
	}
}
