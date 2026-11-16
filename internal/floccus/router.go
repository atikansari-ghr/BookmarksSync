package floccus

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
)

// call is one authenticated, routed request.
type call struct {
	a      *Adapter
	w      http.ResponseWriter
	r      *http.Request
	sc     bookmarks.Scope
	params []string
}

type route struct {
	method  string
	segs    []string // literal segments or "{}" for an id
	mutates bool     // content write: refused with 403 for read-only scopes
	h       func(*call)
}

func (a *Adapter) routeTable() []route {
	return []route{
		{"GET", []string{"bookmark"}, false, a.listBookmarks},
		{"POST", []string{"bookmark"}, true, a.createBookmark},
		{"POST", []string{"bookmark", "click"}, false, a.clickBookmark},
		{"GET", []string{"bookmark", "{}"}, false, a.getBookmark},
		{"PUT", []string{"bookmark", "{}"}, true, a.updateBookmark},
		{"DELETE", []string{"bookmark", "{}"}, true, a.deleteBookmark},
		{"GET", []string{"folder"}, false, a.folderTree},
		{"POST", []string{"folder"}, true, a.createFolder},
		{"GET", []string{"folder", "{}"}, false, a.getFolder},
		{"PUT", []string{"folder", "{}"}, true, a.updateFolder},
		{"DELETE", []string{"folder", "{}"}, true, a.deleteFolder},
		{"GET", []string{"folder", "{}", "children"}, false, a.folderChildren},
		{"GET", []string{"folder", "{}", "hash"}, false, a.folderHash},
		{"PATCH", []string{"folder", "{}", "childorder"}, true, a.childOrder},
		{"POST", []string{"folder", "{}", "import"}, true, a.importFolder},
		{"DELETE", []string{"folder", "{}", "bookmarks", "{}"}, true, a.removePlacement},
		{"POST", []string{"lock"}, false, a.acquireLock},
		{"DELETE", []string{"lock"}, false, a.releaseLock},
	}
}

func matchSegs(pattern, segs []string) ([]string, bool) {
	if len(pattern) != len(segs) {
		return nil, false
	}
	var params []string
	for i, p := range pattern {
		if p == "{}" {
			params = append(params, segs[i])
		} else if p != segs[i] {
			return nil, false
		}
	}
	return params, true
}

func (a *Adapter) dispatch(w http.ResponseWriter, r *http.Request, sc bookmarks.Scope, rest string) {
	trimmed := strings.TrimPrefix(rest, "/")
	var segs []string
	if trimmed != "" {
		segs = strings.Split(trimmed, "/")
	}
	for _, s := range segs {
		if s == "" {
			writeJSON(w, http.StatusNotFound, envelopeError("not found"))
			return
		}
	}
	var allow []string
	for _, rt := range a.routes {
		params, ok := matchSegs(rt.segs, segs)
		if !ok {
			continue
		}
		if rt.method != r.Method {
			allow = append(allow, rt.method)
			continue
		}
		if rt.mutates && !sc.Writable {
			// Checked before the body or any id is parsed, so a read-only credential
			// learns nothing about validity and can never reach a write path.
			a.fail(w, errReadOnly)
			return
		}
		rt.h(&call{a: a, w: w, r: r, sc: sc, params: params})
		return
	}
	if len(allow) > 0 {
		a.methodNotAllowed(w, allow)
		return
	}
	writeJSON(w, http.StatusNotFound, envelopeError("not found"))
}

var errReadOnly = errors.Join(bookmarks.ErrForbidden, errors.New("read-only scope"))

// ---- ids ----------------------------------------------------------------

var idRe = regexp.MustCompile(`^(-1|[0-9]{1,18})$`)

func parseID(s string) (int64, bool) {
	if !idRe.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// flexID accepts a JSON number or numeric string, as floccus sends both (the root
// is -1 or "-1"). Anything else (floats, exponents, junk, booleans) is an error.
type flexID int64

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = str
	}
	n, ok := parseID(s)
	if !ok {
		return errors.New("invalid id")
	}
	*f = flexID(n)
	return nil
}

func ids(in []flexID) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}

func (c *call) idParam(i int) (int64, bool) {
	id, ok := parseID(c.params[i])
	if !ok {
		c.a.badRequest(c.w, "invalid id")
	}
	return id, ok
}

// decode reads exactly one JSON value into v.
func (c *call) decode(v any) bool {
	dec := json.NewDecoder(c.r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			c.a.fail(c.w, err)
		} else {
			c.a.badRequest(c.w, "request body is not valid JSON for this endpoint")
		}
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			c.a.fail(c.w, err)
		} else {
			c.a.badRequest(c.w, "unexpected data after the JSON body")
		}
		return false
	}
	return true
}

// intParam parses an optional integer query parameter; garbage is a 400.
func (c *call) intParam(name string, def int) (int, bool) {
	v := c.r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		c.a.badRequest(c.w, "invalid "+name)
		return 0, false
	}
	return n, true
}
