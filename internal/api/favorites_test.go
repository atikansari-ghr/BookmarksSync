package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

type favFixture struct {
	shared, hiddenF int64
	visible, hidden int64
	share           int64
}

// favSetup gives alice a folder shared view-only with bob holding one bookmark, plus a
// private folder holding another bookmark.
func favSetup(t *testing.T, e *tenv) favFixture {
	t.Helper()
	ctx := context.Background()
	sc, err := e.bm.PrivateScope(ctx, e.alice.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	kids, err := e.bm.FolderTree(ctx, sc, bookmarks.RootID, 1)
	if err != nil || len(kids) < 2 {
		t.Fatalf("roots: %v %v", kids, err)
	}
	other := kids[1].ID
	shared, err := e.bm.CreateFolder(ctx, sc, other, "Shared")
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := e.bm.CreateFolder(ctx, sc, other, "Hidden")
	if err != nil {
		t.Fatal(err)
	}
	vb, err := e.bm.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://visible.test/", Title: "V", Folders: []int64{shared.ID}})
	if err != nil {
		t.Fatal(err)
	}
	hb, err := e.bm.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://hidden.test/", Title: "H", Folders: []int64{hidden.ID}})
	if err != nil {
		t.Fatal(err)
	}
	sh, err := e.bm.GrantShare(ctx, e.alice.ID, shared.ID, e.bob.ID, identity.ModeView)
	if err != nil {
		t.Fatal(err)
	}
	return favFixture{shared: shared.ID, hiddenF: hidden.ID, visible: vb.ID, hidden: hb.ID, share: sh.ID}
}

func favoriteOf(t *testing.T, item map[string]any) bool {
	t.Helper()
	v, ok := item["favorite"].(bool)
	if !ok {
		t.Fatalf("bookmark JSON has no boolean favorite: %v", item)
	}
	return v
}

func listIDs(t *testing.T, e *tenv, path string, mods ...mod) []int64 {
	t.Helper()
	rec := e.do("GET", path, nil, mods...)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	items, _ := decode(t, rec)["items"].([]any)
	ids := []int64{}
	for _, it := range items {
		m := it.(map[string]any)
		favoriteOf(t, m)
		ids = append(ids, int64(m["id"].(float64)))
	}
	return ids
}

func TestFavoriteRoutesSetClearAndFilter(t *testing.T) {
	e := newTEnv(t)
	f := favSetup(t, e)
	alice := e.login(e.alice)
	path := fmt.Sprintf("/api/v1/bookmarks/%d/favorite", f.visible)

	got := decode(t, e.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d", f.visible), nil, alice.mods()...))
	if favoriteOf(t, got) {
		t.Fatal("new bookmark reported as favorite")
	}
	for i := 0; i < 2; i++ { // idempotent
		rec := e.do("PUT", path, nil, alice.mods()...)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("PUT favorite #%d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	got = decode(t, e.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d", f.visible), nil, alice.mods()...))
	if !favoriteOf(t, got) {
		t.Fatal("favorite not reported after PUT")
	}
	if ids := listIDs(t, e, "/api/v1/bookmarks?favorite=true", alice.mods()...); len(ids) != 1 || ids[0] != f.visible {
		t.Fatalf("favorite=true = %v", ids)
	}
	if ids := listIDs(t, e, "/api/v1/bookmarks?favorite=false", alice.mods()...); len(ids) != 1 || ids[0] != f.hidden {
		t.Fatalf("favorite=false = %v", ids)
	}
	if ids := listIDs(t, e, "/api/v1/bookmarks", alice.mods()...); len(ids) != 2 {
		t.Fatalf("unfiltered = %v", ids)
	}
	for _, bad := range []string{"yes", "1", "TRUE", ""} {
		rec := e.do("GET", "/api/v1/bookmarks?favorite="+bad, nil, alice.mods()...)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("favorite=%q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	for i := 0; i < 2; i++ { // idempotent
		rec := e.do("DELETE", path, nil, alice.mods()...)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("DELETE favorite #%d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if ids := listIDs(t, e, "/api/v1/bookmarks?favorite=true", alice.mods()...); len(ids) != 0 {
		t.Fatalf("favorite=true after DELETE = %v", ids)
	}
	// Session writes need the CSRF token.
	if rec := e.do("PUT", path, nil, withCookie(alice.cookie)); rec.Code != http.StatusForbidden || errCode(t, rec) != "csrf_failed" {
		t.Fatalf("PUT without CSRF: %d %s", rec.Code, rec.Body.String())
	}
	// Unknown, malformed and other-user ids are not found.
	for _, p := range []string{"/api/v1/bookmarks/999999/favorite", "/api/v1/bookmarks/abc/favorite", "/api/v1/bookmarks/0/favorite"} {
		if rec := e.do("PUT", p, nil, alice.mods()...); rec.Code != http.StatusNotFound {
			t.Errorf("PUT %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	bob := e.login(e.bob)
	if rec := e.do("PUT", fmt.Sprintf("/api/v1/bookmarks/%d/favorite", f.hidden), nil, bob.mods()...); rec.Code != http.StatusNotFound {
		t.Fatalf("bob favorited alice's private bookmark: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/bookmarks/1/favorite", nil, alice.mods()...); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET favorite: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFavoriteTokenScopes(t *testing.T) {
	e := newTEnv(t)
	f := favSetup(t, e)
	path := fmt.Sprintf("/api/v1/bookmarks/%d/favorite", f.visible)
	read := e.token(e.alice, identity.ScopeBookmarksRead)
	write := e.token(e.alice, identity.ScopeBookmarksWrite)
	both := e.token(e.alice, identity.ScopeBookmarksRead, identity.ScopeBookmarksWrite)
	for _, m := range []string{"PUT", "DELETE"} {
		if rec := e.do(m, path, nil, withBearer(read)); rec.Code != http.StatusForbidden || errCode(t, rec) != "insufficient_scope" {
			t.Fatalf("%s with bookmarks:read: %d %s", m, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do("PUT", path, nil, withBearer(write)); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT with bookmarks:write: %d %s", rec.Code, rec.Body.String())
	}
	// Reading favorite state still needs bookmarks:read.
	if rec := e.do("GET", "/api/v1/bookmarks?favorite=true", nil, withBearer(write)); rec.Code != http.StatusForbidden {
		t.Fatalf("list with only bookmarks:write: %d %s", rec.Code, rec.Body.String())
	}
	if ids := listIDs(t, e, "/api/v1/bookmarks?favorite=true", withBearer(read)); len(ids) != 1 || ids[0] != f.visible {
		t.Fatalf("favorite=true via read token = %v", ids)
	}
	if rec := e.do("DELETE", path, nil, withBearer(both)); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE with bookmarks:write: %d %s", rec.Code, rec.Body.String())
	}
}

// A view-only share recipient can favorite a visible bookmark through the share scope
// without changing the owner's data or favorite, and without gaining edit access.
func TestFavoriteOnReadOnlyShareIsPrivate(t *testing.T) {
	e := newTEnv(t)
	f := favSetup(t, e)
	alice, bob := e.login(e.alice), e.login(e.bob)
	shareQ := fmt.Sprintf("?share=%d", f.share)
	before := e.do("GET", "/api/v1/bookmarks", nil, alice.mods()...).Body.String()

	if rec := e.do("PUT", fmt.Sprintf("/api/v1/bookmarks/%d/favorite%s", f.visible, shareQ), nil, bob.mods()...); rec.Code != http.StatusNoContent {
		t.Fatalf("viewer PUT favorite: %d %s", rec.Code, rec.Body.String())
	}
	got := decode(t, e.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d%s", f.visible, shareQ), nil, bob.mods()...))
	if !favoriteOf(t, got) {
		t.Fatal("viewer's favorite not reported in the share scope")
	}
	if ids := listIDs(t, e, "/api/v1/bookmarks"+shareQ+"&favorite=true", bob.mods()...); len(ids) != 1 || ids[0] != f.visible {
		t.Fatalf("viewer favorite=true = %v", ids)
	}
	if after := e.do("GET", "/api/v1/bookmarks", nil, alice.mods()...).Body.String(); after != before {
		t.Fatalf("viewer's favorite changed owner's view:\n%s\n%s", before, after)
	}
	// The viewer still cannot edit, and a hidden bookmark stays hidden.
	if rec := e.do("PATCH", fmt.Sprintf("/api/v1/bookmarks/%d%s", f.visible, shareQ), map[string]string{"title": "x"}, bob.mods()...); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer PATCH: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("PUT", fmt.Sprintf("/api/v1/bookmarks/%d/favorite%s", f.hidden, shareQ), nil, bob.mods()...); rec.Code != http.StatusNotFound {
		t.Fatalf("viewer favorited a hidden bookmark: %d %s", rec.Code, rec.Body.String())
	}
	// A view-only share with a bookmarks:write token works the same way.
	tok := e.token(e.bob, identity.ScopeBookmarksWrite)
	if rec := e.do("DELETE", fmt.Sprintf("/api/v1/bookmarks/%d/favorite%s", f.visible, shareQ), nil, withBearer(tok)); rec.Code != http.StatusNoContent {
		t.Fatalf("viewer token DELETE favorite: %d %s", rec.Code, rec.Body.String())
	}
	// A carol who is not the recipient gets not found for the share.
	carol, err := e.ids.CreateUser(context.Background(), "carol", pass, false)
	if err != nil {
		t.Fatal(err)
	}
	cs := e.login(carol)
	if rec := e.do("PUT", fmt.Sprintf("/api/v1/bookmarks/%d/favorite%s", f.visible, shareQ), nil, cs.mods()...); rec.Code != http.StatusNotFound {
		t.Fatalf("non-recipient favorite through share: %d %s", rec.Code, rec.Body.String())
	}
	// After revoke the share scope is gone.
	if err := e.bm.RevokeShare(context.Background(), e.alice.ID, f.shared, e.bob.ID); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("PUT", fmt.Sprintf("/api/v1/bookmarks/%d/favorite%s", f.visible, shareQ), nil, bob.mods()...); rec.Code != http.StatusNotFound {
		t.Fatalf("favorite after revoke: %d %s", rec.Code, rec.Body.String())
	}
}
