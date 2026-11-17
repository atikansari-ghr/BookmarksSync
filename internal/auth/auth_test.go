package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

const (
	origin    = "https://bookmarks.example.test"
	peer      = "192.0.2.10:5555"
	alicePass = "correct horse battery"
	bobPass   = "another long passphrase"
	carolPass = "suspended user passphrase"
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

// countingIdentity delegates to the real service and counts password verifications,
// so a test can prove every login path performs the (Argon2id) verification.
type countingIdentity struct {
	*identity.Service
	verifies atomic.Int32
	// gate, when set before requests start, runs inside VerifyPassword before the
	// real verification; tests use it to hold requests in flight.
	gate func()
}

func (c *countingIdentity) VerifyPassword(ctx context.Context, username, password string) (identity.User, error) {
	c.verifies.Add(1)
	if c.gate != nil {
		c.gate()
	}
	return c.Service.VerifyPassword(ctx, username, password)
}

type env struct {
	t     *testing.T
	ids   *identity.Service
	ci    *countingIdentity
	clock *fakeClock
	auth  *auth.Handler
	h     http.Handler
	logs  *bytes.Buffer
	alice identity.User
	db    *database.DB
}

func newEnv(t *testing.T, mods ...func(*auth.Options)) *env {
	t.Helper()
	return newEnvWith(t, nil, mods...)
}

// newEnvWith is newEnv with extra identity service options (instance key, relying party).
func newEnvWith(t *testing.T, idOpts []identity.Option, mods ...func(*auth.Options)) *env {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	ids := identity.New(db.SQL(), append([]identity.Option{identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(clock.Now)}, idOpts...)...)
	ctx := context.Background()
	alice, err := ids.CreateUser(ctx, "alice", alicePass, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ids.CreateUser(ctx, "bob", bobPass, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.CreateUser(ctx, "carol", carolPass, false); err != nil {
		t.Fatal(err)
	}
	if err := ids.SetActive(ctx, "carol", false); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	ci := &countingIdentity{Service: ids}
	opts := auth.Options{
		Identity:     ci,
		PublicOrigin: origin,
		Now:          clock.Now,
		Logger:       slog.New(slog.NewTextHandler(logs, nil)),
	}
	for _, m := range mods {
		m(&opts)
	}
	a, err := auth.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	sensitive := a.RequireSession(a.RequireRecentAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, u, ok := auth.FromContext(r.Context())
		if !ok {
			t.Error("no session in context")
		}
		server.WriteJSON(w, http.StatusOK, map[string]string{"user": u.Username})
	})))
	h := server.NewHandler(server.Options{
		Version: version.Info{Version: "test"},
		Ready:   db,
		Register: func(r server.Registrar) {
			a.Register(r)
			r.Handle("POST /api/v1/test/sensitive", sensitive)
		},
	})
	return &env{t: t, ids: ids, ci: ci, clock: clock, auth: a, h: h, logs: logs, alice: alice, db: db}
}

type reqMod func(*http.Request)

func withCookie(secret string) reqMod {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: secret}) }
}
func withCSRF(tok string) reqMod { return func(r *http.Request) { r.Header.Set("X-CSRF-Token", tok) } }
func withOrigin(o string) reqMod {
	return func(r *http.Request) {
		if o == "" {
			r.Header.Del("Origin")
		} else {
			r.Header.Set("Origin", o)
		}
	}
}
func withPeer(addr string) reqMod   { return func(r *http.Request) { r.RemoteAddr = addr } }
func withHeader(k, v string) reqMod { return func(r *http.Request) { r.Header.Set(k, v) } }

func (e *env) do(method, path string, body any, mods ...reqMod) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.serve(e.h, method, path, body, mods...)
}

// serve sends one request to h with the default origin and peer.
func (e *env) serve(h http.Handler, method, path string, body any, mods ...reqMod) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, "http://bookmarks.example.test"+path, rd)
	req.RemoteAddr = peer
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", origin)
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	return nil
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return m
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	e, _ := decode(t, rec)["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// login signs in and returns the session cookie value and CSRF token.
func (e *env) login(user, pass string, mods ...reqMod) (string, string) {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, mods...)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body.String())
	}
	c := sessionCookie(e.t, rec)
	if c == nil || c.Value == "" {
		e.t.Fatal("login set no session cookie")
	}
	tok, _ := decode(e.t, rec)["csrfToken"].(string)
	if tok == "" {
		e.t.Fatal("login returned no csrfToken")
	}
	return c.Value, tok
}

func (e *env) sessionStatus(secret string) int {
	e.t.Helper()
	return e.do("GET", "/api/v1/auth/session", nil, withCookie(secret)).Code
}

func TestLoginSuccessSetsHardenedHostCookie(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": alicePass})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	raw := rec.Header().Values("Set-Cookie")
	if len(raw) != 1 {
		t.Fatalf("Set-Cookie = %q, want exactly one", raw)
	}
	c := raw[0]
	if !strings.HasPrefix(c, "__Host-") {
		t.Errorf("cookie %q lacks __Host- prefix", c)
	}
	for _, attr := range []string{"Path=/", "Secure", "HttpOnly", "SameSite=Lax", "Max-Age="} {
		if !strings.Contains(c, attr) {
			t.Errorf("cookie %q missing %s", c, attr)
		}
	}
	if strings.Contains(strings.ToLower(c), "domain=") {
		t.Errorf("cookie %q must be host-only", c)
	}
	body := decode(t, rec)
	user, _ := body["user"].(map[string]any)
	if user["username"] != "alice" {
		t.Errorf("user = %v", user)
	}
	if strings.Contains(rec.Body.String(), sessionCookie(t, rec).Value) {
		t.Error("response body leaks the session secret")
	}
	if e.sessionStatus(sessionCookie(t, rec).Value) != http.StatusOK {
		t.Error("new session cookie does not authenticate")
	}
}

func TestLoginFailuresAreIndistinguishableAndDoPasswordWork(t *testing.T) {
	e := newEnv(t)
	cases := map[string][2]string{
		"wrong password": {"alice", "not the password!!"},
		"unknown user":   {"mallory", alicePass},
		"inactive user":  {"carol", carolPass},
		"empty username": {"", alicePass},
		"empty password": {"alice", ""},
		"overlong name":  {strings.Repeat("a", 500), alicePass},
	}
	var first string
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			before := e.ci.verifies.Load()
			// Distinct peers so the rate limiter does not interfere.
			rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": c[0], "password": c[1]},
				withPeer(fmt.Sprintf("198.51.100.%d:1", len(name))))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
			}
			if got := e.ci.verifies.Load() - before; got != 1 {
				t.Errorf("password verifications = %d, want 1 (comparable Argon2id work)", got)
			}
			if len(rec.Header().Values("Set-Cookie")) != 0 {
				t.Error("failed login set a cookie")
			}
			if first == "" {
				first = rec.Body.String()
			} else if rec.Body.String() != first {
				t.Errorf("body %q differs from %q", rec.Body.String(), first)
			}
		})
	}
}

func TestLoginRejectsMalformedAndOversizedBodiesWithoutWork(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("POST", "/api/v1/auth/login", "{not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d", rec.Code)
	}
	big := fmt.Sprintf(`{"username":"alice","password":%q}`, strings.Repeat("x", 64<<10))
	if rec := e.do("POST", "/api/v1/auth/login", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.ci.verifies.Load(); n != 0 {
		t.Errorf("verifications = %d, want 0", n)
	}
}

func TestLoginRotatesAwayAnyPresentedSession(t *testing.T) {
	e := newEnv(t)
	old, _ := e.login("alice", alicePass)
	// A planted/unknown cookie value must never become the session.
	planted := "attacker-chosen-session-value"
	fresh, _ := e.login("alice", alicePass, withCookie(old))
	if fresh == old {
		t.Fatal("login reused the presented session secret")
	}
	if e.sessionStatus(old) != http.StatusUnauthorized {
		t.Error("previous session still valid after re-login")
	}
	again, _ := e.login("alice", alicePass, withCookie(planted))
	if again == planted {
		t.Fatal("login adopted a planted cookie value")
	}
	if e.sessionStatus(fresh) != http.StatusOK || e.sessionStatus(again) != http.StatusOK {
		t.Error("fresh sessions should be valid")
	}
}

func TestCurrentSessionAndCSRFBootstrap(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/api/v1/auth/session", nil); rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthenticated" {
		t.Fatalf("anonymous session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/auth/csrf", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous csrf: %d", rec.Code)
	}
	secret, tok := e.login("alice", alicePass)
	rec := e.do("GET", "/api/v1/auth/session", nil, withCookie(secret))
	if rec.Code != http.StatusOK {
		t.Fatalf("session: %d", rec.Code)
	}
	body := decode(t, rec)
	if u, _ := body["user"].(map[string]any); u["username"] != "alice" {
		t.Errorf("user = %v", body["user"])
	}
	if body["csrfToken"] != tok {
		t.Errorf("session csrfToken %v != login token %v", body["csrfToken"], tok)
	}
	rec = e.do("GET", "/api/v1/auth/csrf", nil, withCookie(secret))
	if rec.Code != http.StatusOK || decode(t, rec)["csrfToken"] != tok {
		t.Errorf("csrf bootstrap: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPersonalAPITokenDoesNotAuthenticateSessionEndpoints(t *testing.T) {
	e := newEnv(t)
	_, tok, err := e.ids.CreateAPIToken(context.Background(), identity.NewAPIToken{
		UserID: e.alice.ID, Label: "cli", Scopes: []identity.Scope{identity.ScopeBookmarksRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/auth/session", "/api/v1/auth/sessions", "/api/v1/auth/csrf"} {
		if rec := e.do("GET", path, nil, withHeader("Authorization", "Bearer "+tok)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with API token: %d", path, rec.Code)
		}
	}
	if rec := e.do("GET", "/api/v1/auth/session", nil, withCookie(tok)); rec.Code != http.StatusUnauthorized {
		t.Errorf("API token as cookie: %d", rec.Code)
	}
}

func TestLogoutRevokesSessionAndClearsCookie(t *testing.T) {
	e := newEnv(t)
	secret, tok := e.login("alice", alicePass)
	rec := e.do("POST", "/api/v1/auth/logout", nil, withCookie(secret), withCSRF(tok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(t, rec)
	if c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("logout cookie = %+v, want cleared", c)
	}
	raw := strings.Join(rec.Header().Values("Set-Cookie"), ";")
	for _, attr := range []string{"Path=/", "Secure", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(raw, attr) {
			t.Errorf("clearing cookie %q missing %s", raw, attr)
		}
	}
	if e.sessionStatus(secret) != http.StatusUnauthorized {
		t.Error("session still valid after logout")
	}
}

func TestIdleExpiry(t *testing.T) {
	e := newEnv(t)
	secret, _ := e.login("alice", alicePass)
	e.clock.Advance(11 * time.Hour)
	if e.sessionStatus(secret) != http.StatusOK {
		t.Fatal("session should survive 11h idle")
	}
	e.clock.Advance(11 * time.Hour) // activity above reset the idle window
	if e.sessionStatus(secret) != http.StatusOK {
		t.Fatal("activity should extend the idle window")
	}
	e.clock.Advance(12*time.Hour + time.Second)
	if e.sessionStatus(secret) != http.StatusUnauthorized {
		t.Fatal("session should expire after 12h idle")
	}
}

func TestAbsoluteExpiryDespiteActivity(t *testing.T) {
	e := newEnv(t)
	secret, _ := e.login("alice", alicePass)
	for elapsed := time.Duration(0); elapsed < 30*24*time.Hour-11*time.Hour; elapsed += 11 * time.Hour {
		e.clock.Advance(11 * time.Hour)
		if e.sessionStatus(secret) != http.StatusOK {
			t.Fatalf("session died early at %v", elapsed+11*time.Hour)
		}
	}
	e.clock.Advance(11 * time.Hour)
	if e.sessionStatus(secret) != http.StatusUnauthorized {
		t.Fatal("session should end at the 30-day absolute limit")
	}
}

func TestSessionListAndImmediateRevoke(t *testing.T) {
	e := newEnv(t)
	a, aTok := e.login("alice", alicePass, withHeader("User-Agent", "laptop"))
	b, _ := e.login("alice", alicePass, withHeader("User-Agent", "phone"))
	bobSecret, _ := e.login("bob", bobPass)

	rec := e.do("GET", "/api/v1/auth/sessions", nil, withCookie(a))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), a) || strings.Contains(rec.Body.String(), b) {
		t.Fatal("session list leaks secrets")
	}
	var list struct {
		Sessions []struct {
			ID      int64  `json:"id"`
			Label   string `json:"label"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want alice's two", list.Sessions)
	}
	var phoneID int64
	for _, s := range list.Sessions {
		if s.Label == "laptop" && !s.Current || s.Label == "phone" && s.Current {
			t.Errorf("current flag wrong: %+v", s)
		}
		if s.Label == "phone" {
			phoneID = s.ID
		}
	}

	// Bob cannot revoke Alice's session.
	bobRec := e.do("GET", "/api/v1/auth/session", nil, withCookie(bobSecret))
	bobTok, _ := decode(t, bobRec)["csrfToken"].(string)
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/auth/sessions/%d", phoneID), nil, withCookie(bobSecret), withCSRF(bobTok)); rec.Code != http.StatusNotFound {
		t.Errorf("cross-user revoke: %d", rec.Code)
	}
	if e.sessionStatus(b) != http.StatusOK {
		t.Fatal("cross-user revoke had an effect")
	}
	if rec := e.do("DELETE", "/api/v1/auth/sessions/notanumber", nil, withCookie(a), withCSRF(aTok)); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: %d", rec.Code)
	}

	rec = e.do("DELETE", fmt.Sprintf("/api/v1/auth/sessions/%d", phoneID), nil, withCookie(a), withCSRF(aTok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if e.sessionStatus(b) != http.StatusUnauthorized {
		t.Error("revoked session still usable")
	}
	if e.sessionStatus(a) != http.StatusOK {
		t.Error("revoking another session ended the current one")
	}
}

func TestRevokingCurrentSessionClearsCookie(t *testing.T) {
	e := newEnv(t)
	a, tok := e.login("alice", alicePass)
	sess := decode(t, e.do("GET", "/api/v1/auth/session", nil, withCookie(a)))["session"].(map[string]any)
	rec := e.do("DELETE", fmt.Sprintf("/api/v1/auth/sessions/%.0f", sess["id"].(float64)), nil, withCookie(a), withCSRF(tok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke current: %d", rec.Code)
	}
	if c := sessionCookie(t, rec); c == nil || c.MaxAge >= 0 {
		t.Errorf("cookie not cleared: %+v", c)
	}
	if e.sessionStatus(a) != http.StatusUnauthorized {
		t.Error("current session still valid")
	}
}

func TestSuspensionRevokesSessionsImmediately(t *testing.T) {
	e := newEnv(t)
	secret, _ := e.login("alice", alicePass)
	if err := e.ids.SetActive(context.Background(), "alice", false); err != nil {
		t.Fatal(err)
	}
	if e.sessionStatus(secret) != http.StatusUnauthorized {
		t.Fatal("suspended user's session still valid")
	}
	if err := e.ids.SetActive(context.Background(), "alice", true); err != nil {
		t.Fatal(err)
	}
	if e.sessionStatus(secret) != http.StatusUnauthorized {
		t.Fatal("reactivation revived an old session")
	}
}

func TestMutationsRequireSessionBoundCSRFToken(t *testing.T) {
	e := newEnv(t)
	secret, tok := e.login("alice", alicePass)
	_, bobTok := e.login("bob", bobPass)
	for name, mods := range map[string][]reqMod{
		"missing token":       {withCookie(secret)},
		"wrong token":         {withCookie(secret), withCSRF("bogus")},
		"other session token": {withCookie(secret), withCSRF(bobTok)},
		"token as secret":     {withCookie(secret), withCSRF(secret)},
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.do("POST", "/api/v1/auth/logout", nil, mods...)
			if rec.Code != http.StatusForbidden || errCode(t, rec) != "csrf_failed" {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	if e.sessionStatus(secret) != http.StatusOK {
		t.Fatal("rejected logout still revoked the session")
	}
	if tok == secret || strings.Contains(secret, tok) {
		t.Fatal("csrf token must not reveal the session secret")
	}
}

func TestMutationsRequireExactSameOrigin(t *testing.T) {
	e := newEnv(t)
	secret, tok := e.login("alice", alicePass)
	cases := map[string][]reqMod{
		"missing origin":   {withOrigin("")},
		"cross site":       {withOrigin("https://evil.example")},
		"null origin":      {withOrigin("null")},
		"http downgrade":   {withOrigin("http://bookmarks.example.test")},
		"other port":       {withOrigin("https://bookmarks.example.test:8443")},
		"suffix trick":     {withOrigin("https://bookmarks.example.test.evil.example")},
		"spoofed host":     {withOrigin("https://evil.example"), func(r *http.Request) { r.Host = "evil.example" }},
		"forwarded header": {withOrigin("https://evil.example"), withHeader("X-Forwarded-Host", "evil.example"), withHeader("Forwarded", "host=evil.example;proto=https")},
	}
	for name, mods := range cases {
		t.Run(name, func(t *testing.T) {
			m := append([]reqMod{withCookie(secret), withCSRF(tok)}, mods...)
			if rec := e.do("POST", "/api/v1/auth/logout", nil, m...); rec.Code != http.StatusForbidden || errCode(t, rec) != "origin_rejected" {
				t.Errorf("logout: %d %s", rec.Code, rec.Body.String())
			}
			before := e.ci.verifies.Load()
			lm := append([]reqMod{withPeer("203.0.113.9:1")}, mods...)
			if rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": alicePass}, lm...); rec.Code != http.StatusForbidden {
				t.Errorf("login: %d", rec.Code)
			}
			if e.ci.verifies.Load() != before {
				t.Error("cross-origin login did password work")
			}
		})
	}
	if e.sessionStatus(secret) != http.StatusOK {
		t.Fatal("a cross-origin request revoked the session")
	}
}

func TestReauthenticationAge(t *testing.T) {
	e := newEnv(t)
	secret, tok := e.login("alice", alicePass)
	sensitive := func(s, tk string) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/test/sensitive", nil, withCookie(s), withCSRF(tk))
	}
	e.clock.Advance(4 * time.Minute)
	if rec := sensitive(secret, tok); rec.Code != http.StatusOK {
		t.Fatalf("fresh login should satisfy recent auth: %d %s", rec.Code, rec.Body.String())
	}
	e.clock.Advance(2 * time.Minute)
	rec := sensitive(secret, tok)
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "reauthentication_required" {
		t.Fatalf("stale auth: %d %s", rec.Code, rec.Body.String())
	}

	reauth := func(pw string, mods ...reqMod) *httptest.ResponseRecorder {
		m := append([]reqMod{withCookie(secret), withCSRF(tok)}, mods...)
		return e.do("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": pw}, m...)
	}
	if rec := reauth(alicePass, withCSRF("")); rec.Code != http.StatusForbidden {
		t.Errorf("reauth without csrf: %d", rec.Code)
	}
	if rec := reauth("wrong password!!"); rec.Code != http.StatusUnauthorized || errCode(t, rec) != "invalid_credentials" {
		t.Fatalf("wrong reauth: %d %s", rec.Code, rec.Body.String())
	}
	if e.sessionStatus(secret) != http.StatusOK {
		t.Fatal("failed reauthentication must not end the session")
	}
	rec = reauth(alicePass)
	if rec.Code != http.StatusOK {
		t.Fatalf("reauth: %d %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(t, rec)
	newTok, _ := decode(t, rec)["csrfToken"].(string)
	if c == nil || c.Value == secret || newTok == "" || newTok == tok {
		t.Fatalf("reauth must rotate the session and CSRF token: cookie=%+v tok=%q", c, newTok)
	}
	if e.sessionStatus(secret) != http.StatusUnauthorized {
		t.Error("pre-reauth secret still valid")
	}
	if rec := sensitive(c.Value, tok); rec.Code != http.StatusForbidden {
		t.Errorf("old csrf token accepted after rotation: %d", rec.Code)
	}
	if rec := sensitive(c.Value, newTok); rec.Code != http.StatusOK {
		t.Errorf("after reauth: %d %s", rec.Code, rec.Body.String())
	}
}

func rateLimited(limits auth.Limits) func(*auth.Options) {
	return func(o *auth.Options) { o.Limits = limits }
}

func (e *env) tryLogin(user, pass, from string, mods ...reqMod) *httptest.ResponseRecorder {
	e.t.Helper()
	m := append([]reqMod{withPeer(from)}, mods...)
	return e.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, m...)
}

// Failures against one username from an attacker's address must not lock the account
// out for its owner at another address; the owner's own failures and the per-peer
// threshold still apply.
func TestUsernameFailuresAreScopedToTheDirectPeer(t *testing.T) {
	e := newEnv(t, rateLimited(auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 3, PeerFailures: 5}))
	attacker, owner := "203.0.113.66:4000", "198.51.100.7:5000"

	for i, name := range []string{"alice", "ALICE", " Alice "} {
		if rec := e.tryLogin(name, "wrong password!!", attacker); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attacker attempt %d: %d", i, rec.Code)
		}
	}
	before := e.ci.verifies.Load()
	rec := e.tryLogin("alice", alicePass, attacker)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("attacker should be blocked for alice (normalized): %d", rec.Code)
	}
	// Forwarded headers naming the owner's address change nothing: the key is the
	// connection's peer.
	if rec := e.tryLogin("alice", alicePass, attacker, withHeader("X-Forwarded-For", "198.51.100.7"),
		withHeader("X-Real-IP", "198.51.100.7"), withHeader("Forwarded", "for=198.51.100.7")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded headers moved the attacker's key: %d", rec.Code)
	}
	if e.ci.verifies.Load() != before {
		t.Error("rate-limited attempts still did password work")
	}

	if rec := e.tryLogin("alice", alicePass, owner); rec.Code != http.StatusOK {
		t.Fatalf("remote failures locked the owner out: %d %s", rec.Code, rec.Body.String())
	}

	// The owner's own failures still block alice from the owner's address.
	for i := 0; i < 3; i++ {
		if rec := e.tryLogin("Alice", "wrong password!!", owner); rec.Code != http.StatusUnauthorized {
			t.Fatalf("owner failure %d: %d", i, rec.Code)
		}
	}
	before = e.ci.verifies.Load()
	if rec := e.tryLogin("alice", alicePass, owner); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same-peer failures should block: %d", rec.Code)
	}
	if e.ci.verifies.Load() != before {
		t.Error("blocked attempt did password work")
	}
	// Other usernames from that address are still under the peer threshold.
	if rec := e.tryLogin("bob", bobPass, owner); rec.Code != http.StatusOK {
		t.Fatalf("bob from owner's address: %d", rec.Code)
	}

	e.clock.Advance(15*time.Minute + time.Second)
	if rec := e.tryLogin("alice", alicePass, attacker); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d", rec.Code)
	}
}

func TestPeerFailureThresholdBlocksEveryUsername(t *testing.T) {
	e := newEnv(t, rateLimited(auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 3, PeerFailures: 5}))
	sprayer := "203.0.113.50:4000"
	for i := 0; i < 5; i++ {
		if rec := e.tryLogin(fmt.Sprintf("user%d", i), "wrong password!!", sprayer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("spray %d: %d", i, rec.Code)
		}
	}
	before := e.ci.verifies.Load()
	rec := e.tryLogin("bob", bobPass, sprayer, withHeader("X-Forwarded-For", "192.0.2.200"), withHeader("X-Real-IP", "192.0.2.201"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("peer over threshold: %d", rec.Code)
	}
	if e.ci.verifies.Load() != before {
		t.Error("blocked attempt did password work")
	}
	if rec := e.tryLogin("bob", bobPass, "203.0.113.51:4000"); rec.Code != http.StatusOK {
		t.Fatalf("other peer should still log in: %d", rec.Code)
	}
	e.clock.Advance(15*time.Minute + time.Second)
	if rec := e.tryLogin("bob", bobPass, sprayer); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d", rec.Code)
	}
}

func TestSuccessfulLoginsAreNotCountedAsFailures(t *testing.T) {
	e := newEnv(t, rateLimited(auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 2, PeerFailures: 3}))
	for i := 0; i < 6; i++ {
		if rec := e.tryLogin("alice", alicePass, peer); rec.Code != http.StatusOK {
			t.Fatalf("successful login %d: %d", i, rec.Code)
		}
	}
	if rec := e.tryLogin("alice", "wrong password!!", peer); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first failure: %d", rec.Code)
	}
	if rec := e.tryLogin("alice", alicePass, peer); rec.Code != http.StatusOK {
		t.Fatalf("one failure plus successes must not block: %d", rec.Code)
	}
}

// holdVerifications makes every VerifyPassword call report on entered and then block
// until release is called. Cleanup releases and waits for wg before the database closes.
func (e *env) holdVerifications(wg *sync.WaitGroup) (entered chan struct{}, release func()) {
	entered = make(chan struct{}, 256)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	e.ci.gate = func() {
		entered <- struct{}{}
		<-gate
	}
	e.t.Cleanup(func() { release(); wg.Wait() })
	return entered, release
}

// assertConcurrencyCap starts n requests at once while VerifyPassword is held, and
// proves that exactly limit of them reach it: the rest are refused with 429 while the
// first limit are still in flight. Released requests must then fail with 401.
func assertConcurrencyCap(t *testing.T, e *env, n, limit int, send func(i int) int) {
	t.Helper()
	var wg sync.WaitGroup
	entered, release := e.holdVerifications(&wg)
	results := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- send(i)
		}(i)
	}
	inside, refused := 0, 0
	timeout := time.After(15 * time.Second)
	for refused < n-limit || inside < limit {
		select {
		case <-entered:
			inside++
			if inside > limit {
				t.Fatalf("%d concurrent requests reached VerifyPassword; the limit is %d", inside, limit)
			}
		case code := <-results:
			refused++
			if code != http.StatusTooManyRequests {
				t.Fatalf("request finished with %d while %d were held; want 429", code, inside)
			}
		case <-timeout:
			t.Fatalf("timed out with %d in VerifyPassword and %d refused", inside, refused)
		}
	}
	release()
	for i := 0; i < limit; i++ {
		select {
		case code := <-results:
			if code != http.StatusUnauthorized {
				t.Errorf("released request: %d, want 401", code)
			}
		case <-timeout:
			t.Fatal("released requests did not finish")
		}
	}
	if got := int(e.ci.verifies.Load()); got != limit {
		t.Errorf("VerifyPassword calls = %d, want %d", got, limit)
	}
}

func TestConcurrentLoginsCannotExceedThePairThreshold(t *testing.T) {
	e := newEnv(t, rateLimited(auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 3, PeerFailures: 50}))
	assertConcurrencyCap(t, e, 12, 3, func(int) int {
		return e.tryLogin("alice", "wrong password!!", peer).Code
	})
	if rec := e.tryLogin("alice", alicePass, peer); rec.Code != http.StatusTooManyRequests {
		t.Errorf("three recorded failures should block: %d", rec.Code)
	}
}

func TestConcurrentLoginsCannotExceedThePeerThreshold(t *testing.T) {
	e := newEnv(t, rateLimited(auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 50, PeerFailures: 4}))
	assertConcurrencyCap(t, e, 12, 4, func(i int) int {
		return e.tryLogin(fmt.Sprintf("user%d", i), "wrong password!!", peer).Code
	})
}

func TestConcurrentReauthenticationCannotExceedTheThreshold(t *testing.T) {
	e := newEnv(t, rateLimited(auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 2, PeerFailures: 50}))
	secret, tok := e.login("alice", alicePass)
	e.ci.verifies.Store(0)
	assertConcurrencyCap(t, e, 10, 2, func(int) int {
		return e.do("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": "wrong password!!"},
			withCookie(secret), withCSRF(tok)).Code
	})
}

func TestLimiterReservationOutcomes(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	l := auth.NewLimiter(auth.Limits{MaxKeys: 16, Window: time.Minute, UserFailures: 2, PeerFailures: 10}, clock.Now)
	reserve := func() *auth.Reservation {
		t.Helper()
		r, wait := l.Reserve("alice", "10.0.0.1:1")
		if (r == nil) != (wait > 0) {
			t.Fatalf("Reserve returned reservation %v with wait %v", r, wait)
		}
		return r
	}
	a, b := reserve(), reserve()
	if a == nil || b == nil {
		t.Fatal("two reservations should fit a threshold of 2")
	}
	if reserve() != nil {
		t.Fatal("a third in-flight reservation exceeded the threshold")
	}
	a.Cancel()
	a.Fail() // finalizing twice is a no-op
	c := reserve()
	if c == nil {
		t.Fatal("cancel did not release capacity")
	}
	b.Success()
	c.Success()
	for i := 0; i < 5; i++ {
		r := reserve()
		if r == nil {
			t.Fatalf("successes or cancels were counted as failures (round %d)", i)
		}
		r.Success()
	}
	if n := l.Len(); n != 0 {
		t.Errorf("tracked keys after only successes = %d, want 0", n)
	}
	reserve().Fail()
	reserve().Fail()
	if r, wait := l.Reserve("ALICE ", "10.0.0.1:1"); r != nil || wait <= 0 || wait > time.Minute {
		t.Fatalf("two failures should block the normalized pair: %v %v", r, wait)
	}
	if r, _ := l.Reserve("alice", "10.0.0.2:1"); r == nil {
		t.Fatal("failures from one peer blocked another peer")
	}
	clock.Advance(time.Minute + time.Second)
	if reserve() == nil {
		t.Fatal("expired window still blocks")
	}
}

func TestLimiterStorageIsBounded(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	l := auth.NewLimiter(auth.Limits{MaxKeys: 16, Window: time.Minute, UserFailures: 3, PeerFailures: 3}, clock.Now)
	for i := 0; i < 5000; i++ {
		r, _ := l.Reserve(fmt.Sprintf("user-%d-%s", i, strings.Repeat("x", i%300)), fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255))
		if r == nil {
			t.Fatalf("flood reservation %d refused", i)
		}
		r.Fail()
		if n := l.Len(); n > 16 {
			t.Fatalf("tracked keys = %d after %d failures, cap is 16", n, i+1)
		}
	}
	// The most recently failing keys are still tracked after the flood.
	for i := 0; i < 3; i++ {
		r, _ := l.Reserve("target", "10.200.0.1:1")
		r.Fail()
	}
	if r, _ := l.Reserve("TARGET", "10.200.0.1:1"); r != nil {
		t.Error("recent pair failures were evicted")
	}
	if r, _ := l.Reserve("someone", "10.200.0.1:1"); r != nil {
		t.Error("recent peer failures were evicted")
	}
	clock.Advance(time.Minute + time.Second)
	if r, _ := l.Reserve("target", "10.200.0.1:1"); r == nil {
		t.Error("expired window still blocks")
	}
}

// Buckets with reservations still in flight must survive floods and window expiry;
// when every slot is in flight, new keys are refused rather than evicting them.
func TestLimiterNeverEvictsInFlightBuckets(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	l := auth.NewLimiter(auth.Limits{MaxKeys: 4, Window: time.Minute, UserFailures: 1, PeerFailures: 1}, clock.Now)
	held, _ := l.Reserve("alice", "10.0.0.1:1")
	if held == nil {
		t.Fatal("first reservation refused")
	}
	for i := 0; i < 200; i++ {
		r, _ := l.Reserve(fmt.Sprintf("flood-%d", i), fmt.Sprintf("10.1.%d.%d:1", i>>8, i&255))
		if r == nil {
			t.Fatalf("flood %d refused although two slots are free", i)
		}
		r.Fail()
		if n := l.Len(); n > 4 {
			t.Fatalf("tracked keys = %d, cap is 4", n)
		}
		if i == 100 {
			clock.Advance(2 * time.Minute) // in-flight buckets outlive their window too
		}
	}
	if r, _ := l.Reserve("alice", "10.0.0.1:1"); r != nil {
		t.Fatal("in-flight pair bucket was evicted: a second reservation exceeded the threshold")
	}
	if r, _ := l.Reserve("bob", "10.0.0.1:1"); r != nil {
		t.Fatal("in-flight peer bucket was evicted: a second reservation exceeded the threshold")
	}
	held.Fail()
	if r, _ := l.Reserve("alice", "10.0.0.1:1"); r != nil {
		t.Fatal("the held reservation's failure was lost")
	}

	// Fill the table with in-flight reservations: new keys are refused, not evicted.
	full := auth.NewLimiter(auth.Limits{MaxKeys: 4, Window: time.Minute, UserFailures: 5, PeerFailures: 5}, clock.Now)
	a, _ := full.Reserve("a", "10.2.0.1:1")
	b, _ := full.Reserve("b", "10.2.0.2:1")
	if a == nil || b == nil {
		t.Fatal("setup reservations refused")
	}
	if r, wait := full.Reserve("c", "10.2.0.3:1"); r != nil || wait <= 0 {
		t.Fatalf("a full in-flight table admitted new keys: %v %v", r, wait)
	}
	if again, _ := full.Reserve("a", "10.2.0.1:1"); again == nil {
		t.Fatal("existing in-flight keys should still admit up to their threshold")
	} else {
		again.Cancel()
	}
	a.Cancel()
	if r, _ := full.Reserve("c", "10.2.0.3:1"); r == nil {
		t.Fatal("finished reservation did not free its slots")
	}
	b.Cancel()
}

// Making room for one key of a reservation must not evict the other key, which would
// erase that key's failure history (here: the pair bucket outlives its peer bucket).
func TestLimiterKeepsTheReservingKeysWhenMakingRoom(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	l := auth.NewLimiter(auth.Limits{MaxKeys: 4, Window: time.Minute, UserFailures: 2, PeerFailures: 10}, clock.Now)
	fail := func(user, from string) {
		t.Helper()
		r, _ := l.Reserve(user, from)
		if r == nil {
			t.Fatalf("Reserve(%s, %s) refused", user, from)
		}
		r.Fail()
	}
	fail("bob", "10.0.0.1:1") // starts the peer window for 10.0.0.1
	clock.Advance(50 * time.Second)
	fail("alice", "10.0.0.1:1") // alice's pair window starts later
	clock.Advance(11 * time.Second)
	// The peer and bob's pair have expired; alice's pair has one live failure.
	fail("x", "10.0.0.9:1") // prunes the expired keys
	fail("y", "10.0.0.9:1") // table is now full; alice's pair is least recently updated
	fail("alice", "10.0.0.1:1")
	if r, _ := l.Reserve("alice", "10.0.0.1:1"); r != nil {
		t.Fatal("alice's earlier failure was evicted while reserving her attempt")
	}
}

// The auth handler must mark its own responses no-store, independent of the server's
// base headers, because they carry session state, CSRF tokens and account details.
func TestAuthResponsesAreNeverStored(t *testing.T) {
	e := newEnv(t, rateLimited(auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 1, PeerFailures: 50}))
	bare := http.NewServeMux()
	e.auth.Register(bare)
	bare.Handle("POST /api/v1/test/sensitive", e.auth.RequireSession(e.auth.RequireRecentAuth(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))))
	check := func(name string, wantStatus int, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != wantStatus {
			t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, wantStatus, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
	}
	creds := func(u, p string) map[string]string { return map[string]string{"username": u, "password": p} }

	rec := e.serve(bare, "POST", "/api/v1/auth/login", creds("alice", alicePass))
	check("login success", http.StatusOK, rec)
	secret := sessionCookie(t, rec).Value
	tok, _ := decode(t, rec)["csrfToken"].(string)
	session, csrf := withCookie(secret), withCSRF(tok)

	check("login failure", http.StatusUnauthorized, e.serve(bare, "POST", "/api/v1/auth/login", creds("bob", "wrong password!!"), withPeer("198.51.100.1:1")))
	check("login rate limited", http.StatusTooManyRequests, e.serve(bare, "POST", "/api/v1/auth/login", creds("bob", bobPass), withPeer("198.51.100.1:1")))
	check("login cross origin", http.StatusForbidden, e.serve(bare, "POST", "/api/v1/auth/login", creds("alice", alicePass), withOrigin("https://evil.example")))
	check("login malformed", http.StatusBadRequest, e.serve(bare, "POST", "/api/v1/auth/login", "{nope"))
	check("session", http.StatusOK, e.serve(bare, "GET", "/api/v1/auth/session", nil, session))
	check("session anonymous", http.StatusUnauthorized, e.serve(bare, "GET", "/api/v1/auth/session", nil))
	check("session unknown cookie", http.StatusUnauthorized, e.serve(bare, "GET", "/api/v1/auth/session", nil, withCookie("stale")))
	check("csrf", http.StatusOK, e.serve(bare, "GET", "/api/v1/auth/csrf", nil, session))
	check("sessions", http.StatusOK, e.serve(bare, "GET", "/api/v1/auth/sessions", nil, session))
	check("revoke unknown", http.StatusNotFound, e.serve(bare, "DELETE", "/api/v1/auth/sessions/999999", nil, session, csrf))
	check("logout without csrf", http.StatusForbidden, e.serve(bare, "POST", "/api/v1/auth/logout", nil, session))
	e.clock.Advance(6 * time.Minute)
	check("stale recent auth", http.StatusForbidden, e.serve(bare, "POST", "/api/v1/test/sensitive", nil, session, csrf))
	check("reauth wrong", http.StatusUnauthorized, e.serve(bare, "POST", "/api/v1/auth/reauthenticate", map[string]string{"password": "nope nope nope"}, session, csrf, withPeer("198.51.100.2:1")))
	rec = e.serve(bare, "POST", "/api/v1/auth/reauthenticate", map[string]string{"password": alicePass}, session, csrf)
	check("reauth success", http.StatusOK, rec)
	secret = sessionCookie(t, rec).Value
	tok, _ = decode(t, rec)["csrfToken"].(string)
	session, csrf = withCookie(secret), withCSRF(tok)
	check("recent auth ok", http.StatusNoContent, e.serve(bare, "POST", "/api/v1/test/sensitive", nil, session, csrf))
	check("logout", http.StatusNoContent, e.serve(bare, "POST", "/api/v1/auth/logout", nil, session, csrf))
}

func TestAuditLogsExcludeSecrets(t *testing.T) {
	e := newEnv(t)
	secret, tok := e.login("alice", alicePass)
	e.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "wrong password!!"})
	e.do("POST", "/api/v1/auth/logout", nil, withCookie(secret), withCSRF(tok))
	logs := e.logs.String()
	if !strings.Contains(logs, "login") {
		t.Errorf("expected structured login audit events, got %q", logs)
	}
	for _, s := range []string{alicePass, "wrong password!!", secret, tok} {
		if strings.Contains(logs, s) {
			t.Errorf("log contains secret %q", s)
		}
	}
}

func TestNewRejectsInvalidOrigin(t *testing.T) {
	for _, o := range []string{"", "bookmarks.example.test", "https://bookmarks.example.test/path", "http://bookmarks.example.test"} {
		if _, err := auth.New(auth.Options{Identity: &countingIdentity{}, PublicOrigin: o}); err == nil {
			t.Errorf("New accepted origin %q", o)
		}
	}
}
