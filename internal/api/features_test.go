package api_test

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func num(v any) int64 { f, _ := v.(float64); return int64(f) }

// otherFolder returns the caller's Other Bookmarks folder id via the API.
func (e *tenv) otherFolder(m ...mod) int64 {
	e.t.Helper()
	rec := e.do("GET", "/api/v1/folders", nil, m...)
	if rec.Code != 200 {
		e.t.Fatalf("folders: %d %s", rec.Code, rec.Body.String())
	}
	for _, f := range decode(e.t, rec)["folders"].([]any) {
		n := f.(map[string]any)
		if n["title"] == "Other Bookmarks" {
			return num(n["id"])
		}
	}
	e.t.Fatal("no Other Bookmarks")
	return 0
}

func (e *tenv) mkBookmark(s sess, folder int64, url string, tags ...string) int64 {
	e.t.Helper()
	body := map[string]any{"url": url, "title": "T " + url, "folders": []int64{folder}}
	if tags != nil {
		body["tags"] = tags
	}
	rec := e.do("POST", "/api/v1/bookmarks", body, s.mods()...)
	if rec.Code != 201 {
		e.t.Fatalf("create bookmark: %d %s", rec.Code, rec.Body.String())
	}
	return num(decode(e.t, rec)["id"])
}

func (e *tenv) mkFolder(s sess, parent int64, title string) int64 {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/folders", map[string]any{"parentId": parent, "title": title}, s.mods()...)
	if rec.Code != 201 {
		e.t.Fatalf("create folder: %d %s", rec.Code, rec.Body.String())
	}
	return num(decode(e.t, rec)["id"])
}

func TestMeAndBookmarkCRUDWithTokensAndPagination(t *testing.T) {
	e := newTEnv(t)
	a := e.login(e.alice)
	// /me works for a session and for a token with any scope, and only shows self.
	for _, m := range [][]mod{{withCookie(a.cookie)}, {withBearer(e.token(e.alice, identity.ScopeSharesRead))}} {
		rec := e.do("GET", "/api/v1/me", nil, m...)
		me := decode(t, rec)
		if rec.Code != 200 || me["username"] != "alice" || me["isAdmin"] != false || num(me["id"]) != e.alice.ID {
			t.Fatalf("me: %d %v", rec.Code, me)
		}
	}
	other := e.otherFolder(withCookie(a.cookie))
	id := e.mkBookmark(a, other, "https://one.test", "go", "news")
	filteredFolder := e.mkFolder(a, other, "Filter target")
	filteredID := e.mkBookmark(a, filteredFolder, "https://folder-filter.test")
	filtered := e.do("GET", fmt.Sprintf("/api/v1/bookmarks?folder=%d", filteredFolder), nil, withCookie(a.cookie))
	filteredItems := decode(t, filtered)["items"].([]any)
	if filtered.Code != 200 || len(filteredItems) != 1 || num(filteredItems[0].(map[string]any)["id"]) != filteredID {
		t.Fatalf("folder filter: %d %s", filtered.Code, filtered.Body.String())
	}
	defaultPage := decode(t, e.do("GET", "/api/v1/bookmarks", nil, withCookie(a.cookie)))
	if num(defaultPage["limit"]) != 50 {
		t.Fatalf("default page size = %v, want 50", defaultPage["limit"])
	}
	rec := e.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d", id), nil, withBearer(e.token(e.alice, identity.ScopeBookmarksRead)))
	b := decode(t, rec)
	if rec.Code != 200 || b["url"] != "https://one.test" || len(b["tags"].([]any)) != 2 || num(b["folders"].([]any)[0]) != other {
		t.Fatalf("get: %d %v", rec.Code, b)
	}
	writer := e.token(e.alice, identity.ScopeBookmarksWrite)
	rec = e.do("PATCH", fmt.Sprintf("/api/v1/bookmarks/%d", id), map[string]any{"title": "Renamed"}, withBearer(writer))
	if rec.Code != 200 || decode(t, rec)["title"] != "Renamed" {
		t.Fatalf("patch by token: %d %s", rec.Code, rec.Body.String())
	}
	// Unknown fields and bad JSON are rejected.
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/bookmarks/%d", id), map[string]any{"titel": "x"}, a.mods()...); rec.Code != 400 {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/bookmarks", "{", append(a.mods(), withContentType("application/json"))...); rec.Code != 400 {
		t.Fatalf("bad json: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/bookmarks", map[string]any{"url": "ftp:" + strings.Repeat("a", 70000), "title": "x", "folders": []int64{other}}, a.mods()...); rec.Code != 413 {
		t.Fatalf("oversized JSON body: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/bookmarks", map[string]any{"url": "gopher://x", "title": "x", "folders": []int64{other}}, a.mods()...); rec.Code != 400 || errCode(t, rec) != "validation_failed" {
		t.Fatalf("bad scheme: %d %s", rec.Code, rec.Body.String())
	}
	// Pagination.
	for i := 0; i < 5; i++ {
		e.mkBookmark(a, other, fmt.Sprintf("https://p%d.test", i))
	}
	rec = e.do("GET", "/api/v1/bookmarks?limit=2&page=1", nil, withCookie(a.cookie))
	page := decode(t, rec)
	if rec.Code != 200 || len(page["items"].([]any)) != 2 || page["hasMore"] != true || num(page["limit"]) != 2 || num(page["page"]) != 1 {
		t.Fatalf("page: %v", page)
	}
	rec = e.do("GET", "/api/v1/bookmarks?limit=5000", nil, withCookie(a.cookie))
	if p := decode(t, rec); num(p["limit"]) != 200 || p["hasMore"] != false {
		t.Fatalf("limit not clamped: %v", p)
	}
	for _, q := range []string{"page=-1", "page=x", "limit=0", "page=1000001"} {
		if rec := e.do("GET", "/api/v1/bookmarks?"+q, nil, withCookie(a.cookie)); rec.Code != 400 {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	// Search, exact tag filter and tags listing.
	rec = e.do("GET", "/api/v1/bookmarks?tag=go", nil, withCookie(a.cookie))
	if items := decode(t, rec)["items"].([]any); len(items) != 1 {
		t.Fatalf("tag filter: %v", items)
	}
	rec = e.do("GET", "/api/v1/tags", nil, withCookie(a.cookie))
	if tags := decode(t, rec)["tags"].([]any); len(tags) != 2 || tags[0].(map[string]any)["name"] != "go" {
		t.Fatalf("tags: %v", tags)
	}
	// Folders: create, move, order, delete; canonical roots are protected.
	f1 := e.mkFolder(a, other, "F1")
	f2 := e.mkFolder(a, other, "F2")
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/folders/%d", f2), map[string]any{"parentId": f1}, a.mods()...); rec.Code != 200 || num(decode(t, rec)["parentId"]) != f1 {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/folders/%d", f1), map[string]any{"parentId": f2}, a.mods()...); rec.Code != 400 {
		t.Fatalf("cycle: %d", rec.Code)
	}
	if rec := e.do("PUT", fmt.Sprintf("/api/v1/folders/%d/order", other), map[string]any{"children": []map[string]any{{"type": "folder", "id": f1}}}, a.mods()...); rec.Code != 204 {
		t.Fatalf("order: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/folders/%d", other), nil, a.mods()...); rec.Code != 400 {
		t.Fatalf("delete canonical: %d", rec.Code)
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/folders/%d/bookmarks/%d", other, id), nil, a.mods()...); rec.Code != 204 {
		t.Fatalf("remove placement: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/bookmarks/%d", id), nil, a.mods()...); rec.Code != 404 {
		t.Fatalf("delete trashed bookmark: %d", rec.Code)
	}
}

func TestBookmarkPaginationHasNoGapsWhenDetectingMoreResults(t *testing.T) {
	e := newTEnv(t)
	a := e.login(e.alice)
	folder := e.otherFolder(withCookie(a.cookie))
	want := make([]int64, 0, 7)
	for i := 0; i < cap(want); i++ {
		want = append(want, e.mkBookmark(a, folder, fmt.Sprintf("https://page-%d.example", i)))
	}

	var got []int64
	for page := 0; page < 4; page++ {
		rec := e.do("GET", fmt.Sprintf("/api/v1/bookmarks?limit=2&page=%d", page), nil, withCookie(a.cookie))
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", page, rec.Code, rec.Body.String())
		}
		items := decode(t, rec)["items"].([]any)
		for _, item := range items {
			got = append(got, num(item.(map[string]any)["id"]))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("pagination returned %v, want all ids %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pagination returned ids %v, want %v", got, want)
		}
	}
}

func TestBearerHeaderTakesPrecedenceOverCookiesAndBasicNeverFallsBack(t *testing.T) {
	e := newTEnv(t)
	alice := e.login(e.alice)
	btok := e.token(e.bob, identity.ScopeBookmarksRead)
	got := e.do("GET", "/api/v1/me", nil, withCookie(alice.cookie), withBearer(btok))
	if got.Code != 200 || decode(t, got)["username"] != "bob" {
		t.Fatalf("bearer did not take precedence: %d %s", got.Code, got.Body.String())
	}
	basic := e.do("GET", "/api/v1/me", nil, withCookie(alice.cookie), withHeader("Authorization", "Basic YWxpY2U6cGFzcw=="))
	if basic.Code != 401 || errCode(t, basic) != "unauthenticated" {
		t.Fatalf("basic fell back to cookie: %d %s", basic.Code, basic.Body.String())
	}
	ambiguous := httptest.NewRequest(http.MethodGet, "http://bookmarks.example.test/api/v1/me", nil)
	ambiguous.RemoteAddr = "192.0.2.1:1000"
	ambiguous.Header.Set("Origin", origin)
	ambiguous.Header.Add("Authorization", "Bearer "+btok)
	ambiguous.Header.Add("Authorization", "Bearer "+btok)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, ambiguous)
	if rec.Code != 401 || errCode(t, rec) != "unauthenticated" {
		t.Fatalf("ambiguous authorization: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAccountIsolation(t *testing.T) {
	e := newTEnv(t)
	a, b := e.login(e.alice), e.login(e.bob)
	other := e.otherFolder(withCookie(a.cookie))
	f := e.mkFolder(a, other, "Private")
	id := e.mkBookmark(a, f, "https://alice-secret.test")
	e.do("DELETE", fmt.Sprintf("/api/v1/folders/%d", f), nil, a.mods()...)
	keep := e.mkBookmark(a, other, "https://alice-keep.test")
	for _, c := range []struct{ method, path string }{
		{"GET", fmt.Sprintf("/api/v1/bookmarks/%d", keep)},
		{"PATCH", fmt.Sprintf("/api/v1/bookmarks/%d", keep)},
		{"DELETE", fmt.Sprintf("/api/v1/bookmarks/%d", keep)},
		{"GET", fmt.Sprintf("/api/v1/folders/%d", other)},
		{"PATCH", fmt.Sprintf("/api/v1/folders/%d", other)},
		{"PUT", fmt.Sprintf("/api/v1/folders/%d/order", other)},
		{"GET", fmt.Sprintf("/api/v1/export?folder=%d", other)},
		{"POST", fmt.Sprintf("/api/v1/trash/folder/%d/restore", f)},
		{"POST", fmt.Sprintf("/api/v1/trash/bookmark/%d/restore", id)},
	} {
		body := any(nil)
		switch c.method {
		case "PATCH":
			body = map[string]any{"title": "pwned"}
		case "PUT":
			body = map[string]any{"children": []any{}}
		}
		if rec := e.do(c.method, c.path, body, b.mods()...); rec.Code != 404 {
			t.Errorf("bob %s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	rec := e.do("GET", "/api/v1/bookmarks", nil, withCookie(b.cookie))
	if strings.Contains(rec.Body.String(), "alice") {
		t.Fatal("bob's listing contains alice's data")
	}
	if rec := e.do("GET", "/api/v1/trash", nil, withCookie(b.cookie)); strings.Contains(rec.Body.String(), "alice") {
		t.Fatal("bob's trash contains alice's data")
	}
}

func TestSharesThroughTheAPI(t *testing.T) {
	e := newTEnv(t)
	a, b := e.login(e.alice), e.login(e.bob)
	other := e.otherFolder(withCookie(a.cookie))
	team := e.mkFolder(a, other, "Team")
	outside := e.mkFolder(a, other, "Outside")
	e.mkBookmark(a, team, "https://team.test")
	// Unknown recipients get the same 404 as any missing resource.
	if rec := e.do("POST", "/api/v1/shares", map[string]any{"folderId": team, "recipient": "nobody", "mode": "view"}, a.mods()...); rec.Code != 404 || errCode(t, rec) != "not_found" {
		t.Fatalf("unknown recipient: %d %s", rec.Code, rec.Body.String())
	}
	// Grant requires shares:manage when using a token.
	if rec := e.do("POST", "/api/v1/shares", map[string]any{"folderId": team, "recipient": "bob", "mode": "view"}, withBearer(e.token(e.alice, identity.ScopeSharesRead))); rec.Code != 403 {
		t.Fatalf("grant with shares:read: %d", rec.Code)
	}
	rec := e.do("POST", "/api/v1/shares", map[string]any{"folderId": team, "recipient": "bob", "mode": "view"}, withBearer(e.token(e.alice, identity.ScopeSharesManage)))
	if rec.Code != 201 {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	shareID := num(decode(t, rec)["id"])
	// Bob cannot grant alice's folder or change the share.
	if rec := e.do("POST", "/api/v1/shares", map[string]any{"folderId": team, "recipient": "root", "mode": "view"}, b.mods()...); rec.Code != 404 {
		t.Fatalf("non-owner grant: %d", rec.Code)
	}
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/shares/%d", shareID), map[string]any{"mode": "edit"}, b.mods()...); rec.Code != 404 {
		t.Fatalf("recipient changed mode: %d", rec.Code)
	}
	in := decode(t, e.do("GET", "/api/v1/shares/incoming", nil, withCookie(b.cookie)))["shares"].([]any)
	if len(in) != 1 || num(in[0].(map[string]any)["id"]) != shareID || in[0].(map[string]any)["folderTitle"] != "Team" {
		t.Fatalf("incoming: %v", in)
	}
	sq := fmt.Sprintf("?share=%d", shareID)
	// View grant: read works, writes refused.
	if rec := e.do("GET", "/api/v1/bookmarks"+sq, nil, withCookie(b.cookie)); rec.Code != 200 || len(decode(t, rec)["items"].([]any)) != 1 {
		t.Fatalf("shared read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/folders"+sq, map[string]any{"parentId": -1, "title": "x"}, b.mods()...); rec.Code != 403 || errCode(t, rec) != "read_only" {
		t.Fatalf("write via view grant: %d %s", rec.Code, rec.Body.String())
	}
	// Edit grant: writes inside work, outside are not found.
	e.do("PATCH", fmt.Sprintf("/api/v1/shares/%d", shareID), map[string]any{"mode": "edit"}, a.mods()...)
	if rec := e.do("POST", "/api/v1/folders"+sq, map[string]any{"parentId": -1, "title": "inside"}, b.mods()...); rec.Code != 201 {
		t.Fatalf("write via edit grant: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/folders"+sq, map[string]any{"parentId": outside, "title": "x"}, b.mods()...); rec.Code != 404 {
		t.Fatalf("write outside the share: %d", rec.Code)
	}
	// A third user cannot use the share.
	r := e.login(e.root)
	if rec := e.do("GET", "/api/v1/bookmarks"+sq, nil, withCookie(r.cookie)); rec.Code != 404 {
		t.Fatalf("stranger used the share: %d", rec.Code)
	}
	// Trash refuses ?share.
	if rec := e.do("GET", "/api/v1/trash"+sq, nil, withCookie(b.cookie)); rec.Code != 400 {
		t.Fatalf("trash with share: %d", rec.Code)
	}
	// The recipient may leave; then the share is gone.
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/shares/%d", shareID), nil, b.mods()...); rec.Code != 204 {
		t.Fatalf("leave: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/bookmarks"+sq, nil, withCookie(b.cookie)); rec.Code != 404 {
		t.Fatalf("after leaving: %d", rec.Code)
	}
	if owned := decode(t, e.do("GET", "/api/v1/shares/owned", nil, withCookie(a.cookie)))["shares"].([]any); len(owned) != 0 {
		t.Fatalf("owned after leave: %v", owned)
	}
}

func TestFloccusLockBlocksAPIWritesButNotReads(t *testing.T) {
	e := newTEnv(t)
	a := e.login(e.alice)
	other := e.otherFolder(withCookie(a.cookie))
	cred, _, _ := e.ids.CreateCredential(context.Background(), identity.NewCredential{UserID: e.alice.ID, Mode: identity.ModeEdit})
	sc, err := e.bm.ResolveScope(context.Background(), identity.Principal{CredentialID: cred.ID, User: e.alice, Kind: identity.KindPrivate, Mode: identity.ModeEdit})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.bm.AcquireLock(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	rec := e.do("POST", "/api/v1/folders", map[string]any{"parentId": other, "title": "x"}, a.mods()...)
	if rec.Code != 423 || errCode(t, rec) != "locked" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("write during floccus sync: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/bookmarks", nil, withCookie(a.cookie)); rec.Code != 200 {
		t.Fatalf("read during floccus sync: %d", rec.Code)
	}
}

func TestTrashOverHTTP(t *testing.T) {
	e := newTEnv(t)
	a := e.login(e.alice)
	other := e.otherFolder(withCookie(a.cookie))
	f := e.mkFolder(a, other, "Gone")
	e.mkBookmark(a, f, "https://gone.test")
	loose := e.mkBookmark(a, other, "https://loose.test")
	e.do("DELETE", fmt.Sprintf("/api/v1/folders/%d", f), nil, a.mods()...)
	e.do("DELETE", fmt.Sprintf("/api/v1/bookmarks/%d", loose), nil, a.mods()...)
	rec := e.do("GET", "/api/v1/trash", nil, withBearer(e.token(e.alice, identity.ScopeBookmarksRead)))
	items := decode(t, rec)["items"].([]any)
	if rec.Code != 200 || len(items) != 2 || items[0].(map[string]any)["purgeAt"] == nil {
		t.Fatalf("trash: %d %v", rec.Code, items)
	}
	if rec := e.do("POST", fmt.Sprintf("/api/v1/trash/folder/%d/restore", f), nil, withBearer(e.token(e.alice, identity.ScopeBookmarksWrite))); rec.Code != 200 {
		t.Fatalf("restore by token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/trash/widget/1/restore", nil, a.mods()...); rec.Code != 400 {
		t.Fatalf("bad type: %d", rec.Code)
	}
	// Purge and empty: recent web session only, never a token.
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/trash/bookmark/%d", loose), nil, withBearer(e.token(e.alice, identity.TokenScopes()...))); rec.Code != 403 || errCode(t, rec) != "token_not_allowed" {
		t.Fatalf("purge by token: %d", rec.Code)
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/trash/bookmark/%d", loose), nil, a.mods()...); rec.Code != 204 {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", "/api/v1/trash", nil, a.mods()...); rec.Code != 204 {
		t.Fatalf("empty: %d", rec.Code)
	}
	if items := decode(t, e.do("GET", "/api/v1/trash", nil, withCookie(a.cookie)))["items"].([]any); len(items) != 0 {
		t.Fatalf("trash after empty: %v", items)
	}
}

func multipartBody(t *testing.T, field, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte(content))
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestImportAndExport(t *testing.T) {
	e := newTEnv(t)
	a := e.login(e.alice)
	other := e.otherFolder(withCookie(a.cookie))
	ns := `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p>
		<DT><H3>Imported</H3><DL><p><DT><A HREF="https://imp.test" TAGS="x,y">Imp &lt;b&gt;</A></DL><p>
	</DL><p>`
	rec := e.do("POST", "/api/v1/import", ns, append(a.mods(), withContentType("text/html"))...)
	if rec.Code != 201 || num(decode(t, rec)["imported"]) != 2 {
		t.Fatalf("netscape import: %d %s", rec.Code, rec.Body.String())
	}
	xbel := `<?xml version="1.0"?><xbel version="1.0"><folder><title>X</title><bookmark href="https://x.test"><title>x</title></bookmark></folder></xbel>`
	body, ct := multipartBody(t, "file", "b.xbel", xbel)
	rec = e.do("POST", fmt.Sprintf("/api/v1/import?folder=%d", other), body.Bytes(), append(a.mods(), withContentType(ct))...)
	if rec.Code != 201 || num(decode(t, rec)["imported"]) != 2 {
		t.Fatalf("multipart xbel import: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/import", "x", append(a.mods(), withContentType("application/pdf"))...); rec.Code != 415 {
		t.Fatalf("unsupported type: %d", rec.Code)
	}
	laughs := `<?xml version="1.0"?><!DOCTYPE l [<!ENTITY a "aaaa"><!ENTITY b "&a;&a;&a;&a;">]><xbel><bookmark href="https://z.test"><title>&b;</title></bookmark></xbel>`
	if rec := e.do("POST", "/api/v1/import", laughs, append(a.mods(), withContentType("application/xbel+xml"))...); rec.Code != 400 {
		t.Fatalf("entity XBEL: %d %s", rec.Code, rec.Body.String())
	}
	// Atomic: a bad URL anywhere imports nothing.
	before := len(decode(t, e.do("GET", "/api/v1/bookmarks", nil, withCookie(a.cookie)))["items"].([]any))
	bad := `<DL><p><DT><A HREF="https://ok.test">ok</A><DT><A HREF="data:text/html,x">bad</A></DL>`
	if rec := e.do("POST", "/api/v1/import", bad, append(a.mods(), withContentType("text/html"))...); rec.Code != 400 {
		t.Fatalf("bad scheme import: %d", rec.Code)
	}
	if after := len(decode(t, e.do("GET", "/api/v1/bookmarks", nil, withCookie(a.cookie)))["items"].([]any)); after != before {
		t.Fatalf("partial import: %d -> %d", before, after)
	}
	// 4 MiB cap (above the server's default 1 MiB, below unlimited).
	huge := "<DL><p>" + strings.Repeat(" ", 4<<20) + "</DL>"
	if rec := e.do("POST", "/api/v1/import", huge, append(a.mods(), withContentType("text/html"))...); rec.Code != 413 && rec.Code != 400 {
		t.Fatalf("oversized import: %d", rec.Code)
	}
	mid := "<DL><p>" + strings.Repeat(" ", 2<<20) + `<DT><A HREF="https://mid.test">m</A></DL>`
	if rec := e.do("POST", "/api/v1/import", mid, append(a.mods(), withContentType("text/html"))...); rec.Code != 201 {
		t.Fatalf("2 MiB import refused: %d %s", rec.Code, rec.Body.String())
	}
	// Export: attachment, escaped, tags kept.
	rec = e.do("GET", "/api/v1/export?format=netscape", nil, withBearer(e.token(e.alice, identity.ScopeBookmarksRead)))
	out := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("export headers: %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(out, `TAGS="x,y"`) || strings.Contains(out, "Imp <b>") || !strings.Contains(out, "Imp &lt;b&gt;") {
		t.Fatalf("export body:\n%s", out)
	}
	rec = e.do("GET", "/api/v1/export?format=xbel", nil, withCookie(a.cookie))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "xbel") || !strings.Contains(rec.Body.String(), "https://x.test") {
		t.Fatalf("xbel export: %d %s", rec.Code, rec.Header())
	}
	if rec := e.do("GET", "/api/v1/export?format=pdf", nil, withCookie(a.cookie)); rec.Code != 400 {
		t.Fatalf("bad format: %d", rec.Code)
	}
}

func TestTokensAndConnectionsShowSecretsOnce(t *testing.T) {
	e := newTEnv(t)
	a, b := e.login(e.alice), e.login(e.bob)
	rec := e.do("POST", "/api/v1/me/tokens", map[string]any{"label": "ci", "scopes": []string{"bookmarks:read"}}, a.mods()...)
	if rec.Code != 201 {
		t.Fatalf("token: %d %s", rec.Code, rec.Body.String())
	}
	secret, _ := decode(t, rec)["secret"].(string)
	tokID := num(decode(t, rec)["token"].(map[string]any)["id"])
	if !strings.HasPrefix(secret, "bmt1_") {
		t.Fatalf("secret %q", secret)
	}
	list := e.do("GET", "/api/v1/me/tokens", nil, withCookie(a.cookie))
	if strings.Contains(list.Body.String(), secret) || strings.Contains(e.logs.String(), secret) {
		t.Fatal("token secret listed or logged")
	}
	for name, body := range map[string]any{
		"bad scope": map[string]any{"label": "x", "scopes": []string{"admin"}},
		"no scope":  map[string]any{"label": "x", "scopes": []string{}},
		"past":      map[string]any{"label": "x", "scopes": []string{"bookmarks:read"}, "expiresAt": e.clock.Now().Add(-time.Hour)},
	} {
		if rec := e.do("POST", "/api/v1/me/tokens", body, a.mods()...); rec.Code != 400 {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/me/tokens/%d", tokID), nil, b.mods()...); rec.Code != 404 {
		t.Fatalf("bob revoked alice's token: %d", rec.Code)
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/me/tokens/%d", tokID), nil, a.mods()...); rec.Code != 204 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := e.do("GET", "/api/v1/me", nil, withBearer(secret)); rec.Code != 401 {
		t.Fatalf("revoked token works: %d", rec.Code)
	}
	// Connections.
	rec = e.do("POST", "/api/v1/me/connections", map[string]any{"label": "laptop", "mode": "edit"}, a.mods()...)
	if rec.Code != 201 {
		t.Fatalf("connection: %d %s", rec.Code, rec.Body.String())
	}
	csecret := decode(t, rec)["secret"].(string)
	cid := num(decode(t, rec)["connection"].(map[string]any)["id"])
	if strings.Contains(e.do("GET", "/api/v1/me/connections", nil, withCookie(a.cookie)).Body.String(), csecret) {
		t.Fatal("connection secret listed")
	}
	if _, err := e.ids.Authenticate(context.Background(), "alice", csecret); err != nil {
		t.Fatal("new connection does not authenticate floccus")
	}
	// A share connection needs a share where the caller is the recipient.
	other := e.otherFolder(withCookie(a.cookie))
	team := e.mkFolder(a, other, "Team")
	sh := decode(t, e.do("POST", "/api/v1/shares", map[string]any{"folderId": team, "recipient": "bob", "mode": "view"}, a.mods()...))
	if rec := e.do("POST", "/api/v1/me/connections", map[string]any{"label": "x", "mode": "view", "shareId": num(sh["id"])}, a.mods()...); rec.Code != 404 {
		t.Fatalf("owner made a recipient connection: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/me/connections", map[string]any{"label": "x", "mode": "view", "shareId": num(sh["id"])}, b.mods()...); rec.Code != 201 {
		t.Fatalf("recipient connection: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/me/connections/%d", cid), nil, b.mods()...); rec.Code != 404 {
		t.Fatalf("bob revoked alice's connection: %d", rec.Code)
	}
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/me/connections/%d", cid), nil, a.mods()...); rec.Code != 204 {
		t.Fatalf("revoke connection: %d", rec.Code)
	}
	if _, err := e.ids.Authenticate(context.Background(), "alice", csecret); err == nil {
		t.Fatal("revoked connection still authenticates")
	}
}

// A share connection's mode may not exceed the live grant: an edit connection on a
// view-only share is refused and stores nothing, so a later owner upgrade cannot
// silently make a credential writable that the recipient never could request.
func TestShareConnectionModeCannotExceedGrant(t *testing.T) {
	e := newTEnv(t)
	a, b := e.login(e.alice), e.login(e.bob)
	other := e.otherFolder(withCookie(a.cookie))
	view := decode(t, e.do("POST", "/api/v1/shares", map[string]any{"folderId": e.mkFolder(a, other, "View"), "recipient": "bob", "mode": "view"}, a.mods()...))
	edit := decode(t, e.do("POST", "/api/v1/shares", map[string]any{"folderId": e.mkFolder(a, other, "Edit"), "recipient": "bob", "mode": "edit"}, a.mods()...))
	countBob := func() int {
		cs, err := e.ids.ListCredentials(context.Background(), e.bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	if rec := e.do("POST", "/api/v1/me/connections", map[string]any{"label": "x", "mode": "edit", "shareId": num(view["id"])}, b.mods()...); rec.Code != 403 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("edit connection on a view share: %d %s", rec.Code, rec.Body.String())
	}
	if n := countBob(); n != 0 {
		t.Fatalf("refused edit connection stored %d credential rows", n)
	}
	if list := decode(t, e.do("GET", "/api/v1/me/connections", nil, withCookie(b.cookie)))["connections"].([]any); len(list) != 0 {
		t.Fatalf("refused edit connection listed: %v", list)
	}
	// The grant still allows a view connection, and an edit grant an edit connection.
	if rec := e.do("POST", "/api/v1/me/connections", map[string]any{"label": "v", "mode": "view", "shareId": num(view["id"])}, b.mods()...); rec.Code != 201 {
		t.Fatalf("view connection on a view share: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/me/connections", map[string]any{"label": "e", "mode": "edit", "shareId": num(edit["id"])}, b.mods()...); rec.Code != 201 {
		t.Fatalf("edit connection on an edit share: %d %s", rec.Code, rec.Body.String())
	}
	if n := countBob(); n != 2 {
		t.Fatalf("credential rows: %d, want 2", n)
	}
}

func TestPasswordChangeAndSettings(t *testing.T) {
	e := newTEnv(t)
	a := e.login(e.alice)
	other := e.login(e.alice)
	tok := e.token(e.alice, identity.ScopeBookmarksRead)
	if rec := e.do("PATCH", "/api/v1/me/password", map[string]any{"currentPassword": "wrong password!!", "newPassword": "another long password"}, a.mods()...); rec.Code != 401 {
		t.Fatalf("wrong current password: %d", rec.Code)
	}
	rec := e.do("PATCH", "/api/v1/me/password", map[string]any{"currentPassword": pass, "newPassword": "another long password"}, a.mods()...)
	if rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body.String())
	}
	var fresh string
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			fresh = c.Value
		}
	}
	csrf, _ := decode(t, rec)["csrfToken"].(string)
	if fresh == "" || fresh == a.cookie || csrf == "" {
		t.Fatal("session not rotated")
	}
	if e.do("GET", "/api/v1/me", nil, withCookie(other.cookie)).Code != 401 || e.do("GET", "/api/v1/me", nil, withBearer(tok)).Code != 401 {
		t.Fatal("other session or API token survived the password change")
	}
	cur := sess{cookie: fresh, csrf: csrf}
	rec = e.do("PATCH", "/api/v1/me/settings", map[string]any{"bookmarks.view": "grid", "ui.timezone": "Asia/Riyadh"}, cur.mods()...)
	if rec.Code != 200 || decode(t, rec)["settings"].(map[string]any)["bookmarks.view"] != "grid" {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []any{map[string]any{"theme": "dark"}, map[string]any{"bookmarks.view": 3}, map[string]any{"ui.timezone": "Nowhere/City"}} {
		if rec := e.do("PATCH", "/api/v1/me/settings", body, cur.mods()...); rec.Code != 400 {
			t.Fatalf("bad settings %v: %d", body, rec.Code)
		}
	}
	if s := decode(t, e.do("GET", "/api/v1/me/settings", nil, withCookie(e.login(e.bob).cookie)))["settings"].(map[string]any); len(s) != 0 {
		t.Fatalf("bob sees settings: %v", s)
	}
}

func TestAdminAPI(t *testing.T) {
	e := newTEnv(t)
	r := e.login(e.root)
	a := e.login(e.alice)
	// Seed private data that must never appear in admin responses.
	other := e.otherFolder(withCookie(a.cookie))
	secretFolder := e.mkFolder(a, other, "SECRET-FOLDER-NAME")
	e.mkBookmark(a, secretFolder, "https://secret-url.example", "secret-tag")
	e.do("POST", "/api/v1/shares", map[string]any{"folderId": secretFolder, "recipient": "bob", "mode": "view"}, a.mods()...)
	e.do("POST", "/api/v1/me/connections", map[string]any{"label": "SECRET-LABEL-ignored-ok", "mode": "edit"}, e.login(e.alice).mods()...)

	rec := e.do("GET", "/api/v1/admin/users", nil, withCookie(r.cookie))
	if rec.Code != 200 {
		t.Fatalf("admin users: %d %s", rec.Code, rec.Body.String())
	}
	var aliceRow map[string]any
	adminPage := decode(t, rec)
	for _, u := range adminPage["users"].([]any) {
		if u.(map[string]any)["username"] == "alice" {
			aliceRow = u.(map[string]any)
		}
	}
	if aliceRow == nil || num(aliceRow["bookmarkCount"]) != 1 || num(aliceRow["folderCount"]) != 1 || num(aliceRow["storageBytes"]) <= 0 || num(aliceRow["connectionCount"]) != 1 || adminPage["limit"] != float64(50) || adminPage["page"] != float64(0) || adminPage["hasMore"] != false {
		t.Fatalf("alice row = %v", aliceRow)
	}
	firstAdminPage := decode(t, e.do("GET", "/api/v1/admin/users?limit=2&page=0", nil, withCookie(r.cookie)))
	secondAdminPage := decode(t, e.do("GET", "/api/v1/admin/users?limit=2&page=1", nil, withCookie(r.cookie)))
	if len(firstAdminPage["users"].([]any)) != 2 || firstAdminPage["hasMore"] != true || len(secondAdminPage["users"].([]any)) != 1 || secondAdminPage["hasMore"] != false {
		t.Fatalf("admin user pages: first=%v second=%v", firstAdminPage, secondAdminPage)
	}
	for _, v := range firstAdminPage["users"].([]any) {
		if _, ok := v.(map[string]any)["lastContactAt"]; !ok {
			t.Fatalf("admin user row omits lastContactAt: %v", v)
		}
	}
	sys := e.do("GET", "/api/v1/admin/system", nil, withCookie(r.cookie))
	system := decode(t, sys)
	if sys.Code != 200 || num(system["users"].(map[string]any)["admins"]) != 1 || num(system["bookmarks"].(map[string]any)["total"]) != 1 || num(system["storageBytes"]) <= 0 {
		t.Fatalf("system: %d %s", sys.Code, sys.Body.String())
	}
	conns := e.do("GET", fmt.Sprintf("/api/v1/admin/users/%d/connections", e.alice.ID), nil, withCookie(r.cookie))
	for _, resp := range []string{rec.Body.String(), sys.Body.String(), conns.Body.String()} {
		for _, secret := range []string{"SECRET-FOLDER-NAME", "secret-url", "secret-tag"} {
			if strings.Contains(resp, secret) {
				t.Fatalf("admin response leaks %q: %s", secret, resp)
			}
		}
	}
	// Create, promote, last-admin protection.
	rec = e.do("POST", "/api/v1/admin/users", map[string]any{"username": "dave", "password": pass, "isAdmin": false}, r.mods()...)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	dave := num(decode(t, rec)["id"])
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", e.root.ID), map[string]any{"isAdmin": false}, r.mods()...); rec.Code != 409 || errCode(t, rec) != "last_admin" {
		t.Fatalf("demote last admin: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", e.root.ID), map[string]any{"active": false}, r.mods()...); rec.Code != 409 {
		t.Fatalf("deactivate last admin: %d", rec.Code)
	}
	// Deactivate alice: her session and tokens die.
	atok := e.token(e.alice, identity.ScopeBookmarksRead)
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", e.alice.ID), map[string]any{"active": false}, r.mods()...); rec.Code != 200 {
		t.Fatalf("deactivate: %d %s", rec.Code, rec.Body.String())
	}
	if e.do("GET", "/api/v1/me", nil, withCookie(a.cookie)).Code != 401 || e.do("GET", "/api/v1/me", nil, withBearer(atok)).Code != 401 {
		t.Fatal("deactivated user still authenticated")
	}
	// Password and factor resets.
	d := func() sess { return e.loginAs("dave", pass) }
	ds := d()
	if rec := e.do("POST", fmt.Sprintf("/api/v1/admin/users/%d/password-reset", dave), map[string]any{"newPassword": "admin set password"}, r.mods()...); rec.Code != 204 {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	if e.do("GET", "/api/v1/me", nil, withCookie(ds.cookie)).Code != 401 {
		t.Fatal("session survived password reset")
	}
	if rec := e.do("POST", fmt.Sprintf("/api/v1/admin/users/%d/factor-reset", dave), map[string]any{"totp": true}, r.mods()...); rec.Code != 204 {
		t.Fatalf("factor reset: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", fmt.Sprintf("/api/v1/admin/users/%d/factor-reset", dave), map[string]any{}, r.mods()...); rec.Code != 400 {
		t.Fatalf("empty factor reset: %d", rec.Code)
	}
	// A demoted admin loses access on the next request.
	e.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", dave), map[string]any{"isAdmin": true}, r.mods()...)
	dv := e.loginAs("dave", "admin set password")
	if e.do("GET", "/api/v1/admin/users", nil, withCookie(dv.cookie)).Code != 200 {
		t.Fatal("promoted admin denied")
	}
	e.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", dave), map[string]any{"isAdmin": false}, r.mods()...)
	if rec := e.do("GET", "/api/v1/admin/users", nil, withCookie(dv.cookie)); rec.Code != 403 {
		t.Fatalf("demoted admin still allowed: %d", rec.Code)
	}
	if rec := e.do("PATCH", "/api/v1/admin/users/99999", map[string]any{"active": true}, r.mods()...); rec.Code != 404 {
		t.Fatalf("unknown user: %d", rec.Code)
	}
	_ = bookmarks.RootID
}

func (e *tenv) loginAs(username, password string) sess {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"username": username, "password": password})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", username, rec.Code, rec.Body.String())
	}
	var c string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.SessionCookieName {
			c = ck.Value
		}
	}
	return sess{cookie: c, csrf: decode(e.t, rec)["csrfToken"].(string)}
}
