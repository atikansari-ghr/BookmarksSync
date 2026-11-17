package interchange_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/interchange"
)

const mib = 1 << 20

var sample = []bookmarks.ExportNode{
	{Folder: true, Title: `Dev <tools> & "more"`, Children: []bookmarks.ExportNode{
		{Title: `Go <b>bold</b>`, URL: `https://go.dev/?a=1&b="2"`, Tags: []string{"go", "lang"}},
		{Folder: true, Title: "Empty", Children: []bookmarks.ExportNode{}},
	}},
	{Title: "<script>alert(1)</script>", URL: "javascript:alert(1)"},
}

func TestNetscapeRoundTripWithTagsAndEscaping(t *testing.T) {
	var buf bytes.Buffer
	if err := interchange.WriteNetscape(&buf, sample); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<!DOCTYPE NETSCAPE-Bookmark-file-1>") {
		t.Fatalf("missing Netscape doctype:\n%s", out)
	}
	for _, raw := range []string{"<script>", "<b>bold", `"2"`, "<tools>"} {
		if strings.Contains(out, raw) {
			t.Fatalf("unescaped %q in export:\n%s", raw, out)
		}
	}
	got, err := interchange.ParseNetscape(strings.NewReader(out), mib, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Folder || got[0].Title != `Dev <tools> & "more"` || len(got[0].Children) != 2 {
		t.Fatalf("parsed = %+v", got)
	}
	g := got[0].Children[0]
	if g.Title != `Go <b>bold</b>` || g.URL != `https://go.dev/?a=1&b="2"` || len(g.Tags) != 2 || g.Tags[0] != "go" || g.Tags[1] != "lang" {
		t.Fatalf("bookmark = %+v", g)
	}
	if !got[0].Children[1].Folder || len(got[0].Children[1].Children) != 0 {
		t.Fatalf("empty folder = %+v", got[0].Children[1])
	}
	if got[1].URL != "javascript:alert(1)" || got[1].Title != "<script>alert(1)</script>" {
		t.Fatalf("script entry = %+v", got[1])
	}
	// Without tag support (floccus behaviour) tags are ignored.
	plain, _ := interchange.ParseNetscape(strings.NewReader(out), mib, false)
	if plain[0].Children[0].Tags != nil {
		t.Fatal("tags parsed with tags disabled")
	}
}

func TestNetscapeTagsAttributeIsNormalized(t *testing.T) {
	in := `<DL><p><DT><A HREF="https://a.test" TAGS=" x, ,y,x ">A</A></DL>`
	got, err := interchange.ParseNetscape(strings.NewReader(in), mib, true)
	if err != nil || len(got) != 1 || len(got[0].Tags) != 2 || got[0].Tags[0] != "x" || got[0].Tags[1] != "y" {
		t.Fatalf("tags = %+v, %v", got, err)
	}
}

func TestNetscapeLimits(t *testing.T) {
	big := strings.Repeat("a", 2*mib)
	if _, err := interchange.ParseNetscape(strings.NewReader(big), mib, true); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("oversized: %v", err)
	}
	var deep strings.Builder
	for i := 0; i < bookmarks.MaxImportDepth+2; i++ {
		deep.WriteString("<DL><p><DT><H3>f</H3>")
	}
	if _, err := interchange.ParseNetscape(strings.NewReader(deep.String()), mib, true); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("too deep: %v", err)
	}
}

func TestXBELRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := interchange.WriteXBEL(&buf, sample); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, `<?xml version="1.0" encoding="UTF-8"?>`) || strings.Contains(out, "<!DOCTYPE") {
		t.Fatalf("xbel header:\n%s", out)
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, "<b>") {
		t.Fatalf("unescaped markup in XBEL:\n%s", out)
	}
	got, err := interchange.ParseXBEL(strings.NewReader(out), mib)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Folder || got[0].Title != `Dev <tools> & "more"` || len(got[0].Children) != 2 {
		t.Fatalf("parsed = %+v", got)
	}
	if g := got[0].Children[0]; g.Title != `Go <b>bold</b>` || g.URL != `https://go.dev/?a=1&b="2"` {
		t.Fatalf("bookmark = %+v", g)
	}
	if got[1].URL != "javascript:alert(1)" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestXBELParsesCommonDocumentsAndIgnoresUnknownElements(t *testing.T) {
	in := `<?xml version="1.0"?>
<xbel version="1.0">
  <title>My bookmarks</title>
  <info><metadata owner="x"><deep><deeper/></deep></metadata></info>
  <folder folded="no"><title>A</title><desc>ignored</desc>
    <bookmark href="https://a.test"><title>a</title><info/></bookmark>
    <separator/>
    <alias ref="b1"/>
  </folder>
  <bookmark href="https://b.test"/>
</xbel>`
	got, err := interchange.ParseXBEL(strings.NewReader(in), mib)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "A" || len(got[0].Children) != 1 || got[0].Children[0].URL != "https://a.test" ||
		got[1].URL != "https://b.test" || got[1].Title != "" {
		t.Fatalf("parsed = %+v", got)
	}
}

func TestXBELRefusesDTDsEntitiesAndMalformedInput(t *testing.T) {
	laughs := `<?xml version="1.0"?>
<!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
<!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">]>
<xbel><bookmark href="https://x.test"><title>&lol3;</title></bookmark></xbel>`
	external := `<?xml version="1.0"?><!DOCTYPE xbel SYSTEM "file:///etc/passwd"><xbel/>`
	undefinedEntity := `<xbel><bookmark href="https://x.test"><title>&secret;</title></bookmark></xbel>`
	for name, in := range map[string]string{
		"billion laughs":   laughs,
		"external DTD":     external,
		"undefined entity": undefinedEntity,
		"malformed":        `<xbel><folder><title>x</folder></xbel>`,
		"not xbel":         `<rss><item/></rss>`,
		"empty":            ``,
		"trailing garbage": `<xbel/><xbel/>`,
	} {
		start := time.Now()
		if _, err := interchange.ParseXBEL(strings.NewReader(in), mib); !errors.Is(err, bookmarks.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
		if time.Since(start) > time.Second {
			t.Fatalf("%s took %v", name, time.Since(start))
		}
	}
}

func TestXBELLimits(t *testing.T) {
	var deep strings.Builder
	deep.WriteString("<xbel>")
	for i := 0; i < bookmarks.MaxImportDepth+2; i++ {
		deep.WriteString("<folder><title>f</title>")
	}
	for i := 0; i < bookmarks.MaxImportDepth+2; i++ {
		deep.WriteString("</folder>")
	}
	deep.WriteString("</xbel>")
	if _, err := interchange.ParseXBEL(strings.NewReader(deep.String()), mib); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("too deep: %v", err)
	}
	var wide strings.Builder
	wide.WriteString("<xbel>")
	for i := 0; i <= bookmarks.MaxImportNodes; i++ {
		fmt.Fprintf(&wide, `<bookmark href="https://x.test/%d"/>`, i)
	}
	wide.WriteString("</xbel>")
	if _, err := interchange.ParseXBEL(strings.NewReader(wide.String()), 4*mib); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("too many items: %v", err)
	}
	// Deeply nested unknown elements are bounded too.
	nest := "<xbel>" + strings.Repeat("<info>", 10000) + strings.Repeat("</info>", 10000) + "</xbel>"
	if _, err := interchange.ParseXBEL(strings.NewReader(nest), mib); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("nesting bomb in unknown elements: %v", err)
	}
	if _, err := interchange.ParseXBEL(strings.NewReader("<xbel>"+strings.Repeat(" ", 2*mib)+"</xbel>"), mib); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("oversized: %v", err)
	}
}
