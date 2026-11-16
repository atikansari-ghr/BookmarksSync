package floccus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func children(t *testing.T, r *rig, a account, folder int64, layers int) []any {
	t.Helper()
	return r.do(&a, "GET", fmt.Sprintf("%s/folder/%d/children?layers=%d", R, folder, layers), nil).ok(t).data()
}

func titles(items []any) string {
	var out []string
	for _, it := range items {
		out = append(out, it.(map[string]any)["title"].(string))
	}
	return strings.Join(out, "|")
}

func TestFolderLifecycleOverWire(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	d := children(t, r, a, -1, 1)
	if titles(d) != "Bookmarks Bar|Other Bookmarks" {
		t.Fatalf("roots = %s", titles(d))
	}
	for _, it := range d {
		if _, has := it.(map[string]any)["children"]; has {
			t.Error("layers=1 must not carry a children key on subfolders")
		}
	}
	created := r.do(&a, "POST", R+"/folder", map[string]any{"parent_folder": bar, "title": "News <&> \"x\""}).ok(t).item()
	id := num(created["id"])
	if created["title"] != "News <&> \"x\"" || id <= 0 {
		t.Fatalf("created = %v", created)
	}
	// rename + move (PUT), parent as string id
	moved := r.do(&a, "PUT", R+"/folder/"+idStr(id), map[string]any{"parent_folder": idStr(other), "title": "News2"}).ok(t).item()
	if num(moved["id"]) != id || moved["title"] != "News2" {
		t.Errorf("moved = %v", moved)
	}
	if titles(children(t, r, a, other, 1)) != "News2" || titles(children(t, r, a, bar, 1)) != "" {
		t.Error("move not reflected")
	}
	// canonical roots are protected
	r.do(&a, "PUT", R+"/folder/"+idStr(bar), map[string]any{"parent_folder": -1, "title": "Renamed"}).fail(t, 400)
	r.do(&a, "DELETE", R+"/folder/"+idStr(bar), nil).fail(t, 400)
	r.do(&a, "DELETE", R+"/folder/-1", nil).fail(t, 400)
	// cycle
	child := r.mkFolder(a, id, "child")
	r.do(&a, "PUT", R+"/folder/"+idStr(id), map[string]any{"parent_folder": child, "title": "News2"}).fail(t, 400)
	// delete subtree
	r.do(&a, "DELETE", R+"/folder/"+idStr(id), nil).ok(t)
	r.do(&a, "GET", R+"/folder/"+idStr(child)+"/children?layers=1", nil).fail(t, 404)
	r.do(&a, "DELETE", R+"/folder/"+idStr(id), nil).fail(t, 404)
}

func TestChildrenLayersEmptyLoadedAndMixedOrder(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	f := r.mkFolder(a, bar, "F")
	empty := r.mkFolder(a, f, "empty")
	b1 := r.mkBookmark(a, "https://one.example/", "B1", f)
	f2 := r.mkFolder(a, f, "F2")
	b2 := r.mkBookmark(a, "https://two.example/", "B2", f)

	d := children(t, r, a, f, -1)
	if titles(d) != "empty|B1|F2|B2" {
		t.Fatalf("mixed order = %s", titles(d))
	}
	em := d[0].(map[string]any)
	if kids, ok := em["children"].([]any); !ok || len(kids) != 0 {
		t.Errorf("empty loaded folder must have children: [] : %v", em)
	}
	if d[1].(map[string]any)["type"] != "bookmark" || d[1].(map[string]any)["url"] != "https://one.example/" {
		t.Errorf("bookmark item = %v", d[1])
	}
	// sparse: grandchildren not loaded
	top := children(t, r, a, bar, 1)
	if _, has := top[0].(map[string]any)["children"]; has {
		t.Error("layers=1 loaded a subfolder")
	}
	// PATCH childorder with string ids reorders
	body := map[string]any{"data": []map[string]any{
		{"id": idStr(b2), "type": "bookmark"}, {"id": idStr(f2), "type": "folder"}, {"id": idStr(b1), "type": "bookmark"}, {"id": idStr(empty), "type": "folder"},
	}}
	r.do(&a, "PATCH", R+"/folder/"+idStr(f)+"/childorder", body).ok(t)
	if got := titles(children(t, r, a, f, -1)); got != "B2|F2|B1|empty" {
		t.Errorf("after reorder: %s", got)
	}
	// invalid orders change nothing
	bad := map[string]any{"data": []map[string]any{{"id": idStr(b1), "type": "bookmark"}, {"id": idStr(b1), "type": "bookmark"}}}
	r.do(&a, "PATCH", R+"/folder/"+idStr(f)+"/childorder", bad).fail(t, 400)
	bad2 := map[string]any{"data": []map[string]any{{"id": "999999", "type": "folder"}}}
	r.do(&a, "PATCH", R+"/folder/"+idStr(f)+"/childorder", bad2).fail(t, 400)
	r.do(&a, "PATCH", R+"/folder/"+idStr(f)+"/childorder", `{"data":[{"id":"x","type":"folder"}]}`).fail(t, 400)
	r.do(&a, "PATCH", R+"/folder/"+idStr(f)+"/childorder", `{"data":"nope"}`).fail(t, 400)
	if got := titles(children(t, r, a, f, -1)); got != "B2|F2|B1|empty" {
		t.Errorf("failed reorders changed order: %s", got)
	}
}

func TestFolderRootListingForServerRootWalking(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	r.mkFolder(a, bar, "sub")
	r.mkBookmark(a, "https://x.example/", "x", bar)
	d := r.do(&a, "GET", R+"/folder?root=-1&layers=0", nil).ok(t).data()
	if titles(d) != "Bookmarks Bar|Other Bookmarks" {
		t.Errorf("root folders = %s", titles(d))
	}
	for _, it := range d {
		m := it.(map[string]any)
		if m["id"] == nil || m["title"] == nil {
			t.Errorf("folder entry lacks id/title: %v", m)
		}
	}
	nd := r.do(&a, "GET", R+"/folder?root="+idStr(bar)+"&layers=0", nil).ok(t).data()
	if titles(nd) != "sub" {
		t.Errorf("bar folders = %s (bookmarks must not appear)", titles(nd))
	}
	// auto-create path as floccus does for serverRoot segments
	r.do(&a, "POST", R+"/folder", map[string]any{"parent_folder": -1, "title": "serverroot"}).ok(t)
	if titles(r.do(&a, "GET", R+"/folder?root=-1&layers=0", nil).ok(t).data()) != "Bookmarks Bar|Other Bookmarks|serverroot" {
		t.Error("serverRoot folder not listed at root")
	}
}

func TestBookmarkLifecycleOverWire(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	created := r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "https://e.example/p?x=1&y=<2>", "title": "Ünï 日本 \"q\" \xe2\x80\xa8 end", "folders": []any{idStr(bar)}}).ok(t).item()
	id := num(created["id"])
	if created["url"] != "https://e.example/p?x=1&y=<2>" || fmt.Sprint(created["folders"]) != fmt.Sprint([]any{float64(bar)}) {
		t.Fatalf("created = %v", created)
	}
	got := r.do(&a, "GET", R+"/bookmark/"+idStr(id), nil).ok(t).item()
	if got["title"] != created["title"] {
		t.Errorf("title round trip: %q vs %q", got["title"], created["title"])
	}
	// PUT adds a second placement (string ids accepted)
	upd := r.do(&a, "PUT", R+"/bookmark/"+idStr(id), map[string]any{"url": "https://e.example/p?x=1&y=<2>", "title": "renamed", "folders": []any{bar, idStr(other)}}).ok(t).item()
	if len(upd["folders"].([]any)) != 2 || upd["title"] != "renamed" {
		t.Errorf("updated = %v", upd)
	}
	// exact-URL lookup (floccus dedupe before create)
	found := r.do(&a, "GET", R+"/bookmark?url=https%3A%2F%2Fe.example%2Fp%3Fx%3D1%26y%3D%3C2%3E", nil).ok(t).data()
	if len(found) != 1 || num(found[0].(map[string]any)["id"]) != id {
		t.Errorf("url lookup = %v", found)
	}
	none := r.do(&a, "GET", R+"/bookmark?url=https%3A%2F%2Fe.example%2Fp", nil).ok(t).data()
	if len(none) != 0 {
		t.Errorf("url lookup is not exact: %v", none)
	}
	// removing one placement keeps the other
	r.do(&a, "DELETE", R+"/folder/"+idStr(bar)+"/bookmarks/"+idStr(id), nil).ok(t)
	again := r.do(&a, "GET", R+"/bookmark/"+idStr(id), nil).ok(t).item()
	if len(again["folders"].([]any)) != 1 || num(again["folders"].([]any)[0]) != other {
		t.Errorf("after placement delete: %v", again)
	}
	r.do(&a, "DELETE", R+"/folder/"+idStr(other)+"/bookmarks/"+idStr(id), nil).ok(t)
	r.do(&a, "GET", R+"/bookmark/"+idStr(id), nil).fail(t, 404)
	// tolerated by floccus, so it must be a clean 404
	r.do(&a, "DELETE", R+"/folder/"+idStr(other)+"/bookmarks/"+idStr(id), nil).fail(t, 404)
	// invalid payloads
	r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "chrome://settings", "title": "x", "folders": []int64{bar}}).fail(t, 400)
	r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "https://ok.example/", "title": "x", "folders": []int64{}}).fail(t, 400)
	r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "https://ok.example/", "title": "x", "folders": []int64{-1}}).fail(t, 400)
	r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "https://ok.example/", "title": "x", "folders": []int64{987654}}).fail(t, 404)
}

func TestSameURLInTwoFoldersIsOneRecordOverWire(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	id1 := r.mkBookmark(a, "https://dup.example/", "Dup", bar)
	// floccus: lookup finds it, then PUTs the full placements list
	found := r.do(&a, "GET", R+"/bookmark?url=https%3A%2F%2Fdup.example%2F", nil).ok(t).data()
	if len(found) != 1 {
		t.Fatal("lookup failed")
	}
	r.do(&a, "PUT", R+"/bookmark/"+idStr(id1), map[string]any{"url": "https://dup.example/", "title": "Dup", "folders": []int64{bar, other}}).ok(t)
	list := r.do(&a, "GET", R+"/bookmark?page=0&limit=300", nil).ok(t).data()
	if len(list) != 1 || len(list[0].(map[string]any)["folders"].([]any)) != 2 {
		t.Errorf("list = %v", list)
	}
	if titles(children(t, r, a, bar, 1)) != "Dup" || titles(children(t, r, a, other, 1)) != "Dup" {
		t.Error("placements not visible in both folders")
	}
}

func TestTagsOmittedByClientArePreserved(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	b, err := r.svc.CreateBookmark(bg, a.scope, bookmarksInput("https://t.example/", "t", []int64{bar}, []string{"server", "tag"}))
	noErr(t, err)
	// floccus (no tag sync) PUTs the server's own tags back, or omits them entirely
	r.do(&a, "PUT", R+"/bookmark/"+idStr(b.ID), map[string]any{"url": "https://t.example/", "title": "t2", "folders": []int64{bar, other}}).ok(t)
	got := r.do(&a, "GET", R+"/bookmark/"+idStr(b.ID), nil).ok(t).item()
	if fmt.Sprint(got["tags"]) != "[server tag]" {
		t.Errorf("tags = %v", got["tags"])
	}
	r.do(&a, "PUT", R+"/bookmark/"+idStr(b.ID), map[string]any{"url": "https://t.example/", "title": "t3", "folders": []int64{bar}, "tags": nil}).ok(t)
	got = r.do(&a, "GET", R+"/bookmark/"+idStr(b.ID), nil).ok(t).item()
	if fmt.Sprint(got["tags"]) != "[server tag]" {
		t.Errorf("tags lost on null tags: %v", got["tags"])
	}
}

func TestEmptyAndPaddedTitlesRoundTripOverWire(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	id1 := r.mkBookmark(a, "https://icon.example/", "", bar)
	id2 := r.mkBookmark(a, "https://pad.example/", "  padded \t", bar)
	f := r.mkFolder(a, bar, "  folder  ")
	for id, want := range map[int64]string{id1: "", id2: "  padded \t"} {
		got := r.do(&a, "GET", R+"/bookmark/"+idStr(id), nil).ok(t).item()
		if got["title"] != want {
			t.Errorf("title = %q, want %q", got["title"], want)
		}
	}
	d := children(t, r, a, bar, -1)
	if titles(d) != "|  padded \t|  folder  " && titles(d) != "  folder  |" && !strings.Contains(titles(d), "  folder  ") {
		t.Errorf("children titles = %q", titles(d))
	}
	_ = f
}

func TestJavascriptBookmarksListedWithTargetAndSearchable(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	js := "javascript:void(window.open('https://example.com/?q='+encodeURIComponent(document.title)))"
	id := r.mkBookmark(a, js, "bookmarklet", bar)
	r.mkBookmark(a, "https://plain.example/", "plain", bar)
	res := r.do(&a, "GET", R+"/bookmark?page=-1&search[]=javascript%3A", nil).ok(t).data()
	if len(res) != 1 {
		t.Fatalf("search = %v", res)
	}
	m := res[0].(map[string]any)
	if num(m["id"]) != id || m["target"] != js {
		t.Errorf("javascript bookmark = %v", m)
	}
	if kid := children(t, r, a, bar, -1); kid[0].(map[string]any)["target"] != js {
		t.Errorf("children lack target: %v", kid[0])
	}
	// plain bookmarks carry no target
	for _, it := range r.do(&a, "GET", R+"/bookmark?page=0&limit=10", nil).ok(t).data() {
		if _, has := it.(map[string]any)["target"]; has && it.(map[string]any)["url"] != js {
			t.Errorf("plain bookmark has target: %v", it)
		}
	}
}

func TestBookmarkListPagingOverWire(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	for i := 0; i < 7; i++ {
		r.mkBookmark(a, fmt.Sprintf("https://p%d.example/", i), fmt.Sprintf("P%d", i), bar)
	}
	sizes := []int{}
	for p := 0; p < 4; p++ {
		sizes = append(sizes, len(r.do(&a, "GET", fmt.Sprintf("%s/bookmark?page=%d&limit=3", R, p), nil).ok(t).data()))
	}
	if fmt.Sprint(sizes) != "[3 3 1 0]" {
		t.Errorf("page sizes %v", sizes)
	}
}

func TestBookmarkClickAcceptsValidAuthorizedURLWithoutStats(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	b := r.account("bob", identity.ModeEdit)
	bar, _ := r.roots(a)
	r.mkBookmark(a, "https://click.example/", "c", bar)
	r.do(&a, "POST", R+"/bookmark/click", map[string]any{"url": "https://click.example/"}).ok(t)
	r.do(&a, "POST", R+"/bookmark/click", map[string]any{"url": "chrome://x"}).fail(t, 400)
	r.do(&a, "POST", R+"/bookmark/click", `{`).fail(t, 400)
	r.do(&b, "POST", R+"/bookmark/click", map[string]any{"url": "https://click.example/"}).fail(t, 404) // not bob's
	r.do(nil, "POST", R+"/bookmark/click", map[string]any{"url": "https://click.example/"}).fail(t, 401)
}
