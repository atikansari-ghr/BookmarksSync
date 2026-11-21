package api_test

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

type bulkFixture struct {
	src, dst, other int64
	recs            []int64 // in src, creation order
	sc              bookmarks.Scope
}

func bulkSetup(t *testing.T, e *tenv, n int) bulkFixture {
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
	mk := func(title string) int64 {
		f, err := e.bm.CreateFolder(ctx, sc, kids[1].ID, title)
		if err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	f := bulkFixture{src: mk("Src"), dst: mk("Dst"), other: mk("Other"), sc: sc}
	for i := 0; i < n; i++ {
		b, err := e.bm.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: fmt.Sprintf("https://b%d.example/", i), Title: fmt.Sprintf("b%d", i), Folders: []int64{f.src}})
		if err != nil {
			t.Fatal(err)
		}
		f.recs = append(f.recs, b.ID)
	}
	return f
}

func folderBookmarkIDs(t *testing.T, e *tenv, sc bookmarks.Scope, folder int64) []int64 {
	t.Helper()
	nodes, err := e.bm.Children(context.Background(), sc, folder, 1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, n := range nodes {
		if n.Type == "bookmark" {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func bulkBody(src, dst int64, ids ...int64) map[string]any {
	return map[string]any{"bookmarkIds": ids, "sourceFolderId": src, "folderId": dst}
}

func TestBulkMoveRouteMovesSelectionInSubmittedOrder(t *testing.T) {
	e := newTEnv(t)
	f := bulkSetup(t, e, 3)
	alice := e.login(e.alice)

	rec := e.do("POST", "/api/v1/bookmarks/bulk-move", bulkBody(f.src, f.dst, f.recs[2], f.recs[0]), alice.mods()...)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bulk-move: %d %s", rec.Code, rec.Body.String())
	}
	if got, want := folderBookmarkIDs(t, e, f.sc, f.dst), []int64{f.recs[2], f.recs[0]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("destination = %v, want %v", got, want)
	}
	if got := folderBookmarkIDs(t, e, f.sc, f.src); !reflect.DeepEqual(got, []int64{f.recs[1]}) {
		t.Fatalf("source = %v", got)
	}
}

func TestBulkMoveRouteValidationAndAtomicFailure(t *testing.T) {
	e := newTEnv(t)
	f := bulkSetup(t, e, 2)
	alice := e.login(e.alice)
	many := make([]int64, 101)
	for i := range many {
		many[i] = int64(i + 1)
	}
	bad := []struct {
		name string
		body any
		code int
		err  string
	}{
		{"empty ids", bulkBody(f.src, f.dst), 400, "validation_failed"},
		{"too many ids", bulkBody(f.src, f.dst, many...), 400, "validation_failed"},
		{"duplicate ids", bulkBody(f.src, f.dst, f.recs[0], f.recs[0]), 400, "validation_failed"},
		{"zero id", bulkBody(f.src, f.dst, 0), 400, "validation_failed"},
		{"same folder", bulkBody(f.src, f.src, f.recs[0]), 400, "validation_failed"},
		{"root destination", bulkBody(f.src, bookmarks.RootID, f.recs[0]), 400, "validation_failed"},
		{"missing source", map[string]any{"bookmarkIds": []int64{f.recs[0]}, "folderId": f.dst}, 400, "invalid_request"},
		{"missing folder id", map[string]any{"bookmarkIds": []int64{f.recs[0]}, "sourceFolderId": f.src}, 400, "invalid_request"},
		{"unknown field", map[string]any{"bookmarkIds": []int64{f.recs[0]}, "sourceFolderId": f.src, "folderId": f.dst, "x": 1}, 400, "invalid_request"},
		{"unknown record", bulkBody(f.src, f.dst, f.recs[0], 999999), 404, "not_found"},
		{"unknown destination", bulkBody(f.src, 999999, f.recs[0]), 404, "not_found"},
	}
	for _, c := range bad {
		rec := e.do("POST", "/api/v1/bookmarks/bulk-move", c.body, alice.mods()...)
		if rec.Code != c.code || errCode(t, rec) != c.err {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if got := folderBookmarkIDs(t, e, f.sc, f.src); len(got) != 2 || len(folderBookmarkIDs(t, e, f.sc, f.dst)) != 0 {
		t.Fatalf("failed requests changed data: src=%v", got)
	}
}

func TestBulkMoveRouteConflictAndShareIsolation(t *testing.T) {
	e := newTEnv(t)
	ctx := context.Background()
	f := bulkSetup(t, e, 2)
	alice, bob := e.login(e.alice), e.login(e.bob)

	// Bob (edit share of Dst) adds a separate record with recs[1]'s exact URL inside Dst.
	sh, err := e.bm.GrantShare(ctx, e.alice.ID, f.dst, e.bob.ID, identity.ModeEdit)
	if err != nil {
		t.Fatal(err)
	}
	bsc, err := e.bm.ShareScopeFor(ctx, e.bob.ID, sh.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.bm.CreateBookmark(ctx, bsc, bookmarks.BookmarkInput{URL: "https://b1.example/", Title: "bob", Folders: []int64{bookmarks.RootID}}); err != nil {
		t.Fatal(err)
	}
	rec := e.do("POST", "/api/v1/bookmarks/bulk-move", bulkBody(f.src, f.dst, f.recs[0], f.recs[1]), alice.mods()...)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "conflict" {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
	if got := folderBookmarkIDs(t, e, f.sc, f.src); len(got) != 2 {
		t.Fatalf("conflict left a partial move: %v", got)
	}

	// Bob cannot use alice's private folders through his share: they look missing.
	rec = e.do("POST", fmt.Sprintf("/api/v1/bookmarks/bulk-move?share=%d", sh.ID), bulkBody(f.src, f.dst, f.recs[0]), bob.mods()...)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("recipient moved from a hidden folder: %d %s", rec.Code, rec.Body.String())
	}
	// A view-only share cannot move at all.
	carol, err := e.ids.CreateUser(ctx, "carol", pass, false)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := e.bm.GrantShare(ctx, e.alice.ID, f.src, carol.ID, identity.ModeView)
	if err != nil {
		t.Fatal(err)
	}
	rec = e.do("POST", fmt.Sprintf("/api/v1/bookmarks/bulk-move?share=%d", vs.ID), bulkBody(f.src, f.other, f.recs[0]), e.login(carol).mods()...)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("view-only share: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBulkMoveRouteAuthAndTokenScopes(t *testing.T) {
	e := newTEnv(t)
	f := bulkSetup(t, e, 2)
	body := bulkBody(f.src, f.dst, f.recs[0])
	if rec := e.do("POST", "/api/v1/bookmarks/bulk-move", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/bookmarks/bulk-move", body, withCookie(e.login(e.alice).cookie)); rec.Code != http.StatusForbidden { // no CSRF
		t.Fatalf("no CSRF: %d", rec.Code)
	}
	read := e.token(e.alice, identity.ScopeBookmarksRead)
	if rec := e.do("POST", "/api/v1/bookmarks/bulk-move", body, withBearer(read)); rec.Code != http.StatusForbidden || errCode(t, rec) != "insufficient_scope" {
		t.Fatalf("read token: %d %s", rec.Code, rec.Body.String())
	}
	if len(folderBookmarkIDs(t, e, f.sc, f.dst)) != 0 {
		t.Fatal("refused requests moved data")
	}
	write := e.token(e.alice, identity.ScopeBookmarksWrite)
	if rec := e.do("POST", "/api/v1/bookmarks/bulk-move", body, withBearer(write)); rec.Code != http.StatusNoContent {
		t.Fatalf("write token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/bookmarks/bulk-move", nil, e.login(e.alice).mods()...); rec.Code == http.StatusOK {
		t.Fatalf("GET must not succeed: %d", rec.Code)
	}
}
