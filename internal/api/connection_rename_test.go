package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func (e *tenv) credentialSnapshot(id int64) string {
	e.t.Helper()
	var out string
	err := e.db.SQL().QueryRowContext(context.Background(), `SELECT id||'|'||user_id||'|'||secret_hash||'|'||kind||'|'||coalesce(share_id,'-')||'|'||mode||'|'||created_at||'|'||coalesce(last_used_at,'-')||'|'||coalesce(revoked_at,'-') FROM credentials WHERE id=?`, id).Scan(&out)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *tenv) credentialLabel(id int64) string {
	e.t.Helper()
	var label string
	if err := e.db.SQL().QueryRowContext(context.Background(), `SELECT label FROM credentials WHERE id=?`, id).Scan(&label); err != nil {
		e.t.Fatal(err)
	}
	return label
}

func TestRenameConnectionKeepsSecretAndPermissions(t *testing.T) {
	e := newTEnv(t)
	a := e.login(e.alice)
	rec := e.do("POST", "/api/v1/me/connections", map[string]any{"label": "Login Flow v2 connection", "mode": "edit"}, a.mods()...)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	created := decode(t, rec)
	secret := created["secret"].(string)
	cid := num(created["connection"].(map[string]any)["id"])
	if _, err := e.ids.Authenticate(context.Background(), "alice", secret); err != nil {
		t.Fatal(err)
	}
	before := e.credentialSnapshot(cid)
	var hash string
	if err := e.db.SQL().QueryRow(`SELECT secret_hash FROM credentials WHERE id=?`, cid).Scan(&hash); err != nil {
		t.Fatal(err)
	}

	// Rename is metadata-only and therefore does not require recent reauthentication.
	e.clock.Advance(auth.ReauthWindow + 1)
	rec = e.do("PATCH", fmt.Sprintf("/api/v1/me/connections/%d", cid), map[string]any{"label": "  BookmarksSync-Home-Firefox "}, a.mods()...)
	if rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if len(body) != 1 {
		t.Fatalf("rename response has extra fields: %v", body)
	}
	conn, ok := body["connection"].(map[string]any)
	if !ok {
		t.Fatalf("rename response = %v", body)
	}
	if num(conn["id"]) != cid || conn["label"] != "BookmarksSync-Home-Firefox" || conn["kind"] != "private" || conn["mode"] != "edit" || num(conn["shareId"]) != 0 || conn["revoked"] != false || conn["createdAt"] == nil {
		t.Fatalf("renamed connection = %v", conn)
	}
	if _, has := conn["secret"]; has || strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), hash) || strings.Contains(rec.Body.String(), strings.Split(secret, ".")[2]) {
		t.Fatalf("rename response leaked secret material: %s", rec.Body.String())
	}
	if got := e.credentialLabel(cid); got != "BookmarksSync-Home-Firefox" {
		t.Fatalf("stored label = %q", got)
	}
	if after := e.credentialSnapshot(cid); after != before {
		t.Fatalf("rename changed credential metadata:\n before %s\n after  %s", before, after)
	}
	p, err := e.ids.Authenticate(context.Background(), "alice", secret)
	if err != nil || p.CredentialID != cid || p.Mode != identity.ModeEdit || p.Kind != identity.KindPrivate {
		t.Fatalf("secret after rename = %+v %v", p, err)
	}
	list := decode(t, e.do("GET", "/api/v1/me/connections", nil, withCookie(a.cookie)))["connections"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["label"] != "BookmarksSync-Home-Firefox" {
		t.Fatalf("listed connections = %v", list)
	}

	// A share connection keeps its kind, share and view-only mode.
	other := e.otherFolder(withCookie(a.cookie))
	sh := decode(t, e.do("POST", "/api/v1/shares", map[string]any{"folderId": e.mkFolder(a, other, "Team"), "recipient": "bob", "mode": "view"}, a.mods()...))
	b := e.login(e.bob)
	rec = e.do("POST", "/api/v1/me/connections", map[string]any{"label": "share", "mode": "view", "shareId": num(sh["id"])}, b.mods()...)
	if rec.Code != 201 {
		t.Fatalf("share connection: %d %s", rec.Code, rec.Body.String())
	}
	shareConn := decode(t, rec)
	sid := num(shareConn["connection"].(map[string]any)["id"])
	shareBefore := e.credentialSnapshot(sid)
	rec = e.do("PATCH", fmt.Sprintf("/api/v1/me/connections/%d", sid), map[string]any{"label": "Team on Firefox"}, b.mods()...)
	if rec.Code != 200 {
		t.Fatalf("share rename: %d %s", rec.Code, rec.Body.String())
	}
	conn = decode(t, rec)["connection"].(map[string]any)
	if conn["kind"] != "share" || conn["mode"] != "view" || num(conn["shareId"]) != num(sh["id"]) || e.credentialSnapshot(sid) != shareBefore {
		t.Fatalf("share connection changed: %v", conn)
	}
	sp, err := e.ids.Authenticate(context.Background(), "bob", shareConn["secret"].(string))
	if err != nil || sp.Mode != identity.ModeView || sp.Kind != identity.KindShare || sp.ShareID != num(sh["id"]) {
		t.Fatalf("share secret after rename = %+v %v", sp, err)
	}
}

func TestRenameConnectionAuthorizationAndValidation(t *testing.T) {
	e := newTEnv(t)
	a, b := e.login(e.alice), e.login(e.bob)
	rec := e.do("POST", "/api/v1/me/connections", map[string]any{"label": "original", "mode": "edit"}, a.mods()...)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	cid := num(decode(t, rec)["connection"].(map[string]any)["id"])
	path := fmt.Sprintf("/api/v1/me/connections/%d", cid)
	rename := map[string]any{"label": "renamed"}
	token := e.token(e.alice, identity.ScopeBookmarksRead, identity.ScopeBookmarksWrite, identity.ScopeSharesRead, identity.ScopeSharesManage)
	for _, tc := range []struct {
		name string
		mods []mod
		path string
		want int
	}{
		{"no session", nil, path, 401},
		{"no csrf", []mod{withCookie(a.cookie)}, path, 403},
		{"wrong csrf", []mod{withCookie(a.cookie), withCSRF("nope")}, path, 403},
		{"foreign origin", append(a.mods(), withHeader("Origin", "https://evil.example")), path, 403},
		{"personal token", []mod{withBearer(token)}, path, 403},
		{"other owner", b.mods(), path, 404},
		{"nonexistent", a.mods(), "/api/v1/me/connections/999999", 404},
		{"non-numeric id", a.mods(), "/api/v1/me/connections/abc", 404},
		{"zero id", a.mods(), "/api/v1/me/connections/0", 404},
		{"negative id", a.mods(), "/api/v1/me/connections/-1", 404},
	} {
		if rec := e.do("PATCH", tc.path, rename, tc.mods...); rec.Code != tc.want {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if got := e.credentialLabel(cid); got != "original" {
		t.Fatalf("refused renames changed label to %q", got)
	}
	for i, body := range []any{
		map[string]any{"label": ""},
		map[string]any{"label": " \t "},
		map[string]any{"label": strings.Repeat("x", 201)},
		map[string]any{"label": strings.Repeat("é", 201)},
		map[string]any{"label": "a\nb"},
		map[string]any{"label": "a\u0000b"},
		map[string]any{"label": "a\u0007b"},
		map[string]any{},
		map[string]any{"label": nil},
		map[string]any{"label": 7},
		map[string]any{"label": "ok", "mode": "view"},
		"label=x",
	} {
		rec := e.do("PATCH", path, body, append(a.mods(), withContentType("application/json"))...)
		if rec.Code != 400 {
			t.Errorf("invalid body %d (%v): %d %s", i, body, rec.Code, rec.Body.String())
		}
	}
	if got := e.credentialLabel(cid); got != "original" {
		t.Fatalf("invalid renames changed label to %q", got)
	}
	if rec := e.do("PATCH", path, map[string]any{"label": strings.Repeat("書", 200)}, a.mods()...); rec.Code != 200 {
		t.Fatalf("200-rune label: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", path, nil, a.mods()...); rec.Code != 204 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := e.do("PATCH", path, rename, a.mods()...); rec.Code != 404 {
		t.Fatalf("revoked connection rename: %d %s", rec.Code, rec.Body.String())
	}
	if got := e.credentialLabel(cid); got != strings.Repeat("書", 200) {
		t.Fatalf("revoked connection label changed to %q", got)
	}
}
