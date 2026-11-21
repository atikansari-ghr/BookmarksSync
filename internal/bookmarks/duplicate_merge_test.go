package bookmarks_test

import (
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// Approved policy (duplicate-merge-design.md): when RemovePlacement removes the LAST live
// placement of a record and exactly one same-URL record is visible to the same caller scope,
// the source's title and tags are copied to that survivor before the source is trashed.

const dupURL = "https://dup.example/page"

type dupWorld struct {
	e                 *env
	alice, bob        identity.User
	owner, recipient  bookmarks.Scope
	priv, shared, sub int64
	survivor          bookmarks.Bookmark // created by bob inside Alice's share: separate record
}

func newDupWorld(t *testing.T) *dupWorld {
	t.Helper()
	e := newEnv(t)
	w := &dupWorld{e: e, alice: e.user("alice"), bob: e.user("bob")}
	w.owner = e.private(w.alice, identity.ModeEdit)
	bar, other := e.roots(w.owner)
	w.shared = e.folder(w.owner, bar, "Shared")
	w.priv = e.folder(w.owner, other, "Private")
	return w
}

// withSurvivor makes bob (who cannot see Alice's private folder) create the shared-scope record.
func (w *dupWorld) withSurvivor(t *testing.T, title string, tags []string) {
	t.Helper()
	w.recipient, _, _ = w.e.shared(w.alice, w.shared, w.bob, identity.ModeEdit, identity.ModeEdit)
	var err error
	w.survivor, err = w.e.svc.CreateBookmark(ctx, w.recipient, bookmarks.BookmarkInput{URL: dupURL, Title: title, Folders: []int64{bookmarks.RootID}, Tags: &tags})
	noErr(t, err)
}

func (w *dupWorld) source(t *testing.T, title string, tags []string, folders ...int64) bookmarks.Bookmark {
	t.Helper()
	b, err := w.e.svc.CreateBookmark(ctx, w.owner, bookmarks.BookmarkInput{URL: dupURL, Title: title, Folders: folders, Tags: &tags})
	noErr(t, err)
	return b
}

func TestRemovingLastPlacementMergesMetadataIntoSingleVisibleSurvivor(t *testing.T) {
	w := newDupWorld(t)
	// the private source is created first, so bob's POST (blind to it) makes a second record
	src := w.source(t, "Newest title", []string{"b", "a"}, w.priv)
	w.withSurvivor(t, "Old title", []string{"old"})
	if src.ID == w.survivor.ID {
		t.Fatal("setup: expected two separate records")
	}

	noErr(t, w.e.svc.RemovePlacement(ctx, w.owner, w.priv, src.ID))

	got, err := w.e.svc.GetBookmark(ctx, w.owner, w.survivor.ID)
	noErr(t, err)
	if got.Title != "Newest title" || strings.Join(got.Tags, ",") != "a,b" {
		t.Errorf("survivor metadata = %q %v, want the source's title and tags", got.Title, got.Tags)
	}
	if got.ID != w.survivor.ID || len(got.Folders) != 1 || got.Folders[0] != w.shared {
		t.Errorf("survivor id/placements changed: %+v", got)
	}
	_, err = w.e.svc.GetBookmark(ctx, w.owner, src.ID)
	is(t, err, bookmarks.ErrNotFound)
	list, err := w.e.svc.ListBookmarks(ctx, w.owner, bookmarks.ListQuery{Page: -1, URL: dupURL})
	noErr(t, err)
	if len(list) != 1 || list[0].ID != w.survivor.ID {
		t.Errorf("owner should see only the survivor now: %+v", list)
	}
}

func TestRecipientCannotMergeMetadataFromInvisiblePrivateRecord(t *testing.T) {
	w := newDupWorld(t)
	private := w.source(t, "SECRET private title", []string{"secret-tag"}, w.priv)
	rec, _, _ := w.e.shared(w.alice, w.shared, w.bob, identity.ModeEdit, identity.ModeEdit)
	sub := w.e.folder(rec, bookmarks.RootID, "Sub")
	mine, err := w.e.svc.CreateBookmark(ctx, rec, bookmarks.BookmarkInput{URL: dupURL, Title: "Bob title", Folders: []int64{sub}})
	noErr(t, err)
	if mine.ID == private.ID {
		t.Fatal("setup: bob reused the private record")
	}

	noErr(t, w.e.svc.RemovePlacement(ctx, rec, sub, mine.ID))

	got, err := w.e.svc.GetBookmark(ctx, w.owner, private.ID)
	noErr(t, err)
	if got.Title != "SECRET private title" || strings.Join(got.Tags, ",") != "secret-tag" || len(got.Folders) != 1 || got.Folders[0] != w.priv {
		t.Errorf("hidden private record was touched: %+v", got)
	}
	_, err = w.e.svc.GetBookmark(ctx, rec, mine.ID)
	is(t, err, bookmarks.ErrNotFound)
}

func TestNoMergeWhileSourceStillHasAPlacement(t *testing.T) {
	w := newDupWorld(t)
	priv2 := w.e.folder(w.owner, mustOther(t, w), "Private2")
	src := w.source(t, "Newest title", []string{"n"}, w.priv)
	w.withSurvivor(t, "Old title", []string{"old"})
	_, err := w.e.svc.UpdateBookmark(ctx, w.owner, src.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{w.priv, priv2}})
	noErr(t, err)

	noErr(t, w.e.svc.RemovePlacement(ctx, w.owner, w.priv, src.ID))

	got, err := w.e.svc.GetBookmark(ctx, w.owner, w.survivor.ID)
	noErr(t, err)
	if got.Title != "Old title" || strings.Join(got.Tags, ",") != "old" {
		t.Errorf("survivor changed although the source keeps a placement: %+v", got)
	}
	kept, err := w.e.svc.GetBookmark(ctx, w.owner, src.ID)
	noErr(t, err)
	if len(kept.Folders) != 1 || kept.Folders[0] != priv2 {
		t.Errorf("source placements = %v, want only Private2", kept.Folders)
	}
}

func TestNoMergeWhenSourceHasPlacementOutsideTheCallerScope(t *testing.T) {
	w := newDupWorld(t)
	rec, _, _ := w.e.shared(w.alice, w.shared, w.bob, identity.ModeEdit, identity.ModeEdit)
	sub := w.e.folder(rec, bookmarks.RootID, "Sub")
	other := w.e.folder(rec, bookmarks.RootID, "Other")
	priv2 := w.e.folder(w.owner, mustOther(t, w), "Private2")
	// Z starts private, S is bob's; both then become visible to bob (Z via a placement in the share)
	z := w.source(t, "Z title", []string{"z"}, w.priv)
	s, err := w.e.svc.CreateBookmark(ctx, rec, bookmarks.BookmarkInput{URL: dupURL, Title: "S title", Folders: []int64{sub}})
	noErr(t, err)
	if s.ID == z.ID {
		t.Fatal("setup: bob reused the private record")
	}
	_, err = w.e.svc.UpdateBookmark(ctx, w.owner, z.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{w.priv, other}})
	noErr(t, err)
	// S additionally lives in a private folder bob cannot see
	_, err = w.e.svc.UpdateBookmark(ctx, w.owner, s.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{sub, priv2}})
	noErr(t, err)

	noErr(t, w.e.svc.RemovePlacement(ctx, rec, sub, s.ID))

	got, err := w.e.svc.GetBookmark(ctx, w.owner, z.ID)
	noErr(t, err)
	if got.Title != "Z title" || strings.Join(got.Tags, ",") != "z" {
		t.Errorf("survivor merged although the source still has a hidden placement: %+v", got)
	}
	kept, err := w.e.svc.GetBookmark(ctx, w.owner, s.ID)
	noErr(t, err)
	if len(kept.Folders) != 1 || kept.Folders[0] != priv2 {
		t.Errorf("source placements = %v, want only Private2", kept.Folders)
	}
}

func TestNoMergeWhenSeveralVisibleSurvivorsExist(t *testing.T) {
	w := newDupWorld(t)
	src := w.source(t, "Newest title", []string{"n"}, w.priv)
	carol := w.e.user("carol")
	bar, _ := w.e.roots(w.owner)
	shared2 := w.e.folder(w.owner, bar, "Shared2")
	rb, _, _ := w.e.shared(w.alice, w.shared, w.bob, identity.ModeEdit, identity.ModeEdit)
	rc, _, _ := w.e.shared(w.alice, shared2, carol, identity.ModeEdit, identity.ModeEdit)
	y1, err := w.e.svc.CreateBookmark(ctx, rb, bookmarks.BookmarkInput{URL: dupURL, Title: "Y1", Folders: []int64{bookmarks.RootID}})
	noErr(t, err)
	y2, err := w.e.svc.CreateBookmark(ctx, rc, bookmarks.BookmarkInput{URL: dupURL, Title: "Y2", Folders: []int64{bookmarks.RootID}})
	noErr(t, err)
	if y1.ID == y2.ID || y1.ID == src.ID || y2.ID == src.ID {
		t.Fatal("setup: expected three separate records")
	}

	noErr(t, w.e.svc.RemovePlacement(ctx, w.owner, w.priv, src.ID))

	for _, y := range []bookmarks.Bookmark{y1, y2} {
		got, err := w.e.svc.GetBookmark(ctx, w.owner, y.ID)
		noErr(t, err)
		if got.Title != y.Title || len(got.Tags) != 0 {
			t.Errorf("%s changed although two survivors were visible: %+v", y.Title, got)
		}
	}
	_, err = w.e.svc.GetBookmark(ctx, w.owner, src.ID)
	is(t, err, bookmarks.ErrNotFound)
}

func TestNoSurvivorKeepsExistingBehaviour(t *testing.T) {
	w := newDupWorld(t)
	src := w.source(t, "Only copy", []string{"t"}, w.priv)
	noErr(t, w.e.svc.RemovePlacement(ctx, w.owner, w.priv, src.ID))
	_, err := w.e.svc.GetBookmark(ctx, w.owner, src.ID)
	is(t, err, bookmarks.ErrNotFound)
}

// Favorites are private per user and follow a bookmark across placements, so the merge must
// carry each source favorite to the survivor for the same user only, and keep the source row
// so a Trash restore of the source still shows it as a favorite. API scopes are used because
// Floccus scopes are actorless and cannot hold favorites.
func TestMergeTransfersSourceFavoritesPerUserAndKeepsSourceRow(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	asc := e.apiPrivate(alice)
	other := e.otherRoot(asc)
	priv := e.folder(asc, other, "Private")
	shared := e.folder(asc, other, "Shared")
	src := e.bm(asc, dupURL, "Source", priv)
	sh, err := e.svc.GrantShare(ctx, alice.ID, shared, bob.ID, identity.ModeEdit)
	noErr(t, err)
	bsc, err := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, true)
	noErr(t, err)
	// bob cannot see the private source, so his POST makes a separate same-URL record
	survivor := e.bm(bsc, dupURL, "Survivor", bookmarks.RootID)
	if survivor.ID == src.ID {
		t.Fatal("setup: expected two separate records")
	}
	noErr(t, e.svc.SetFavorite(ctx, asc, src.ID, true))

	noErr(t, e.svc.RemovePlacement(ctx, asc, priv, src.ID))

	got, err := e.svc.GetBookmark(ctx, asc, survivor.ID)
	noErr(t, err)
	if !got.Favorite {
		t.Error("alice's favorite on the merged source did not follow to the survivor")
	}
	if got, _ := e.svc.GetBookmark(ctx, bsc, survivor.ID); got.Favorite {
		t.Error("alice's favorite became bob's favorite on the survivor")
	}
	var n int
	noErr(t, e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM bookmark_favorites WHERE user_id = ? AND bookmark_id = ?`, alice.ID, src.ID).Scan(&n))
	if n != 1 {
		t.Errorf("source favorite rows for alice = %d, want 1 kept for Trash/restore", n)
	}
	noErr(t, e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM bookmark_favorites WHERE user_id = ?`, bob.ID).Scan(&n))
	if n != 0 {
		t.Errorf("bob has %d favorite rows, want 0", n)
	}
	if _, err := e.svc.RestoreTrash(ctx, asc, "bookmark", src.ID); err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}
	restored, err := e.svc.GetBookmark(ctx, asc, src.ID)
	noErr(t, err)
	if !restored.Favorite {
		t.Error("restored source lost alice's favorite")
	}
}

func mustOther(t *testing.T, w *dupWorld) int64 {
	t.Helper()
	_, other := w.e.roots(w.owner)
	return other
}
