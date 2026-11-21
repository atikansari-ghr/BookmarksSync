package bookmarks_test

import (
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// Dashboard (workspace UI Task 1): the owner's private collection only. A record counts as
// recent by its CURRENT state: deleted (in the Trash, deleted_at in the window), else
// created (created_at in the window), else updated (updated_at in the window). The window
// start is inclusive. Placement-only edits do not touch the record and are not changes.

func (e *env) setNow(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = t
}

func changeTypes(items []bookmarks.DashboardChange) map[int64]string {
	m := map[int64]string{}
	for _, it := range items {
		m[it.ID] = it.Type
	}
	return m
}

func TestDashboardOwnSummary(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	sc := e.private(u, identity.ModeEdit)
	_, other := e.roots(sc)
	f2 := e.folder(sc, other, "F2")
	e.bm(sc, "https://one.example/", "one", other, f2) // one record, two placements
	e.bm(sc, "https://two.example/", "two", other)
	gone := e.bm(sc, "https://gone.example/", "gone", other)
	noErr(t, e.svc.DeleteBookmark(ctx, sc, gone.ID))

	d, err := e.svc.Dashboard(ctx, sc, e.clock().Add(-7*24*time.Hour), 0, 50)
	noErr(t, err)
	if d.TotalBookmarks != 2 {
		t.Fatalf("TotalBookmarks = %d, want 2 distinct live records", d.TotalBookmarks)
	}
	r := d.Recent
	if r.Created != 2 || r.Updated != 0 || r.Deleted != 1 || len(r.Items) != 3 || r.HasMore {
		t.Fatalf("recent = %+v", r)
	}
	if got := changeTypes(r.Items)[gone.ID]; got != "deleted" {
		t.Fatalf("trashed record reported as %q", got)
	}
}

func TestDashboardEmptyCollection(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeView)
	d, err := e.svc.Dashboard(ctx, sc, e.clock().Add(-7*24*time.Hour), 0, 50)
	noErr(t, err)
	if d.TotalBookmarks != 0 || d.Recent.Created+d.Recent.Updated+d.Recent.Deleted != 0 || d.Recent.Items == nil || len(d.Recent.Items) != 0 || d.Recent.HasMore {
		t.Fatalf("empty dashboard = %+v", d)
	}
	if !d.Recent.Since.Equal(e.clock().Add(-7 * 24 * time.Hour)) {
		t.Fatalf("Since = %v", d.Recent.Since)
	}
}

func TestDashboardRecentChangesWindowAndPagination(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	sc := e.private(u, identity.ModeEdit)
	_, other := e.roots(sc)
	base := e.clock()
	since := base.Add(24 * time.Hour)
	at := func(d time.Duration) { e.setNow(since.Add(d)) }

	mk := func(name string, when time.Duration) bookmarks.Bookmark {
		at(when)
		return e.bm(sc, "https://"+name+".example/", name, other)
	}
	retitle := func(b bookmarks.Bookmark, when time.Duration) {
		at(when)
		title := b.Title + "!"
		_, err := e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Title: &title})
		noErr(t, err)
	}
	del := func(b bookmarks.Bookmark, when time.Duration) {
		at(when)
		noErr(t, e.svc.DeleteBookmark(ctx, sc, b.ID))
	}
	ms := time.Millisecond
	day := 24 * time.Hour

	justBefore := mk("created-just-before", -ms)   // outside: created before the window
	atStart := mk("created-at-start", 0)           // inside: boundary is inclusive
	updatedAtStart := mk("updated-at-start", -day) // created long ago ...
	retitle(updatedAtStart, 0)                     // ... updated exactly at the start: inside
	updatedBefore := mk("updated-before", -day)    // updated 1ms before the window: outside
	retitle(updatedBefore, -ms)
	createdThenUpdated := mk("created-then-updated", ms) // created in window, edited in window: created
	retitle(createdThenUpdated, 2*ms)
	deletedInWindow := mk("deleted-in-window", -day) // deleted exactly at the start: inside
	del(deletedInWindow, 0)
	deletedBefore := mk("deleted-before", -day) // deleted 1ms before the window: outside
	del(deletedBefore, -ms)
	editedThenDeleted := mk("edited-then-deleted", -day) // one entry: deleted
	retitle(editedThenDeleted, 3*ms)
	del(editedThenDeleted, 4*ms)
	_ = justBefore

	at(day)
	d, err := e.svc.Dashboard(ctx, sc, since, 0, 50)
	noErr(t, err)
	got := changeTypes(d.Recent.Items)
	want := map[int64]string{
		atStart.ID: "created", updatedAtStart.ID: "updated", createdThenUpdated.ID: "created",
		deletedInWindow.ID: "deleted", editedThenDeleted.ID: "deleted",
	}
	if len(got) != len(want) || len(d.Recent.Items) != len(want) {
		t.Fatalf("items = %+v, want types %v", d.Recent.Items, want)
	}
	for id, typ := range want {
		if got[id] != typ {
			t.Errorf("record %d reported %q, want %q", id, got[id], typ)
		}
	}
	if d.Recent.Created != 2 || d.Recent.Updated != 1 || d.Recent.Deleted != 2 {
		t.Fatalf("counts = %d/%d/%d, want 2/1/2", d.Recent.Created, d.Recent.Updated, d.Recent.Deleted)
	}
	// Live records: justBefore, atStart, updatedAtStart, updatedBefore, createdThenUpdated.
	if d.TotalBookmarks != 5 {
		t.Fatalf("TotalBookmarks = %d, want 5", d.TotalBookmarks)
	}
	// Newest change first; the item carries current state (title after the edit, URL, time).
	last := d.Recent.Items[0]
	if last.ID != editedThenDeleted.ID || last.Title != "edited-then-deleted!" || last.URL != "https://edited-then-deleted.example/" ||
		!last.At.Equal(since.Add(4*ms)) {
		t.Fatalf("newest item = %+v", last)
	}
	for i := 1; i < len(d.Recent.Items); i++ {
		a, b := d.Recent.Items[i-1], d.Recent.Items[i]
		if b.At.After(a.At) || b.At.Equal(a.At) && b.ID > a.ID {
			t.Fatalf("items not ordered newest first: %+v", d.Recent.Items)
		}
	}

	// Pagination: counts always cover the whole window, items only the page.
	var ids []int64
	for page := 0; page < 3; page++ {
		p, err := e.svc.Dashboard(ctx, sc, since, page, 2)
		noErr(t, err)
		if p.Recent.Created != 2 || p.Recent.Updated != 1 || p.Recent.Deleted != 2 || p.TotalBookmarks != 5 {
			t.Fatalf("page %d counts changed: %+v", page, p)
		}
		wantLen, wantMore := 2, true
		if page == 2 {
			wantLen, wantMore = 1, false
		}
		if len(p.Recent.Items) != wantLen || p.Recent.HasMore != wantMore {
			t.Fatalf("page %d = %d items hasMore=%v", page, len(p.Recent.Items), p.Recent.HasMore)
		}
		for _, it := range p.Recent.Items {
			ids = append(ids, it.ID)
		}
	}
	for i, it := range d.Recent.Items {
		if ids[i] != it.ID {
			t.Fatalf("paged order %v differs from full order %+v", ids, d.Recent.Items)
		}
	}
	if p, err := e.svc.Dashboard(ctx, sc, since, 3, 2); err != nil || len(p.Recent.Items) != 0 || p.Recent.HasMore {
		t.Fatalf("past-the-end page = %+v, %v", p, err)
	}
	for _, bad := range [][2]int{{-1, 10}, {0, 0}, {0, -1}, {1_000_001, 10}} {
		_, err := e.svc.Dashboard(ctx, sc, since, bad[0], bad[1])
		is(t, err, bookmarks.ErrInvalid)
	}
	if p, err := e.svc.Dashboard(ctx, sc, since, 0, bookmarks.MaxPageLimit*10); err != nil || len(p.Recent.Items) != 5 {
		t.Fatalf("oversized limit: %+v, %v", p, err)
	}
}

func TestDashboardDoesNotIncludeSharedOrOtherUserRecords(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	bs := e.private(bob, identity.ModeEdit)
	_, aOther := e.roots(as)
	bBar, bOther := e.roots(bs)
	e.bm(as, "https://alice.example/", "alice mark", aOther)
	bobShared := e.folder(bs, bBar, "BobShared")
	e.bm(bs, "https://bob-secret.example/", "BOB SECRET", bOther)
	e.bm(bs, "https://bob-shared.example/", "BOB SHARED", bobShared)
	trashed := e.bm(bs, "https://bob-trashed.example/", "BOB TRASHED", bOther)
	noErr(t, e.svc.DeleteBookmark(ctx, bs, trashed.ID))
	// Bob shares a folder with alice, view and edit; alice's private dashboard must not grow.
	recip, _, _ := e.shared(bob, bobShared, alice, identity.ModeEdit, identity.ModeEdit)
	since := e.clock().Add(-7 * 24 * time.Hour)

	d, err := e.svc.Dashboard(ctx, as, since, 0, 50)
	noErr(t, err)
	if d.TotalBookmarks != 1 || len(d.Recent.Items) != 1 || d.Recent.Items[0].Title != "alice mark" {
		t.Fatalf("alice dashboard leaked or lost data: %+v", d)
	}
	// A share scope is never a dashboard scope, even a writable one.
	_, err = e.svc.Dashboard(ctx, recip, since, 0, 50)
	is(t, err, bookmarks.ErrForbidden)

	bd, err := e.svc.Dashboard(ctx, bs, since, 0, 50)
	noErr(t, err)
	if bd.TotalBookmarks != 2 || bd.Recent.Created != 2 || bd.Recent.Deleted != 1 {
		t.Fatalf("bob dashboard = %+v", bd)
	}
	for _, it := range d.Recent.Items {
		if it.URL == "" || it.Title == "" {
			t.Fatalf("incomplete item %+v", it)
		}
	}
}
