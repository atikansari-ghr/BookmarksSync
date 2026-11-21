package bookmarks_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// Atomic multi-bookmark moves (workspace UI Task 3). MoveBookmarks moves ONLY the placement in
// the source folder to the destination; placements in other folders are kept. Moved records are
// appended after the destination's existing children in submitted order. It is all or nothing.

type bulkWorld struct {
	e        *env
	u        identity.User
	sc       bookmarks.Scope
	src, dst int64
	recs     []bookmarks.Bookmark // placed in src, in creation order
}

func newBulkWorld(t *testing.T, n int) *bulkWorld {
	t.Helper()
	e := newEnv(t)
	u := e.user("alice")
	sc := e.private(u, identity.ModeEdit)
	_, other := e.roots(sc)
	w := &bulkWorld{e: e, u: u, sc: sc, src: e.folder(sc, other, "Src"), dst: e.folder(sc, other, "Dst")}
	for i := 0; i < n; i++ {
		w.recs = append(w.recs, e.bm(sc, fmt.Sprintf("https://s%d.example/", i), fmt.Sprintf("s%d", i), w.src))
	}
	return w
}

// order lists the live bookmark ids directly inside a folder, in stored order.
func (w *bulkWorld) order(t *testing.T, folder int64) []int64 {
	t.Helper()
	nodes, err := w.e.svc.Children(ctx, w.sc, folder, 1)
	noErr(t, err)
	var ids []int64
	for _, n := range nodes {
		if n.Type == "bookmark" {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func ids(bs ...bookmarks.Bookmark) []int64 {
	var out []int64
	for _, b := range bs {
		out = append(out, b.ID)
	}
	return out
}

func TestMoveBookmarksAppendsInSubmittedOrderKeepingOtherPlacements(t *testing.T) {
	w := newBulkWorld(t, 4)
	_, other := w.e.roots(w.sc)
	keep := w.e.folder(w.sc, other, "Keep")
	d1 := w.e.bm(w.sc, "https://d1.example/", "d1", w.dst)
	d2 := w.e.bm(w.sc, "https://d2.example/", "d2", w.dst)
	// recs[2] also lives in Keep: only its Src placement may move.
	_, err := w.e.svc.UpdateBookmark(ctx, w.sc, w.recs[2].ID, bookmarks.BookmarkUpdate{Folders: &[]int64{w.src, keep}})
	noErr(t, err)

	noErr(t, w.e.svc.MoveBookmarks(ctx, w.sc, []int64{w.recs[2].ID, w.recs[0].ID, w.recs[3].ID}, w.src, w.dst))

	if got, want := w.order(t, w.dst), []int64{d1.ID, d2.ID, w.recs[2].ID, w.recs[0].ID, w.recs[3].ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("destination order = %v, want %v", got, want)
	}
	if got, want := w.order(t, w.src), []int64{w.recs[1].ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source = %v, want %v", got, want)
	}
	if got := w.order(t, keep); !reflect.DeepEqual(got, []int64{w.recs[2].ID}) {
		t.Fatalf("other placement lost: %v", got)
	}
	b, err := w.e.svc.GetBookmark(ctx, w.sc, w.recs[2].ID)
	noErr(t, err)
	if len(b.Folders) != 2 {
		t.Fatalf("folders = %v, want destination and Keep", b.Folders)
	}
	// Moving is placement-only: records keep id, title and url.
	if b.Title != "s2" || b.URL != "https://s2.example/" {
		t.Fatalf("record changed: %+v", b)
	}
}

func TestMoveBookmarksSameRecordAlreadyAtDestinationIsNotDuplicated(t *testing.T) {
	w := newBulkWorld(t, 3)
	d1 := w.e.bm(w.sc, "https://d1.example/", "d1", w.dst)
	// recs[0] is already in Dst as well as Src.
	_, err := w.e.svc.UpdateBookmark(ctx, w.sc, w.recs[0].ID, bookmarks.BookmarkUpdate{Folders: &[]int64{w.src, w.dst}})
	noErr(t, err)
	w.e.bm(w.sc, "https://d2.example/", "d2", w.dst)

	noErr(t, w.e.svc.MoveBookmarks(ctx, w.sc, []int64{w.recs[1].ID, w.recs[0].ID}, w.src, w.dst))

	got := w.order(t, w.dst)
	count := 0
	for _, id := range got {
		if id == w.recs[0].ID {
			count++
		}
	}
	if count != 1 || len(got) != 4 || got[0] != d1.ID {
		t.Fatalf("destination = %v, want one placement of record %d and no duplicates", got, w.recs[0].ID)
	}
	// The moved ones keep the submitted relative order, after the other children.
	if got[len(got)-2] != w.recs[1].ID || got[len(got)-1] != w.recs[0].ID {
		t.Fatalf("destination order = %v, want ... %d, %d", got, w.recs[1].ID, w.recs[0].ID)
	}
	if got := w.order(t, w.src); !reflect.DeepEqual(got, []int64{w.recs[2].ID}) {
		t.Fatalf("source = %v", got)
	}
}

func TestMoveBookmarksValidatesInput(t *testing.T) {
	w := newBulkWorld(t, 2)
	root := bookmarks.RootID
	many := make([]int64, 101)
	for i := range many {
		many[i] = int64(i + 1)
	}
	a, b := w.recs[0].ID, w.recs[1].ID
	cases := map[string]struct {
		ids      []int64
		src, dst int64
		want     error
	}{
		"empty":            {nil, w.src, w.dst, bookmarks.ErrInvalid},
		"too many":         {many, w.src, w.dst, bookmarks.ErrInvalid},
		"duplicate id":     {[]int64{a, b, a}, w.src, w.dst, bookmarks.ErrInvalid},
		"zero id":          {[]int64{a, 0}, w.src, w.dst, bookmarks.ErrInvalid},
		"negative id":      {[]int64{-1}, w.src, w.dst, bookmarks.ErrInvalid},
		"destination root": {[]int64{a}, w.src, root, bookmarks.ErrInvalid},
		"source root":      {[]int64{a}, root, w.dst, bookmarks.ErrInvalid},
		"same folder":      {[]int64{a}, w.src, w.src, bookmarks.ErrInvalid},
		"missing dest":     {[]int64{a}, w.src, 999999, bookmarks.ErrNotFound},
		"missing source":   {[]int64{a}, 999999, w.dst, bookmarks.ErrNotFound},
		"missing record":   {[]int64{a, 999999}, w.src, w.dst, bookmarks.ErrNotFound},
	}
	before := w.e.snapshot(w.sc)
	for name, c := range cases {
		is(t, w.e.svc.MoveBookmarks(ctx, w.sc, c.ids, c.src, c.dst), c.want)
		if after := w.e.snapshot(w.sc); after != before {
			t.Fatalf("%s changed data:\n%s\n%s", name, before, after)
		}
	}
	// Exactly 100 unique ids is accepted by validation (missing records then fail atomically).
	hundred := many[:100]
	is(t, w.e.svc.MoveBookmarks(ctx, w.sc, hundred, w.src, w.dst), bookmarks.ErrNotFound)
	if after := w.e.snapshot(w.sc); after != before {
		t.Fatalf("100-id attempt changed data")
	}
}

func TestMoveBookmarksAtomicOnInvalidOrLockedItem(t *testing.T) {
	w := newBulkWorld(t, 3)
	_, other := w.e.roots(w.sc)
	elsewhere := w.e.folder(w.sc, other, "Elsewhere")
	notInSrc := w.e.bm(w.sc, "https://elsewhere.example/", "elsewhere", elsewhere)
	trashed := w.e.bm(w.sc, "https://trashed.example/", "trashed", w.src)
	noErr(t, w.e.svc.DeleteBookmark(ctx, w.sc, trashed.ID))
	bob := w.e.user("bob")
	bobSc := w.e.private(bob, identity.ModeEdit)
	_, bobOther := w.e.roots(bobSc)
	foreign := w.e.bm(bobSc, "https://bob.example/", "bob", bobOther)

	before := w.e.snapshot(w.sc)
	good := []int64{w.recs[0].ID, w.recs[1].ID}
	for name, bad := range map[string]int64{
		"not in source": notInSrc.ID, "trashed": trashed.ID, "other user": foreign.ID,
	} {
		// The invalid item is last: earlier valid items must not stay moved.
		is(t, w.e.svc.MoveBookmarks(ctx, w.sc, append(append([]int64{}, good...), bad), w.src, w.dst), bookmarks.ErrNotFound)
		if after := w.e.snapshot(w.sc); after != before {
			t.Fatalf("%s: partial move:\n%s\n%s", name, before, after)
		}
		if len(w.order(t, w.dst)) != 0 {
			t.Fatalf("%s: destination changed: %v", name, w.order(t, w.dst))
		}
	}

	// Another credential holds the lock: nothing moves; the holder can move.
	one := w.sc
	two := w.e.private(w.u, identity.ModeEdit)
	noErr(t, w.e.svc.AcquireLock(ctx, one))
	is(t, w.e.svc.MoveBookmarks(ctx, two, good, w.src, w.dst), bookmarks.ErrLocked)
	if after := w.e.snapshot(w.sc); after != before {
		t.Fatalf("locked move changed data")
	}
	noErr(t, w.e.svc.MoveBookmarks(ctx, one, good, w.src, w.dst))
	if got := w.order(t, w.dst); !reflect.DeepEqual(got, good) {
		t.Fatalf("holder move = %v, want %v", got, good)
	}
}

func TestMoveBookmarksRejectsReadOnlyScope(t *testing.T) {
	w := newBulkWorld(t, 2)
	ro := w.e.private(w.u, identity.ModeView)
	before := w.e.snapshot(w.sc)
	is(t, w.e.svc.MoveBookmarks(ctx, ro, ids(w.recs...), w.src, w.dst), bookmarks.ErrForbidden)
	if after := w.e.snapshot(w.sc); after != before {
		t.Fatalf("read-only move changed data")
	}
}

// A REAL second record with the same exact URL sits in the destination (created by a share
// recipient who cannot see the source record): the move is refused and nothing changes.
func TestMoveBookmarksRejectsDuplicatePlacement(t *testing.T) {
	w := newBulkWorld(t, 3)
	// Make recs[1]'s URL held by another record inside Dst: Dst is shared; the recipient
	// creates the record from inside the share, where the private source record is hidden.
	bob := w.e.user("bob")
	recip, _, _ := w.e.shared(w.u, w.dst, bob, identity.ModeEdit, identity.ModeEdit)
	other := w.e.bm(recip, w.recs[1].URL, "bob copy", bookmarks.RootID)
	if other.ID == w.recs[1].ID {
		t.Fatal("setup: expected a separate record")
	}
	before := w.e.snapshot(w.sc)
	err := w.e.svc.MoveBookmarks(ctx, w.sc, []int64{w.recs[0].ID, w.recs[1].ID, w.recs[2].ID}, w.src, w.dst)
	is(t, err, bookmarks.ErrConflict)
	if after := w.e.snapshot(w.sc); after != before {
		t.Fatalf("conflicting move left a partial change:\n%s\n%s", before, after)
	}
	// Without the conflicting item the same move succeeds.
	noErr(t, w.e.svc.MoveBookmarks(ctx, w.sc, []int64{w.recs[0].ID, w.recs[2].ID}, w.src, w.dst))
}

func TestMoveBookmarksRevalidatesShareBeforeCommit(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	_, other := e.roots(as)
	root := e.folder(as, other, "Shared")
	a := e.folder(as, root, "A")
	b := e.folder(as, root, "B")
	x := e.bm(as, "https://x.example/", "x", a)
	y := e.bm(as, "https://y.example/", "y", a)
	recip, _, sh := e.shared(alice, root, bob, identity.ModeEdit, identity.ModeEdit)

	// Inside a live share the move works and stays inside the share.
	noErr(t, e.svc.MoveBookmarks(ctx, recip, []int64{x.ID}, a, b))
	// A destination outside the share looks like a missing folder.
	is(t, e.svc.MoveBookmarks(ctx, recip, []int64{y.ID}, a, other), bookmarks.ErrNotFound)

	// Downgrade to view: the stale writable scope is refused and nothing moves.
	before := e.snapshot(as)
	_, err := e.svc.UpdateShare(ctx, alice.ID, sh.ID, identity.ModeView)
	noErr(t, err)
	is(t, e.svc.MoveBookmarks(ctx, recip, []int64{y.ID}, a, b), bookmarks.ErrForbidden)
	if after := e.snapshot(as); after != before {
		t.Fatalf("downgraded share moved data")
	}
	// Revoked: unauthorized, still nothing moves.
	noErr(t, e.svc.RevokeShare(ctx, alice.ID, root, bob.ID))
	if err := e.svc.MoveBookmarks(ctx, recip, []int64{y.ID}, a, b); err == nil {
		t.Fatal("revoked share moved data")
	}
	if after := e.snapshot(as); after != before {
		t.Fatalf("revoked share moved data")
	}
}
