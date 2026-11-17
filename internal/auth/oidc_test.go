package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/oidcfake"
)

const (
	oidcClientID     = "bookmarksync-client"
	oidcClientSecret = "fake-client-secret-value"
	callbackPath     = "/api/v1/auth/oidc/authentik/callback"
	completePath     = "/auth/oidc/complete"
)

type oidcEnv struct {
	*env
	fake *oidcfake.Server
}

func newOIDCEnv(t *testing.T, mods ...func(*auth.Options)) *oidcEnv {
	t.Helper()
	fake := oidcfake.New(oidcClientID, oidcClientSecret)
	t.Cleanup(fake.Close)
	base := func(o *auth.Options) {
		o.OIDC = auth.OIDCOptions{
			Providers: []auth.OIDCProvider{{ID: "authentik", Name: "Authentik", Issuer: fake.Issuer, ClientID: oidcClientID, ClientSecret: oidcClientSecret}},
			Timeout:   2 * time.Second,
		}
	}
	e := newFactorEnv(t, append([]func(*auth.Options){base}, mods...)...)
	fake.Now = e.clock.Now
	return &oidcEnv{env: e, fake: fake}
}

// linkDirect links subject to user through the service (bypassing HTTP).
func (o *oidcEnv) linkDirect(u identity.User, subject string) {
	o.t.Helper()
	sess, _, err := o.ids.CreateSession(context.Background(), u.ID, "link")
	if err != nil {
		o.t.Fatal(err)
	}
	if _, _, err := o.ids.LinkOIDCIdentity(context.Background(), identity.OIDCLinkRequest{UserID: u.ID, SessionID: sess.ID,
		Provider: "authentik", Issuer: o.fake.Issuer, Subject: subject, DisplayName: u.Username}); err != nil {
		o.t.Fatal(err)
	}
}

func bindingCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.OIDCBindingCookieName {
			return c
		}
	}
	return nil
}

type begun struct {
	authURL string
	binding string
	rec     *httptest.ResponseRecorder
}

func (o *oidcEnv) begin(path string, mods ...reqMod) begun {
	o.t.Helper()
	rec := o.do("POST", path, nil, mods...)
	if rec.Code != http.StatusOK {
		o.t.Fatalf("begin %s: %d %s", path, rec.Code, rec.Body.String())
	}
	u, _ := decode(o.t, rec)["authorizationUrl"].(string)
	c := bindingCookie(o.t, rec)
	if u == "" || c == nil || c.Value == "" {
		o.t.Fatalf("begin returned url %q cookie %+v", u, c)
	}
	return begun{authURL: u, binding: c.Value, rec: rec}
}

func (o *oidcEnv) beginLogin(mods ...reqMod) begun {
	o.t.Helper()
	return o.begin("/api/v1/auth/oidc/authentik/login", mods...)
}

func withBinding(v string) reqMod {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.OIDCBindingCookieName, Value: v}) }
}

// callback follows the provider redirect to the callback and returns the recorder and
// the parsed Location.
func (o *oidcEnv) callback(cbURL string, mods ...reqMod) (*httptest.ResponseRecorder, *url.URL) {
	o.t.Helper()
	u, err := url.Parse(cbURL)
	if err != nil {
		o.t.Fatal(err)
	}
	rec := o.do("GET", u.RequestURI(), nil, mods...)
	if rec.Code != http.StatusSeeOther {
		o.t.Fatalf("callback: %d %s, want 303", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		o.t.Fatal(err)
	}
	if loc.Path != completePath || loc.Host != "" || loc.Scheme != "" {
		o.t.Fatalf("callback redirected to %q, want the same-origin %s", rec.Header().Get("Location"), completePath)
	}
	if c := bindingCookie(o.t, rec); c == nil || c.MaxAge >= 0 {
		o.t.Fatalf("binding cookie not cleared: %+v", c)
	}
	return rec, loc
}

// loginAs runs a full OIDC login for subject and returns the callback response.
func (o *oidcEnv) loginAs(subject string) (*httptest.ResponseRecorder, *url.URL) {
	o.t.Helper()
	b := o.beginLogin()
	cb, _, err := o.fake.Authorize(b.authURL, subject)
	if err != nil {
		o.t.Fatal(err)
	}
	return o.callback(cb, withBinding(b.binding))
}

func resultOf(loc *url.URL) string {
	if e := loc.Query().Get("error"); e != "" {
		return "error=" + e
	}
	return "result=" + loc.Query().Get("result")
}

func TestOIDCProvidersListIsPublicAndSecretFree(t *testing.T) {
	o := newOIDCEnv(t)
	rec := o.do("GET", "/api/v1/auth/oidc/providers", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("providers: %d", rec.Code)
	}
	body := rec.Body.String()
	if body != `{"providers":[{"id":"authentik","name":"Authentik"}]}`+"\n" {
		t.Fatalf("providers body = %s", body)
	}
	if strings.Contains(body, oidcClientSecret) || strings.Contains(body, oidcClientID) || strings.Contains(body, o.fake.Issuer) {
		t.Fatal("providers list exposes configuration")
	}
}

func TestOIDCRoutesAbsentWithoutProviders(t *testing.T) {
	e := newFactorEnv(t)
	for _, p := range []string{"/api/v1/auth/oidc/providers", "/api/v1/auth/oidc/identities"} {
		if rec := e.do("GET", p, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s without providers: %d", p, rec.Code)
		}
	}
}

func TestOIDCLoginForALinkedAccountIssuesASession(t *testing.T) {
	o := newOIDCEnv(t)
	if n := o.fake.Discovered(); n != 0 {
		t.Fatalf("handler contacted the provider at construction (%d discoveries)", n)
	}
	o.linkDirect(o.alice, "sub-alice")
	b := o.beginLogin()
	if b.rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("begin response cacheable")
	}
	c := bindingCookie(t, b.rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" ||
		c.MaxAge <= 0 || c.MaxAge > int(identity.OIDCFlowTTL/time.Second) {
		t.Fatalf("binding cookie = %+v", c)
	}
	cb, req, err := o.fake.Authorize(b.authURL, "sub-alice")
	if err != nil {
		t.Fatal(err)
	}
	q := req.Query
	if req.ResponseType != "code" || req.RedirectURI != origin+callbackPath || !strings.Contains(" "+req.Scope+" ", " openid ") ||
		req.CodeChallengeMethod != "S256" || len(req.CodeChallenge) != 43 || len(req.Nonce) < 32 || len(req.State) < 43 ||
		q.Has("code_verifier") || q.Has("client_secret") {
		t.Fatalf("authorization request = %+v", q)
	}
	rec, loc := o.callback(cb, withBinding(b.binding))
	if resultOf(loc) != "result=signed_in" || loc.Fragment != "" {
		t.Fatalf("callback result %q", rec.Header().Get("Location"))
	}
	sc := sessionCookie(t, rec)
	if sc == nil || o.sessionStatus(sc.Value) != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("no working session after OIDC login")
	}
	// The token request carried the exact redirect URI and the PKCE verifier.
	tr := o.fake.LastTokenRequest()
	sum := sha256.Sum256([]byte(tr.Get("code_verifier")))
	if tr.Get("redirect_uri") != origin+callbackPath || base64.RawURLEncoding.EncodeToString(sum[:]) != req.CodeChallenge {
		t.Fatalf("token request = %v", tr)
	}
	// Discovery is cached across logins.
	o.loginAs("sub-alice")
	if n := o.fake.Discovered(); n != 1 {
		t.Fatalf("%d discoveries for two logins", n)
	}
}

func TestOIDCLoginRejectsInvalidIDTokens(t *testing.T) {
	stranger, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := map[string]func(f *oidcfake.Server){
		"wrong issuer":          func(f *oidcfake.Server) { f.Claims = func(c map[string]any) { c["iss"] = "https://evil.example" } },
		"issuer trailing slash": func(f *oidcfake.Server) { f.Claims = func(c map[string]any) { c["iss"] = f.Issuer + "/" } },
		"wrong audience":        func(f *oidcfake.Server) { f.Claims = func(c map[string]any) { c["aud"] = "someone-else" } },
		"multi aud without azp": func(f *oidcfake.Server) {
			f.Claims = func(c map[string]any) { c["aud"] = []string{oidcClientID, "other"} }
		},
		"single aud with other azp": func(f *oidcfake.Server) {
			f.Claims = func(c map[string]any) { c["aud"] = oidcClientID; c["azp"] = "attacker" }
		},
		"single-element aud array with other azp": func(f *oidcfake.Server) {
			f.Claims = func(c map[string]any) { c["aud"] = []string{oidcClientID}; c["azp"] = "attacker" }
		},
		"multi aud with other azp": func(f *oidcfake.Server) {
			f.Claims = func(c map[string]any) { c["aud"] = []string{oidcClientID, "other"}; c["azp"] = "other" }
		},
		"expired": func(f *oidcfake.Server) {
			f.Claims = func(c map[string]any) { c["exp"] = f.Now().Add(-time.Minute).Unix() }
		},
		"not yet valid": func(f *oidcfake.Server) {
			f.Claims = func(c map[string]any) { c["nbf"] = f.Now().Add(time.Hour).Unix() }
		},
		"nonce mismatch":      func(f *oidcfake.Server) { f.Claims = func(c map[string]any) { c["nonce"] = "attacker-nonce" } },
		"nonce missing":       func(f *oidcfake.Server) { f.Claims = func(c map[string]any) { delete(c, "nonce") } },
		"subject missing":     func(f *oidcfake.Server) { f.Claims = func(c map[string]any) { delete(c, "sub") } },
		"subject empty":       func(f *oidcfake.Server) { f.Claims = func(c map[string]any) { c["sub"] = "" } },
		"unknown signing key": func(f *oidcfake.Server) { f.SignWith = stranger },
		"alg none":            func(f *oidcfake.Server) { f.Unsigned = true },
		"no id_token":         func(f *oidcfake.Server) { f.OmitIDToken = true },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			o := newOIDCEnv(t)
			o.linkDirect(o.alice, "sub-alice")
			tamper(o.fake)
			rec, loc := o.loginAs("sub-alice")
			if resultOf(loc) != "error=invalid_request" || sessionCookie(t, rec) != nil {
				t.Fatalf("got %q with session %v", rec.Header().Get("Location"), sessionCookie(t, rec) != nil)
			}
		})
	}
	// A single audience with azp naming this client is fine (OIDC Core: when azp is
	// present it must be the client ID).
	o1 := newOIDCEnv(t)
	o1.linkDirect(o1.alice, "sub-alice")
	o1.fake.Claims = func(c map[string]any) { c["azp"] = oidcClientID }
	if _, loc := o1.loginAs("sub-alice"); resultOf(loc) != "result=signed_in" {
		t.Fatalf("single aud with azp=client: %s", resultOf(loc))
	}
	// Multiple audiences are fine when azp names this client.
	o := newOIDCEnv(t)
	o.linkDirect(o.alice, "sub-alice")
	o.fake.Claims = func(c map[string]any) { c["aud"] = []string{oidcClientID, "other"}; c["azp"] = oidcClientID }
	if _, loc := o.loginAs("sub-alice"); resultOf(loc) != "result=signed_in" {
		t.Fatalf("multi aud with azp=client: %s", resultOf(loc))
	}
}

func TestOIDCLoginNeverCreatesOrMatchesAccountsByEmail(t *testing.T) {
	o := newOIDCEnv(t)
	o.linkDirect(o.alice, "sub-alice")
	// Same email and name as alice's identity, different subject.
	o.fake.Claims = func(c map[string]any) { c["email"] = "sub-alice@example.test"; c["name"] = "Name of sub-alice" }
	rec, loc := o.loginAs("sub-other")
	if resultOf(loc) != "error=not_linked" || sessionCookie(t, rec) != nil {
		t.Fatalf("other subject with alice's email: %q", rec.Header().Get("Location"))
	}
	rec, loc = o.loginAs("brand-new-subject")
	if resultOf(loc) != "error=not_linked" || sessionCookie(t, rec) != nil {
		t.Fatalf("unlinked subject: %q", rec.Header().Get("Location"))
	}
	var users int
	o.db.SQL().QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	if users != 3 {
		t.Fatalf("OIDC login created accounts: %d users", users)
	}
}

func TestOIDCLoginRefusesSuspendedAccounts(t *testing.T) {
	o := newOIDCEnv(t)
	o.linkDirect(o.alice, "sub-alice")
	b := o.beginLogin()
	cb, _, _ := o.fake.Authorize(b.authURL, "sub-alice")
	if err := o.ids.SetActive(context.Background(), "alice", false); err != nil {
		t.Fatal(err)
	}
	rec, loc := o.callback(cb, withBinding(b.binding))
	if resultOf(loc) != "error=not_linked" || sessionCookie(t, rec) != nil {
		t.Fatalf("suspended account: %q", rec.Header().Get("Location"))
	}
}

func TestOIDCCallbackStateAndBrowserBinding(t *testing.T) {
	o := newOIDCEnv(t)
	o.linkDirect(o.alice, "sub-alice")
	b := o.beginLogin()
	cb, _, _ := o.fake.Authorize(b.authURL, "sub-alice")
	u, _ := url.Parse(cb)
	state, code := u.Query().Get("state"), u.Query().Get("code")
	at := func(q url.Values) string { return callbackPath + "?" + q.Encode() }

	// Malformed callbacks.
	for name, c := range map[string]struct {
		target string
		mods   []reqMod
		want   string
	}{
		"no state":          {at(url.Values{"code": {code}}), []reqMod{withBinding(b.binding)}, "error=invalid_request"},
		"no code":           {at(url.Values{"state": {state}}), []reqMod{withBinding(b.binding)}, "error=invalid_request"},
		"no binding cookie": {cb, nil, "error=invalid_request"},
		"unknown state":     {at(url.Values{"state": {"x" + state}, "code": {code}}), []reqMod{withBinding(b.binding)}, "error=expired"},
		"wrong binding":     {cb, []reqMod{withBinding("attacker-binding")}, "error=expired"},
		"wrong provider":    {strings.Replace(cb, "/authentik/", "/google/", 1), []reqMod{withBinding(b.binding)}, "error=expired"},
	} {
		rec, loc := o.callback(c.target, c.mods...)
		if resultOf(loc) != c.want || sessionCookie(t, rec) != nil {
			t.Fatalf("%s: %s, want %s", name, resultOf(loc), c.want)
		}
	}
	// None of those consumed the flow: the genuine callback still succeeds, once.
	if _, loc := o.callback(cb, withBinding(b.binding)); resultOf(loc) != "result=signed_in" {
		t.Fatalf("genuine callback after rejected ones: %s", resultOf(loc))
	}
	if rec, loc := o.callback(cb, withBinding(b.binding)); resultOf(loc) != "error=expired" || sessionCookie(t, rec) != nil {
		t.Fatalf("replayed callback: %s", resultOf(loc))
	}
	// Expiry.
	b2 := o.beginLogin()
	cb2, _, _ := o.fake.Authorize(b2.authURL, "sub-alice")
	o.clock.Advance(identity.OIDCFlowTTL)
	if _, loc := o.callback(cb2, withBinding(b2.binding)); resultOf(loc) != "error=expired" {
		t.Fatalf("expired flow: %s", resultOf(loc))
	}
	// Provider-reported errors are not reflected.
	b3 := o.beginLogin()
	q := url.Values{"state": {mustState(t, b3.authURL)}, "error": {"access_denied"}, "error_description": {"<script>alert(1)</script>"}}
	rec, loc := o.callback(at(q), withBinding(b3.binding))
	if resultOf(loc) != "error=access_denied" || strings.Contains(rec.Header().Get("Location"), "script") {
		t.Fatalf("provider error: %q", rec.Header().Get("Location"))
	}
}

func mustState(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func TestOIDCRedirectURIIgnoresHostAndForwardedHeaders(t *testing.T) {
	o := newOIDCEnv(t)
	b := o.beginLogin(withHeader("Host", "evil.example"), withHeader("X-Forwarded-Host", "evil.example"),
		withHeader("X-Forwarded-Proto", "http"), withHeader("Forwarded", "host=evil.example;proto=http"))
	_, req, err := o.fake.Authorize(b.authURL, "x")
	if err != nil || req.RedirectURI != origin+callbackPath {
		t.Fatalf("redirect_uri = %q, %v", req.RedirectURI, err)
	}
	// Begin is a state-changing POST: the exact Origin is required.
	for _, bad := range []string{"", "https://evil.example"} {
		if rec := o.do("POST", "/api/v1/auth/oidc/authentik/login", nil, withOrigin(bad)); rec.Code != http.StatusForbidden {
			t.Fatalf("begin with Origin %q: %d", bad, rec.Code)
		}
	}
	if rec := o.do("POST", "/api/v1/auth/oidc/google/login", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured provider: %d", rec.Code)
	}
}

func TestOIDCLinkOverHTTP(t *testing.T) {
	o := newOIDCEnv(t)
	other, _ := o.login("alice", alicePass)
	cookie, csrf := o.login("alice", alicePass)
	linkPath := "/api/v1/auth/oidc/authentik/link"
	for name, mods := range map[string][]reqMod{
		"no csrf":        {withCookie(cookie)},
		"foreign origin": {withCookie(cookie), withCSRF(csrf), withOrigin("https://evil.example")},
		"no session":     {withCSRF(csrf)},
	} {
		if rec := o.do("POST", linkPath, nil, mods...); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	o.clock.Advance(auth.ReauthWindow + time.Second)
	if rec := o.do("POST", linkPath, nil, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusForbidden || errCode(t, rec) != "reauthentication_required" {
		t.Fatalf("stale link begin: %d", rec.Code)
	}
	rec := o.do("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": alicePass}, withCookie(cookie), withCSRF(csrf))
	cookie, csrf = sessionCookie(t, rec).Value, decode(t, rec)["csrfToken"].(string)

	b := o.begin(linkPath, withCookie(cookie), withCSRF(csrf))
	cb, _, _ := o.fake.Authorize(b.authURL, "sub-alice")
	// Without the same live session the link flow is neither completed nor consumed.
	bobCookie, _ := o.login("bob", bobPass)
	third, _ := o.login("alice", alicePass)
	for name, mods := range map[string][]reqMod{
		"no session":    {withBinding(b.binding)},
		"other session": {withBinding(b.binding), withCookie(third)},
		"other user":    {withBinding(b.binding), withCookie(bobCookie)},
	} {
		rec, loc := o.callback(cb, mods...)
		if resultOf(loc) != "error=expired" || sessionCookie(t, rec) != nil && sessionCookie(t, rec).MaxAge > 0 {
			t.Fatalf("%s: %s", name, resultOf(loc))
		}
	}
	rec, loc := o.callback(cb, withBinding(b.binding), withCookie(cookie))
	if resultOf(loc) != "result=linked" {
		t.Fatalf("link callback: %q", rec.Header().Get("Location"))
	}
	fresh := sessionCookie(t, rec)
	if fresh == nil || fresh.Value == cookie || o.sessionStatus(fresh.Value) != http.StatusOK {
		t.Fatal("link did not rotate the acting session")
	}
	if o.sessionStatus(cookie) != http.StatusUnauthorized || o.sessionStatus(other) != http.StatusUnauthorized || o.sessionStatus(third) != http.StatusUnauthorized {
		t.Fatal("link did not revoke the pre-link and other sessions")
	}
	if o.sessionStatus(bobCookie) != http.StatusOK {
		t.Fatal("another user's session was affected")
	}
	list := o.do("GET", "/api/v1/auth/oidc/identities", nil, withCookie(fresh.Value))
	ids, _ := decode(t, list)["identities"].([]any)
	if list.Code != http.StatusOK || len(ids) != 1 || ids[0].(map[string]any)["provider"] != "authentik" {
		t.Fatalf("identities: %d %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), "sub-alice") && !strings.Contains(list.Body.String(), "Name of sub-alice") {
		t.Fatal("identity list exposes the subject")
	}
	if strings.Contains(list.Body.String(), o.fake.Issuer) || strings.Contains(list.Body.String(), `"subject"`) {
		t.Fatal("identity list exposes issuer/subject")
	}
	// OIDC login now works for alice.
	if _, loc := o.loginAs("sub-alice"); resultOf(loc) != "result=signed_in" {
		t.Fatalf("login after link: %s", resultOf(loc))
	}
	// Bob cannot link alice's identity.
	bc, bcsrf := o.login("bob", bobPass)
	bb := o.begin(linkPath, withCookie(bc), withCSRF(bcsrf))
	bcb, _, _ := o.fake.Authorize(bb.authURL, "sub-alice")
	if _, loc := o.callback(bcb, withBinding(bb.binding), withCookie(bc)); resultOf(loc) != "error=already_linked" {
		t.Fatalf("second account linking the same identity: %s", resultOf(loc))
	}
}

func TestOIDCUnlinkOverHTTP(t *testing.T) {
	o := newOIDCEnv(t)
	o.linkDirect(o.alice, "sub-alice")
	other, _ := o.login("alice", alicePass)
	cookie, csrf := o.login("alice", alicePass)
	list := decode(t, o.do("GET", "/api/v1/auth/oidc/identities", nil, withCookie(cookie)))["identities"].([]any)
	id := int64(list[0].(map[string]any)["id"].(float64))
	path := fmt.Sprintf("/api/v1/auth/oidc/identities/%d", id)
	bc, bcsrf := o.login("bob", bobPass)
	if rec := o.do("DELETE", path, nil, withCookie(bc), withCSRF(bcsrf)); rec.Code != http.StatusNotFound {
		t.Fatalf("bob unlinked alice's identity: %d", rec.Code)
	}
	if rec := o.do("DELETE", path, nil, withCookie(cookie)); rec.Code != http.StatusForbidden {
		t.Fatalf("unlink without CSRF: %d", rec.Code)
	}
	o.clock.Advance(auth.ReauthWindow + time.Second)
	if rec := o.do("DELETE", path, nil, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusForbidden || errCode(t, rec) != "reauthentication_required" {
		t.Fatalf("stale unlink: %d", rec.Code)
	}
	rec := o.do("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": alicePass}, withCookie(cookie), withCSRF(csrf))
	cookie, csrf = sessionCookie(t, rec).Value, decode(t, rec)["csrfToken"].(string)
	rec = o.do("DELETE", path, nil, withCookie(cookie), withCSRF(csrf))
	if rec.Code != http.StatusOK {
		t.Fatalf("unlink: %d %s", rec.Code, rec.Body.String())
	}
	fresh, _ := rotated(t, rec, cookie)
	if o.sessionStatus(fresh) != http.StatusOK || o.sessionStatus(cookie) != http.StatusUnauthorized || o.sessionStatus(other) != http.StatusUnauthorized {
		t.Fatal("unlink did not rotate/revoke sessions")
	}
	if _, loc := o.loginAs("sub-alice"); resultOf(loc) != "error=not_linked" {
		t.Fatalf("login after unlink: %s", resultOf(loc))
	}
}

func TestOIDCNetworkCallsAreBounded(t *testing.T) {
	for _, what := range []string{"discovery", "token", "keys"} {
		t.Run(what, func(t *testing.T) {
			o := newOIDCEnv(t, func(opt *auth.Options) { opt.OIDC.Timeout = 300 * time.Millisecond })
			o.linkDirect(o.alice, "sub-alice")
			if what != "discovery" {
				// Discover first so only the later call hangs.
				o.beginLogin()
			}
			o.fake.SetDelay(what, 5*time.Second)
			start := time.Now()
			if what == "discovery" {
				rec := o.do("POST", "/api/v1/auth/oidc/authentik/login", nil)
				if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "provider_unavailable" {
					t.Fatalf("begin with hanging discovery: %d %s", rec.Code, rec.Body.String())
				}
			} else {
				rec, loc := o.loginAs("sub-alice")
				if resultOf(loc) != "error=unavailable" || sessionCookie(t, rec) != nil {
					t.Fatalf("hanging %s: %s", what, resultOf(loc))
				}
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Fatalf("hanging %s took %v", what, d)
			}
			o.fake.SetDelay(what, 0)
		})
	}
}

func TestOIDCLogsAndRedirectsCarryNoSecrets(t *testing.T) {
	o := newOIDCEnv(t)
	o.linkDirect(o.alice, "sub-alice")
	b := o.beginLogin()
	cb, req, _ := o.fake.Authorize(b.authURL, "sub-alice")
	code := mustQuery(t, cb, "code")
	rec, _ := o.callback(cb, withBinding(b.binding))
	_, _ = o.loginAs("sub-unknown")
	verifier := o.fake.LastTokenRequest().Get("code_verifier")
	logs := o.logs.String()
	for name, secret := range map[string]string{
		"code": code, "state": req.State, "nonce": req.Nonce, "verifier": verifier, "client secret": oidcClientSecret,
		"binding": b.binding, "subject": "sub-alice", "unknown subject": "sub-unknown",
	} {
		if secret == "" {
			t.Fatalf("%s empty in test", name)
		}
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain the %s", name)
		}
		if strings.Contains(rec.Header().Get("Location"), secret) {
			t.Fatalf("redirect contains the %s", name)
		}
	}
	if !strings.Contains(logs, "oidc") {
		t.Fatal("no OIDC audit events logged")
	}
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

func TestOIDCBeginIsLimitedPerPeer(t *testing.T) {
	o := newOIDCEnv(t, func(opt *auth.Options) { opt.OIDCBeginLimits = auth.Limits{PeerFailures: 2} })
	a := withPeer("198.51.100.20:1")
	o.beginLogin(a)
	o.beginLogin(a)
	rec := o.do("POST", "/api/v1/auth/oidc/authentik/login", nil, a, withHeader("X-Forwarded-For", "203.0.113.1"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || bindingCookie(t, rec) != nil {
		t.Fatalf("third begin from one peer: %d", rec.Code)
	}
	o.beginLogin(withPeer("198.51.100.21:1"))
	if auth.DefaultOIDCBeginLimits.PeerFailures <= 0 || auth.DefaultOIDCBeginLimits.Window < identity.OIDCFlowTTL/2 {
		t.Fatalf("default OIDC begin limits = %+v", auth.DefaultOIDCBeginLimits)
	}
}

func TestOIDCLoginForTOTPAccountStillRequiresTheSecondFactor(t *testing.T) {
	o := newOIDCEnv(t)
	secret, _ := o.enableTOTP(o.alice)
	o.linkDirect(o.alice, "sub-alice")
	rec, loc := o.loginAs("sub-alice")
	if sessionCookie(t, rec) != nil {
		t.Fatal("OIDC login bypassed TOTP with a session")
	}
	if resultOf(loc) != "result=second_factor_required" {
		t.Fatalf("result %q", rec.Header().Get("Location"))
	}
	frag, err := url.ParseQuery(loc.Fragment)
	flow := frag.Get("flowId")
	if err != nil || len(flow) < 43 {
		t.Fatalf("fragment %q", loc.Fragment)
	}
	if strings.Contains(loc.RawQuery, flow) || strings.Contains(o.logs.String(), flow) {
		t.Fatal("second-factor handle outside the fragment")
	}
	done := o.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": totpCode(t, secret, o.clock.Now())})
	if done.Code != http.StatusOK || sessionCookie(t, done) == nil {
		t.Fatalf("second factor after OIDC: %d %s", done.Code, done.Body.String())
	}
}

func TestOIDCResponsesAreNeverStored(t *testing.T) {
	o := newOIDCEnv(t)
	bare := http.NewServeMux()
	o.auth.Register(bare)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"providers": o.serve(bare, "GET", "/api/v1/auth/oidc/providers", nil),
		"begin":     o.serve(bare, "POST", "/api/v1/auth/oidc/authentik/login", nil),
		"callback":  o.serve(bare, "GET", callbackPath+"?state=x&code=y", nil),
	} {
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s (%d): Cache-Control %q", name, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}
