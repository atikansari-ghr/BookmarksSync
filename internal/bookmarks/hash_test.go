package bookmarks_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// vectorNode mirrors testdata/hashvectors.json, produced by genhashvectors.mjs with
// real JS JSON.stringify + WebCrypto SHA-256 (the exact floccus v5.13.0 algorithm).
type vectorNode struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	URL      string       `json:"url"`
	Hash     string       `json:"hash"`
	Children []vectorNode `json:"children"`
}

func loadVectors(t *testing.T) vectorNode {
	t.Helper()
	raw, err := os.ReadFile("testdata/hashvectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var root vectorNode
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestVectorFixtureContainsTheHardCases(t *testing.T) {
	root := loadVectors(t)
	var titles []string
	var walk func(vectorNode)
	walk = func(n vectorNode) {
		titles = append(titles, n.Title+"\x00"+n.URL)
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	all := ""
	for _, s := range titles {
		all += s
	}
	for name, needle := range map[string]string{
		"real U+2028":                  "\xe2\x80\xa8",
		"real U+2029":                  "\xe2\x80\xa9",
		"literal backslash-u2028":      "\\u2028",
		"literal backslash-u2029":      "\\u2029",
		"double quote":                 "\"",
		"backslash":                    "\\\\",
		"angle brackets and ampersand": "<b>&amp;",
		"non-ascii":                    "日本",
		"control char":                 "\x01",
	} {
		if !contains(all, needle) {
			t.Errorf("fixture lacks %s", name)
		}
	}
	if root.Hash == "" || len(root.Hash) != 64 {
		t.Errorf("fixture root hash = %q", root.Hash)
	}
	// real and literal forms must hash differently (a blind unescape would conflate them)
	a := bookmarks.HashBookmark("x\xe2\x80\xa8", "https://e.example/")
	b := bookmarks.HashBookmark("x\\u2028", "https://e.example/")
	if a == b {
		t.Error("real U+2028 and literal backslash-u2028 hashed identically")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestHashMatchesJSVectorsForEveryNode(t *testing.T) {
	root := loadVectors(t)
	var check func(n vectorNode, isRoot bool) string
	check = func(n vectorNode, isRoot bool) string {
		var got string
		if n.Type == "bookmark" {
			got = bookmarks.HashBookmark(n.Title, n.URL)
		} else {
			var kids []string
			for _, c := range n.Children {
				kids = append(kids, check(c, false))
			}
			if isRoot {
				got = bookmarks.HashRoot(kids)
			} else {
				got = bookmarks.HashFolder(n.Title, kids)
			}
		}
		if got != n.Hash {
			t.Errorf("%s %q: hash %s, JS says %s", n.Type, n.Title, got, n.Hash)
		}
		return got
	}
	check(root, true)
}

func buildFromVectors(t *testing.T, e *env, sc bookmarks.Scope, parent int64, kids []vectorNode) {
	t.Helper()
	for _, k := range kids {
		if k.Type == "folder" {
			id := e.folder(sc, parent, k.Title)
			buildFromVectors(t, e, sc, id, k.Children)
		} else {
			e.bm(sc, k.URL, k.Title, parent)
		}
	}
}

func TestServiceFolderHashesMatchJSVectors(t *testing.T) {
	root := loadVectors(t)
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	// The canonical roots already exist; fill them from the fixture.
	bar, other := e.roots(sc)
	buildFromVectors(t, e, sc, bar, root.Children[0].Children)
	buildFromVectors(t, e, sc, other, root.Children[1].Children)

	got, err := e.svc.FolderHash(ctx, sc, bookmarks.RootID)
	noErr(t, err)
	if got != root.Hash {
		t.Errorf("root hash %s, JS %s", got, root.Hash)
	}
	gotBar, err := e.svc.FolderHash(ctx, sc, bar)
	noErr(t, err)
	if gotBar != root.Children[0].Hash {
		t.Errorf("bar hash %s, JS %s", gotBar, root.Children[0].Hash)
	}
	// every nested folder as well (non-root folders include their title)
	nested := root.Children[0].Children[9]
	nodes, _ := e.svc.Children(ctx, sc, bar, 1)
	var nestedID int64
	for _, n := range nodes {
		if n.Type == "folder" {
			nestedID = n.ID
		}
	}
	h, err := e.svc.FolderHash(ctx, sc, nestedID)
	noErr(t, err)
	if h != nested.Hash {
		t.Errorf("nested hash %s, JS %s", h, nested.Hash)
	}
}

func TestHashChangesWithOrderAndAnyDescendantChange(t *testing.T) {
	e := newEnv(t)
	sc := e.private(e.user("alice"), identity.ModeEdit)
	bar, _ := e.roots(sc)
	a := e.folder(sc, bar, "a")
	b1 := e.bm(sc, "https://1.example/", "1", a)
	b2 := e.bm(sc, "https://2.example/", "2", a)
	h0, _ := e.svc.FolderHash(ctx, sc, bookmarks.RootID)

	noErr(t, e.svc.SetChildOrder(ctx, sc, a, []bookmarks.ChildRef{{Type: "bookmark", ID: b2.ID}, {Type: "bookmark", ID: b1.ID}}))
	h1, _ := e.svc.FolderHash(ctx, sc, bookmarks.RootID)
	if h1 == h0 {
		t.Error("reordering did not change the root hash (stale hash would hide changes)")
	}
	_, err := e.svc.UpdateBookmark(ctx, sc, b1.ID, bookmarks.BookmarkUpdate{Title: ptr("1b")})
	noErr(t, err)
	h2, _ := e.svc.FolderHash(ctx, sc, bookmarks.RootID)
	if h2 == h1 {
		t.Error("retitle did not change the root hash")
	}
	// tags are not part of the sha256 title+url hash
	_, err = e.svc.UpdateBookmark(ctx, sc, b1.ID, bookmarks.BookmarkUpdate{Tags: &[]string{"t"}})
	noErr(t, err)
	h3, _ := e.svc.FolderHash(ctx, sc, bookmarks.RootID)
	if h3 != h2 {
		t.Error("tags must not influence the title+url hash")
	}
	noErr(t, e.svc.DeleteFolder(ctx, sc, a))
	h4, _ := e.svc.FolderHash(ctx, sc, bookmarks.RootID)
	if h4 == h3 {
		t.Error("trashing a folder did not change the root hash")
	}
}

func TestShareRootHashOmitsTitleLikeTheVirtualRoot(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	bar, _ := e.roots(as)
	shared := e.folder(as, bar, "Shared title")
	e.bm(as, "https://x.example/", "x", shared)
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeView, identity.ModeView)
	asRoot, err := e.svc.FolderHash(ctx, ss, bookmarks.RootID)
	noErr(t, err)
	want := bookmarks.HashRoot([]string{bookmarks.HashBookmark("x", "https://x.example/")})
	if asRoot != want {
		t.Errorf("share root hash %s, want %s", asRoot, want)
	}
	viaReal, _ := e.svc.FolderHash(ctx, ss, shared)
	if viaReal != want {
		t.Error("share root addressed by real id must hash as the root alias")
	}
	owner, _ := e.svc.FolderHash(ctx, as, shared)
	if owner == want {
		t.Error("the owner's view of that folder is a titled folder, not a root")
	}
}
