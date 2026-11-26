package floccus_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

type vnode struct {
	Type     string  `json:"type"`
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Hash     string  `json:"hash"`
	Children []vnode `json:"children"`
}

func loadVectors(t *testing.T) vnode {
	t.Helper()
	raw, err := os.ReadFile("../bookmarks/testdata/hashvectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vnode
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (r *rig) build(a account, parent int64, kids []vnode) {
	for _, k := range kids {
		if k.Type == "folder" {
			id := r.mkFolder(a, parent, k.Title)
			r.build(a, id, k.Children)
		} else {
			r.mkBookmark(a, k.URL, k.Title, parent)
		}
	}
}

func hashOf(t *testing.T, r *rig, a account, id string) string {
	t.Helper()
	res := r.do(&a, "GET", R+"/folder/"+id+"/hash?hashFn=sha256", nil).ok(t)
	s, _ := res.body["data"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s) {
		t.Fatalf("hash %q is not lowercase hex sha256", s)
	}
	return s
}

// The wire path (JSON in, JSON out, hash over the stored values) must still hit the
// JS-computed vectors: real U+2028/U+2029, literal backslash-u2028 text, quotes,
// backslashes, <>&, unicode, control characters, empty titles and root title omission.
func TestHashEndpointMatchesJSVectorsThroughTheWire(t *testing.T) {
	v := loadVectors(t)
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(a)
	r.build(a, bar, v.Children[0].Children)
	r.build(a, other, v.Children[1].Children)

	if got := hashOf(t, r, a, "-1"); got != v.Hash {
		t.Errorf("root hash %s, JS %s", got, v.Hash)
	}
	if got := hashOf(t, r, a, idStr(bar)); got != v.Children[0].Hash {
		t.Errorf("bar hash %s, JS %s", got, v.Children[0].Hash)
	}
	if got := hashOf(t, r, a, idStr(other)); got != v.Children[1].Hash {
		t.Errorf("other hash %s, JS %s", got, v.Children[1].Hash)
	}
	// the title/url that came back out of storage is byte-identical to what went in
	d := r.do(&a, "GET", R+"/folder/"+idStr(bar)+"/children?layers=1", nil).ok(t).data()
	for i, want := range v.Children[0].Children {
		got := d[i].(map[string]any)
		if got["title"] != want.Title {
			t.Errorf("child %d title %q != %q", i, got["title"], want.Title)
		}
		if want.Type == "bookmark" && got["url"] != want.URL {
			t.Errorf("child %d url %q != %q", i, got["url"], want.URL)
		}
	}
	// real and literal forms are distinct strings on the wire
	if !strings.Contains(r.do(&a, "GET", R+"/folder/"+idStr(bar)+"/children?layers=1", nil).raw, `\\u2028`) {
		t.Error("literal backslash-u2028 text lost its backslash in the response")
	}
}

func TestHashChangesOnDescendantChangeAndOmitsRootTitleForShares(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	f := r.mkFolder(a, bar, "f")
	b := r.mkBookmark(a, "https://h.example/", "h", f)
	h0 := hashOf(t, r, a, "-1")
	r.do(&a, "PUT", R+"/bookmark/"+idStr(b), map[string]any{"url": "https://h.example/", "title": "h2", "folders": []int64{f}}).ok(t)
	h1 := hashOf(t, r, a, "-1")
	if h0 == h1 {
		t.Error("stale root hash after a deep change")
	}
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bc := r.shareAccount(a, f, bob, identity.ModeView, identity.ModeView)
	shareRoot := hashOf(t, r, bc, "-1")
	if shareRoot != hashOf(t, r, bc, idStr(f)) {
		t.Error("share root by real id must hash like the -1 alias")
	}
	if shareRoot == hashOf(t, r, a, idStr(f)) {
		t.Error("the owner's titled folder hash must differ from the untitled share root hash")
	}
}

// A realistic floccus sync, sent in the order the audit documents (start: capabilities,
// lock, hash, children; write: folder, import, order, bookmark; end: unlock).
func TestFloccusStyleSyncSequence(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	if res := r.do(&a, "GET", "/ocs/v2.php/cloud/capabilities?format=json", nil); res.status != 200 {
		t.Fatalf("capabilities: %d", res.status)
	}
	r.do(&a, "POST", R+"/lock", nil).ok(t)
	root := hashOf(t, r, a, "-1")
	d := children(t, r, a, -1, -1)
	bar := num(d[0].(map[string]any)["id"])
	f := num(r.do(&a, "POST", R+"/folder", map[string]any{"parent_folder": bar, "title": "Reading"}).ok(t).item()["id"])
	r.multipart(&a, R+"/folder/"+idStr(f)+"/import", "bm_import", "upload.html", floccusHTML).ok(t)
	look := r.do(&a, "GET", R+"/bookmark?url=https%3A%2F%2Fnew.example%2F", nil).ok(t).data()
	if len(look) != 0 {
		t.Fatal("lookup found a phantom")
	}
	id := num(r.do(&a, "POST", R+"/bookmark", map[string]any{"url": "https://new.example/", "title": "New", "folders": []int64{f}}).ok(t).item()["id"])
	got := r.do(&a, "GET", R+"/bookmark/"+idStr(id), nil).ok(t).item()
	r.do(&a, "PUT", R+"/bookmark/"+idStr(id), map[string]any{"url": got["url"], "title": "New 2", "folders": got["folders"], "tags": got["tags"]}).ok(t)
	ord := children(t, r, a, f, 1)
	var order []map[string]any
	for i := len(ord) - 1; i >= 0; i-- {
		m := ord[i].(map[string]any)
		order = append(order, map[string]any{"id": idStr(num(m["id"])), "type": m["type"]})
	}
	r.do(&a, "PATCH", R+"/folder/"+idStr(f)+"/childorder", map[string]any{"data": order}).ok(t)
	r.do(&a, "DELETE", R+"/lock", nil).ok(t)
	if hashOf(t, r, a, "-1") == root {
		t.Error("root hash did not change after the sync")
	}
}

// The server's request log is the evidence for acceptance script A8: a download-only
// connection must show no content writes, only GETs and the lock verbs.
func TestDownloadOnlySyncLogsOnlyReadsAndLockVerbs(t *testing.T) {
	r := newRig(t)
	owner := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, _ := r.roots(owner)
	shared := r.mkFolder(owner, bar, "Shared")
	r.mkBookmark(owner, "https://s.example/", "s", shared)
	ro := r.shareAccount(owner, shared, bob, identity.ModeView, identity.ModeView)

	r.do(&ro, "GET", "/ocs/v2.php/cloud/capabilities?format=json", nil)
	r.do(&ro, "POST", R+"/lock", nil).ok(t)
	hashOf(t, r, ro, "-1")
	children(t, r, ro, -1, -1)
	r.do(&ro, "DELETE", R+"/lock", nil).ok(t)

	needle := "credential=" + idStr(ro.cred.ID) + " "
	seen := 0
	for _, line := range strings.Split(r.logs.String(), "\n") {
		if !strings.Contains(line, "floccus request") || !strings.Contains(line+" ", needle) {
			continue
		}
		seen++
		isLock := strings.Contains(line, "path=/index.php/apps/bookmarks/public/rest/v2/lock")
		if !strings.Contains(line, "method=GET") && !isLock {
			t.Errorf("download-only credential made a non-read, non-lock request: %s", line)
		}
	}
	if seen != 5 {
		t.Errorf("log lines for the credential = %d, want 5:\n%s", seen, r.logs.String())
	}
}
