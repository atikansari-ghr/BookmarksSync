package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

const apiDupURL = "https://dup.example/page"

type dupFixture struct {
	private, shared int64 // two records with the same exact URL
	sharedFolder    int64
	share           int64 // bob's edit share of sharedFolder
}

// dupSetup gives alice a private record (invisible to share recipients) and, through an
// edit share, a second record with the same URL created by bob inside the shared folder.
// A third record with a distinct URL sits in two folders: placements are not duplicates.
func dupSetup(t *testing.T, e *tenv) dupFixture {
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
	priv, err := e.bm.CreateFolder(ctx, sc, other, "Private")
	if err != nil {
		t.Fatal(err)
	}
	priv2, err := e.bm.CreateFolder(ctx, sc, other, "Private 2")
	if err != nil {
		t.Fatal(err)
	}
	shared, err := e.bm.CreateFolder(ctx, sc, other, "Shared")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := e.bm.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: apiDupURL, Title: "SECRET private title", Folders: []int64{priv.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.bm.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://twice.example/", Title: "Twice", Folders: []int64{priv.ID, priv2.ID}}); err != nil {
		t.Fatal(err)
	}
	sh, err := e.bm.GrantShare(ctx, e.alice.ID, shared.ID, e.bob.ID, identity.ModeEdit)
	if err != nil {
		t.Fatal(err)
	}
	bsc, err := e.bm.ShareScopeFor(ctx, e.bob.ID, sh.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := e.bm.CreateBookmark(ctx, bsc, bookmarks.BookmarkInput{URL: apiDupURL, Title: "shared copy", Folders: []int64{bookmarks.RootID}})
	if err != nil {
		t.Fatal(err)
	}
	if pb.ID == sb.ID {
		t.Fatal("setup: expected two separate records")
	}
	return dupFixture{private: pb.ID, shared: sb.ID, sharedFolder: shared.ID, share: sh.ID}
}

func TestDuplicatesRouteReportsExactURLGroups(t *testing.T) {
	e := newTEnv(t)
	f := dupSetup(t, e)
	alice := e.login(e.alice)

	rec := e.do("GET", "/api/v1/duplicates", nil, alice.mods()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /duplicates: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	// Placements of one record do not count; the two same-URL records give one excess.
	if body["count"].(float64) != 1 || body["hasMore"] != false || body["page"].(float64) != 0 {
		t.Fatalf("body = %v", body)
	}
	groups := body["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v", groups)
	}
	g := groups[0].(map[string]any)
	if g["url"] != apiDupURL {
		t.Fatalf("group url = %v", g["url"])
	}
	recs := g["bookmarks"].([]any)
	if len(recs) != 2 {
		t.Fatalf("group bookmarks = %v", recs)
	}
	first, second := recs[0].(map[string]any), recs[1].(map[string]any)
	if int64(first["id"].(float64)) != f.private || int64(second["id"].(float64)) != f.shared {
		t.Fatalf("group records = %v, want %d then %d", recs, f.private, f.shared)
	}
	for _, k := range []string{"id", "url", "title", "tags", "folders", "favorite"} {
		if _, ok := first[k]; !ok {
			t.Errorf("group bookmark lacks %q: %v", k, first)
		}
	}
	// Reviewing deletes nothing.
	if ids := listIDs(t, e, "/api/v1/bookmarks", alice.mods()...); len(ids) != 3 {
		t.Fatalf("bookmarks after review = %v, want 3", ids)
	}
	if items, _ := decode(t, e.do("GET", "/api/v1/trash", nil, alice.mods()...))["items"].([]any); len(items) != 0 {
		t.Fatalf("trash after review = %v", items)
	}
}

func TestDuplicatesRoutePaginationValidation(t *testing.T) {
	e := newTEnv(t)
	dupSetup(t, e)
	alice := e.login(e.alice)
	for _, q := range []string{"page=-1", "limit=0", "limit=-2", "page=abc", "limit=x", "page=1000001"} {
		if rec := e.do("GET", "/api/v1/duplicates?"+q, nil, alice.mods()...); rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_request" {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	rec := e.do("GET", "/api/v1/duplicates?page=1&limit=1", nil, alice.mods()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if len(body["groups"].([]any)) != 0 || body["count"].(float64) != 1 || body["hasMore"] != false || body["limit"].(float64) != 1 {
		t.Fatalf("page 1 body = %v", body)
	}
	body = decode(t, e.do("GET", "/api/v1/duplicates?limit=100000", nil, alice.mods()...))
	if body["limit"].(float64) != 1000 {
		t.Fatalf("limit not clamped: %v", body["limit"])
	}
}

func TestDuplicatesRouteIsolationAndTokenScopes(t *testing.T) {
	e := newTEnv(t)
	f := dupSetup(t, e)
	bob := e.login(e.bob)
	shareQ := fmt.Sprintf("?share=%d", f.share)

	// Bob only sees his shared record: no duplicate, and nothing private leaks.
	rec := e.do("GET", "/api/v1/duplicates"+shareQ, nil, bob.mods()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("share duplicates: %d %s", rec.Code, rec.Body.String())
	}
	if body := decode(t, rec); body["count"].(float64) != 0 || len(body["groups"].([]any)) != 0 || strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("recipient saw hidden duplicates: %s", rec.Body.String())
	}
	// Bob's own private collection has none either, and carol cannot use bob's share.
	if body := decode(t, e.do("GET", "/api/v1/duplicates", nil, bob.mods()...)); body["count"].(float64) != 0 {
		t.Fatalf("bob private duplicates = %v", body)
	}
	carol, err := e.ids.CreateUser(context.Background(), "carol", pass, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", "/api/v1/duplicates"+shareQ, nil, e.login(carol).mods()...); rec.Code != http.StatusNotFound {
		t.Fatalf("non-recipient: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/duplicates", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", rec.Code, rec.Body.String())
	}
	// Token scopes: read works, write-only does not.
	read := e.token(e.alice, identity.ScopeBookmarksRead)
	write := e.token(e.alice, identity.ScopeBookmarksWrite)
	if rec := e.do("GET", "/api/v1/duplicates", nil, withBearer(read)); rec.Code != http.StatusOK {
		t.Fatalf("read token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/duplicates", nil, withBearer(write)); rec.Code != http.StatusForbidden || errCode(t, rec) != "insufficient_scope" {
		t.Fatalf("write-only token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/api/v1/duplicates", nil, e.login(e.alice).mods()...); rec.Code == http.StatusOK || rec.Code == http.StatusCreated || rec.Code == http.StatusNoContent {
		t.Fatalf("POST /duplicates must not succeed: %d", rec.Code)
	}
}

func TestDuplicateRemovalUsesBookmarkDeleteAndTrash(t *testing.T) {
	e := newTEnv(t)
	f := dupSetup(t, e)
	alice, bob := e.login(e.alice), e.login(e.bob)
	del := func(id int64, q string, m ...mod) int {
		return e.do("DELETE", fmt.Sprintf("/api/v1/bookmarks/%d%s", id, q), nil, m...).Code
	}

	// A view-only recipient and a read-scoped token cannot remove either duplicate.
	carol, err := e.ids.CreateUser(context.Background(), "carol", pass, false)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := e.bm.GrantShare(context.Background(), e.alice.ID, f.sharedFolder, carol.ID, identity.ModeView)
	if err != nil {
		t.Fatal(err)
	}
	if got := del(f.shared, fmt.Sprintf("?share=%d", vs.ID), e.login(carol).mods()...); got != http.StatusForbidden {
		t.Fatalf("view-only share delete: %d", got)
	}
	if got := del(f.private, "", withBearer(e.token(e.alice, identity.ScopeBookmarksRead))); got != http.StatusForbidden {
		t.Fatalf("read token delete: %d", got)
	}
	if got := del(f.private, "", withCookie(alice.cookie)); got != http.StatusForbidden { // no CSRF
		t.Fatalf("delete without CSRF: %d", got)
	}
	// Bob cannot reach alice's private record at all.
	if got := del(f.private, fmt.Sprintf("?share=%d", f.share), bob.mods()...); got != http.StatusNotFound {
		t.Fatalf("recipient delete of hidden record: %d", got)
	}
	if body := decode(t, e.do("GET", "/api/v1/duplicates", nil, alice.mods()...)); body["count"].(float64) != 1 {
		t.Fatalf("data changed by refused removals: %v", body)
	}

	// The owner's chosen removal goes to the Trash; the other record and its title stay.
	if got := del(f.private, "", alice.mods()...); got != http.StatusNoContent {
		t.Fatalf("owner delete: %d", got)
	}
	body := decode(t, e.do("GET", "/api/v1/duplicates", nil, alice.mods()...))
	if body["count"].(float64) != 0 || len(body["groups"].([]any)) != 0 {
		t.Fatalf("duplicates after removal = %v", body)
	}
	trash, _ := decode(t, e.do("GET", "/api/v1/trash", nil, alice.mods()...))["items"].([]any)
	if len(trash) != 1 || int64(trash[0].(map[string]any)["id"].(float64)) != f.private || trash[0].(map[string]any)["type"] != "bookmark" {
		t.Fatalf("trash = %v", trash)
	}
	kept := decode(t, e.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d", f.shared), nil, alice.mods()...))
	if kept["title"] != "shared copy" {
		t.Fatalf("surviving record changed: %v", kept)
	}
	// Restoring it makes the duplicate reappear: removal is reversible and never automatic.
	if rec := e.do("POST", fmt.Sprintf("/api/v1/trash/bookmark/%d/restore", f.private), nil, alice.mods()...); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}
	if body := decode(t, e.do("GET", "/api/v1/duplicates", nil, alice.mods()...)); body["count"].(float64) != 1 {
		t.Fatalf("duplicates after restore = %v", body)
	}
}
