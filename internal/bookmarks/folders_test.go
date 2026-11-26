package bookmarks_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestCanonicalRootsExistInOrderAndEnsureIsIdempotent(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	sc := e.private(u, identity.ModeEdit)
	noErr(t, e.svc.EnsureCollection(ctx, u.ID))
	nodes, err := e.svc.Children(ctx, sc, bookmarks.RootID, 1)
	noErr(t, err)
	if len(nodes) != 2 || nodes[0].Title != "Bookmarks Bar" || nodes[1].Title != "Other Bookmarks" {
		t.Fatalf("root children = %+v", nodes)
	}
	for _, n := range nodes {
		if n.Type != "folder" || n.ID <= 0 {
			t.Errorf("bad root node %+v", n)
		}
	}
}

func TestCanonicalRootsCannotBeRenamedMovedOrDeleted(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	sub := e.folder(sc, bar, "sub")
	before := e.snapshot(sc)

	title := "Renamed"
	_, err := e.svc.UpdateFolder(ctx, sc, bar, nil, &title)
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.UpdateFolder(ctx, sc, bar, &sub, nil)
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.UpdateFolder(ctx, sc, other, ptr(bar), nil)
	is(t, err, bookmarks.ErrInvalid)
	is(t, e.svc.DeleteFolder(ctx, sc, bar), bookmarks.ErrInvalid)
	is(t, e.svc.DeleteFolder(ctx, sc, other), bookmarks.ErrInvalid)
	// the virtual root itself is not deletable or renamable either
	is(t, e.svc.DeleteFolder(ctx, sc, bookmarks.RootID), bookmarks.ErrInvalid)
	_, err = e.svc.UpdateFolder(ctx, sc, bookmarks.RootID, nil, &title)
	is(t, err, bookmarks.ErrInvalid)
	if got := e.snapshot(sc); got != before {
		t.Errorf("failed operations changed state:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestRootLevelFoldersCannotImpersonateCanonicalRoots(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	_, err := e.svc.CreateFolder(ctx, sc, bookmarks.RootID, "Bookmarks Bar")
	is(t, err, bookmarks.ErrInvalid)
	// Firefox's extra roots are allowed as ordinary top-level folders.
	f, err := e.svc.CreateFolder(ctx, sc, bookmarks.RootID, "Bookmarks Menu")
	noErr(t, err)
	if f.ParentID != bookmarks.RootID {
		t.Errorf("parent = %d, want -1", f.ParentID)
	}
	bar, _ := e.roots(sc)
	sub := e.folder(sc, bar, "x")
	// A top-level move onto the canonical names is rejected too.
	n := "Other Bookmarks"
	_, err = e.svc.UpdateFolder(ctx, sc, sub, ptr(bookmarks.RootID), &n)
	is(t, err, bookmarks.ErrInvalid)
	// but the same title deeper in the tree is fine
	e.folder(sc, bar, "Bookmarks Bar")
}

func TestBookmarksCannotLiveDirectlyUnderPrivateRoot(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	_, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://a.example/", Title: "a", Folders: []int64{bookmarks.RootID}})
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://a.example/", Title: "a"})
	is(t, err, bookmarks.ErrInvalid)
}

func TestFolderCreateGetRenameMovePersistAcrossReopen(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	sc := e.private(u, identity.ModeEdit)
	bar, other := e.roots(sc)
	a := e.folder(sc, bar, "A")
	b := e.folder(sc, a, "B")
	c := e.folder(sc, other, "C")

	got, err := e.svc.GetFolder(ctx, sc, b)
	noErr(t, err)
	if got.Title != "B" || got.ParentID != a {
		t.Fatalf("GetFolder = %+v", got)
	}
	_, err = e.svc.UpdateFolder(ctx, sc, b, ptr(c), ptr("B2 ünï/日本 <&>"))
	noErr(t, err)
	e.reopen()
	sc = e.scopeAfterReopen(u)
	got, err = e.svc.GetFolder(ctx, sc, b)
	noErr(t, err)
	if got.Title != "B2 ünï/日本 <&>" || got.ParentID != c {
		t.Errorf("after reopen %+v", got)
	}
}

// scopeAfterReopen builds a fresh private scope for u after the DB was reopened.
func (e *env) scopeAfterReopen(u identity.User) bookmarks.Scope {
	return e.private(u, identity.ModeEdit)
}

func TestIDsAreNumericGloballyUniqueAndNeverReused(t *testing.T) {
	e := newEnv(t)
	sa := e.private(e.user("alice"), identity.ModeEdit)
	sb := e.private(e.user("bob"), identity.ModeEdit)
	seen := map[int64]string{}
	add := func(id int64, what string) {
		t.Helper()
		if id <= 0 {
			t.Fatalf("%s id = %d", what, id)
		}
		if prev, dup := seen[id]; dup {
			t.Fatalf("folder id %d used by %s and %s", id, prev, what)
		}
		seen[id] = what
	}
	barA, otherA := e.roots(sa)
	barB, otherB := e.roots(sb)
	add(barA, "alice bar")
	add(otherA, "alice other")
	add(barB, "bob bar")
	add(otherB, "bob other")
	f1 := e.folder(sa, barA, "f1")
	add(f1, "f1")
	noErr(t, e.svc.DeleteFolder(ctx, sa, f1))
	f2 := e.folder(sa, barA, "f2")
	add(f2, "f2 after deleting f1") // would collide if ids were reused
}

func TestCycleAndSelfParentAreRejectedWithoutPartialWrites(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	a := e.folder(sc, bar, "A")
	b := e.folder(sc, a, "B")
	c := e.folder(sc, b, "C")
	before := e.snapshot(sc)

	for name, target := range map[string]int64{"self": a, "child": b, "grandchild": c} {
		_, err := e.svc.UpdateFolder(ctx, sc, a, ptr(target), ptr("changed-"+name))
		is(t, err, bookmarks.ErrInvalid)
	}
	if got := e.snapshot(sc); got != before {
		t.Errorf("rejected cycle changed state (title change leaked?)\n%s\n---\n%s", before, got)
	}
}

func TestMoveAppendsAtEndAndRenameKeepsPosition(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	x := e.folder(sc, bar, "x")
	y := e.folder(sc, bar, "y")
	z := e.folder(sc, bar, "z")
	e.folder(sc, other, "o1")

	_, err := e.svc.UpdateFolder(ctx, sc, x, nil, ptr("x-renamed"))
	noErr(t, err)
	assertOrder(t, e, sc, bar, []string{"x-renamed", "y", "z"})
	_, err = e.svc.UpdateFolder(ctx, sc, x, ptr(other), nil)
	noErr(t, err)
	assertOrder(t, e, sc, bar, []string{"y", "z"})
	assertOrder(t, e, sc, other, []string{"o1", "x-renamed"})
	_ = y
	_ = z
}

func assertOrder(t *testing.T, e *env, sc bookmarks.Scope, parent int64, want []string) {
	t.Helper()
	nodes, err := e.svc.Children(ctx, sc, parent, 1)
	noErr(t, err)
	var got []string
	for _, n := range nodes {
		got = append(got, n.Title)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestChildOrderMixedFoldersAndBookmarks(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	f1 := e.folder(sc, bar, "F1")
	b1 := e.bm(sc, "https://one.example/", "B1", bar)
	f2 := e.folder(sc, bar, "F2")
	b2 := e.bm(sc, "https://two.example/", "B2", bar)
	assertOrder(t, e, sc, bar, []string{"F1", "B1", "F2", "B2"})

	noErr(t, e.svc.SetChildOrder(ctx, sc, bar, []bookmarks.ChildRef{
		{Type: "bookmark", ID: b2.ID}, {Type: "folder", ID: f2}, {Type: "bookmark", ID: b1.ID}, {Type: "folder", ID: f1},
	}))
	assertOrder(t, e, sc, bar, []string{"B2", "F2", "B1", "F1"})
	e.reopen()
	sc = e.scopeAfterReopen(mustUser(t, e, "alice"))
	assertOrder(t, e, sc, bar, []string{"B2", "F2", "B1", "F1"})
}

func mustUser(t *testing.T, e *env, name string) identity.User {
	t.Helper()
	u, err := e.ids.GetUser(ctx, name)
	noErr(t, err)
	return u
}

func TestInvalidChildOrderIsRejectedAtomically(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	other := e.private(e.user("bob"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	f1 := e.folder(sc, bar, "F1")
	b1 := e.bm(sc, "https://one.example/", "B1", bar)
	f2 := e.folder(sc, bar, "F2")
	bobBar, _ := e.roots(other)
	bobF := e.folder(other, bobBar, "bob")
	elsewhere := e.folder(sc, f1, "nested")
	before := e.snapshot(sc)

	cases := map[string][]bookmarks.ChildRef{
		"duplicate":        {{Type: "folder", ID: f2}, {Type: "folder", ID: f2}},
		"unknown folder":   {{Type: "folder", ID: 987654}},
		"other users":      {{Type: "folder", ID: bobF}},
		"not a child":      {{Type: "folder", ID: elsewhere}},
		"wrong type":       {{Type: "folder", ID: b1.ID}},
		"bookmark as fold": {{Type: "bookmark", ID: f1}},
		"bad type":         {{Type: "link", ID: f1}},
		"valid then bad":   {{Type: "folder", ID: f2}, {Type: "folder", ID: 987654}},
	}
	for name, order := range cases {
		t.Run(name, func(t *testing.T) {
			is(t, e.svc.SetChildOrder(ctx, sc, bar, order), bookmarks.ErrInvalid)
			if got := e.snapshot(sc); got != before {
				t.Errorf("state changed after rejected order")
			}
		})
	}
	// order in the other user's folder is a 404, not a leak
	is(t, e.svc.SetChildOrder(ctx, sc, bobBar, nil), bookmarks.ErrNotFound)
}

func TestChildOrderAppendsUnlistedChildrenInExistingOrder(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	a := e.folder(sc, bar, "a")
	e.folder(sc, bar, "b")
	e.folder(sc, bar, "c")
	noErr(t, e.svc.SetChildOrder(ctx, sc, bar, []bookmarks.ChildRef{{Type: "folder", ID: a}}))
	assertOrder(t, e, sc, bar, []string{"a", "b", "c"})
}

func TestChildrenLayersSparseAndComplete(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	a := e.folder(sc, bar, "a")
	e.folder(sc, a, "empty")
	e.bm(sc, "https://x.example/", "x", a)

	full, err := e.svc.Children(ctx, sc, bar, -1)
	noErr(t, err)
	if len(full) != 1 || !full[0].Loaded || len(full[0].Children) != 2 {
		t.Fatalf("full = %+v", full)
	}
	if empty := full[0].Children[0]; empty.Type != "folder" || !empty.Loaded || empty.Children == nil || len(empty.Children) != 0 {
		t.Errorf("empty folder must be loaded with a non-nil empty children list: %+v", empty)
	}
	one, err := e.svc.Children(ctx, sc, bar, 1)
	noErr(t, err)
	if len(one) != 1 || one[0].Loaded || one[0].Children != nil {
		t.Errorf("layers=1 must not load grandchildren: %+v", one)
	}
	two, err := e.svc.Children(ctx, sc, bar, 2)
	noErr(t, err)
	if !two[0].Loaded || two[0].Children[0].Loaded {
		t.Errorf("layers=2 loads exactly two levels: %+v", two)
	}
	for _, bad := range []int{0, -2, 1001} {
		_, err := e.svc.Children(ctx, sc, bar, bad)
		is(t, err, bookmarks.ErrInvalid)
	}
}

func TestDeepTreeAndSparseEmptyTrees(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	empty, err := e.svc.Children(ctx, sc, other, -1)
	noErr(t, err)
	if empty == nil || len(empty) != 0 {
		t.Errorf("empty folder children = %#v, want empty non-nil", empty)
	}
	parent := bar
	const depth = 150
	for i := 0; i < depth; i++ {
		parent = e.folder(sc, parent, fmt.Sprintf("d%d", i))
	}
	e.bm(sc, "https://deep.example/", "deep", parent)
	nodes, err := e.svc.Children(ctx, sc, bar, -1)
	noErr(t, err)
	n := nodes[0]
	levels := 1
	for len(n.Children) > 0 && n.Children[0].Type == "folder" {
		n = n.Children[0]
		levels++
	}
	if levels != depth || len(n.Children) != 1 || n.Children[0].URL != "https://deep.example/" {
		t.Errorf("deep walk: levels=%d", levels)
	}
	h, err := e.svc.FolderHash(ctx, sc, bookmarks.RootID)
	noErr(t, err)
	if len(h) != 64 {
		t.Errorf("hash = %q", h)
	}
}

func TestDeleteFolderSoftDeletesSubtreeTransactionally(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	a := e.folder(sc, bar, "a")
	b := e.folder(sc, a, "b")
	e.bm(sc, "https://only-in-b.example/", "only", b)
	shared := e.bm(sc, "https://both.example/", "both", b, other)
	e.bm(sc, "https://keep.example/", "keep", bar)

	noErr(t, e.svc.DeleteFolder(ctx, sc, a))
	assertOrder(t, e, sc, bar, []string{"keep"})
	_, err := e.svc.GetFolder(ctx, sc, b)
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.Children(ctx, sc, a, 1)
	is(t, err, bookmarks.ErrNotFound)

	list, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1})
	noErr(t, err)
	urls := map[string][]int64{}
	for _, bm := range list {
		urls[bm.URL] = bm.Folders
	}
	if _, ok := urls["https://only-in-b.example/"]; ok {
		t.Error("bookmark whose only placement was trashed is still listed")
	}
	if f := urls["https://both.example/"]; len(f) != 1 || f[0] != other {
		t.Errorf("shared bookmark folders = %v, want [%d]", f, other)
	}
	got, err := e.svc.GetBookmark(ctx, sc, shared.ID)
	noErr(t, err)
	if len(got.Folders) != 1 {
		t.Errorf("folders = %v", got.Folders)
	}
	// trash keeps rows for later restore: they are soft deleted, not removed
	var n int
	noErr(t, e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM folders WHERE deleted_at IS NOT NULL`).Scan(&n))
	if n != 2 {
		t.Errorf("soft-deleted folders = %d, want 2", n)
	}
	noErr(t, e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE deleted_at IS NOT NULL`).Scan(&n))
	if n != 2 {
		t.Errorf("soft-deleted placements = %d, want 2", n)
	}
	noErr(t, e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM bookmarks WHERE deleted_at IS NOT NULL`).Scan(&n))
	if n != 1 {
		t.Errorf("trashed bookmark records = %d, want exactly the one whose only placement was trashed", n)
	}
	// deleting again is not found, never a panic or silent success
	is(t, e.svc.DeleteFolder(ctx, sc, a), bookmarks.ErrNotFound)
}
