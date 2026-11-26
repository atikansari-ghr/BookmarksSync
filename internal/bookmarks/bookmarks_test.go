package bookmarks_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestBookmarkCRUDPersistsAcrossReopen(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	sc := e.private(u, identity.ModeEdit)
	bar, other := e.roots(sc)
	created, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{
		URL: "https://example.com/p?q=1&r=<2>", Title: "Ünï 日本 \"quoted\" \xe2\x80\xa8", Folders: []int64{bar},
		Tags: &[]string{"zeta", "alpha"},
	})
	noErr(t, err)
	if created.ID <= 0 || len(created.Folders) != 1 || created.Folders[0] != bar {
		t.Fatalf("created = %+v", created)
	}
	if strings.Join(created.Tags, ",") != "alpha,zeta" {
		t.Errorf("tags = %v, want sorted", created.Tags)
	}
	upd, err := e.svc.UpdateBookmark(ctx, sc, created.ID, bookmarks.BookmarkUpdate{Title: ptr("new title"), Folders: &[]int64{other}})
	noErr(t, err)
	if upd.Title != "new title" || len(upd.Folders) != 1 || upd.Folders[0] != other {
		t.Fatalf("updated = %+v", upd)
	}
	e.reopen()
	sc = e.scopeAfterReopen(u)
	got, err := e.svc.GetBookmark(ctx, sc, created.ID)
	noErr(t, err)
	if got.Title != "new title" || got.URL != created.URL || len(got.Folders) != 1 || got.Folders[0] != other {
		t.Errorf("after reopen %+v", got)
	}
}

func TestSameURLInTwoFoldersIsOneRecordAndRemovingOnePlacementKeepsOther(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	first := e.bm(sc, "https://dup.example/", "Dup", bar)
	second := e.bm(sc, "https://dup.example/", "Dup", other)
	if first.ID != second.ID {
		t.Fatalf("same URL produced two records: %d and %d", first.ID, second.ID)
	}
	if len(second.Folders) != 2 {
		t.Fatalf("folders = %v, want both placements", second.Folders)
	}
	got, err := e.svc.GetBookmark(ctx, sc, first.ID)
	noErr(t, err)
	if len(got.Folders) != 2 {
		t.Fatalf("folders = %v", got.Folders)
	}
	list, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1})
	noErr(t, err)
	if len(list) != 1 {
		t.Fatalf("list has %d records, want 1", len(list))
	}

	noErr(t, e.svc.RemovePlacement(ctx, sc, bar, first.ID))
	got, err = e.svc.GetBookmark(ctx, sc, first.ID)
	noErr(t, err)
	if len(got.Folders) != 1 || got.Folders[0] != other {
		t.Fatalf("after removing one placement folders = %v, want [%d]", got.Folders, other)
	}
	assertOrder(t, e, sc, bar, nil)
	assertOrder(t, e, sc, other, []string{"Dup"})
	// removing the last placement deletes the (soft) bookmark
	noErr(t, e.svc.RemovePlacement(ctx, sc, other, first.ID))
	_, err = e.svc.GetBookmark(ctx, sc, first.ID)
	is(t, err, bookmarks.ErrNotFound)
	is(t, e.svc.RemovePlacement(ctx, sc, other, first.ID), bookmarks.ErrNotFound)
	// the URL can be created again afterwards
	again := e.bm(sc, "https://dup.example/", "Dup", bar)
	if again.ID == first.ID {
		t.Error("a trashed record id was reused for a new bookmark")
	}
}

func TestSameURLTwiceInSameFolderIsOnePlacement(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	e.bm(sc, "https://same.example/", "S", bar, bar)
	e.bm(sc, "https://same.example/", "S", bar)
	assertOrder(t, e, sc, bar, []string{"S"})
}

func TestUpdateKeepsServerTagsWhenOmittedAndReplacesWhenGiven(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	b, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://t.example/", Title: "t", Folders: []int64{bar}, Tags: &[]string{"work", "read-later"}})
	noErr(t, err)

	got, err := e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Title: ptr("t2"), Folders: &[]int64{bar, other}})
	noErr(t, err)
	if strings.Join(got.Tags, ",") != "read-later,work" {
		t.Errorf("tags lost on update without tags: %v", got.Tags)
	}
	// re-posting the same URL without tags (floccus-style) must not wipe tags either
	again, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://t.example/", Title: "t2", Folders: []int64{other}})
	noErr(t, err)
	if len(again.Tags) != 2 {
		t.Errorf("tags wiped by create-without-tags: %v", again.Tags)
	}
	got, err = e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Tags: &[]string{"only"}})
	noErr(t, err)
	if strings.Join(got.Tags, ",") != "only" {
		t.Errorf("tags = %v", got.Tags)
	}
	got, err = e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Tags: &[]string{}})
	noErr(t, err)
	if len(got.Tags) != 0 {
		t.Errorf("explicit empty tags should clear: %v", got.Tags)
	}
}

func TestUpdatePlacementsAreAtomicOnFailure(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bob := e.private(e.user("bob"), identity.ModeEdit)
	bar, other := e.roots(sc)
	bobBar, _ := e.roots(bob)
	b := e.bm(sc, "https://a.example/", "A", bar)
	before := e.snapshot(sc)

	_, err := e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Title: ptr("changed"), Folders: &[]int64{other, 424242}})
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Title: ptr("changed"), Folders: &[]int64{other, bobBar}})
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Title: ptr("changed"), Folders: &[]int64{}})
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{URL: ptr("ftp-less"), Title: ptr("changed")})
	is(t, err, bookmarks.ErrInvalid)
	if got := e.snapshot(sc); got != before {
		t.Errorf("failed updates changed state")
	}
}

func TestURLChangeMergesIntoConflictOnlyByError(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	e.bm(sc, "https://one.example/", "1", bar)
	two := e.bm(sc, "https://two.example/", "2", bar)
	_, err := e.svc.UpdateBookmark(ctx, sc, two.ID, bookmarks.BookmarkUpdate{URL: ptr("https://one.example/")})
	is(t, err, bookmarks.ErrConflict)
	got, err := e.svc.UpdateBookmark(ctx, sc, two.ID, bookmarks.BookmarkUpdate{URL: ptr("https://three.example/")})
	noErr(t, err)
	if got.URL != "https://three.example/" {
		t.Errorf("url = %s", got.URL)
	}
}

func TestURLSchemeValidation(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	good := []string{"http://a.example/", "https://a.example/x?y#z", "ftp://a.example/", "file:///tmp/a.html", "javascript:alert(1)", "HTTPS://UPPER.example/"}
	for i, u := range good {
		if _, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: u, Title: "g", Folders: []int64{bar}}); err != nil {
			t.Errorf("good #%d %q rejected: %v", i, u, err)
		}
	}
	bad := []string{"", "   ", "about:blank", "chrome://settings", "data:text/html,x", "no-scheme.example", "vbscript:x", "://x", "http://" + strings.Repeat("a", 9000)}
	for _, u := range bad {
		_, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: u, Title: "b", Folders: []int64{bar}})
		if err == nil {
			t.Errorf("bad url %q accepted", u[:min(len(u), 30)])
			continue
		}
		is(t, err, bookmarks.ErrInvalid)
	}
	_, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://t.example/", Title: strings.Repeat("t", 5000), Folders: []int64{bar}})
	is(t, err, bookmarks.ErrInvalid)
}

func TestJavascriptBookmarksAreStoredAndFoundBySearchButNeverExecuted(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	js := e.bm(sc, "javascript:void(document.title='x')", "bookmarklet", bar)
	e.bm(sc, "https://plain.example/", "plain", bar)
	got, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1, Search: []string{"javascript:"}})
	noErr(t, err)
	if len(got) != 1 || got[0].ID != js.ID || got[0].URL != "javascript:void(document.title='x')" {
		t.Fatalf("search = %+v", got)
	}
}

func TestListPagingLimitsURLLookupAndSearch(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	for i := 0; i < 25; i++ {
		e.bm(sc, fmt.Sprintf("https://site%02d.example/%%_x", i), fmt.Sprintf("Title %02d", i), bar)
	}
	p0, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: 0, Limit: 10})
	noErr(t, err)
	p1, _ := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: 1, Limit: 10})
	p2, _ := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: 2, Limit: 10})
	p3, _ := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: 3, Limit: 10})
	if len(p0) != 10 || len(p1) != 10 || len(p2) != 5 || len(p3) != 0 {
		t.Fatalf("page sizes %d %d %d %d", len(p0), len(p1), len(p2), len(p3))
	}
	if p0[9].ID >= p1[0].ID {
		t.Error("pages overlap or are unordered")
	}
	byURL, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: 0, Limit: 5, URL: "https://site07.example/%_x"})
	noErr(t, err)
	if len(byURL) != 1 || byURL[0].Title != "Title 07" {
		t.Fatalf("url lookup = %+v", byURL)
	}
	// LIKE wildcards in the URL must not match as patterns
	wild, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: 0, Limit: 5, URL: "https://site%.example/%_x"})
	noErr(t, err)
	if len(wild) != 0 {
		t.Errorf("wildcard url matched %d records", len(wild))
	}
	s, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1, Search: []string{"title 1"}})
	noErr(t, err)
	if len(s) != 10 {
		t.Errorf("search 'title 1' = %d, want 10", len(s))
	}
	pct, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1, Search: []string{"%"}})
	noErr(t, err)
	if len(pct) != 25 { // every URL contains a literal '%'
		t.Errorf("literal %% search = %d, want 25", len(pct))
	}
	for _, bad := range []bookmarks.ListQuery{{Page: -2, Limit: 10}, {Page: 0, Limit: 0}, {Page: 0, Limit: -5}, {Page: 0}} {
		_, err := e.svc.ListBookmarks(ctx, sc, bad)
		is(t, err, bookmarks.ErrInvalid)
	}
	big, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: 0, Limit: 1 << 30})
	noErr(t, err) // clamped, never unbounded
	if len(big) != 25 {
		t.Errorf("clamped list = %d", len(big))
	}
}

func TestDeleteBookmarkRemovesAllVisiblePlacements(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	b := e.bm(sc, "https://d.example/", "d", bar, other)
	noErr(t, e.svc.DeleteBookmark(ctx, sc, b.ID))
	_, err := e.svc.GetBookmark(ctx, sc, b.ID)
	is(t, err, bookmarks.ErrNotFound)
	assertOrder(t, e, sc, bar, nil)
	assertOrder(t, e, sc, other, nil)
	is(t, e.svc.DeleteBookmark(ctx, sc, b.ID), bookmarks.ErrNotFound)
}

func TestReadOnlyScopeRejectsEveryWriteAndKeepsReads(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	rw := e.private(u, identity.ModeEdit)
	ro := e.private(u, identity.ModeView)
	bar, other := e.roots(rw)
	f := e.folder(rw, bar, "f")
	b := e.bm(rw, "https://r.example/", "r", f)
	before := e.snapshot(ro)

	_, err := e.svc.CreateFolder(ctx, ro, bar, "x")
	is(t, err, bookmarks.ErrForbidden)
	_, err = e.svc.UpdateFolder(ctx, ro, f, nil, ptr("y"))
	is(t, err, bookmarks.ErrForbidden)
	is(t, e.svc.DeleteFolder(ctx, ro, f), bookmarks.ErrForbidden)
	is(t, e.svc.SetChildOrder(ctx, ro, bar, nil), bookmarks.ErrForbidden)
	_, err = e.svc.CreateBookmark(ctx, ro, bookmarks.BookmarkInput{URL: "https://n.example/", Title: "n", Folders: []int64{bar}})
	is(t, err, bookmarks.ErrForbidden)
	_, err = e.svc.UpdateBookmark(ctx, ro, b.ID, bookmarks.BookmarkUpdate{Title: ptr("z")})
	is(t, err, bookmarks.ErrForbidden)
	is(t, e.svc.RemovePlacement(ctx, ro, f, b.ID), bookmarks.ErrForbidden)
	is(t, e.svc.DeleteBookmark(ctx, ro, b.ID), bookmarks.ErrForbidden)
	_, err = e.svc.ImportTree(ctx, ro, other, []bookmarks.ImportNode{{Title: "a", URL: "https://i.example/"}})
	is(t, err, bookmarks.ErrForbidden)
	if got := e.snapshot(ro); got != before {
		t.Errorf("read-only scope changed state")
	}
	if _, err := e.svc.GetBookmark(ctx, ro, b.ID); err != nil {
		t.Errorf("read-only read failed: %v", err)
	}
}

// Native bookmarks routinely have empty titles (icon-only links) and padded titles.
// floccus hashes exactly what it sent, so the server must never trim or rewrite them.
func TestTitlesAndURLsRoundTripUnchanged(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	sc := e.private(u, identity.ModeEdit)
	bar, _ := e.roots(sc)
	cases := []struct{ url, title string }{
		{"https://icon.example/", ""},
		{"https://pad.example/", "  leading and trailing  "},
		{"https://tab.example/", "\ttab\n"},
		{"HTTPS://Mixed.Example/Path?Q=1#Frag", "case kept"},
		{"https://trailing.example/dir/", "slash kept"},
		{"https://space.example/a b", "url with space"},
	}
	for _, c := range cases {
		b := e.bm(sc, c.url, c.title, bar)
		got, err := e.svc.GetBookmark(ctx, sc, b.ID)
		noErr(t, err)
		if got.Title != c.title || got.URL != c.url {
			t.Errorf("round trip %q/%q -> %q/%q", c.url, c.title, got.URL, got.Title)
		}
	}
	f := e.folder(sc, bar, "  padded folder  ")
	gf, err := e.svc.GetFolder(ctx, sc, f)
	noErr(t, err)
	if gf.Title != "  padded folder  " {
		t.Errorf("folder title = %q", gf.Title)
	}
	empty := e.folder(sc, bar, "")
	ge, _ := e.svc.GetFolder(ctx, sc, empty)
	if ge.Title != "" {
		t.Errorf("empty folder title = %q", ge.Title)
	}
	// the hash sees the exact stored values
	b := e.bm(sc, "https://hash.example/", "  x ", bar)
	_ = b
	nodes, _ := e.svc.Children(ctx, sc, bar, 1)
	for _, n := range nodes {
		if n.URL == "https://hash.example/" && n.Title != "  x " {
			t.Errorf("children title = %q", n.Title)
		}
	}
	// updates keep them too
	upd, err := e.svc.UpdateBookmark(ctx, sc, b.ID, bookmarks.BookmarkUpdate{Title: ptr("")})
	noErr(t, err)
	if upd.Title != "" {
		t.Errorf("title cleared to empty became %q", upd.Title)
	}
}
