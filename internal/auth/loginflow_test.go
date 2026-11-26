package auth_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func beginLoginFlow(t *testing.T, e *env) (string, string) {
	t.Helper()
	r := e.do("POST", "/index.php/login/v2", nil, withOrigin(""))
	if r.Code != 200 {
		t.Fatalf("begin = %d %s", r.Code, r.Body.String())
	}
	data := decode(t, r)
	poll := data["poll"].(map[string]any)
	if poll["endpoint"] != origin+"/login/v2/poll" {
		t.Fatalf("poll endpoint = %v", poll)
	}
	u, err := url.Parse(data["login"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host != origin || u.Path != "/auth/connect" {
		t.Fatal(u)
	}
	return poll["token"].(string), u.Query().Get("flow")
}

func TestLoginFlowIssuesOneRevocablePrivateCredential(t *testing.T) {
	e := newEnv(t)
	token, handle := beginLoginFlow(t, e)
	poll := func() int {
		return e.do("POST", "/login/v2/poll", "token="+url.QueryEscape(token), withHeader("Content-Type", "application/x-www-form-urlencoded"), withOrigin("")).Code
	}
	if poll() != 404 {
		t.Fatal("unapproved flow returned credentials")
	}
	cookie, csrf := e.login("alice", alicePass)
	approval := e.do("POST", "/api/v1/auth/connect/approve", approvalBody(e, cookie, handle), withCookie(cookie), withCSRF(csrf))
	if approval.Code != 200 {
		t.Fatalf("approval = %d %s", approval.Code, approval.Body.String())
	}
	result := e.do("POST", "/login/v2/poll", "token="+url.QueryEscape(token), withHeader("Content-Type", "application/x-www-form-urlencoded"), withOrigin(""))
	if result.Code != 200 {
		t.Fatalf("poll = %d %s", result.Code, result.Body.String())
	}
	data := decode(t, result)
	if data["server"] != origin || data["loginName"] != "alice" || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(data)
	}
	principal, err := e.ids.Authenticate(context.Background(), "alice", data["appPassword"].(string))
	if err != nil || string(principal.Kind) != "private" || string(principal.Mode) != "edit" {
		t.Fatalf("app authentication = %+v %v", principal, err)
	}
	if poll() != 404 {
		t.Fatal("poll replay succeeded")
	}
	if err = e.ids.RevokeCredential(context.Background(), principal.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.ids.Authenticate(context.Background(), "alice", data["appPassword"].(string)); err == nil {
		t.Fatal("revoked app credential accepted")
	}
	if strings.Contains(e.logs.String(), token) || strings.Contains(e.logs.String(), handle) || strings.Contains(e.logs.String(), data["appPassword"].(string)) {
		t.Fatal("flow secrets logged")
	}
}

func TestLoginFlowConsentSecurityAndExpiry(t *testing.T) {
	e := newEnv(t)
	token, handle := beginLoginFlow(t, e)
	cookie, csrf := e.login("alice", alicePass)
	body := approvalBody(e, cookie, handle)
	for _, tc := range []struct {
		name string
		mods []reqMod
		want int
	}{
		{"no session", nil, 401},
		{"no csrf", []reqMod{withCookie(cookie)}, 403},
		{"wrong origin", []reqMod{withCookie(cookie), withCSRF(csrf), withOrigin("https://evil.example")}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.do("POST", "/api/v1/auth/connect/approve", body, tc.mods...)
			if r.Code != tc.want {
				t.Fatalf("status=%d %s", r.Code, r.Body.String())
			}
		})
	}
	e.clock.Advance(6 * time.Minute)
	r := e.do("POST", "/api/v1/auth/connect/approve", body, withCookie(cookie), withCSRF(csrf))
	if r.Code != 403 || errCode(t, r) != "reauthentication_required" {
		t.Fatalf("stale auth=%d %s", r.Code, r.Body.String())
	}
	cookie, csrf = e.login("alice", alicePass)
	if changed := e.do("POST", "/api/v1/auth/connect/approve", body, withCookie(cookie), withCSRF(csrf)); changed.Code != 409 {
		t.Fatalf("changed session approval=%d %s", changed.Code, changed.Body.String())
	}
	// A poll secret cannot be used as the browser consent handle.
	r = e.do("POST", "/api/v1/auth/connect/approve", approvalBody(e, cookie, token), withCookie(cookie), withCSRF(csrf))
	if r.Code != 404 {
		t.Fatal("poll secret authorized browser flow")
	}
	e.clock.Advance(15 * time.Minute)
	r = e.do("POST", "/login/v2/poll", "token="+token, withHeader("Content-Type", "application/x-www-form-urlencoded"))
	if r.Code != 404 {
		t.Fatal("expired poll accepted")
	}
	cookie, csrf = e.login("alice", alicePass)
	r = e.do("POST", "/api/v1/auth/connect/approve", approvalBody(e, cookie, handle), withCookie(cookie), withCSRF(csrf))
	if r.Code != 404 {
		t.Fatalf("expired approval=%d", r.Code)
	}
}

func TestLoginFlowConcurrentPollAndRevokedApprovals(t *testing.T) {
	for _, state := range []string{"concurrent", "suspended", "revoked session"} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			token, handle := beginLoginFlow(t, e)
			cookie, csrf := e.login("alice", alicePass)
			r := e.do("POST", "/api/v1/auth/connect/approve", approvalBody(e, cookie, handle), withCookie(cookie), withCSRF(csrf))
			if r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			if state == "suspended" {
				if err := e.ids.SetActive(context.Background(), "alice", false); err != nil {
					t.Fatal(err)
				}
			}
			if state == "revoked session" {
				r = e.do("POST", "/api/v1/auth/logout", nil, withCookie(cookie), withCSRF(csrf))
				if r.Code != 204 {
					t.Fatalf("logout=%d", r.Code)
				}
			}
			var wg sync.WaitGroup
			codes := make(chan int, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					codes <- e.do("POST", "/login/v2/poll", "token="+token, withHeader("Content-Type", "application/x-www-form-urlencoded")).Code
				}()
			}
			wg.Wait()
			close(codes)
			successes := 0
			for code := range codes {
				if code == 200 {
					successes++
				} else if code != 404 {
					t.Fatalf("poll=%d", code)
				}
			}
			expected := 0
			if state == "concurrent" {
				expected = 1
			}
			if successes != expected {
				t.Fatalf("successes=%d want %d", successes, expected)
			}
		})
	}
}

func TestLoginFlowStateIsHashedAndRequestsAreBounded(t *testing.T) {
	e := newEnv(t)
	token, handle := beginLoginFlow(t, e)
	var browserHash, pollHash string
	if err := e.db.SQL().QueryRow(`SELECT browser_hash,poll_hash FROM login_flows_v2`).Scan(&browserHash, &pollHash); err != nil {
		t.Fatal(err)
	}
	if browserHash == handle || pollHash == token || browserHash == pollHash {
		t.Fatal("flow secrets not independently hashed")
	}
	for i := 0; i < 19; i++ {
		beginLoginFlow(t, e)
	}
	if r := e.do("POST", "/index.php/login/v2", nil); r.Code != 429 {
		t.Fatalf("unbounded initiation=%d", r.Code)
	}
	for _, body := range []string{"token=unknown", "token=" + token + "&token=" + token, strings.Repeat("x", 9000)} {
		r := e.do("POST", "/login/v2/poll", body, withHeader("Content-Type", "application/x-www-form-urlencoded"))
		if r.Code != 404 {
			t.Fatalf("bad poll=%d", r.Code)
		}
	}
}

func TestLoginFlowApprovalDoesNotPreventSessionPurge(t *testing.T) {
	e := newEnv(t)
	_, handle := beginLoginFlow(t, e)
	cookie, csrf := e.login("alice", alicePass)
	if r := e.do("POST", "/api/v1/auth/connect/approve", approvalBody(e, cookie, handle), withCookie(cookie), withCSRF(csrf)); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	e.do("POST", "/api/v1/auth/logout", nil, withCookie(cookie), withCSRF(csrf))
	if n, err := e.ids.PurgeExpiredSessions(context.Background()); err != nil || n != 1 {
		t.Fatalf("purge=%d %v", n, err)
	}
	var n int
	if err := e.db.SQL().QueryRow(`SELECT count(*) FROM login_flows_v2`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("remaining flows=%d %v", n, err)
	}
}

func TestLoginFlowContinuationIsSessionOnlyAndAllowlisted(t *testing.T) {
	e := newEnv(t)
	_, handle := beginLoginFlow(t, e)
	cookie, _ := e.login("alice", alicePass)
	addContinuation := func(value string) reqMod {
		return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "__Host-bookmarksync_connect", Value: value}) }
	}
	if r := e.do("GET", "/api/v1/auth/connect/continuation", nil, addContinuation(handle)); r.Code != 401 {
		t.Fatalf("anonymous continuation=%d", r.Code)
	}
	for _, value := range []string{handle, "https://evil.example", "a"} {
		r := e.do("GET", "/api/v1/auth/connect/continuation", nil, withCookie(cookie), addContinuation(value))
		if r.Code != 200 {
			t.Fatal(r.Code)
		}
		want := "/auth/security"
		if value == handle {
			want = "/auth/connect?flow=" + handle
		}
		if decode(t, r)["destination"] != want {
			t.Fatal(r.Body.String())
		}
		if !strings.Contains(r.Header().Get("Set-Cookie"), "HttpOnly") {
			t.Fatal("continuation cookie not cleared securely")
		}
	}
}

func approvalBody(e *env, cookie, handle string) map[string]any {
	session, _, err := e.ids.ResolveSession(context.Background(), cookie)
	if err != nil {
		e.t.Fatal(err)
	}
	return map[string]any{"flow": handle, "sessionId": session.ID}
}
