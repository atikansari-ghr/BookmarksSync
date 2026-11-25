package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/api"
	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/backup"
	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/health"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck"
	"github.com/atikansari-ghr/BookmarksSync/internal/notifications"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

const (
	origin = "https://bookmarks.example.test"
	pass   = "a long enough password"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type tenv struct {
	t                *testing.T
	db               *database.DB
	ids              *identity.Service
	bm               *bookmarks.Service
	lc               *linkcheck.Service
	backupManager    *backup.Manager
	notifications    *notifications.Service
	backupDataDir    string
	clock            *fakeClock
	api              *api.API
	h                http.Handler
	logs             *bytes.Buffer
	assetDir         string
	alice, bob, root identity.User
}

func newTEnv(t *testing.T) *tenv {
	return newTEnvWithCatalog(t, nil, nil)
}

func newTEnvWithCatalog(t *testing.T, updateCatalog func(context.Context, string) (updates.Catalog, error), updateApply func(context.Context, string) (string, error)) *tenv {
	return newTEnvWithOptions(t, updateCatalog, updateApply)
}

func newTEnvWithOptions(t *testing.T, updateCatalog func(context.Context, string) (updates.Catalog, error), updateApply func(context.Context, string) (string, error), modify ...func(*api.Options)) *tenv {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	ids := identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(clock.Now))
	bm := bookmarks.New(db.SQL(), bookmarks.WithClock(clock.Now))
	lc := linkcheck.NewService(db.SQL(), bm, ids, okChecker{}, linkcheck.WithClock(clock.Now))
	backupDataDir := t.TempDir()
	if err := config.SaveRuntime(config.RuntimePath(backupDataDir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	backupManager, err := backup.NewManager(backup.ManagerOptions{DB: db, DataDir: backupDataDir, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	notificationService := notifications.NewService(config.RuntimePath(backupDataDir), config.NotificationConfig{})
	log := slog.New(slog.NewTextHandler(logs, nil))
	authH, err := auth.New(auth.Options{Identity: ids, PublicOrigin: origin, Now: clock.Now, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	assetDir := filepath.Join(t.TempDir(), "branding")
	opts := api.Options{Auth: authH, Identity: ids, Bookmarks: bm, LinkChecks: lc, Backups: backupTestAdapter{manager: backupManager}, Notifications: notificationService, NotificationUsers: notifications.NewUserService(db.SQL(), notificationService, clock.Now), UpdateCatalog: updateCatalog, UpdateApply: updateApply, Now: clock.Now, Logger: log, Health: health.New(t.TempDir()), Readiness: db, AssetDir: assetDir,
		System: api.SystemInfo{Version: "test", Commit: "dddddddddddddddddddddddddddddddddddddddd", PublicOrigin: origin, PasskeysEnabled: false, OIDCProviders: []string{}}}
	for _, change := range modify {
		change(&opts)
	}
	a, err := api.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	h := server.NewHandler(server.Options{Version: version.Info{Version: "test"}, Ready: db, Logger: log, BodyLimit: api.BodyLimit,
		Register: func(r server.Registrar) { authH.Register(r); a.Register(r) }})
	e := &tenv{t: t, db: db, ids: ids, bm: bm, lc: lc, backupManager: backupManager, notifications: notificationService, backupDataDir: backupDataDir, clock: clock, api: a, h: h, logs: logs, assetDir: assetDir}
	ctx := context.Background()
	for _, u := range []struct {
		name  string
		admin bool
		dst   *identity.User
	}{{"alice", false, &e.alice}, {"bob", false, &e.bob}, {"root", true, &e.root}} {
		usr, err := ids.CreateUser(ctx, u.name, pass, u.admin)
		if err != nil {
			t.Fatal(err)
		}
		*u.dst = usr
	}
	return e
}

type mod func(*http.Request)

func withCookie(v string) mod {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: v}) }
}
func withCSRF(v string) mod         { return func(r *http.Request) { r.Header.Set("X-CSRF-Token", v) } }
func withHeader(k, v string) mod    { return func(r *http.Request) { r.Header.Set(k, v) } }
func withBearer(tok string) mod     { return withHeader("Authorization", "Bearer "+tok) }
func withContentType(ct string) mod { return withHeader("Content-Type", ct) }

func (e *tenv) do(method, path string, body any, mods ...mod) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
		ct = "application/json"
	}
	req := httptest.NewRequest(method, "http://bookmarks.example.test"+path, rd)
	req.RemoteAddr = "192.0.2.1:1000"
	req.Header.Set("Origin", origin)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return m
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	e, _ := decode(t, rec)["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// session signs u in and returns cookie and CSRF token.
type sess struct{ cookie, csrf string }

func (s sess) mods() []mod { return []mod{withCookie(s.cookie), withCSRF(s.csrf)} }

func (e *tenv) login(u identity.User) sess {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": u.Username, "password": pass})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", u.Username, rec.Code, rec.Body.String())
	}
	var c string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.SessionCookieName {
			c = ck.Value
		}
	}
	return sess{cookie: c, csrf: decode(e.t, rec)["csrfToken"].(string)}
}

func (e *tenv) token(u identity.User, scopes ...identity.Scope) string {
	e.t.Helper()
	_, secret, err := e.ids.CreateAPIToken(context.Background(), identity.NewAPIToken{UserID: u.ID, Label: "t", Scopes: scopes})
	if err != nil {
		e.t.Fatal(err)
	}
	return secret
}

var placeholder = regexp.MustCompile(`\{[^}]+\}`)

func fill(path string) string { return placeholder.ReplaceAllString(path, "1") }

func safe(m string) bool { return m == "GET" || m == "HEAD" }

func allScopesExcept(s identity.Scope) []identity.Scope {
	var out []identity.Scope
	for _, sc := range identity.TokenScopes() {
		if sc != s {
			out = append(out, sc)
		}
	}
	return out
}

// The auth matrix is driven by the same route table that feeds OpenAPI.
func TestEveryRouteEnforcesItsAuthRules(t *testing.T) {
	e := newTEnv(t)
	routes := e.api.Routes()
	if len(routes) < 40 {
		t.Fatalf("only %d routes", len(routes))
	}
	alice := e.login(e.alice)
	root := e.login(e.root)
	allTok := e.token(e.alice, identity.TokenScopes()...)
	adminTok := e.token(e.root, identity.TokenScopes()...)
	revoked := e.token(e.alice, identity.TokenScopes()...)
	toks, _ := e.ids.ListAPITokens(context.Background(), e.alice.ID)
	e.ids.RevokeAPIToken(context.Background(), e.alice.ID, toks[len(toks)-1].ID)
	carol, _ := e.ids.CreateUser(context.Background(), "carol", pass, false)
	suspendedTok := e.token(carol, identity.TokenScopes()...)
	e.ids.SetActive(context.Background(), "carol", false)

	for _, rt := range routes {
		path := fill(rt.Path)
		name := rt.Method + " " + rt.Path
		check := func(what string, rec *httptest.ResponseRecorder, status int, code string) {
			t.Helper()
			if rec.Code != status || (code != "" && errCode(t, rec) != code) {
				t.Errorf("%s: %s: %d %s, want %d %s", name, what, rec.Code, rec.Body.String(), status, code)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s: %s: Cache-Control %q", name, what, rec.Header().Get("Cache-Control"))
			}
		}
		if rt.Public {
			want := http.StatusOK
			if strings.Contains(rt.Path, "/presentation/assets/") {
				want = http.StatusNotFound // no uploaded asset is configured in this matrix fixture
			}
			check("public request", e.do(rt.Method, path, nil), want, "")
			continue
		}
		check("no credential", e.do(rt.Method, path, nil), 401, "unauthenticated")
		check("floccus basic credential", e.do(rt.Method, path, nil, withHeader("Authorization", "Basic YWxpY2U6Ym1zMS4xLnh4eA==")), 401, "unauthenticated")
		check("malformed bearer", e.do(rt.Method, path, nil, withHeader("Authorization", "Bearer")), 401, "")
		owner := alice
		if rt.Admin {
			owner = root
		}
		if !safe(rt.Method) {
			check("session without CSRF", e.do(rt.Method, path, nil, withCookie(owner.cookie)), 403, "csrf_failed")
		}
		switch {
		case rt.TokenScope == "" && !rt.AnyToken:
			check("token on a session-only route", e.do(rt.Method, path, nil, withBearer(allTok)), 403, "token_not_allowed")
			check("admin token on a session-only route", e.do(rt.Method, path, nil, withBearer(adminTok)), 403, "token_not_allowed")
		case rt.TokenScope != "":
			wrong := e.token(e.alice, allScopesExcept(identity.Scope(rt.TokenScope))...)
			check("token without the exact scope", e.do(rt.Method, path, nil, withBearer(wrong)), 403, "insufficient_scope")
			check("revoked token", e.do(rt.Method, path, nil, withBearer(revoked)), 401, "unauthenticated")
			check("suspended user's token", e.do(rt.Method, path, nil, withBearer(suspendedTok)), 401, "unauthenticated")
		case rt.AnyToken:
			check("revoked token", e.do(rt.Method, path, nil, withBearer(revoked)), 401, "unauthenticated")
		}
		if rt.Admin {
			check("non-admin session", e.do(rt.Method, path, nil, alice.mods()...), 403, "admin_required")
			if rt.TokenScope != "" || rt.AnyToken {
				t.Errorf("%s: admin route accepts tokens", name)
			}
		}
	}
	// Recent reauthentication: age every session past the window.
	e.clock.Advance(auth.ReauthWindow + time.Second)
	for _, rt := range routes {
		if !rt.Recent {
			continue
		}
		owner := alice
		if rt.Admin {
			owner = root
		}
		rec := e.do(rt.Method, fill(rt.Path), nil, owner.mods()...)
		if rec.Code != 403 || errCode(t, rec) != "reauthentication_required" {
			t.Errorf("%s %s with a stale session: %d %s", rt.Method, rt.Path, rec.Code, rec.Body.String())
		}
		if rt.TokenScope != "" || rt.AnyToken {
			t.Errorf("%s %s: recent-auth route accepts tokens", rt.Method, rt.Path)
		}
	}
}

// Required route policies from the brief and rulings.
func TestRoutePolicies(t *testing.T) {
	e := newTEnv(t)
	byKey := map[string]api.RouteInfo{}
	for _, r := range e.api.Routes() {
		byKey[r.Method+" "+r.Path] = r
	}
	want := map[string]api.RouteInfo{
		"GET /api/v1/me/notifications":                             {},
		"PATCH /api/v1/me/notifications":                           {},
		"POST /api/v1/me/notifications/bindings":                   {},
		"POST /api/v1/me/notifications/bindings/confirm":           {},
		"DELETE /api/v1/me/notifications/bindings/{kind}":          {},
		"GET /api/v1/me":                                           {AnyToken: true},
		"PATCH /api/v1/me/password":                                {Recent: true},
		"POST /api/v1/me/tokens":                                   {Recent: true},
		"POST /api/v1/me/connections":                              {Recent: true},
		"GET /api/v1/bookmarks":                                    {TokenScope: "bookmarks:read"},
		"POST /api/v1/bookmarks":                                   {TokenScope: "bookmarks:write"},
		"PUT /api/v1/bookmarks/{id}/favorite":                      {TokenScope: "bookmarks:write"},
		"DELETE /api/v1/bookmarks/{id}/favorite":                   {TokenScope: "bookmarks:write"},
		"DELETE /api/v1/trash/{type}/{id}":                         {Recent: true},
		"DELETE /api/v1/trash":                                     {Recent: true},
		"POST /api/v1/trash/{type}/{id}/restore":                   {TokenScope: "bookmarks:write"},
		"POST /api/v1/import":                                      {TokenScope: "bookmarks:write"},
		"GET /api/v1/export":                                       {TokenScope: "bookmarks:read"},
		"GET /api/v1/shares/owned":                                 {TokenScope: "shares:read"},
		"POST /api/v1/shares":                                      {TokenScope: "shares:manage"},
		"DELETE /api/v1/shares/{id}":                               {TokenScope: "shares:manage"},
		"GET /api/v1/admin/users":                                  {Admin: true},
		"PATCH /api/v1/admin/users/{id}":                           {Admin: true, Recent: true},
		"POST /api/v1/admin/users/{id}/password-reset":             {Admin: true, Recent: true},
		"GET /api/v1/admin/system":                                 {Admin: true},
		"GET /api/v1/admin/health":                                 {Admin: true},
		"GET /api/v1/admin/activity":                               {Admin: true},
		"GET /api/v1/admin/notifications":                          {Admin: true},
		"PUT /api/v1/admin/notifications":                          {Admin: true, Recent: true},
		"POST /api/v1/admin/notifications/test":                    {Admin: true, Recent: true},
		"GET /api/v1/admin/settings":                               {Admin: true},
		"PATCH /api/v1/admin/settings":                             {Admin: true, Recent: true},
		"GET /api/v1/admin/network-access":                         {Admin: true},
		"PATCH /api/v1/admin/network-access":                       {Admin: true, Recent: true},
		"GET /api/v1/admin/backups/destinations/{id}/recovery-key": {Admin: true, Recent: true},
		"GET /api/v1/admin/users/{id}/settings":                    {Admin: true},
		"PATCH /api/v1/admin/users/{id}/settings":                  {Admin: true, Recent: true},
		"GET /api/v1/auth/presentation":                            {Public: true},
		"DELETE /api/v1/admin/users/{id}/connections/{cid}":        {Admin: true, Recent: true},
	}
	for key, w := range want {
		got, ok := byKey[key]
		if !ok {
			t.Errorf("missing route %s", key)
			continue
		}
		if got.TokenScope != w.TokenScope || got.AnyToken != w.AnyToken || got.Admin != w.Admin || got.Recent != w.Recent {
			t.Errorf("%s = %+v, want %+v", key, got, w)
		}
	}
	for key := range byKey {
		if strings.HasPrefix(key, "POST /api/v1/admin") || strings.HasPrefix(key, "PATCH /api/v1/admin") || strings.HasPrefix(key, "DELETE /api/v1/admin") {
			if !byKey[key].Recent {
				t.Errorf("admin mutation %s without recent auth", key)
			}
		}
	}
}

func TestUnknownAndWrongMethodRoutesAreJSON(t *testing.T) {
	e := newTEnv(t)
	wrong := e.do(http.MethodPut, "/api/v1/bookmarks", nil)
	if wrong.Code != http.StatusMethodNotAllowed || errCode(t, wrong) != "method_not_allowed" {
		t.Fatalf("wrong method: %d %s", wrong.Code, wrong.Body.String())
	}
	unknown := e.do(http.MethodGet, "/api/v1/not-a-route", nil)
	if unknown.Code != http.StatusNotFound || errCode(t, unknown) != "not_found" {
		t.Fatalf("unknown route: %d %s", unknown.Code, unknown.Body.String())
	}
}

// Handlers never touch SQL: all data access goes through domain services.
func TestAPIPackageHasNoSQL(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	// Upper-case SQL keywords only, so English words in comments do not match.
	bad := regexp.MustCompile(`database/sql|\.SQL\(\)|\b(SELECT|INSERT INTO|UPDATE \w+ SET|DELETE FROM)\b`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := bad.FindIndex(src); loc != nil {
			t.Errorf("%s contains SQL or database access: %q", f, src[loc[0]:loc[1]])
		}
	}
}
