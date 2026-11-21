package bookmarks_test

import (
	"fmt"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// Duplicate review (workspace UI Task 2): two live records of one owner with the exact same
// URL, judged only among records visible to the caller. Several folder placements of ONE
// record are intentional and never a duplicate. Review never deletes anything.

// hiddenPair creates a record placed in priv (invisible to a recipient of the share) and
// then, as the share recipient, a second record with the same URL inside the share.
func (w *dupWorld) hiddenPair(t *testing.T, url string) (private, shared bookmarks.Bookmark) {
	t.Helper()
	var err error
	private, err = w.e.svc.CreateBookmark(ctx, w.owner, bookmarks.BookmarkInput{URL: url, Title: "private " + url, Folders: []int64{w.priv}})
	noErr(t, err)
	if w.recipient.OwnerID == 0 {
		w.recipient, _, _ = w.e.shared(w.alice, w.shared, w.bob, identity.ModeEdit, identity.ModeEdit)
	}
	shared, err = w.e.svc.CreateBookmark(ctx, w.recipient, bookmarks.BookmarkInput{URL: url, Title: "shared " + url, Folders: []int64{bookmarks.RootID}})
	noErr(t, err)
	if private.ID == shared.ID {
		t.Fatal("setup: expected two separate records")
	}
	return private, shared
}

func TestListDuplicatesCountsDistinctRecordsNotPlacements(t *testing.T) {
	w := newDupWorld(t)
	priv2 := w.e.folder(w.owner, w.priv, "Private 2")

	// One record in two folders is not a duplicate. Near-miss URLs are different URLs.
	one := w.source(t, "one", nil, w.priv, priv2)
	w.e.bm(w.owner, dupURL+"/", "slash", w.priv)
	w.e.bm(w.owner, "https://DUP.example/page", "case", w.priv)
	got, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
	noErr(t, err)
	if got.Count != 0 || len(got.Groups) != 0 || got.Groups == nil || got.HasMore {
		t.Fatalf("placements/near-miss URLs reported as duplicates: %+v", got)
	}

	// A second distinct record makes one excess record, however many placements exist.
	_, other := w.hiddenPair(t, dupURL)
	got, err = w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
	noErr(t, err)
	if got.Count != 1 || len(got.Groups) != 1 {
		t.Fatalf("count/groups = %d/%d, want 1/1: %+v", got.Count, len(got.Groups), got)
	}
	g := got.Groups[0]
	// hiddenPair's private POST reuses `one` (same exact URL, same visible folder), so the
	// group holds `one` (two placements) and the recipient's own record.
	if g.URL != dupURL || len(g.Bookmarks) != 2 {
		t.Fatalf("group = %+v", g)
	}
	if g.Bookmarks[0].ID != one.ID || len(g.Bookmarks[0].Folders) != 2 || g.Bookmarks[1].ID != other.ID {
		t.Fatalf("group records = %+v, want record %d with 2 folders then %d", g.Bookmarks, one.ID, other.ID)
	}
	if len(g.Bookmarks[1].Folders) != 1 || g.Bookmarks[1].Folders[0] != w.shared {
		t.Fatalf("shared record folders = %v, want [%d]", g.Bookmarks[1].Folders, w.shared)
	}
}

func TestListDuplicatesCountsExcessAcrossGroups(t *testing.T) {
	w := newDupWorld(t)
	for i := 0; i < 3; i++ {
		w.hiddenPair(t, fmt.Sprintf("https://g%d.example/", i))
	}
	// A third record for group 0, created by a second share recipient who cannot see the
	// other two: that group now has two excess records, the others one each.
	shared2 := w.e.folder(w.owner, otherRootOf(t, w.e, w.owner), "Shared 2")
	carol := w.e.user("carol")
	cs, _, _ := w.e.shared(w.alice, shared2, carol, identity.ModeEdit, identity.ModeEdit)
	w.e.bm(cs, "https://g0.example/", "carol copy", bookmarks.RootID)
	got, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
	noErr(t, err)
	if got.Count != 4 || len(got.Groups) != 3 {
		t.Fatalf("count/groups = %d/%d, want 4/3", got.Count, len(got.Groups))
	}
	if len(got.Groups[0].Bookmarks) != 3 {
		t.Fatalf("group 0 holds %d records, want 3", len(got.Groups[0].Bookmarks))
	}
}

func TestListDuplicatesHidesOutOfScopeRecords(t *testing.T) {
	w := newDupWorld(t)
	private, shared := w.hiddenPair(t, dupURL)

	owner, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
	noErr(t, err)
	if owner.Count != 1 || len(owner.Groups) != 1 {
		t.Fatalf("owner = %+v", owner)
	}

	// The share recipient sees only the shared record, so it has no duplicate and the
	// private record's existence, title and id are not revealed in any form.
	rec, err := w.e.svc.ListDuplicates(ctx, w.recipient, 0, 50)
	noErr(t, err)
	if rec.Count != 0 || len(rec.Groups) != 0 {
		t.Fatalf("recipient learned about a hidden duplicate: %+v", rec)
	}

	// Another user with the same URL is isolated from alice entirely.
	carol := w.e.user("carol")
	cs := w.e.private(carol, identity.ModeEdit)
	w.e.bm(cs, dupURL, "carol", otherRootOf(t, w.e, cs))
	cd, err := w.e.svc.ListDuplicates(ctx, cs, 0, 50)
	noErr(t, err)
	if cd.Count != 0 || len(cd.Groups) != 0 {
		t.Fatalf("carol sees duplicates built from alice's data: %+v", cd)
	}
	again, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
	noErr(t, err)
	if again.Count != 1 || len(again.Groups[0].Bookmarks) != 2 {
		t.Fatalf("carol's bookmark leaked into alice's duplicates: %+v", again)
	}

	// Trashed records are not duplicates.
	noErr(t, w.e.svc.DeleteBookmark(ctx, w.owner, private.ID))
	after, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
	noErr(t, err)
	if after.Count != 0 || len(after.Groups) != 0 {
		t.Fatalf("trashed record still counted: %+v", after)
	}
	_ = shared
}

func otherRootOf(t *testing.T, e *env, sc bookmarks.Scope) int64 {
	t.Helper()
	_, other := e.roots(sc)
	return other
}

func TestListDuplicatesPagination(t *testing.T) {
	w := newDupWorld(t)
	urls := []string{"https://a.example/", "https://b.example/", "https://c.example/"}
	for _, u := range urls {
		w.hiddenPair(t, u)
	}
	p0, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 2)
	noErr(t, err)
	p1, err := w.e.svc.ListDuplicates(ctx, w.owner, 1, 2)
	noErr(t, err)
	p2, err := w.e.svc.ListDuplicates(ctx, w.owner, 2, 2)
	noErr(t, err)
	if len(p0.Groups) != 2 || !p0.HasMore || len(p1.Groups) != 1 || p1.HasMore || len(p2.Groups) != 0 || p2.HasMore {
		t.Fatalf("pages = %d/%v, %d/%v, %d/%v", len(p0.Groups), p0.HasMore, len(p1.Groups), p1.HasMore, len(p2.Groups), p2.HasMore)
	}
	// The count covers every page, not just the one returned.
	for i, p := range []bookmarks.DuplicateList{p0, p1, p2} {
		if p.Count != 3 {
			t.Errorf("page %d count = %d, want 3", i, p.Count)
		}
	}
	var got []string
	for _, p := range []bookmarks.DuplicateList{p0, p1} {
		for _, g := range p.Groups {
			got = append(got, g.URL)
		}
	}
	for i, u := range urls {
		if got[i] != u {
			t.Fatalf("group order = %v, want %v", got, urls)
		}
	}
	for _, bad := range [][2]int{{-1, 10}, {0, 0}, {0, -3}, {1_000_001, 10}} {
		_, err := w.e.svc.ListDuplicates(ctx, w.owner, bad[0], bad[1])
		is(t, err, bookmarks.ErrInvalid)
	}
	// An oversized limit is clamped, not rejected.
	big, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, bookmarks.MaxPageLimit*10)
	noErr(t, err)
	if len(big.Groups) != 3 || big.HasMore {
		t.Fatalf("clamped page = %+v", big)
	}
}

func TestDuplicateRemovalMovesRecordToTrash(t *testing.T) {
	w := newDupWorld(t)
	private, shared := w.hiddenPair(t, dupURL)

	// Reviewing never deletes anything.
	before := w.e.snapshot(w.owner)
	for i := 0; i < 2; i++ {
		_, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
		noErr(t, err)
	}
	if after := w.e.snapshot(w.owner); after != before {
		t.Fatalf("ListDuplicates changed data:\n%s\n%s", before, after)
	}
	trash, err := w.e.svc.ListTrash(ctx, w.owner, 10, 0)
	noErr(t, err)
	if len(trash) != 0 {
		t.Fatalf("trash after review = %+v", trash)
	}

	// A read-only caller sees the group but cannot remove either record.
	ro := w.e.private(w.alice, identity.ModeView)
	seen, err := w.e.svc.ListDuplicates(ctx, ro, 0, 50)
	noErr(t, err)
	if seen.Count != 1 {
		t.Fatalf("read-only caller count = %d, want 1", seen.Count)
	}
	is(t, w.e.svc.DeleteBookmark(ctx, ro, private.ID), bookmarks.ErrForbidden)
	if after := w.e.snapshot(w.owner); after != before {
		t.Fatalf("forbidden removal changed data:\n%s\n%s", before, after)
	}

	// A view-only share recipient cannot remove the shared record either.
	carol := w.e.user("carol")
	viewer, _, _ := w.e.shared(w.alice, w.shared, carol, identity.ModeView, identity.ModeView)
	is(t, w.e.svc.DeleteBookmark(ctx, viewer, shared.ID), bookmarks.ErrForbidden)

	// The owner removes the chosen record through the existing delete: it goes to the
	// Trash, the other record is untouched (no metadata merge), and the group is gone.
	noErr(t, w.e.svc.DeleteBookmark(ctx, w.owner, private.ID))
	trash, err = w.e.svc.ListTrash(ctx, w.owner, 10, 0)
	noErr(t, err)
	if len(trash) != 1 || trash[0].Type != "bookmark" || trash[0].ID != private.ID {
		t.Fatalf("trash = %+v, want only record %d", trash, private.ID)
	}
	left, err := w.e.svc.GetBookmark(ctx, w.owner, shared.ID)
	noErr(t, err)
	if left.Title != shared.Title {
		t.Fatalf("survivor title changed to %q", left.Title)
	}
	done, err := w.e.svc.ListDuplicates(ctx, w.owner, 0, 50)
	noErr(t, err)
	if done.Count != 0 || len(done.Groups) != 0 {
		t.Fatalf("duplicates after removal = %+v", done)
	}
}
