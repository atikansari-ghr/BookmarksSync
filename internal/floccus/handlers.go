package floccus

import (
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
)

// ---- JSON shapes --------------------------------------------------------

type bookmarkJSON struct {
	ID          int64    `json:"id"`
	URL         string   `json:"url"`
	Target      string   `json:"target,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Folders     []int64  `json:"folders"`
}

func isJS(u string) bool { return len(u) >= 11 && strings.EqualFold(u[:11], "javascript:") }

// For javascript: bookmarks, `target` carries the code, which floccus reads as
// `item.target || item.url`. The URL is stored and echoed only, never executed.
func toBookmarkJSON(b bookmarks.Bookmark) bookmarkJSON {
	out := bookmarkJSON{ID: b.ID, URL: b.URL, Title: b.Title, Description: "", Tags: b.Tags, Folders: b.Folders}
	if isJS(b.URL) {
		out.Target = b.URL
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.Folders == nil {
		out.Folders = []int64{}
	}
	return out
}

type nodeJSON struct {
	Type     string      `json:"type"`
	ID       int64       `json:"id"`
	Title    string      `json:"title"`
	URL      string      `json:"url,omitempty"`
	Target   string      `json:"target,omitempty"`
	Children *[]nodeJSON `json:"children,omitempty"`
}

func toNodes(ns []bookmarks.Node) []nodeJSON {
	out := make([]nodeJSON, 0, len(ns))
	for _, n := range ns {
		j := nodeJSON{Type: n.Type, ID: n.ID, Title: n.Title}
		if n.Type == "bookmark" {
			j.URL = n.URL
			if isJS(n.URL) {
				j.Target = n.URL
			}
		}
		if n.Loaded {
			kids := toNodes(n.Children)
			j.Children = &kids
		}
		out = append(out, j)
	}
	return out
}

type folderListJSON struct {
	ID       int64             `json:"id"`
	Title    string            `json:"title"`
	Children *[]folderListJSON `json:"children,omitempty"`
}

func toFolderList(ns []bookmarks.Node) []folderListJSON {
	out := make([]folderListJSON, 0, len(ns))
	for _, n := range ns {
		j := folderListJSON{ID: n.ID, Title: n.Title}
		if n.Loaded {
			kids := toFolderList(n.Children)
			j.Children = &kids
		}
		out = append(out, j)
	}
	return out
}

func folderItem(f bookmarks.Folder) map[string]any {
	return map[string]any{"id": f.ID, "title": f.Title, "parent_folder": f.ParentID}
}

// ---- bookmarks ----------------------------------------------------------

func (a *Adapter) listBookmarks(c *call) {
	q := c.r.URL.Query()
	url := q.Get("url")
	limitDefault := 10
	if url != "" {
		limitDefault = 100
	}
	page, ok := c.intParam("page", 0)
	if !ok {
		return
	}
	limit, ok := c.intParam("limit", limitDefault)
	if !ok {
		return
	}
	search := append(append([]string{}, q["search[]"]...), q["search"]...)
	list, err := a.svc.ListBookmarks(c.r.Context(), c.sc, bookmarks.ListQuery{Page: page, Limit: limit, URL: url, Search: search})
	if err != nil {
		a.fail(c.w, err)
		return
	}
	out := make([]bookmarkJSON, 0, len(list))
	for _, b := range list {
		out = append(out, toBookmarkJSON(b))
	}
	success(c.w, map[string]any{"data": out})
}

type bookmarkBody struct {
	URL     *string   `json:"url"`
	Title   *string   `json:"title"`
	Folders *[]flexID `json:"folders"`
	Tags    *[]string `json:"tags"`
}

func (a *Adapter) createBookmark(c *call) {
	var body bookmarkBody
	if !c.decode(&body) {
		return
	}
	if body.URL == nil {
		a.badRequest(c.w, "url is required")
		return
	}
	in := bookmarks.BookmarkInput{URL: *body.URL, Tags: body.Tags}
	if body.Title != nil {
		in.Title = *body.Title
	}
	if body.Folders != nil {
		in.Folders = ids(*body.Folders)
	}
	b, err := a.svc.CreateBookmark(c.r.Context(), c.sc, in)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"item": toBookmarkJSON(b)})
}

func (a *Adapter) getBookmark(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	b, err := a.svc.GetBookmark(c.r.Context(), c.sc, id)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"item": toBookmarkJSON(b)})
}

func (a *Adapter) updateBookmark(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	var body bookmarkBody
	if !c.decode(&body) {
		return
	}
	up := bookmarks.BookmarkUpdate{URL: body.URL, Title: body.Title, Tags: body.Tags}
	if body.Folders != nil {
		f := ids(*body.Folders)
		up.Folders = &f
	}
	b, err := a.svc.UpdateBookmark(c.r.Context(), c.sc, id, up)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"item": toBookmarkJSON(b)})
}

func (a *Adapter) deleteBookmark(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	if err := a.svc.DeleteBookmark(c.r.Context(), c.sc, id); err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, nil)
}

func (a *Adapter) removePlacement(c *call) {
	folder, ok := c.idParam(0)
	if !ok {
		return
	}
	bm, ok := c.idParam(1)
	if !ok {
		return
	}
	if err := a.svc.RemovePlacement(c.r.Context(), c.sc, folder, bm); err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, nil)
}

// clickBookmark accepts a click for a valid URL that is visible to the caller. No
// click statistics exist, so nothing is recorded.
func (a *Adapter) clickBookmark(c *call) {
	var body struct {
		URL *string `json:"url"`
	}
	if !c.decode(&body) {
		return
	}
	if body.URL == nil {
		a.badRequest(c.w, "url is required")
		return
	}
	if err := bookmarks.ValidateURL(*body.URL); err != nil {
		a.fail(c.w, err)
		return
	}
	list, err := a.svc.ListBookmarks(c.r.Context(), c.sc, bookmarks.ListQuery{Page: 0, Limit: 1, URL: *body.URL})
	if err != nil {
		a.fail(c.w, err)
		return
	}
	if len(list) == 0 {
		a.fail(c.w, bookmarks.ErrNotFound)
		return
	}
	success(c.w, nil)
}

// ---- folders ------------------------------------------------------------

func (a *Adapter) folderTree(c *call) {
	q := c.r.URL.Query()
	root := int64(-1)
	if v := q.Get("root"); v != "" {
		id, ok := parseID(v)
		if !ok {
			a.badRequest(c.w, "invalid root")
			return
		}
		root = id
	}
	layers, ok := c.intParam("layers", -1)
	if !ok {
		return
	}
	ns, err := a.svc.FolderTree(c.r.Context(), c.sc, root, layers)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"data": toFolderList(ns)})
}

type folderBody struct {
	Parent *flexID `json:"parent_folder"`
	Title  *string `json:"title"`
}

func (a *Adapter) createFolder(c *call) {
	var body folderBody
	if !c.decode(&body) {
		return
	}
	if body.Title == nil {
		a.badRequest(c.w, "title is required")
		return
	}
	parent := int64(-1)
	if body.Parent != nil {
		parent = int64(*body.Parent)
	}
	f, err := a.svc.CreateFolder(c.r.Context(), c.sc, parent, *body.Title)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"item": folderItem(f)})
}

func (a *Adapter) getFolder(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	f, err := a.svc.GetFolder(c.r.Context(), c.sc, id)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"item": folderItem(f)})
}

func (a *Adapter) updateFolder(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	var body folderBody
	if !c.decode(&body) {
		return
	}
	var parent *int64
	if body.Parent != nil {
		p := int64(*body.Parent)
		parent = &p
	}
	f, err := a.svc.UpdateFolder(c.r.Context(), c.sc, id, parent, body.Title)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"item": folderItem(f)})
}

func (a *Adapter) deleteFolder(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	if err := a.svc.DeleteFolder(c.r.Context(), c.sc, id); err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, nil)
}

func (a *Adapter) folderChildren(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	layers, ok := c.intParam("layers", -1)
	if !ok {
		return
	}
	ns, err := a.svc.Children(c.r.Context(), c.sc, id, layers)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"data": toNodes(ns)})
}

// folderHash serves the sha256 title+url hash. Anything else (other functions, tag
// fields) is refused; floccus then falls back to its "changed" sentinel.
func (a *Adapter) folderHash(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	if fn := q.Get("hashFn"); fn != "" && fn != "sha256" {
		a.badRequest(c.w, "unsupported hashFn; only sha256 is available")
		return
	}
	if fields := q["fields[]"]; len(fields) > 0 && !(len(fields) == 2 && fields[0] == "title" && fields[1] == "url") {
		a.badRequest(c.w, "unsupported hash fields; only title and url are hashed")
		return
	}
	h, err := a.svc.FolderHash(c.r.Context(), c.sc, id)
	if err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, map[string]any{"data": h})
}

func (a *Adapter) childOrder(c *call) {
	id, ok := c.idParam(0)
	if !ok {
		return
	}
	var body struct {
		Data *[]struct {
			ID   *flexID `json:"id"`
			Type string  `json:"type"`
		} `json:"data"`
	}
	if !c.decode(&body) {
		return
	}
	if body.Data == nil {
		a.badRequest(c.w, "data is required")
		return
	}
	refs := make([]bookmarks.ChildRef, 0, len(*body.Data))
	for _, d := range *body.Data {
		if d.ID == nil {
			a.badRequest(c.w, "every entry needs an id")
			return
		}
		refs = append(refs, bookmarks.ChildRef{Type: d.Type, ID: int64(*d.ID)})
	}
	if err := a.svc.SetChildOrder(c.r.Context(), c.sc, id, refs); err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, nil)
}

// ---- locks --------------------------------------------------------------

func (a *Adapter) acquireLock(c *call) {
	if err := a.svc.AcquireLock(c.r.Context(), c.sc); err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, nil)
}

func (a *Adapter) releaseLock(c *call) {
	if err := a.svc.ReleaseLock(c.r.Context(), c.sc); err != nil {
		a.fail(c.w, err)
		return
	}
	success(c.w, nil)
}
