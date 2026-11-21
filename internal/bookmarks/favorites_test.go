package bookmarks_test

import (
	"errors"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func favPtr(v bool) *bool { return &v }

func (e *env) favIDs(sc bookmarks.Scope, fav bool) []int64 {
	e.t.Helper()
	list, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1, Favorite: favPtr(fav)})
	if err != nil {
		e.t.Fatalf("ListBookmarks(favorite=%v): %v", fav, err)
	}
	ids := []int64{}
	for _, b := range list {
		if b.Favorite != fav {
			e.t.Fatalf("favorite=%v filter returned %d with Favorite=%v", fav, b.ID, b.Favorite)
		}
		ids = append(ids, b.ID)
	}
	return ids
}

func sameIDs(t *testing.T, what string, got []int64, want ...int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

// A favorite is per user and per bookmark record: it follows the record across every
// placement and folder move, is idempotent, and survives a database reopen.
func TestFavoriteIsPerUserBookmarkLevelAndFollowsPlacements(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	sc := e.apiPrivate(alice)
	other := e.otherRoot(sc)
	f1 := e.folder(sc, other, "One")
	f2 := e.folder(sc, other, "Two")
	b := e.bm(sc, "https://fav.test/", "Fav", f1, f2)
	plain := e.bm(sc, "https://plain.test/", "Plain", f1)
	if b.Favorite {
		t.Fatal("new bookmark is already a favorite")
	}
	noErr(t, e.svc.SetFavorite(ctx, sc, b.ID, true))
	noErr(t, e.svc.SetFavorite(ctx, sc, b.ID, true)) // idempotent
	got, err := e.svc.GetBookmark(ctx, sc, b.ID)
	noErr(t, err)
	if !got.Favorite {
		t.Fatal("GetBookmark does not report the favorite")
	}
	sameIDs(t, "favorites", e.favIDs(sc, true), b.ID)
	sameIDs(t, "non-favorites", e.favIDs(sc, false), plain.ID)
	// Both placements are the same favorite; filtering by either folder finds it.
	for _, f := range []int64{f1, f2} {
		list, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1, FolderID: f, Favorite: favPtr(true)})
		noErr(t, err)
		if len(list) != 1 || list[0].ID != b.ID {
			t.Fatalf("favorites in folder %d = %+v", f, list)
		}
	}
	// Moving the bookmark to a new folder keeps its favorite.
	f3 := e.folder(sc, other, "Three")
	moved, err := e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{f3}})
	noErr(t, err)
	if !moved.Favorite {
		t.Fatal("favorite lost after moving the bookmark")
	}
	e.reopen()
	sc = e.apiPrivate(alice)
	sameIDs(t, "favorites after reopen", e.favIDs(sc, true), b.ID)
	noErr(t, e.svc.SetFavorite(ctx, sc, b.ID, false))
	noErr(t, e.svc.SetFavorite(ctx, sc, b.ID, false)) // idempotent
	sameIDs(t, "favorites after unset", e.favIDs(sc, true))
}

// A view-only share recipient may favorite a visible bookmark. The owner's data,
// the owner's favorites and the grant are unchanged, and the recipient gains no
// write access. Each user's favorite state is private.
func TestShareViewerFavoriteIsPrivateAndGrantsNoWrite(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	asc := e.apiPrivate(alice)
	shared := e.folder(asc, e.otherRoot(asc), "Shared")
	b := e.bm(asc, "https://shared.test/", "Shared", shared)
	sh, err := e.svc.GrantShare(ctx, alice.ID, shared, bob.ID, identity.ModeView)
	noErr(t, err)
	csh, err := e.svc.GrantShare(ctx, alice.ID, shared, carol.ID, identity.ModeView)
	noErr(t, err)
	bsc, err := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, false)
	noErr(t, err)
	csc, err := e.svc.ShareScopeFor(ctx, carol.ID, csh.ID, false)
	noErr(t, err)
	before := e.snapshot(asc)

	noErr(t, e.svc.SetFavorite(ctx, bsc, b.ID, true))
	if got, _ := e.svc.GetBookmark(ctx, bsc, b.ID); !got.Favorite {
		t.Fatal("viewer's favorite not reported in the share scope")
	}
	if got, _ := e.svc.GetBookmark(ctx, asc, b.ID); got.Favorite {
		t.Fatal("viewer's favorite leaked to the owner")
	}
	if got, _ := e.svc.GetBookmark(ctx, csc, b.ID); got.Favorite {
		t.Fatal("viewer's favorite leaked to another recipient")
	}
	if after := e.snapshot(asc); after != before {
		t.Fatalf("favoriting changed owner data:\n%s\nvs\n%s", before, after)
	}
	if _, err := e.svc.CreateFolder(ctx, bsc, bookmarks.RootID, "x"); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Fatalf("viewer gained write access: %v", err)
	}
	if _, err := e.svc.UpdateBookmark(ctx, bsc, b.ID, bookmarks.BookmarkUpdate{Title: strPtr("hijack")}); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Fatalf("viewer edited the bookmark: %v", err)
	}
	if again, _ := e.svc.ShareByID(ctx, sh.ID); again.Mode != identity.ModeView {
		t.Fatalf("grant changed to %s", again.Mode)
	}
	// The owner's own favorite is independent of the viewer's.
	noErr(t, e.svc.SetFavorite(ctx, asc, b.ID, true))
	noErr(t, e.svc.SetFavorite(ctx, bsc, b.ID, false))
	if got, _ := e.svc.GetBookmark(ctx, asc, b.ID); !got.Favorite {
		t.Fatal("viewer's unfavorite cleared the owner's favorite")
	}
	if got, _ := e.svc.GetBookmark(ctx, bsc, b.ID); got.Favorite {
		t.Fatal("viewer still sees a favorite after unsetting it")
	}
}

// Favorite writes revalidate the actor and visibility inside the transaction: other
// users' bookmarks, hidden placements, revoked grants, suspended actors and actorless
// (Floccus) scopes cannot change favorites.
func TestFavoriteWritesRequireLiveActorAndVisibility(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	asc := e.apiPrivate(alice)
	other := e.otherRoot(asc)
	shared := e.folder(asc, other, "Shared")
	hiddenF := e.folder(asc, other, "Hidden")
	visible := e.bm(asc, "https://visible.test/", "V", shared)
	hidden := e.bm(asc, "https://hidden.test/", "H", hiddenF)
	sh, err := e.svc.GrantShare(ctx, alice.ID, shared, bob.ID, identity.ModeView)
	noErr(t, err)
	bsc, err := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, false)
	noErr(t, err)

	// Another user's private bookmark id is not found.
	is(t, e.svc.SetFavorite(ctx, e.apiPrivate(carol), visible.ID, true), bookmarks.ErrNotFound)
	// A bookmark outside the shared subtree is not found for the viewer.
	is(t, e.svc.SetFavorite(ctx, bsc, hidden.ID, true), bookmarks.ErrNotFound)
	is(t, e.svc.SetFavorite(ctx, bsc, 999999, true), bookmarks.ErrNotFound)
	// A forged private scope for another owner is refused.
	forged := bookmarks.Scope{OwnerID: alice.ID, Actor: bob.ID}
	is(t, e.svc.SetFavorite(ctx, forged, visible.ID, true), identity.ErrUnauthorized)
	// Actorless scopes (Floccus credentials, internal callers) never write favorites.
	floccus := e.private(alice, identity.ModeEdit)
	is(t, e.svc.SetFavorite(ctx, floccus, visible.ID, true), bookmarks.ErrForbidden)
	if got, _ := e.svc.GetBookmark(ctx, floccus, visible.ID); got.Favorite {
		t.Fatal("actorless scope reports a favorite")
	}
	// A favorite on a trashed bookmark is not found.
	trashed := e.bm(asc, "https://trashed.test/", "T", other)
	noErr(t, e.svc.DeleteBookmark(ctx, asc, trashed.ID))
	is(t, e.svc.SetFavorite(ctx, asc, trashed.ID, true), bookmarks.ErrNotFound)
	// A stale share scope cannot write after revoke.
	noErr(t, e.svc.SetFavorite(ctx, bsc, visible.ID, true))
	noErr(t, e.svc.RevokeShare(ctx, alice.ID, shared, bob.ID))
	is(t, e.svc.SetFavorite(ctx, bsc, visible.ID, false), bookmarks.ErrNotFound)
	// A suspended actor cannot write.
	noErr(t, e.ids.SetActive(ctx, "alice", false))
	is(t, e.svc.SetFavorite(ctx, asc, visible.ID, true), identity.ErrUnauthorized)
}

// The favorite filter never reveals a bookmark outside the active scope, even when the
// actor's favorite row still exists for it.
func TestFavoriteFilterStaysInsideScope(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	asc := e.apiPrivate(alice)
	other := e.otherRoot(asc)
	shared := e.folder(asc, other, "Shared")
	inner := e.folder(asc, shared, "Inner")
	b := e.bm(asc, "https://both.test/", "Both", inner)
	sh, err := e.svc.GrantShare(ctx, alice.ID, shared, bob.ID, identity.ModeView)
	noErr(t, err)
	bsc, err := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, false)
	noErr(t, err)
	noErr(t, e.svc.SetFavorite(ctx, bsc, b.ID, true))
	sameIDs(t, "viewer favorites", e.favIDs(bsc, true), b.ID)
	// Bob's own collection never shows Alice's bookmark, favorite or not.
	bobPrivate := e.apiPrivate(bob)
	sameIDs(t, "bob private favorites", e.favIDs(bobPrivate, true))
	sameIDs(t, "bob private non-favorites", e.favIDs(bobPrivate, false))
	// Moving the bookmark out of the shared subtree hides it from the viewer's filter,
	// while the favorite row survives for when it becomes visible again.
	_, err = e.svc.UpdateBookmark(ctx, asc, b.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{other}})
	noErr(t, err)
	sameIDs(t, "viewer favorites after move-out", e.favIDs(bsc, true))
	sameIDs(t, "viewer non-favorites after move-out", e.favIDs(bsc, false))
	if got, _ := e.svc.GetBookmark(ctx, asc, b.ID); got.Favorite {
		t.Fatal("viewer's favorite leaked to the owner after move-out")
	}
	_, err = e.svc.UpdateBookmark(ctx, asc, b.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{inner}})
	noErr(t, err)
	sameIDs(t, "viewer favorites after move-back", e.favIDs(bsc, true), b.ID)
	// Actorless scopes treat every bookmark as a non-favorite.
	floccus := e.private(alice, identity.ModeView)
	sameIDs(t, "actorless favorites", e.favIDs(floccus, true))
	sameIDs(t, "actorless non-favorites", e.favIDs(floccus, false), b.ID)
}

// Purging a trashed bookmark removes its favorite rows (no orphan rows, no FK failure).
func TestPurgedBookmarkDropsFavorites(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	sc := e.apiPrivate(alice)
	b := e.bm(sc, "https://purge.test/", "P", e.otherRoot(sc))
	noErr(t, e.svc.SetFavorite(ctx, sc, b.ID, true))
	noErr(t, e.svc.DeleteBookmark(ctx, sc, b.ID))
	noErr(t, e.svc.EmptyTrash(ctx, sc))
	var n int
	if err := e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM bookmark_favorites`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("favorite rows after purge = %d (%v)", n, err)
	}
}

func strPtr(s string) *string { return &s }
