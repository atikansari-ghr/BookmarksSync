package bookmarks_test

import (
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// Controller ruling: exact-URL dedupe considers only records visible in the caller's
// scope. A recipient POSTing a URL the owner holds privately gets a SEPARATE record.
func collisionSetup(t *testing.T) (e *env, as, ss bookmarks.Scope, private bookmarks.Bookmark, shared, hiddenFolder int64) {
	t.Helper()
	e = newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as = e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	shared = e.folder(as, aBar, "Shared")
	hiddenFolder = e.folder(as, aOther, "PrivateStuff")
	var err error
	private, err = e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{
		URL: "https://collide.example/x", Title: "SECRET private title", Folders: []int64{hiddenFolder},
		Tags: &[]string{"secret-tag"},
	})
	noErr(t, err)
	ss, _, _ = e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	return
}

func TestCrossScopeURLCollisionGET(t *testing.T) {
	e, _, ss, private, _, _ := collisionSetup(t)
	// the private record is invisible by id, by exact URL and by search
	_, err := e.svc.GetBookmark(ctx, ss, private.ID)
	is(t, err, bookmarks.ErrNotFound)
	byURL, err := e.svc.ListBookmarks(ctx, ss, bookmarks.ListQuery{Page: 0, Limit: 10, URL: "https://collide.example/x"})
	noErr(t, err)
	if len(byURL) != 0 {
		t.Fatalf("private record visible by URL lookup: %+v", byURL)
	}
	for _, term := range []string{"collide", "SECRET", "secret-tag"} {
		found, err := e.svc.ListBookmarks(ctx, ss, bookmarks.ListQuery{Page: -1, Search: []string{term}})
		noErr(t, err)
		if len(found) != 0 {
			t.Errorf("search %q leaked %+v", term, found)
		}
	}
}

func TestCrossScopeURLCollisionPOSTCreatesSeparateRecord(t *testing.T) {
	e, as, ss, private, shared, hiddenFolder := collisionSetup(t)
	got, err := e.svc.CreateBookmark(ctx, ss, bookmarks.BookmarkInput{URL: "https://collide.example/x", Title: "Recipient title", Folders: []int64{bookmarks.RootID}})
	noErr(t, err)
	if got.ID == private.ID {
		t.Fatal("recipient POST reused the owner's private record")
	}
	if got.Title != "Recipient title" || len(got.Tags) != 0 {
		t.Errorf("recipient learned/inherited private metadata: %+v", got)
	}
	if len(got.Folders) != 1 || got.Folders[0] != bookmarks.RootID {
		t.Errorf("folders = %v", got.Folders)
	}
	owner, err := e.svc.GetBookmark(ctx, as, private.ID)
	noErr(t, err)
	if owner.Title != "SECRET private title" || strings.Join(owner.Tags, ",") != "secret-tag" || len(owner.Folders) != 1 || owner.Folders[0] != hiddenFolder {
		t.Errorf("owner's private record was altered: %+v", owner)
	}
	// a repeated POST is idempotent against the now-visible shared record
	again, err := e.svc.CreateBookmark(ctx, ss, bookmarks.BookmarkInput{URL: "https://collide.example/x", Title: "Recipient title", Folders: []int64{bookmarks.RootID}})
	noErr(t, err)
	if again.ID != got.ID {
		t.Errorf("second POST created another record: %d vs %d", again.ID, got.ID)
	}
	// the owner sees both records (their private one and the one living in the share)
	list, _ := e.svc.ListBookmarks(ctx, as, bookmarks.ListQuery{Page: -1, URL: "https://collide.example/x"})
	if len(list) != 2 {
		t.Errorf("owner sees %d records for the URL, want 2", len(list))
	}
	// the recipient's lookup finds only its own record
	mine, _ := e.svc.ListBookmarks(ctx, ss, bookmarks.ListQuery{Page: 0, Limit: 10, URL: "https://collide.example/x"})
	if len(mine) != 1 || mine[0].ID != got.ID {
		t.Errorf("recipient lookup = %+v", mine)
	}
	_ = shared
}

func TestCrossScopeURLCollisionPUT(t *testing.T) {
	e, as, ss, private, shared, hiddenFolder := collisionSetup(t)
	mine, err := e.svc.CreateBookmark(ctx, ss, bookmarks.BookmarkInput{URL: "https://collide.example/x", Title: "Recipient title", Folders: []int64{bookmarks.RootID}})
	noErr(t, err)

	// PUT on the private id is not found and writes nothing
	_, err = e.svc.UpdateBookmark(ctx, ss, private.ID, bookmarks.BookmarkUpdate{Title: ptr("overwrite"), Tags: &[]string{"x"}, Folders: &[]int64{bookmarks.RootID}})
	is(t, err, bookmarks.ErrNotFound)
	// PUT on the recipient's record touches only that record
	upd, err := e.svc.UpdateBookmark(ctx, ss, mine.ID, bookmarks.BookmarkUpdate{Title: ptr("Recipient v2"), Folders: &[]int64{bookmarks.RootID}})
	noErr(t, err)
	if upd.Title != "Recipient v2" || len(upd.Tags) != 0 {
		t.Errorf("update = %+v", upd)
	}
	// changing the recipient record's URL onto the private URL does not conflict with or merge into it
	other := e.bm(ss, "https://elsewhere.example/", "e", bookmarks.RootID)
	moved, err := e.svc.UpdateBookmark(ctx, ss, other.ID, bookmarks.BookmarkUpdate{URL: ptr("https://collide.example/x")})
	// the visible record `mine` already owns that URL in this scope, so this is a conflict
	is(t, err, bookmarks.ErrConflict)
	_ = moved
	// but a URL only the private scope holds is free for the recipient
	free := e.bm(as, "https://private-only.example/", "po", hiddenFolder)
	got, err := e.svc.UpdateBookmark(ctx, ss, other.ID, bookmarks.BookmarkUpdate{URL: ptr("https://private-only.example/")})
	noErr(t, err)
	if got.ID == free.ID || got.ID != other.ID {
		t.Errorf("url edit merged into an invisible record: %+v", got)
	}

	priv, err := e.svc.GetBookmark(ctx, as, private.ID)
	noErr(t, err)
	if priv.Title != "SECRET private title" || strings.Join(priv.Tags, ",") != "secret-tag" || len(priv.Folders) != 1 || priv.Folders[0] != hiddenFolder {
		t.Errorf("private record changed: %+v", priv)
	}
	_ = shared
}

func TestAlreadyVisibleSharedRecordKeepsSharedMetadataAndHiddenPlacements(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	hidden := e.folder(as, aOther, "Hidden")
	rec, err := e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{URL: "https://both.example/", Title: "Both", Folders: []int64{shared, hidden}, Tags: &[]string{"keep"}})
	noErr(t, err)
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)

	// recipient POST of an already-visible URL reuses the record (metadata is shared across known placements)
	got, err := e.svc.CreateBookmark(ctx, ss, bookmarks.BookmarkInput{URL: "https://both.example/", Title: "Both renamed", Folders: []int64{bookmarks.RootID}})
	noErr(t, err)
	if got.ID != rec.ID || len(got.Folders) != 1 || got.Folders[0] != bookmarks.RootID {
		t.Fatalf("got %+v", got)
	}
	owner, _ := e.svc.GetBookmark(ctx, as, rec.ID)
	if len(owner.Folders) != 2 || strings.Join(owner.Tags, ",") != "keep" {
		t.Errorf("hidden placement or tags lost: %+v", owner)
	}
	// PUT with the visible list preserves the hidden placement
	_, err = e.svc.UpdateBookmark(ctx, ss, rec.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{bookmarks.RootID}})
	noErr(t, err)
	owner, _ = e.svc.GetBookmark(ctx, as, rec.ID)
	found := false
	for _, f := range owner.Folders {
		if f == hidden {
			found = true
		}
	}
	if !found {
		t.Errorf("hidden placement removed by share PUT: %v", owner.Folders)
	}
}
