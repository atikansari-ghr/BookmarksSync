package floccus_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// The Floccus wire never carries a positive concrete folder id: internal folder n is wire -(n+1),
// -1 is the virtual root, bookmark ids stay positive. These tests pin every folder-valued field.

func wireOf(internal int64) int64  { return -(internal + 1) }
func internalOf(wire int64) int64  { return -wire - 1 }
func fid(wire int64) string        { return idStr(wire) }
func folderPath(wire int64) string { return R + "/folder/" + fid(wire) }
func asMap(v any) map[string]any   { m, _ := v.(map[string]any); return m }
func asList(v any) []any           { l, _ := v.([]any); return l }
func isNeg(n int64) bool           { return n < 0 }
func positiveFolderRefs(raw string) bool {
	return regexp.MustCompile(`"(parent_folder|folders)":\s*\[?\s*[0-9]`).MatchString(raw)
}

// walkFolders visits every folder node of a children/tree listing.
func walkFolders(items []any, visit func(m map[string]any)) {
	for _, it := range items {
		m := asMap(it)
		if m["type"] == nil || m["type"] == "folder" {
			visit(m)
		}
		walkFolders(asList(m["children"]), visit)
	}
}

func TestCanonicalRootsAndNestedFoldersAreNegativeOnTheWire(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	rootItem := r.do(&a, "GET", R+"/folder/-1", nil).ok(t).item()
	if num(rootItem["id"]) != -1 || num(rootItem["parent_folder"]) != 0 {
		t.Fatalf("virtual root item = %v, want id -1 and parent_folder 0", rootItem)
	}

	top := r.do(&a, "GET", R+"/folder/-1/children?layers=1", nil).ok(t).data()
	if len(top) != 2 || num(asMap(top[0])["id"]) != -2 || num(asMap(top[1])["id"]) != -3 {
		t.Fatalf("canonical roots must be -2 and -3, got %v", top)
	}
	bar, other := num(asMap(top[0])["id"]), num(asMap(top[1])["id"])

	nested := r.do(&a, "POST", R+"/folder", map[string]any{"parent_folder": bar, "title": "News"}).ok(t).item()
	nid := num(nested["id"])
	if !isNeg(nid) || nid >= -3 {
		t.Fatalf("a new folder must get a negative wire id below the roots, got %d", nid)
	}
	if num(nested["parent_folder"]) != bar {
		t.Errorf("parent_folder = %v, want %d", nested["parent_folder"], bar)
	}
	// the service row is the positive internal one
	row, err := r.svc.GetFolder(bg, a.scope, internalOf(nid))
	if err != nil || row.Title != "News" || row.ParentID != internalOf(bar) {
		t.Fatalf("internal row = %+v, %v (wire %d -> internal %d)", row, err, nid, internalOf(nid))
	}
	deep := r.mkFolder(a, nid, "Deeper")

	for _, p := range []string{"/folder/-1/children?layers=-1", "/folder?layers=-1", "/folder?root=-1&layers=-1"} {
		res := r.do(&a, "GET", R+p, nil).ok(t)
		seen := 0
		walkFolders(res.data(), func(m map[string]any) {
			seen++
			if id := num(m["id"]); id >= -1 {
				t.Errorf("%s: folder %v has id %d (not negative wire)", p, m["title"], id)
			}
		})
		if seen < 4 {
			t.Errorf("%s: saw %d folders", p, seen)
		}
	}
	// per-folder views
	gi := r.do(&a, "GET", folderPath(nid), nil).ok(t).item()
	if num(gi["id"]) != nid || num(gi["parent_folder"]) != bar {
		t.Errorf("GET folder = %v", gi)
	}
	ch := r.do(&a, "GET", folderPath(bar)+"/children?layers=1", nil).ok(t).data()
	if len(ch) != 1 || num(asMap(ch[0])["id"]) != nid {
		t.Errorf("children of the bar = %v", ch)
	}
	_ = other
	_ = deep
}

func TestBookmarkIDsStayPositiveFolderIDsAreWire(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	f := r.mkFolder(a, bar, "F")
	b1 := r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "https://one.example/", "title": "one", "folders": []int64{f}}).ok(t).item()
	if num(b1["id"]) <= 0 {
		t.Fatalf("bookmark id must stay positive: %v", b1)
	}
	if fmt.Sprint(b1["folders"]) != fmt.Sprintf("[%d]", f) {
		t.Errorf("folders = %v, want [%d]", b1["folders"], f)
	}
	// string form accepted
	b2 := r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "https://two.example/", "title": "two", "folders": []string{fid(other)}}).ok(t).item()
	if fmt.Sprint(b2["folders"]) != fmt.Sprintf("[%d]", other) {
		t.Errorf("folders = %v, want [%d]", b2["folders"], other)
	}
	// the stored placement is the internal folder
	got, err := r.svc.GetBookmark(bg, a.scope, num(b1["id"]))
	if err != nil || len(got.Folders) != 1 || got.Folders[0] != internalOf(f) {
		t.Fatalf("service view = %+v, %v", got, err)
	}
	// PUT folders both ways
	up := r.do(&a, "PUT", R+"/bookmark/"+idStr(num(b1["id"])), map[string]any{"folders": []int64{f, other}}).ok(t).item()
	if fmt.Sprint(up["folders"]) != fmt.Sprintf("[%d %d]", f, other) && fmt.Sprint(up["folders"]) != fmt.Sprintf("[%d %d]", other, f) {
		t.Errorf("PUT folders = %v", up["folders"])
	}
	// lists carry wire folders too, ids positive
	list := r.do(&a, "GET", R+"/bookmark?page=-1", nil).ok(t)
	for _, it := range list.data() {
		m := asMap(it)
		if num(m["id"]) <= 0 {
			t.Errorf("bookmark id %v", m["id"])
		}
		for _, fv := range asList(m["folders"]) {
			if num(fv) >= -1 {
				t.Errorf("folders entry %v is not a wire folder id", fv)
			}
		}
	}
	// tree items: bookmarks positive, folders negative
	walk := r.do(&a, "GET", folderPath(f)+"/children?layers=-1", nil).ok(t).data()
	for _, it := range walk {
		m := asMap(it)
		if m["type"] == "bookmark" && num(m["id"]) <= 0 {
			t.Errorf("bookmark node id %v", m["id"])
		}
	}
	// remove one placement: wire folder + positive bookmark in the path
	r.do(&a, "DELETE", folderPath(other)+"/bookmarks/"+idStr(num(b2["id"])), nil).ok(t)
}

func TestFolderIDsInEveryPathAndFieldRejectOldPositiveForm(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	before := r.snapshot(a)
	bad := []string{"1", "2", "7", "0", "-0", "+2", "-02", "-1.5", "-1e3", "abc", "-", "1;2", "-9223372036854775808", "-99999999999999999999"}
	for _, id := range bad {
		for _, c := range []struct{ method, path string }{
			{"GET", "/folder/" + id},
			{"GET", "/folder/" + id + "/children?layers=1"},
			{"GET", "/folder/" + id + "/hash?hashFn=sha256"},
			{"PUT", "/folder/" + id},
			{"DELETE", "/folder/" + id},
			{"PATCH", "/folder/" + id + "/childorder"},
			{"POST", "/folder/" + id + "/import"},
			{"DELETE", "/folder/" + id + "/bookmarks/1"},
			{"GET", "/folder?root=" + id + "&layers=1"},
		} {
			if strings.Contains(id, ";") && strings.Contains(c.path, "?root=") {
				continue // net/http drops a query pair containing ';' before the handler sees it (unchanged behavior)
			}
			var body any
			if c.method == "PUT" {
				body = map[string]any{"title": "x"}
			}
			if c.method == "PATCH" {
				body = map[string]any{"data": []any{}}
			}
			res := r.do(&a, c.method, R+c.path, body)
			if res.status != 400 && res.status != 404 && res.status != 405 || res.body["status"] != "error" {
				t.Errorf("%s %s -> %d %s", c.method, c.path, res.status, res.raw)
			}
		}
		// JSON fields
		for _, in := range []struct {
			path string
			body map[string]any
		}{
			{"/folder", map[string]any{"parent_folder": asJSONID(id), "title": "x"}},
			{"/bookmark", map[string]any{"url": "https://bad.example/", "title": "b", "folders": []any{asJSONID(id)}}},
		} {
			res := r.do(&a, "POST", R+in.path, in.body)
			if res.status != 400 || res.body["status"] != "error" {
				t.Errorf("POST %s with folder %q -> %d %s", in.path, id, res.status, res.raw)
			}
		}
		res := r.do(&a, "PATCH", folderPath(bar)+"/childorder", map[string]any{"data": []any{map[string]any{"id": asJSONID(id), "type": "folder"}}})
		if res.status != 400 {
			t.Errorf("childorder with folder %q -> %d %s", id, res.status, res.raw)
		}
	}
	if after := r.snapshot(a); after != before {
		t.Errorf("an invalid folder id mutated data:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// asJSONID returns the value as floccus might send it: numeric when it looks like an integer, else a string.
func asJSONID(id string) any {
	var n int64
	if _, err := fmt.Sscanf(id, "%d", &n); err == nil && fmt.Sprint(n) == id {
		return n
	}
	return id
}

// snapshot is a stable dump of the whole account tree seen through the service.
func (r *rig) snapshot(a account) string {
	r.t.Helper()
	ns, err := r.svc.Children(bg, a.scope, bookmarks.RootID, -1)
	if err != nil {
		r.t.Fatal(err)
	}
	var sb strings.Builder
	var walk func(ns []bookmarks.Node, d int)
	walk = func(ns []bookmarks.Node, d int) {
		for _, n := range ns {
			fmt.Fprintf(&sb, "%d|%s|%d|%s|%s\n", d, n.Type, n.ID, n.Title, n.URL)
			walk(n.Children, d+1)
		}
	}
	walk(ns, 0)
	return sb.String()
}

func TestFolderLifecycleUsesWireIDsEndToEnd(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	f := r.mkFolder(a, bar, "Move me")
	moved := r.do(&a, "PUT", folderPath(f), map[string]any{"parent_folder": fid(other), "title": "Moved"}).ok(t).item()
	if num(moved["id"]) != f || num(moved["parent_folder"]) != other {
		t.Errorf("moved = %v", moved)
	}
	row, _ := r.svc.GetFolder(bg, a.scope, internalOf(f))
	if row.ParentID != internalOf(other) || row.Title != "Moved" {
		t.Errorf("service row = %+v", row)
	}
	// roots protected, root sentinel is not a concrete folder
	r.do(&a, "DELETE", folderPath(bar), nil).fail(t, 400)
	r.do(&a, "DELETE", R+"/folder/-1", nil).fail(t, 400)
	// hash by wire id and for the root
	h := r.do(&a, "GET", folderPath(f)+"/hash?hashFn=sha256", nil).ok(t).body["data"]
	if s, _ := h.(string); len(s) != 64 {
		t.Errorf("hash = %v", h)
	}
	if s, _ := r.do(&a, "GET", R+"/folder/-1/hash?hashFn=sha256", nil).ok(t).body["data"].(string); len(s) != 64 {
		t.Errorf("root hash = %v", s)
	}
	// delete by wire id: gone through both views
	r.do(&a, "DELETE", folderPath(f), nil).ok(t)
	r.do(&a, "GET", folderPath(f), nil).fail(t, 404)
	if _, err := r.svc.GetFolder(bg, a.scope, internalOf(f)); err == nil {
		t.Error("the internal row is still visible after a wire delete")
	}
	// a wire id that never existed is a 404, not a 400
	r.do(&a, "GET", folderPath(wireOf(99999))+"/children?layers=1", nil).fail(t, 404)
}

func TestChildOrderMapsFolderEntriesOnlyAndKeepsBookmarkIDs(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	f1 := r.mkFolder(a, bar, "F1")
	f2 := r.mkFolder(a, bar, "F2")
	b := r.mkBookmark(a, "https://order.example/", "B", bar)
	order := []any{
		map[string]any{"id": b, "type": "bookmark"},
		map[string]any{"id": fid(f2), "type": "folder"},
		map[string]any{"id": f1, "type": "folder"},
	}
	r.do(&a, "PATCH", folderPath(bar)+"/childorder", map[string]any{"data": order}).ok(t)
	got := children(t, r, a, bar, 1)
	if titles(got) != "B|F2|F1" {
		t.Fatalf("order = %s", titles(got))
	}
	// the bookmark keeps its positive id; folders are wire
	for _, it := range got {
		m := asMap(it)
		if m["type"] == "bookmark" && num(m["id"]) != b {
			t.Errorf("bookmark id changed: %v", m)
		}
		if m["type"] == "folder" && num(m["id"]) >= -1 {
			t.Errorf("folder id not wire: %v", m)
		}
	}
	bad := []map[string]any{
		{"id": internalOf(f1), "type": "folder"}, // old positive folder form
		{"id": -b - 1, "type": "bookmark"},       // a wire-style negative for a bookmark
		{"id": b, "type": "folder"},              // bookmark id with the folder type
		{"id": fid(f1), "type": "bookmark"},      // folder id with the bookmark type
		{"id": -1, "type": "folder"},             // the root is not a child
	}
	foreign := r.mkFolder(a, other, "Foreign")
	before := r.snapshot(a)
	for i, e := range bad {
		res := r.do(&a, "PATCH", folderPath(bar)+"/childorder", map[string]any{"data": []any{e}})
		if res.status != 400 && res.status != 404 {
			t.Errorf("bad entry %d %v -> %d %s", i, e, res.status, res.raw)
		}
	}
	for name, order := range map[string][]any{
		"foreign folder":   {map[string]any{"id": fid(foreign), "type": "folder"}},
		"duplicate folder": {map[string]any{"id": fid(f1), "type": "folder"}, map[string]any{"id": fid(f1), "type": "folder"}},
	} {
		res := r.do(&a, "PATCH", folderPath(bar)+"/childorder", map[string]any{"data": order})
		if res.status != 400 || strings.Contains(res.raw, fmt.Sprint(internalOf(foreign))) || strings.Contains(res.raw, fmt.Sprint(internalOf(f1))) {
			t.Errorf("%s childorder response exposes internal folder id: %d %s", name, res.status, res.raw)
		}
	}
	if after := r.snapshot(a); after != before {
		t.Errorf("rejected childorder changed data")
	}
}

func TestImportUsesWireFolderPathAndReturnsWireFolderIDs(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	html := `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p><DT><H3 ID="6">Sub</H3><DL><p><DT><A HREF="https://imp.example/" ID="3">Imp</A></DL><p><DT><A HREF="https://top.example/">Top</A></DL>`
	res := r.multipart(&a, folderPath(bar)+"/import", "bm_import", "x.html", html).ok(t)
	seen := 0
	var visit func(items []any)
	visit = func(items []any) {
		for _, it := range items {
			m := asMap(it)
			seen++
			id := num(m["id"])
			if m["type"] == "folder" && id >= -1 {
				t.Errorf("imported folder %v has non-wire id %d", m["title"], id)
			}
			if m["type"] == "bookmark" && id <= 0 {
				t.Errorf("imported bookmark %v has id %d", m["title"], id)
			}
			visit(asList(m["children"]))
		}
	}
	visit(res.data())
	if seen != 3 {
		t.Errorf("imported %d nodes: %s", seen, res.raw)
	}
	// positive folder path never reaches the import
	before := r.snapshot(a)
	r.multipart(&a, R+"/folder/1/import", "bm_import", "x.html", html).fail(t, 400)
	if r.snapshot(a) != before {
		t.Error("import into an invalid folder mutated data")
	}
}

func TestShareScopeWireIDsStayIsolatedAndAliasTheRoot(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, other := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	sub := r.mkFolder(alice, shared, "Sub")
	secret := r.mkFolder(alice, other, "Secret")
	r.mkBookmark(alice, "https://s.example/", "s", sub)
	bc := r.shareAccount(alice, internalOf(shared), bob, identity.ModeEdit, identity.ModeEdit)
	shareRootItem := r.do(&bc, "GET", R+"/folder/-1", nil).ok(t).item()
	if num(shareRootItem["id"]) != -1 || num(shareRootItem["parent_folder"]) != 0 {
		t.Fatalf("share root item = %v, want id -1 and parent_folder 0", shareRootItem)
	}

	// the share root is -1; its children are wire; the real folder wire id is an accepted alias
	root := r.do(&bc, "GET", R+"/folder/-1/children?layers=-1", nil).ok(t).data()
	if titles(root) != "Sub" || num(asMap(root[0])["id"]) != sub {
		t.Fatalf("share root = %v", root)
	}
	alias := r.do(&bc, "GET", folderPath(shared)+"/children?layers=1", nil).ok(t).data()
	if titles(alias) != "Sub" {
		t.Errorf("alias children = %v", alias)
	}
	// out-of-scope wire ids are 404 for reads and writes
	for _, id := range []int64{bar, other, secret} {
		r.do(&bc, "GET", folderPath(id)+"/children?layers=1", nil).fail(t, 404)
		r.do(&bc, "POST", R+"/folder", map[string]any{"parent_folder": id, "title": "x"}).fail(t, 404)
		r.do(&bc, "POST", R+"/bookmark", map[string]any{"url": "https://leak.example/", "title": "x", "folders": []int64{id}}).fail(t, 404)
	}
	// bookmark placed directly in the share reports the root sentinel, not a positive id
	b := r.do(&bc, "POST", R+"/bookmark", map[string]any{"url": "https://direct.example/", "title": "d", "folders": []int64{-1}}).ok(t).item()
	if fmt.Sprint(b["folders"]) != "[-1]" {
		t.Errorf("direct placement folders = %v", b["folders"])
	}
	nested := r.do(&bc, "POST", R+"/bookmark", map[string]any{"url": "https://nested.example/", "title": "n", "folders": []int64{sub}}).ok(t).item()
	if fmt.Sprint(nested["folders"]) != fmt.Sprintf("[%d]", sub) {
		t.Errorf("nested placement folders = %v", nested["folders"])
	}
	raw := r.do(&bc, "GET", R+"/bookmark?page=-1", nil).raw
	if positiveFolderRefs(raw) {
		t.Errorf("a positive folder id leaked in the share listing: %s", raw)
	}
}

func TestNoPositiveFolderIDAnywhereInAnyFolderResponse(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	f := r.mkFolder(a, bar, "F")
	r.mkBookmark(a, "https://leak.example/", "L", f, other)
	for _, c := range []struct{ method, path string }{
		{"GET", "/folder/-1/children?layers=-1"}, {"GET", "/folder?layers=-1"}, {"GET", "/bookmark?page=-1"},
		{"GET", "/folder/" + fid(f)}, {"GET", "/folder/" + fid(bar) + "/children?layers=1"},
	} {
		raw := r.do(&a, c.method, R+c.path, nil).raw
		for _, m := range regexp.MustCompile(`"(?:parent_folder|folders)":\s*\[?\s*(\d+)`).FindAllStringSubmatch(raw, -1) {
			t.Errorf("%s %s leaks positive folder id %s: %s", c.method, c.path, m[1], raw)
		}
	}
	// folder nodes in listings: every "type":"folder" has a negative id
	raw := r.do(&a, "GET", R+"/folder/-1/children?layers=-1", nil).raw
	for _, m := range regexp.MustCompile(`"id":(\d+),"title":"[^"]*","type":"folder"|"type":"folder","id":(\d+)`).FindAllStringSubmatch(raw, -1) {
		t.Errorf("positive folder node id in %s", raw)
		_ = m
	}
}
