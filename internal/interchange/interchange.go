// Package interchange reads and writes bookmark files: Netscape bookmark HTML (what
// browsers and floccus export) and XBEL. Parsing never fetches, resolves or evaluates
// anything; size, node count and depth are bounded, and XBEL document type
// declarations (DTDs, entities) are refused. Writers escape every title and URL.
package interchange

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"

	nethtml "golang.org/x/net/html"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", bookmarks.ErrInvalid, fmt.Sprintf(format, a...))
}

// readBounded reads at most maxBytes, failing with ErrInvalid when larger.
func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, invalid("upload larger than %d bytes", maxBytes)
	}
	return data, nil
}

// ---- Netscape HTML ----

type nsParser struct {
	count    int
	withTags bool
}

// ParseNetscape reads a Netscape bookmark file with a real HTML parser: <DL><p> lists
// of <DT> entries whose first element is <H3> (folder; children in the following <DL>)
// or <A HREF> (bookmark). The HTML5 tree builder nests a folder's <DL> inside its <DT>.
// withTags imports the comma-separated TAGS attribute.
func ParseNetscape(r io.Reader, maxBytes int64, withTags bool) ([]bookmarks.ImportNode, error) {
	data, err := readBounded(r, maxBytes)
	if err != nil {
		return nil, err
	}
	doc, err := nethtml.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, invalid("unparsable HTML")
	}
	body := findFirst(doc, "body")
	if body == nil {
		return []bookmarks.ImportNode{}, nil
	}
	var dl *nethtml.Node
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == nethtml.ElementNode && c.Data == "dl" {
			dl = c
			break
		}
	}
	if dl == nil {
		return []bookmarks.ImportNode{}, nil
	}
	p := &nsParser{withTags: withTags}
	return p.list(dl, 1)
}

func (p *nsParser) list(dl *nethtml.Node, depth int) ([]bookmarks.ImportNode, error) {
	if depth > bookmarks.MaxImportDepth {
		return nil, invalid("import nested deeper than %d levels", bookmarks.MaxImportDepth)
	}
	out := []bookmarks.ImportNode{}
	for c := dl.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != nethtml.ElementNode || c.Data != "dt" {
			continue
		}
		e := firstElement(c)
		if e == nil {
			continue
		}
		switch e.Data {
		case "h3":
			p.count++
			if p.count > bookmarks.MaxImportNodes {
				return nil, invalid("import larger than %d items", bookmarks.MaxImportNodes)
			}
			n := bookmarks.ImportNode{Folder: true, Title: textOf(e), Children: []bookmarks.ImportNode{}}
			for s := e.NextSibling; s != nil; s = s.NextSibling {
				if s.Type == nethtml.ElementNode && s.Data == "dl" {
					kids, err := p.list(s, depth+1)
					if err != nil {
						return nil, err
					}
					n.Children = kids
					break
				}
			}
			out = append(out, n)
		case "a":
			p.count++
			if p.count > bookmarks.MaxImportNodes {
				return nil, invalid("import larger than %d items", bookmarks.MaxImportNodes)
			}
			n := bookmarks.ImportNode{Title: textOf(e), URL: attr(e, "href")}
			if p.withTags {
				n.Tags = splitTags(attr(e, "tags"))
			}
			out = append(out, n)
		}
	}
	return out, nil
}

// splitTags normalizes a TAGS attribute: comma-separated, trimmed, no empties or
// duplicates. It returns nil when there are none.
func splitTags(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func findFirst(n *nethtml.Node, tag string) *nethtml.Node {
	if n.Type == nethtml.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findFirst(c, tag); f != nil {
			return f
		}
	}
	return nil
}

func firstElement(n *nethtml.Node) *nethtml.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == nethtml.ElementNode {
			return c
		}
	}
	return nil
}

func textOf(n *nethtml.Node) string {
	var out []byte
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.TextNode {
			out = append(out, n.Data...)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return string(out)
}

func attr(n *nethtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// WriteNetscape writes a Netscape bookmark file. Every title, URL and tag is escaped.
func WriteNetscape(w io.Writer, nodes []bookmarks.ExportNode) error {
	var b strings.Builder
	b.WriteString("<!DOCTYPE NETSCAPE-Bookmark-file-1>\n<!-- This is an automatically generated file. Do not edit. -->\n")
	b.WriteString(`<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">` + "\n<TITLE>Bookmarks</TITLE>\n<H1>Bookmarks</H1>\n")
	writeNSList(&b, nodes, 0)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeNSList(b *strings.Builder, nodes []bookmarks.ExportNode, depth int) {
	pad := strings.Repeat("    ", depth)
	b.WriteString(pad + "<DL><p>\n")
	for _, n := range nodes {
		if n.Folder {
			b.WriteString(pad + "    <DT><H3>" + html.EscapeString(n.Title) + "</H3>\n")
			writeNSList(b, n.Children, depth+1)
			continue
		}
		b.WriteString(pad + `    <DT><A HREF="` + html.EscapeString(n.URL) + `"`)
		if len(n.Tags) > 0 {
			b.WriteString(` TAGS="` + html.EscapeString(strings.Join(n.Tags, ",")) + `"`)
		}
		b.WriteString(">" + html.EscapeString(n.Title) + "</A>\n")
	}
	b.WriteString(pad + "</DL><p>\n")
}

// ---- XBEL ----

const (
	maxXMLNesting = 128     // element nesting, including ignored elements
	maxXMLTokens  = 1 << 20 // tokens per document
)

type xbelParser struct {
	dec    *xml.Decoder
	count  int
	tokens int
	open   int
}

func (p *xbelParser) token() (xml.Token, error) {
	p.tokens++
	if p.tokens > maxXMLTokens {
		return nil, invalid("XBEL document too large")
	}
	tok, err := p.dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, invalid("unexpected end of XBEL document")
		}
		return nil, invalid("malformed XBEL")
	}
	switch t := tok.(type) {
	case xml.Directive:
		return nil, invalid("document type declarations are not allowed in XBEL imports")
	case xml.StartElement:
		p.open++
		if p.open > maxXMLNesting {
			return nil, invalid("XBEL nested too deeply")
		}
		return t, nil
	case xml.EndElement:
		p.open--
	}
	return tok, nil
}

// skip consumes the rest of the current element (bounded by token).
func (p *xbelParser) skip() error {
	depth := 1
	for depth > 0 {
		tok, err := p.token()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// text reads the character data of a simple element such as <title>.
func (p *xbelParser) text() (string, error) {
	var b strings.Builder
	for {
		tok, err := p.token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if b.Len()+len(t) > bookmarks.MaxTitleBytes+1 {
				return "", invalid("title longer than %d bytes", bookmarks.MaxTitleBytes)
			}
			b.Write(t)
		case xml.StartElement:
			if err := p.skip(); err != nil {
				return "", err
			}
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

func (p *xbelParser) bump() error {
	p.count++
	if p.count > bookmarks.MaxImportNodes {
		return invalid("import larger than %d items", bookmarks.MaxImportNodes)
	}
	return nil
}

// container reads the children of <xbel> or <folder> up to its end element. title is
// set from a <title> child.
func (p *xbelParser) container(depth int, title *string) ([]bookmarks.ImportNode, error) {
	out := []bookmarks.ImportNode{}
	for {
		tok, err := p.token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return out, nil
		case xml.StartElement:
			switch t.Name.Local {
			case "title":
				s, err := p.text()
				if err != nil {
					return nil, err
				}
				if title != nil {
					*title = s
				}
			case "folder":
				if depth+1 > bookmarks.MaxImportDepth {
					return nil, invalid("import nested deeper than %d levels", bookmarks.MaxImportDepth)
				}
				if err := p.bump(); err != nil {
					return nil, err
				}
				n := bookmarks.ImportNode{Folder: true}
				kids, err := p.container(depth+1, &n.Title)
				if err != nil {
					return nil, err
				}
				n.Children = kids
				out = append(out, n)
			case "bookmark":
				if err := p.bump(); err != nil {
					return nil, err
				}
				n := bookmarks.ImportNode{}
				for _, a := range t.Attr {
					if a.Name.Local == "href" {
						n.URL = a.Value
					}
				}
				if _, err := p.container(depth, &n.Title); err != nil { // title and ignored children
					return nil, err
				}
				out = append(out, n)
			default: // desc, info, separator, alias, metadata...
				if err := p.skip(); err != nil {
					return nil, err
				}
			}
		}
	}
}

// ParseXBEL reads an XBEL document. Document type declarations are refused (no DTD or
// entity processing at all), undefined entities fail, element nesting and token count
// are bounded, and only folder/bookmark/title are used.
func ParseXBEL(r io.Reader, maxBytes int64) ([]bookmarks.ImportNode, error) {
	data, err := readBounded(r, maxBytes)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil // predefined XML entities only
	p := &xbelParser{dec: dec}
	for {
		tok, err := p.token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.ProcInst, xml.Comment:
			continue
		case xml.CharData:
			if len(bytes.TrimSpace(t)) != 0 {
				return nil, invalid("malformed XBEL")
			}
			continue
		case xml.StartElement:
			if t.Name.Local != "xbel" {
				return nil, invalid("not an XBEL document")
			}
			nodes, err := p.container(0, nil)
			if err != nil {
				return nil, err
			}
			// Only whitespace, comments or processing instructions may follow.
			for {
				tok, err := dec.Token()
				if errors.Is(err, io.EOF) {
					return nodes, nil
				}
				if err != nil {
					return nil, invalid("malformed XBEL")
				}
				switch t := tok.(type) {
				case xml.CharData:
					if len(bytes.TrimSpace(t)) != 0 {
						return nil, invalid("malformed XBEL")
					}
				case xml.Comment, xml.ProcInst:
				default:
					return nil, invalid("content after the XBEL document")
				}
			}
		default:
			return nil, invalid("malformed XBEL")
		}
	}
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// WriteXBEL writes an XBEL 1.0 document (no DOCTYPE). Every title and URL is escaped.
func WriteXBEL(w io.Writer, nodes []bookmarks.ExportNode) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<xbel version="1.0">` + "\n")
	writeXBEL(&b, nodes, 1)
	b.WriteString("</xbel>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func writeXBEL(b *strings.Builder, nodes []bookmarks.ExportNode, depth int) {
	pad := strings.Repeat("  ", depth)
	for _, n := range nodes {
		if n.Folder {
			b.WriteString(pad + "<folder>\n" + pad + "  <title>" + xmlEscape(n.Title) + "</title>\n")
			writeXBEL(b, n.Children, depth+1)
			b.WriteString(pad + "</folder>\n")
			continue
		}
		b.WriteString(pad + `<bookmark href="` + xmlEscape(n.URL) + `">` + "\n" + pad + "  <title>" + xmlEscape(n.Title) + "</title>\n" + pad + "</bookmark>\n")
	}
}
