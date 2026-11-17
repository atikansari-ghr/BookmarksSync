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
}

func (c *countingIdentity) VerifyPassword(ctx context.Context, username, password string) (identity.User, error) {
	c.verifies.Add(1)
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
}

func newEnv(t *testing.T, mods ...func(*auth.Options)) *env {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	ids := identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(clock.Now))
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
	return &env{t: t, ids: ids, ci: ci, clock: clock, auth: a, h: h, logs: logs, alice: alice}
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
	e.h.ServeHTTP(rec, req)
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

func TestLoginRateLimitedPerUsernameAndPeer(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	e := newEnv(t, func(o *auth.Options) {
		o.Limits = auth.Limits{MaxKeys: 100, Window: 15 * time.Minute, UserFailures: 3, PeerFailures: 5}
		o.Now = clock.Now
	})
	try := func(user, pass, from string) int {
		return e.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, withPeer(from)).Code
	}
	// Per-username: failures from many peers, case/space variants count together.
	for i, name := range []string{"alice", "ALICE", " Alice "} {
		if code := try(name, "wrong password!!", fmt.Sprintf("198.51.100.%d:1", i+1)); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	before := e.ci.verifies.Load()
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": alicePass}, withPeer("198.51.100.99:1"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("locked username: %d", rec.Code)
	}
	if e.ci.verifies.Load() != before {
		t.Error("rate-limited attempt still did password work")
	}
	// Per-peer: one peer spraying many usernames; forwarded headers are ignored.
	sprayer := "203.0.113.50:4000"
	for i := 0; i < 5; i++ {
		try(fmt.Sprintf("user%d", i), "wrong password!!", sprayer)
	}
	rec = e.do("POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": bobPass},
		withPeer(sprayer), withHeader("X-Forwarded-For", "192.0.2.200"), withHeader("X-Real-IP", "192.0.2.201"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked peer: %d", rec.Code)
	}
	if code := try("bob", bobPass, "203.0.113.51:4000"); code != http.StatusOK {
		t.Fatalf("other peer should still log in: %d", code)
	}
	// Window expiry releases both.
	clock.Advance(15*time.Minute + time.Second)
	e.clock.Advance(15*time.Minute + time.Second)
	if code := try("alice", alicePass, sprayer); code != http.StatusOK {
		t.Fatalf("after window: %d", code)
	}
}

func TestLimiterStorageIsBounded(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	l := auth.NewLimiter(auth.Limits{MaxKeys: 16, Window: time.Minute, UserFailures: 3, PeerFailures: 3}, clock.Now)
	for i := 0; i < 5000; i++ {
		l.Fail(fmt.Sprintf("user-%d-%s", i, strings.Repeat("x", i%300)), fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255))
		if n := l.Len(); n > 16 {
			t.Fatalf("tracked keys = %d after %d failures, cap is 16", n, i+1)
		}
	}
	// The most recently failing key is still tracked after the flood.
	for i := 0; i < 3; i++ {
		l.Fail("target", "10.200.0.1:1")
	}
	if ok, _ := l.Allow("target", "10.200.0.2:1"); ok {
		t.Error("recent username failures were evicted")
	}
	if ok, _ := l.Allow("someone", "10.200.0.1:1"); ok {
		t.Error("recent peer failures were evicted")
	}
	clock.Advance(time.Minute + time.Second)
	if ok, _ := l.Allow("target", "10.200.0.1:1"); !ok {
		t.Error("expired window still blocks")
	}
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
