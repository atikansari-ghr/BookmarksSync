package bookmarks_test

import (
	"fmt"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func leaf(title, url string) bookmarks.ImportNode {
	return bookmarks.ImportNode{Title: title, URL: url}
}
func dir(title string, kids ...bookmarks.ImportNode) bookmarks.ImportNode {
	return bookmarks.ImportNode{Folder: true, Title: title, Children: kids}
}

func TestImportTreeCreatesNestedOrderedItemsAndReturnsOnlyImported(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	existing := e.bm(sc, "https://existing.example/", "existing", bar)

	out, err := e.svc.ImportTree(ctx, sc, bar, []bookmarks.ImportNode{
		leaf("one", "https://one.example/"),
		dir("sub", leaf("two", "https://two.example/"), dir("subsub", leaf("three", "https://three.example/")), dir("empty")),
		leaf("last", "https://last.example/"),
	})
	noErr(t, err)
	if len(out) != 3 || out[0].Title != "one" || out[1].Title != "sub" || out[2].Title != "last" {
		t.Fatalf("returned = %+v", out)
	}
	if out[1].Children == nil || len(out[1].Children) != 3 || out[1].Children[1].Children[0].URL != "https://three.example/" {
		t.Errorf("nested = %+v", out[1])
	}
	if out[1].Children[2].Children == nil {
		t.Error("empty imported folder must carry an empty (non-nil) children list")
	}
	// appended after existing content, in order, nothing duplicated
	assertOrder(t, e, sc, bar, []string{"existing", "one", "sub", "last"})
	for _, n := range out {
		if n.ID <= 0 {
			t.Errorf("imported node without id: %+v", n)
		}
	}
	// a second import appends again; an already-present URL keeps its id and is not duplicated
	out2, err := e.svc.ImportTree(ctx, sc, bar, []bookmarks.ImportNode{leaf("existing", "https://existing.example/"), leaf("new", "https://new.example/")})
	noErr(t, err)
	if len(out2) != 1 || out2[0].Title != "new" {
		// the existing placement in this folder is skipped, not re-created or re-ordered
		t.Fatalf("second import returned %+v", out2)
	}
	assertOrder(t, e, sc, bar, []string{"existing", "one", "sub", "last", "new"})
	got, _ := e.svc.GetBookmark(ctx, sc, existing.ID)
	if len(got.Folders) != 1 {
		t.Errorf("existing bookmark folders = %v", got.Folders)
	}
}

func TestImportReusesRecordWhenURLLivesInAnotherFolder(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, other := e.roots(sc)
	first := e.bm(sc, "https://x.example/", "x", other)
	out, err := e.svc.ImportTree(ctx, sc, bar, []bookmarks.ImportNode{leaf("x", "https://x.example/")})
	noErr(t, err)
	if len(out) != 1 || out[0].ID != first.ID {
		t.Fatalf("out = %+v, want existing id %d", out, first.ID)
	}
	got, _ := e.svc.GetBookmark(ctx, sc, first.ID)
	if len(got.Folders) != 2 {
		t.Errorf("placements = %v", got.Folders)
	}
}

func TestImportDeduplicatesSameURLWithinOneFolder(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	out, err := e.svc.ImportTree(ctx, sc, bar, []bookmarks.ImportNode{leaf("a", "https://d.example/"), leaf("a again", "https://d.example/"), leaf("b", "https://b.example/")})
	noErr(t, err)
	if len(out) != 2 {
		t.Errorf("returned %d, want duplicate dropped: %+v", len(out), out)
	}
	assertOrder(t, e, sc, bar, []string{"a", "b"})
}

func TestImportFailureLeavesNothingBehind(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	e.bm(sc, "https://keep.example/", "keep", bar)
	before := e.snapshot(sc)

	bad := map[string][]bookmarks.ImportNode{
		"bad scheme deep in tree": {leaf("ok", "https://ok.example/"), dir("d", leaf("fine", "https://fine.example/"), dir("e", leaf("bad", "chrome://settings")))},
		"empty url":               {dir("d", leaf("ok", "https://ok.example/")), leaf("no url", "")},
		"too many nodes":          manyLeaves(bookmarks.MaxImportNodes + 1),
		"too deep":                deepDirs(bookmarks.MaxImportDepth + 1),
	}
	for name, nodes := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.ImportTree(ctx, sc, bar, nodes)
			is(t, err, bookmarks.ErrInvalid)
			if got := e.snapshot(sc); got != before {
				t.Errorf("failed import wrote data")
			}
		})
	}
	var folders int
	noErr(t, e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM folders`).Scan(&folders))
	if folders != 2 {
		t.Errorf("folders table has %d rows after failed imports (orphans), want 2", folders)
	}
	_, err := e.svc.ImportTree(ctx, sc, bookmarks.RootID, []bookmarks.ImportNode{leaf("x", "https://x.example/")})
	is(t, err, bookmarks.ErrInvalid) // bookmarks cannot land in the private root
	// folders may be imported at the root though
	out, err := e.svc.ImportTree(ctx, sc, bookmarks.RootID, []bookmarks.ImportNode{dir("Top")})
	noErr(t, err)
	if len(out) != 1 {
		t.Errorf("root folder import = %+v", out)
	}
}

func manyLeaves(n int) []bookmarks.ImportNode {
	var out []bookmarks.ImportNode
	for i := 0; i < n; i++ {
		out = append(out, leaf("t", fmt.Sprintf("https://n%d.example/", i)))
	}
	return out
}

func deepDirs(depth int) []bookmarks.ImportNode {
	n := dir("leafdir")
	for i := 0; i < depth; i++ {
		n = dir(fmt.Sprintf("d%d", i), n)
	}
	return []bookmarks.ImportNode{n}
}

func TestImportHonoursScopeAndReadOnly(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	out, err := e.svc.ImportTree(ctx, ss, bookmarks.RootID, []bookmarks.ImportNode{leaf("in", "https://in.example/")})
	noErr(t, err)
	if len(out) != 1 {
		t.Fatal("import into share failed")
	}
	_, err = e.svc.ImportTree(ctx, ss, aOther, []bookmarks.ImportNode{leaf("out", "https://out.example/")})
	is(t, err, bookmarks.ErrNotFound)
	got, _ := e.svc.Children(ctx, as, shared, -1)
	if len(got) != 1 || got[0].Title != "in" {
		t.Errorf("owner sees %+v", got)
	}
}
