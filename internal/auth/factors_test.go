package auth_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/webauthntest"
)

func newFactorEnv(t *testing.T, mods ...func(*auth.Options)) *env {
	t.Helper()
	rp, err := identity.NewRelyingParty(origin)
	if err != nil {
		t.Fatal(err)
	}
	opts := []identity.Option{identity.WithInstanceKey(bytes.Repeat([]byte{0x33}, 32)), identity.WithRelyingParty(rp)}
	var e *env
	e = newEnvWith(t, opts, append([]func(*auth.Options){func(o *auth.Options) {
		o.Factors = o.Identity.(*countingIdentity).Service
	}}, mods...)...)
	return e
}

func totpCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	m := hmac.New(sha1.New, seed)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

// enableTOTP enrolls alice through the service and returns the seed and codes.
func (e *env) enableTOTP(user identity.User) (string, []string) {
	e.t.Helper()
	ctx := context.Background()
	enr, err := e.ids.BeginTOTPEnrollment(ctx, user.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	codes, err := e.ids.ConfirmTOTPEnrollment(ctx, user.ID, 0, totpCode(e.t, enr.Secret, e.clock.Now()))
	if err != nil {
		e.t.Fatal(err)
	}
	e.clock.Advance(30 * time.Second)
	return enr.Secret, codes
}

func (e *env) pendingLogin(user, pass string, mods ...reqMod) string {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, mods...)
	if rec.Code != http.StatusAccepted {
		e.t.Fatalf("login with TOTP: %d %s, want 202", rec.Code, rec.Body.String())
	}
	if c := sessionCookie(e.t, rec); c != nil {
		e.t.Fatal("pending second-factor login set a session cookie")
	}
	body := decode(e.t, rec)
	flow, _ := body["flowId"].(string)
	if body["status"] != "second_factor_required" || flow == "" || body["csrfToken"] != nil {
		e.t.Fatalf("pending login body = %v", body)
	}
	methods, _ := body["methods"].([]any)
	if len(methods) != 2 || methods[0] != "totp" || methods[1] != "recoveryCode" {
		e.t.Fatalf("methods = %v", body["methods"])
	}
	return flow
}

func TestPasswordLoginForTOTPUserReturnsPendingChallengeOnly(t *testing.T) {
	e := newFactorEnv(t)
	secret, _ := e.enableTOTP(e.alice)
	flow := e.pendingLogin("alice", alicePass)
	// The pending handle authorizes nothing.
	if st := e.sessionStatus(flow); st != http.StatusUnauthorized {
		t.Fatalf("flow handle as session cookie: %d", st)
	}
	if rec := e.do("POST", "/api/v1/test/sensitive", nil, withCookie(flow), withCSRF(flow)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("flow handle on a normal route: %d", rec.Code)
	}
	// Wrong code: generic failure, no session.
	rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": "000000"})
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "invalid_second_factor" || sessionCookie(t, rec) != nil {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body.String())
	}
	code := totpCode(t, secret, e.clock.Now())
	rec = e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": code})
	if rec.Code != http.StatusOK {
		t.Fatalf("second factor: %d %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(t, rec)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %+v", c)
	}
	if tok, _ := decode(t, rec)["csrfToken"].(string); tok == "" {
		t.Fatal("no csrfToken")
	}
	if e.sessionStatus(c.Value) != http.StatusOK {
		t.Fatal("issued session does not work")
	}
	// The flow and the code are single-use.
	rec = e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": code})
	if rec.Code != http.StatusUnauthorized || sessionCookie(t, rec) != nil {
		t.Fatalf("replayed flow: %d", rec.Code)
	}
	flow2 := e.pendingLogin("alice", alicePass)
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow2, "code": code}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed TOTP code on a new flow: %d", rec.Code)
	}
	// Users without TOTP are unaffected.
	e.login("bob", bobPass)
}

func TestSecondFactorCompletionRequiresOriginAndWellFormedProof(t *testing.T) {
	e := newFactorEnv(t)
	secret, codes := e.enableTOTP(e.alice)
	flow := e.pendingLogin("alice", alicePass)
	code := totpCode(t, secret, e.clock.Now())
	for name, mods := range map[string][]reqMod{
		"no origin":      {withOrigin("")},
		"foreign origin": {withOrigin("https://evil.example")},
	} {
		if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": code}, mods...); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	for name, body := range map[string]any{
		"both proofs": map[string]string{"flowId": flow, "code": code, "recoveryCode": codes[0]},
		"no proof":    map[string]string{"flowId": flow},
	} {
		if rec := e.do("POST", "/api/v1/auth/login/second-factor", body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", "{not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed: %d", rec.Code)
	}
	// The flow survived the rejected requests above and still completes.
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": code}); rec.Code != http.StatusOK {
		t.Fatalf("legit completion after rejected requests: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSecondFactorRecoveryCodeIsSingleUse(t *testing.T) {
	e := newFactorEnv(t)
	_, codes := e.enableTOTP(e.alice)
	flow := e.pendingLogin("alice", alicePass)
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "recoveryCode": codes[0]}); rec.Code != http.StatusOK {
		t.Fatalf("recovery code: %d %s", rec.Code, rec.Body.String())
	}
	flow = e.pendingLogin("alice", alicePass)
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "recoveryCode": codes[0]}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused recovery code: %d", rec.Code)
	}
}

func TestSecondFactorRefusesSuspendedAccount(t *testing.T) {
	e := newFactorEnv(t)
	secret, _ := e.enableTOTP(e.alice)
	flow := e.pendingLogin("alice", alicePass)
	if err := e.ids.SetActive(context.Background(), "alice", false); err != nil {
		t.Fatal(err)
	}
	rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": totpCode(t, secret, e.clock.Now())})
	if rec.Code != http.StatusUnauthorized || sessionCookie(t, rec) != nil {
		t.Fatalf("suspended account completed login: %d", rec.Code)
	}
}

func TestSecondFactorFailuresAreLimitedPerAccount(t *testing.T) {
	e := newFactorEnv(t, func(o *auth.Options) { o.FactorLimits = auth.Limits{UserFailures: 3} })
	secret, _ := e.enableTOTP(e.alice)
	peers := []string{"198.51.100.1:1", "198.51.100.2:1", "198.51.100.3:1"}
	for _, p := range peers {
		flow := e.pendingLogin("alice", alicePass, withPeer(p))
		if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": "000000"}, withPeer(p)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code: %d", rec.Code)
		}
	}
	// Across peers, the account's second factor is now locked for the window, even
	// with the right password.
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": alicePass}, withPeer("198.51.100.9:1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("login after per-account second-factor failures: %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	// Other accounts are unaffected; the lock expires with the window.
	e.login("bob", bobPass, withPeer("198.51.100.9:1"))
	e.clock.Advance(auth.DefaultLimits.Window + time.Second)
	flow := e.pendingLogin("alice", alicePass)
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": totpCode(t, secret, e.clock.Now())}); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOnlyOnePendingSecondFactorFlowPerAccount(t *testing.T) {
	e := newFactorEnv(t)
	secret, _ := e.enableTOTP(e.alice)
	first := e.pendingLogin("alice", alicePass)
	second := e.pendingLogin("alice", alicePass)
	code := totpCode(t, secret, e.clock.Now())
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": first, "code": code}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("superseded flow completed: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": second, "code": code}); rec.Code != http.StatusOK {
		t.Fatalf("latest flow: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTOTPEnrollmentOverHTTP(t *testing.T) {
	e := newFactorEnv(t)
	other, _ := e.login("alice", alicePass)
	cookie, csrf := e.login("alice", alicePass)
	// Origin, CSRF and recent authentication are all required.
	for name, mods := range map[string][]reqMod{
		"no csrf":        {withCookie(cookie)},
		"foreign origin": {withCookie(cookie), withCSRF(csrf), withOrigin("https://evil.example")},
		"no session":     {withCSRF(csrf)},
	} {
		if rec := e.do("POST", "/api/v1/auth/totp/enroll", nil, mods...); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	e.clock.Advance(auth.ReauthWindow + time.Second)
	if rec := e.do("POST", "/api/v1/auth/totp/enroll", nil, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusForbidden || errCode(t, rec) != "reauthentication_required" {
		t.Fatalf("stale session enrolled: %d %s", rec.Code, rec.Body.String())
	}
	rec := e.do("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": alicePass}, withCookie(cookie), withCSRF(csrf))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	cookie = sessionCookie(t, rec).Value
	csrf = decode(t, rec)["csrfToken"].(string)
	rec = e.do("POST", "/api/v1/auth/totp/enroll", nil, withCookie(cookie), withCSRF(csrf))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	secret, _ := body["secret"].(string)
	uri, _ := body["provisioningUri"].(string)
	if secret == "" || !strings.HasPrefix(uri, "otpauth://totp/") || !strings.Contains(uri, secret) {
		t.Fatalf("enroll body = %v", body)
	}
	if rec := e.do("POST", "/api/v1/auth/totp/confirm", map[string]string{"code": "000000"}, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong confirmation: %d", rec.Code)
	}
	rec = e.do("POST", "/api/v1/auth/totp/confirm", map[string]string{"code": totpCode(t, secret, e.clock.Now())}, withCookie(cookie), withCSRF(csrf))
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	codes, _ := decode(t, rec)["recoveryCodes"].([]any)
	if len(codes) != identity.RecoveryCodeCount {
		t.Fatalf("recovery codes = %v", codes)
	}
	// Enabling a factor revokes the user's other sessions but keeps this one.
	if e.sessionStatus(other) != http.StatusUnauthorized || e.sessionStatus(cookie) != http.StatusOK {
		t.Fatal("authenticator change did not revoke exactly the other sessions")
	}
	rec = e.do("GET", "/api/v1/auth/totp", nil, withCookie(cookie))
	if st := decode(t, rec); rec.Code != http.StatusOK || st["enabled"] != true || st["recoveryCodesRemaining"] != float64(10) || st["secret"] != nil {
		t.Fatalf("status = %d %v", rec.Code, st)
	}
	// Secrets never reach the logs.
	logs := e.logs.String()
	for _, s := range append([]string{secret}, toStrings(codes)...) {
		if strings.Contains(logs, s) {
			t.Fatalf("log contains a secret: %q", s)
		}
	}
}

func toStrings(v []any) []string {
	out := make([]string, 0, len(v))
	for _, x := range v {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestTOTPDisableAndRegenerateRequireRecentAuthAndProof(t *testing.T) {
	e := newFactorEnv(t)
	secret, codes := e.enableTOTP(e.alice)
	flow := e.pendingLogin("alice", alicePass)
	rec := e.do("POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "recoveryCode": codes[9]})
	cookie, csrf := sessionCookie(t, rec).Value, decode(t, rec)["csrfToken"].(string)
	auth := []reqMod{withCookie(cookie), withCSRF(csrf)}

	if rec := e.do("POST", "/api/v1/auth/totp/disable", map[string]string{}, auth...); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disable without proof: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/auth/totp/disable", map[string]string{"code": "000000"}, auth...); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disable with wrong code: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/auth/recovery-codes", map[string]string{"code": "000000"}, auth...); rec.Code != http.StatusUnauthorized {
		t.Fatalf("regenerate with wrong code: %d", rec.Code)
	}
	rec = e.do("POST", "/api/v1/auth/recovery-codes", map[string]string{"code": totpCode(t, secret, e.clock.Now())}, auth...)
	if rec.Code != http.StatusOK || len(decode(t, rec)["recoveryCodes"].([]any)) != identity.RecoveryCodeCount {
		t.Fatalf("regenerate: %d %s", rec.Code, rec.Body.String())
	}
	e.clock.Advance(auth_ReauthPlus())
	if rec := e.do("POST", "/api/v1/auth/totp/disable", map[string]string{"code": totpCode(t, secret, e.clock.Now())}, auth...); rec.Code != http.StatusForbidden || errCode(t, rec) != "reauthentication_required" {
		t.Fatalf("stale disable: %d", rec.Code)
	}
	rec = e.do("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": alicePass}, auth...)
	cookie, csrf = sessionCookie(t, rec).Value, decode(t, rec)["csrfToken"].(string)
	rec = e.do("POST", "/api/v1/auth/totp/disable", map[string]string{"code": totpCode(t, secret, e.clock.Now())}, withCookie(cookie), withCSRF(csrf))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	// Password-only login is back.
	e.login("alice", alicePass)
}

func auth_ReauthPlus() time.Duration { return auth.ReauthWindow + time.Second }

// passkeyRegister registers a passkey for the signed-in session over HTTP.
func (e *env) passkeyRegister(a *webauthntest.Authenticator, cookie, csrf string, mods ...reqMod) *httptest.ResponseRecorder {
	e.t.Helper()
	base := []reqMod{withCookie(cookie), withCSRF(csrf)}
	rec := e.do("POST", "/api/v1/auth/passkeys/register/begin", nil, append(base, mods...)...)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("register begin: %d %s", rec.Code, rec.Body.String())
	}
	var begin struct {
		FlowID  string          `json:"flowId"`
		Options json.RawMessage `json:"options"`
	}
	json.Unmarshal(rec.Body.Bytes(), &begin)
	resp, _, err := a.Register(begin.Options, webauthntest.Overrides{})
	if err != nil {
		e.t.Fatal(err)
	}
	return e.do("POST", "/api/v1/auth/passkeys/register/finish",
		map[string]any{"flowId": begin.FlowID, "name": "Laptop", "credential": json.RawMessage(resp)}, base...)
}

func (e *env) passkeyLogin(a *webauthntest.Authenticator, o webauthntest.Overrides, mods ...reqMod) (*httptest.ResponseRecorder, string, []byte) {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/auth/passkey/login/begin", nil, mods...)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login begin: %d %s", rec.Code, rec.Body.String())
	}
	var begin struct {
		FlowID  string          `json:"flowId"`
		Options json.RawMessage `json:"options"`
	}
	json.Unmarshal(rec.Body.Bytes(), &begin)
	resp, err := a.Assert(begin.Options, o)
	if err != nil {
		e.t.Fatal(err)
	}
	fin := e.do("POST", "/api/v1/auth/passkey/login/finish", map[string]any{"flowId": begin.FlowID, "credential": json.RawMessage(resp)}, mods...)
	return fin, begin.FlowID, resp
}

func TestPasskeyRegistrationAndLoginOverHTTP(t *testing.T) {
	e := newFactorEnv(t)
	other, _ := e.login("alice", alicePass)
	cookie, csrf := e.login("alice", alicePass)
	a := webauthntest.New(origin)
	// Host and forwarded headers cannot move the relying party.
	rec := e.do("POST", "/api/v1/auth/passkeys/register/begin", nil, withCookie(cookie), withCSRF(csrf),
		withHeader("Host", "evil.example"), withHeader("X-Forwarded-Host", "evil.example"), withHeader("Forwarded", "host=evil.example"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"bookmarks.example.test"`) || strings.Contains(rec.Body.String(), "evil") {
		t.Fatalf("register begin: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.passkeyRegister(a, cookie, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register finish: %d %s", rec.Code, rec.Body.String())
	}
	if e.sessionStatus(other) != http.StatusUnauthorized {
		t.Fatal("adding a passkey did not revoke other sessions")
	}
	rec = e.do("GET", "/api/v1/auth/passkeys", nil, withCookie(cookie))
	list, _ := decode(t, rec)["passkeys"].([]any)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["name"] != "Laptop" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "publicKey") || strings.Contains(rec.Body.String(), "credentialId") {
		t.Fatal("list exposes key material")
	}

	fin, flow, resp := e.passkeyLogin(a, webauthntest.Overrides{})
	if fin.Code != http.StatusOK {
		t.Fatalf("passkey login: %d %s", fin.Code, fin.Body.String())
	}
	c := sessionCookie(t, fin)
	if c == nil || e.sessionStatus(c.Value) != http.StatusOK {
		t.Fatal("passkey login issued no working session")
	}
	// Replay of the same flow/assertion fails without a cookie.
	again := e.do("POST", "/api/v1/auth/passkey/login/finish", map[string]any{"flowId": flow, "credential": json.RawMessage(resp)})
	if again.Code != http.StatusUnauthorized || errCode(t, again) != "invalid_passkey" || sessionCookie(t, again) != nil {
		t.Fatalf("replayed assertion: %d %s", again.Code, again.Body.String())
	}
	// A foreign origin in the assertion fails.
	if fin, _, _ := e.passkeyLogin(a, webauthntest.Overrides{Origin: "https://evil.example"}); fin.Code != http.StatusUnauthorized {
		t.Fatalf("foreign-origin assertion: %d", fin.Code)
	}
	// Ceremony endpoints themselves require the exact Origin header.
	if rec := e.do("POST", "/api/v1/auth/passkey/login/begin", nil, withOrigin("https://evil.example")); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin begin: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/auth/passkey/login/finish", map[string]any{"flowId": "x", "credential": map[string]any{}}, withOrigin("")); rec.Code != http.StatusForbidden {
		t.Fatalf("finish without origin: %d", rec.Code)
	}
	// Flow handles never reach the logs.
	if strings.Contains(e.logs.String(), flow) {
		t.Fatal("flow handle logged")
	}
}

func TestPasskeyLoginBypassesTOTPAndRefusesSuspendedAccount(t *testing.T) {
	e := newFactorEnv(t)
	cookie, csrf := e.login("alice", alicePass)
	a := webauthntest.New(origin)
	if rec := e.passkeyRegister(a, cookie, csrf); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	e.enableTOTP(e.alice)
	// A user-verified passkey is itself multi-factor: no TOTP step.
	if fin, _, _ := e.passkeyLogin(a, webauthntest.Overrides{}); fin.Code != http.StatusOK || sessionCookie(t, fin) == nil {
		t.Fatalf("passkey login for TOTP user: %d %s", fin.Code, fin.Body.String())
	}
	if err := e.ids.SetActive(context.Background(), "alice", false); err != nil {
		t.Fatal(err)
	}
	if fin, _, _ := e.passkeyLogin(a, webauthntest.Overrides{}); fin.Code != http.StatusUnauthorized || sessionCookie(t, fin) != nil {
		t.Fatalf("suspended account signed in with a passkey: %d", fin.Code)
	}
}

func TestPasskeyRegistrationRequiresRecentAuthCSRFAndOrigin(t *testing.T) {
	e := newFactorEnv(t)
	cookie, csrf := e.login("alice", alicePass)
	for name, mods := range map[string][]reqMod{
		"no csrf":        {withCookie(cookie)},
		"foreign origin": {withCookie(cookie), withCSRF(csrf), withOrigin("https://evil.example")},
	} {
		for _, path := range []string{"/api/v1/auth/passkeys/register/begin", "/api/v1/auth/passkeys/register/finish"} {
			if rec := e.do("POST", path, map[string]any{}, mods...); rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s: %d", name, path, rec.Code)
			}
		}
	}
	e.clock.Advance(auth.ReauthWindow + time.Second)
	for _, path := range []string{"/api/v1/auth/passkeys/register/begin", "/api/v1/auth/passkeys/register/finish"} {
		if rec := e.do("POST", path, map[string]any{}, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusForbidden || errCode(t, rec) != "reauthentication_required" {
			t.Fatalf("stale %s: %d", path, rec.Code)
		}
	}
}

func TestPasskeyManagementIsOwnerScopedAndRemovalNeedsRecentAuth(t *testing.T) {
	e := newFactorEnv(t)
	cookie, csrf := e.login("alice", alicePass)
	if rec := e.passkeyRegister(webauthntest.New(origin), cookie, csrf); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	id := int64(decode(t, e.do("GET", "/api/v1/auth/passkeys", nil, withCookie(cookie)))["passkeys"].([]any)[0].(map[string]any)["id"].(float64))
	path := fmt.Sprintf("/api/v1/auth/passkeys/%d", id)
	bc, bcsrf := e.login("bob", bobPass)
	if rec := e.do("PATCH", path, map[string]string{"name": "pwned"}, withCookie(bc), withCSRF(bcsrf)); rec.Code != http.StatusNotFound {
		t.Fatalf("bob renamed alice's passkey: %d", rec.Code)
	}
	if rec := e.do("DELETE", path, nil, withCookie(bc), withCSRF(bcsrf)); rec.Code != http.StatusNotFound {
		t.Fatalf("bob deleted alice's passkey: %d", rec.Code)
	}
	if rec := e.do("PATCH", path, map[string]string{"name": "Phone"}, withCookie(cookie)); rec.Code != http.StatusForbidden {
		t.Fatalf("rename without CSRF: %d", rec.Code)
	}
	if rec := e.do("PATCH", path, map[string]string{"name": "Phone"}, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusNoContent {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("PATCH", path, map[string]string{"name": ""}, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name: %d", rec.Code)
	}
	e.clock.Advance(auth.ReauthWindow + time.Second)
	if rec := e.do("DELETE", path, nil, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusForbidden || errCode(t, rec) != "reauthentication_required" {
		t.Fatalf("stale delete: %d", rec.Code)
	}
	rec := e.do("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": alicePass}, withCookie(cookie), withCSRF(csrf))
	cookie, csrf = sessionCookie(t, rec).Value, decode(t, rec)["csrfToken"].(string)
	if rec := e.do("DELETE", path, nil, withCookie(cookie), withCSRF(csrf)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFactorRoutesAbsentWithoutFactorService(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("POST", "/api/v1/auth/passkey/login/begin", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("factor route registered without factors: %d", rec.Code)
	}
}

func TestFactorResponsesAreNeverStored(t *testing.T) {
	e := newFactorEnv(t)
	secret, _ := e.enableTOTP(e.alice)
	// A bare mux: no server base headers, so the handler itself must set no-store.
	bare := http.NewServeMux()
	e.auth.Register(bare)
	checks := map[string]*httptest.ResponseRecorder{
		"second factor fail": e.serve(bare, "POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": "nope", "code": "000000"}),
		"passkey begin":      e.serve(bare, "POST", "/api/v1/auth/passkey/login/begin", nil),
		"passkey finish 401": e.serve(bare, "POST", "/api/v1/auth/passkey/login/finish", map[string]any{"flowId": "x", "credential": map[string]any{}}),
	}
	pending := e.serve(bare, "POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": alicePass})
	checks["pending login"] = pending
	flow, _ := decode(t, pending)["flowId"].(string)
	checks["second factor ok"] = e.serve(bare, "POST", "/api/v1/auth/login/second-factor", map[string]string{"flowId": flow, "code": totpCode(t, secret, e.clock.Now())})
	if checks["second factor ok"].Code != http.StatusOK {
		t.Fatalf("second factor: %d", checks["second factor ok"].Code)
	}
	for name, rec := range checks {
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s (%d): Cache-Control = %q", name, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}
