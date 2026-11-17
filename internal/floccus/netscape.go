package floccus

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/net/html"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
)

// maxImportBytes bounds the uploaded HTML (the server-wide body cap is also in force).
const maxImportBytes = 1 << 20

func (a *Adapter) importFolder(c *call) {
	id, ok := c.folderParam(0)
	if !ok {
		return
	}
	mr, err := c.r.MultipartReader()
	if err != nil {
		a.badRequest(c.w, "expected a multipart/form-data upload with a bm_import field")
		return
	}
	var nodes []bookmarks.ImportNode
	found := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			a.importReadError(c, err)
			return
		}
		if part.FormName() != "bm_import" {
			continue
		}
		nodes, err = parseNetscape(part)
		if err != nil {
			a.importReadError(c, err)
			return
		}
		found = true
		break
	}
	if !found {
		a.badRequest(c.w, "missing bm_import upload")
		return
	}
	out, err := a.svc.ImportTree(c.r.Context(), c.sc, id, nodes)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	imported, err := toNodes(out)
	if err != nil {
		a.internal(c.w, err)
		return
	}
	success(c.w, map[string]any{"data": imported})
}

func (a *Adapter) importReadError(c *call, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) || errors.Is(err, bookmarks.ErrInvalid) {
		a.fail(c.w, err)
		return
	}
	a.badRequest(c.w, "could not read the upload")
}

type nsParser struct{ count int }

// parseNetscape reads a Netscape bookmark file with a real HTML parser, exactly the
// shape floccus serialises (and browsers export): <DL><p> lists of <DT> entries whose
// first element is <H3> (folder, children in the following <DL>) or <A HREF> (bookmark).
// The HTML5 tree builder nests the <DL> of a folder inside its <DT>, as cheerio does in
// floccus. It never fetches, resolves or evaluates anything. Size, node count and depth
// are bounded.
func parseNetscape(r io.Reader) ([]bookmarks.ImportNode, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxImportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImportBytes {
		return nil, fmt.Errorf("%w: upload larger than %d bytes", bookmarks.ErrInvalid, maxImportBytes)
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: unparsable HTML", bookmarks.ErrInvalid)
	}
	body := findFirst(doc, "body")
	if body == nil {
		return []bookmarks.ImportNode{}, nil
	}
	var dl *html.Node
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "dl" {
			dl = c
			break
		}
	}
	if dl == nil {
		return []bookmarks.ImportNode{}, nil
	}
	p := &nsParser{}
	return p.list(dl, 1)
}

func (p *nsParser) list(dl *html.Node, depth int) ([]bookmarks.ImportNode, error) {
	if depth > bookmarks.MaxImportDepth {
		return nil, fmt.Errorf("%w: import nested deeper than %d levels", bookmarks.ErrInvalid, bookmarks.MaxImportDepth)
	}
	out := []bookmarks.ImportNode{}
	for c := dl.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "dt" {
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
				return nil, fmt.Errorf("%w: import larger than %d items", bookmarks.ErrInvalid, bookmarks.MaxImportNodes)
			}
			n := bookmarks.ImportNode{Folder: true, Title: textOf(e), Children: []bookmarks.ImportNode{}}
			for s := e.NextSibling; s != nil; s = s.NextSibling {
				if s.Type == html.ElementNode && s.Data == "dl" {
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
				return nil, fmt.Errorf("%w: import larger than %d items", bookmarks.ErrInvalid, bookmarks.MaxImportNodes)
			}
			out = append(out, bookmarks.ImportNode{Title: textOf(e), URL: attr(e, "href")})
		}
	}
	return out, nil
}

func findFirst(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findFirst(c, tag); f != nil {
			return f
		}
	}
	return nil
}

func firstElement(n *html.Node) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			return c
		}
	}
	return nil
}

func textOf(n *html.Node) string {
	var out []byte
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			out = append(out, n.Data...)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return string(out)
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
