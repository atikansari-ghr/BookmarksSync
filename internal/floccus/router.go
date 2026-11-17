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

// idText returns the textual form of a JSON id: the digits of a number, or the contents of a string.
// Anything else (objects, arrays, booleans, null) is not an id.
func idText(b []byte) (string, error) {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return "", err
		}
		return str, nil
	}
	if s == "" || !(s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) {
		return "", errors.New("invalid id")
	}
	return s, nil
}

// flexFolderID is a folder reference in a JSON body as floccus sends it: a wire folder id as a number
// or numeric string. It is stored in the domain's scope-relative form: bookmarks.RootID (-1) for the
// virtual root, otherwise the positive internal folder id. Positive and malformed ids are errors.
type flexFolderID int64

func (f *flexFolderID) UnmarshalJSON(b []byte) error {
	s, err := idText(b)
	if err != nil {
		return err
	}
	id, ok := folderWireToInternal(s)
	if !ok {
		return errors.New("invalid folder id")
	}
	*f = flexFolderID(id)
	return nil
}

func folderIDs(in []flexFolderID) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}

// flexRawID keeps a JSON id as text so it can be interpreted by the entry's declared type
// (child-order entries mix folder and bookmark ids).
type flexRawID string

func (f *flexRawID) UnmarshalJSON(b []byte) error {
	s, err := idText(b)
	if err != nil {
		return err
	}
	*f = flexRawID(s)
	return nil
}

// folderWireToInternal parses a wire folder id (path, query or text) into the domain's scope-relative
// form: bookmarks.RootID for -1, otherwise the positive internal id.
func folderWireToInternal(s string) (int64, bool) {
	ref, err := parseWireFolder(s)
	if err != nil {
		return 0, false
	}
	if ref.IsRoot() {
		return bookmarks.RootID, true
	}
	id, err := ref.Concrete()
	return id, err == nil
}

// folderOut turns a domain folder id into its wire form: the root alias stays -1, a concrete folder
// becomes negative. Any other value would put a positive folder id on the wire, so it is an error.
func folderOut(id int64) (int64, error) {
	if id == bookmarks.RootID {
		return -1, nil
	}
	ref, err := folderRefFromInternal(id)
	if err != nil {
		return 0, err
	}
	return ref.Wire(), nil
}

// folderParam reads the folder id of path parameter i; invalid or positive ids are a 400.
func (c *call) folderParam(i int) (int64, bool) {
	id, ok := folderWireToInternal(c.params[i])
	if !ok {
		c.a.badRequest(c.w, "invalid folder id")
	}
	return id, ok
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
