package floccus_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/floccus"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// floccusHTML reproduces src/lib/serializers/Html.ts output (entities for <>&"').
const floccusHTML = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<DL><p>
<DT><A HREF="https://a.example/?x=1&#38;y=2" TAGS="" ID="11;5">First &#60;one&#62; &#38; &#34;quoted&#34; &#39;apos&#39;</A>
<DT><H3 ID="6">Sub &#38; folder</H3>
<DL><p>
  <DT><A HREF="https://b.example/" TAGS="" ID="12;6">Second</A>
  <DT><H3 ID="7">Deeper</H3>
  <DL><p>
    <DT><A HREF="https://c.example/" TAGS="" ID="13;7">Third</A>
  </DL><p>
  <DT><H3 ID="8">Empty</H3>
  <DL><p>
  </DL><p>
</DL><p>
<DT><A HREF="https://d.example/" TAGS="" ID="14;5">Last</A>
</DL><p>
`

func TestParseNetscapeFloccusSerializerOutput(t *testing.T) {
	got, err := floccus.ParseNetscape(strings.NewReader(floccusHTML))
	noErr(t, err)
	if len(got) != 3 {
		t.Fatalf("top-level = %+v", got)
	}
	if got[0].Folder || got[0].URL != "https://a.example/?x=1&y=2" || got[0].Title != `First <one> & "quoted" 'apos'` {
		t.Errorf("first = %+v", got[0])
	}
	sub := got[1]
	if !sub.Folder || sub.Title != "Sub & folder" || len(sub.Children) != 3 {
		t.Fatalf("sub = %+v", sub)
	}
	if sub.Children[0].Title != "Second" || !sub.Children[1].Folder || len(sub.Children[1].Children) != 1 ||
		!sub.Children[2].Folder || len(sub.Children[2].Children) != 0 || sub.Children[2].Title != "Empty" {
		t.Errorf("sub children = %+v", sub.Children)
	}
	if got[2].Title != "Last" || got[2].Folder {
		t.Errorf("last = %+v (order lost: the </DL> of Sub must close before Last)", got[2])
	}
}

func TestParseNetscapeBrowserExportStyle(t *testing.T) {
	html := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<!-- This is an automatically generated file. -->
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><H3 ADD_DATE="1" LAST_MODIFIED="2" PERSONAL_TOOLBAR_FOLDER="true">Bookmarks bar</H3>
    <DL><p>
        <DT><A HREF="https://x.example/" ADD_DATE="3" ICON="data:image/png;base64,AAAA">Ünï 日本</A>
        <DT><A HREF="javascript:alert(1)">bookmarklet</A>
    </DL><p>
    <HR>
    <DT><H3>Other</H3>
    <DL><p></DL><p>
</DL><p>`
	got, err := floccus.ParseNetscape(strings.NewReader(html))
	noErr(t, err)
	if len(got) != 2 || got[0].Title != "Bookmarks bar" || len(got[0].Children) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Children[0].Title != "Ünï 日本" || got[0].Children[1].URL != "javascript:alert(1)" {
		t.Errorf("children = %+v", got[0].Children)
	}
	if got[1].Title != "Other" || len(got[1].Children) != 0 {
		t.Errorf("other = %+v", got[1])
	}
}

func TestParseNetscapeEmptyAndGarbageProduceNoItemsOrSafeError(t *testing.T) {
	for name, in := range map[string]string{
		"empty": "", "no list": "<html><body><p>hello</p></body></html>", "script": "<DL><p><script>alert(1)</script></DL>",
		"binary": "\x00\x01\x02\xff\xfe", "unclosed": "<DL><p><DT><H3>x</H3><DL><p><DT><A HREF=\"https://a.example/\">a",
	} {
		got, err := floccus.ParseNetscape(strings.NewReader(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if name != "unclosed" && len(got) != 0 {
			t.Errorf("%s: parsed %+v", name, got)
		}
	}
}

func TestParseNetscapeBoundsDepthAndCount(t *testing.T) {
	var deep strings.Builder
	n := bookmarks.MaxImportDepth + 5
	for i := 0; i < n; i++ {
		deep.WriteString("<DL><p><DT><H3>f</H3>")
	}
	deep.WriteString("<DL><p><DT><A HREF=\"https://x.example/\">x</A></DL><p>")
	for i := 0; i < n; i++ {
		deep.WriteString("</DL><p>")
	}
	if _, err := floccus.ParseNetscape(strings.NewReader(deep.String())); err == nil {
		t.Error("depth bomb accepted")
	}
	var wide strings.Builder
	wide.WriteString("<DL><p>")
	for i := 0; i < bookmarks.MaxImportNodes+1; i++ {
		fmt.Fprintf(&wide, `<DT><A HREF="https://w%d.example/">w</A>`, i)
	}
	wide.WriteString("</DL><p>")
	if _, err := floccus.ParseNetscape(strings.NewReader(wide.String())); err == nil {
		t.Error("node-count bomb accepted")
	}
}

func TestImportEndpointCreatesNestedTreeAndReturnsOnlyImported(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	target := r.mkFolder(a, bar, "Target")
	r.mkBookmark(a, "https://pre.example/", "pre", target)

	res := r.multipart(&a, R+"/folder/"+idStr(target)+"/import", "bm_import", "upload.html", floccusHTML).ok(t)
	d := res.data()
	if titles(d) != `First <one> & "quoted" 'apos'|Sub & folder|Last` {
		t.Fatalf("returned = %s (must be only the imported tree)", titles(d))
	}
	sub := d[1].(map[string]any)
	if sub["type"] != "folder" || len(sub["children"].([]any)) != 3 {
		t.Errorf("sub = %v", sub)
	}
	if empty := sub["children"].([]any)[2].(map[string]any); empty["children"] == nil {
		t.Errorf("empty imported folder must carry children: [] %v", empty)
	}
	for _, it := range d {
		if num(it.(map[string]any)["id"]) <= 0 {
			t.Errorf("no id: %v", it)
		}
	}
	// appended after what was already there, nested content really exists
	if got := titles(children(t, r, a, target, -1)); got != `pre|First <one> & "quoted" 'apos'|Sub & folder|Last` {
		t.Errorf("target now = %s", got)
	}
	// importing the same payload again appends only the folder (bookmarks already placed here are skipped)
	again := r.multipart(&a, R+"/folder/"+idStr(target)+"/import", "bm_import", "upload.html", floccusHTML).ok(t).data()
	if titles(again) != "Sub & folder" {
		t.Errorf("second import returned %s", titles(again))
	}
}

func TestImportEndpointIsTransactionalAndBounded(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	target := r.mkFolder(a, bar, "Target")
	before := dump(t, r, a)

	badTree := `<DL><p><DT><A HREF="https://ok.example/">ok</A><DT><H3>d</H3><DL><p><DT><A HREF="https://fine.example/">f</A><DT><H3>e</H3><DL><p><DT><A HREF="chrome://settings">bad</A></DL><p></DL><p></DL><p>`
	r.multipart(&a, R+"/folder/"+idStr(target)+"/import", "bm_import", "u.html", badTree).fail(t, 400)
	var wide strings.Builder
	wide.WriteString("<DL><p>")
	for i := 0; i < bookmarks.MaxImportNodes+1; i++ {
		fmt.Fprintf(&wide, `<DT><A HREF="https://w%d.example/">w</A>`, i)
	}
	r.multipart(&a, R+"/folder/"+idStr(target)+"/import", "bm_import", "u.html", wide.String()).fail(t, 400)
	r.multipart(&a, R+"/folder/"+idStr(target)+"/import", "wrong_field", "u.html", floccusHTML).fail(t, 400)
	r.do(&a, "POST", R+"/folder/"+idStr(target)+"/import", `{"x":1}`).fail(t, 400)
	r.do(&a, "POST", R+"/folder/"+idStr(target)+"/import", nil).fail(t, 400)
	huge := strings.Repeat("<DT><A HREF=\"https://h.example/\">h</A>", 100000)
	if res := r.multipart(&a, R+"/folder/"+idStr(target)+"/import", "bm_import", "u.html", "<DL><p>"+huge); res.status != 413 && res.status != 400 {
		t.Errorf("oversized upload: %d", res.status)
	}
	r.multipart(&a, R+"/folder/987654/import", "bm_import", "u.html", floccusHTML).fail(t, 404)
	if dump(t, r, a) != before {
		t.Error("a failed import left data behind")
	}
}

func TestImportOfSeventyFiveBookmarkSubtree(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	target := r.mkFolder(a, bar, "Target")
	var sb strings.Builder
	sb.WriteString("<DL><p>\n")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&sb, "<DT><H3>folder %d</H3>\n<DL><p>\n", i)
		for j := 0; j < 15; j++ {
			fmt.Fprintf(&sb, `<DT><A HREF="https://s%d-%d.example/" TAGS="">b %d.%d</A>`+"\n", i, j, i, j)
		}
		sb.WriteString("</DL><p>\n")
	}
	sb.WriteString("</DL><p>\n")
	d := r.multipart(&a, R+"/folder/"+idStr(target)+"/import", "bm_import", "u.html", sb.String()).ok(t).data()
	if len(d) != 5 {
		t.Fatalf("returned %d folders", len(d))
	}
	total := 0
	for _, f := range d {
		total += len(f.(map[string]any)["children"].([]any))
	}
	if total != 75 {
		t.Errorf("imported %d bookmarks, want 75", total)
	}
}

func TestImportNeverFetchesURLs(t *testing.T) {
	var hits atomic.Int32
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer probe.Close()
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(a)
	html := fmt.Sprintf(`<DL><p><DT><A HREF="%s/a">a</A><DT><A HREF="javascript:fetch('%s/js')">js</A></DL><p>`, probe.URL, probe.URL)
	r.multipart(&a, R+"/folder/"+idStr(bar)+"/import", "bm_import", "u.html", html).ok(t)
	r.mkBookmark(a, probe.URL+"/direct", "direct", bar)
	if n := hits.Load(); n != 0 {
		t.Errorf("server fetched imported/stored URLs %d times", n)
	}
}

func TestImportIntoShareStaysInsideTheShare(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, other := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	r.multipart(&bc, R+"/folder/-1/import", "bm_import", "u.html", floccusHTML).ok(t)
	r.multipart(&bc, R+"/folder/"+idStr(other)+"/import", "bm_import", "u.html", floccusHTML).fail(t, 404)
	if len(children(t, r, alice, shared, 1)) != 3 {
		t.Errorf("owner's shared folder = %s", titles(children(t, r, alice, shared, 1)))
	}
}
