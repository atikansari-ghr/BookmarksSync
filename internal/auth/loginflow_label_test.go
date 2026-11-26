package auth_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func pollLoginFlowFor(t *testing.T, e *env, token string) string {
	t.Helper()
	r := e.do("POST", "/login/v2/poll", "token="+url.QueryEscape(token), withHeader("Content-Type", "application/x-www-form-urlencoded"), withOrigin(""))
	if r.Code != 200 {
		t.Fatalf("poll = %d %s", r.Code, r.Body.String())
	}
	return decode(t, r)["appPassword"].(string)
}

func labelOfAppPassword(t *testing.T, e *env, secret string) string {
	t.Helper()
	ctx := context.Background()
	p, err := e.ids.Authenticate(ctx, "alice", secret)
	if err != nil || string(p.Kind) != "private" || string(p.Mode) != "edit" {
		t.Fatalf("app password = %+v %v", p, err)
	}
	cs, err := e.ids.ListCredentials(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == p.CredentialID {
			return c.Label
		}
	}
	t.Fatal("credential not listed")
	return ""
}

func TestLoginFlowApprovalNamesTheConnection(t *testing.T) {
	e := newEnv(t)
	token, handle := beginLoginFlow(t, e)
	cookie, csrf := e.login("alice", alicePass)
	body := approvalBody(e, cookie, handle)
	body["label"] = "  BookmarksSync-Home-Firefox\t"
	r := e.do("POST", "/api/v1/auth/connect/approve", body, withCookie(cookie), withCSRF(csrf))
	if r.Code != 200 || decode(t, r)["status"] != "approved" {
		t.Fatalf("approval = %d %s", r.Code, r.Body.String())
	}
	if got := labelOfAppPassword(t, e, pollLoginFlowFor(t, e, token)); got != "BookmarksSync-Home-Firefox" {
		t.Fatalf("label = %q", got)
	}
}

func TestLoginFlowApprovalWithoutLabelKeepsLegacyName(t *testing.T) {
	for name, extra := range map[string]map[string]any{"omitted": nil, "null": {"label": nil}} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			token, handle := beginLoginFlow(t, e)
			cookie, csrf := e.login("alice", alicePass)
			body := approvalBody(e, cookie, handle)
			for k, v := range extra {
				body[k] = v
			}
			if r := e.do("POST", "/api/v1/auth/connect/approve", body, withCookie(cookie), withCSRF(csrf)); r.Code != 200 {
				t.Fatalf("approval = %d %s", r.Code, r.Body.String())
			}
			if got := labelOfAppPassword(t, e, pollLoginFlowFor(t, e, token)); got != "Login Flow v2 connection" {
				t.Fatalf("label = %q", got)
			}
		})
	}
}

func TestLoginFlowApprovalRejectsInvalidLabels(t *testing.T) {
	e := newEnv(t)
	token, handle := beginLoginFlow(t, e)
	cookie, csrf := e.login("alice", alicePass)
	for _, bad := range []any{"", "   ", strings.Repeat("x", 201), "a\nb", "a\u0000b", "a\u0007b", 42, true, []string{"x"}} {
		body := approvalBody(e, cookie, handle)
		body["label"] = bad
		r := e.do("POST", "/api/v1/auth/connect/approve", body, withCookie(cookie), withCSRF(csrf))
		if r.Code != 400 {
			t.Fatalf("label %#v: %d %s", bad, r.Code, r.Body.String())
		}
	}
	// Rejected approvals neither consumed nor approved the flow.
	if r := e.do("POST", "/login/v2/poll", "token="+url.QueryEscape(token), withHeader("Content-Type", "application/x-www-form-urlencoded"), withOrigin("")); r.Code != 404 {
		t.Fatalf("rejected approval made the flow redeemable: %d", r.Code)
	}
	if r := e.do("GET", "/api/v1/auth/connect?flow="+handle, nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("flow no longer pending: %d", r.Code)
	}
	body := approvalBody(e, cookie, handle)
	body["label"] = strings.Repeat("書", 200)
	if r := e.do("POST", "/api/v1/auth/connect/approve", body, withCookie(cookie), withCSRF(csrf)); r.Code != 200 {
		t.Fatalf("200-rune label: %d %s", r.Code, r.Body.String())
	}
	if got := labelOfAppPassword(t, e, pollLoginFlowFor(t, e, token)); got != strings.Repeat("書", 200) {
		t.Fatalf("label = %q", got)
	}
	// A named approval still requires CSRF protection.
	token2, handle2 := beginLoginFlow(t, e)
	body = approvalBody(e, cookie, handle2)
	body["label"] = "BookmarksSync-Home-Firefox"
	if r := e.do("POST", "/api/v1/auth/connect/approve", body, withCookie(cookie)); r.Code != 403 {
		t.Fatalf("named approval without CSRF = %d", r.Code)
	}
	if r := e.do("POST", "/login/v2/poll", "token="+url.QueryEscape(token2), withHeader("Content-Type", "application/x-www-form-urlencoded"), withOrigin("")); r.Code != 404 {
		t.Fatal("CSRF-less approval took effect")
	}
}
