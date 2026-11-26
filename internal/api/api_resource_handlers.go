package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

func (a *API) serveFolders(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/folders") {
		return false
	}
	if r.Method == http.MethodDelete && strings.Contains(p, "/bookmarks/") {
		return false
	}
	sc, err := a.scope(r, r.Method != http.MethodGet)
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	if p == "/api/v1/folders" {
		switch r.Method {
		case http.MethodGet:
			id, e1 := queryInt(r, "parent", bookmarks.RootID)
			depth, e2 := queryInt(r, "depth", 2)
			if e1 != nil || e2 != nil || depth < 1 || depth > bookmarks.MaxChildLayers {
				server.WriteError(w, 400, "invalid_request", "invalid folder query")
				return true
			}
			nodes, err := a.bm.FolderTree(r.Context(), sc, id, int(depth))
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, map[string]any{"folders": treeJSON(nodes)})
			}
			return true
		case http.MethodPost:
			var in struct {
				ParentID int64  `json:"parentId"`
				Title    string `json:"title"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			f, err := a.bm.CreateFolder(r.Context(), sc, in.ParentID, in.Title)
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 201, folderJSON(f))
			}
			return true
		}
	}
	if strings.HasPrefix(p, "/api/v1/folders/") {
		if strings.HasSuffix(p, "/order") && r.Method == http.MethodPut {
			path := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/folders/"), "/order")
			id, e := strconv.ParseInt(strings.Trim(path, "/"), 10, 64)
			if e != nil {
				writeDomainError(w, bookmarks.ErrNotFound)
				return true
			}
			var in struct {
				Children []bookmarks.ChildRef `json:"children"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			if err := a.bm.SetChildOrder(r.Context(), sc, id, in.Children); err != nil {
				writeDomainError(w, err)
			} else {
				empty(w)
			}
			return true
		}
		id, e := requestID(r, "id")
		if e != nil {
			writeDomainError(w, bookmarks.ErrNotFound)
			return true
		}
		switch r.Method {
		case http.MethodGet:
			f, err := a.bm.GetFolder(r.Context(), sc, id)
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, folderJSON(f))
			}
			return true
		case http.MethodPatch:
			var in struct {
				ParentID *int64  `json:"parentId"`
				Title    *string `json:"title"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			f, err := a.bm.UpdateFolder(r.Context(), sc, id, in.ParentID, in.Title)
			if err != nil {
				writeDomainError(w, err)
			} else {
				writeJSON(w, 200, folderJSON(f))
			}
			return true
		case http.MethodDelete:
			if err := a.bm.DeleteFolder(r.Context(), sc, id); err != nil {
				writeDomainError(w, err)
			} else {
				empty(w)
			}
			return true
		}
	}
	return false
}

func (a *API) serveTags(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v1/tags" {
		return false
	}
	sc, err := a.scope(r, false)
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	tags, err := a.bm.ListTags(r.Context(), sc)
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	out := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		out = append(out, map[string]any{"name": t.Name, "count": t.Count})
	}
	writeJSON(w, 200, map[string]any{"tags": out})
	return true
}

func (a *API) serveDuplicates(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v1/duplicates" {
		return false
	}
	sc, err := a.scope(r, false)
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	page, e1 := queryInt(r, "page", 0)
	limit, e2 := queryInt(r, "limit", 50)
	if e1 != nil || e2 != nil || page < 0 || page > 1_000_000 || limit < 1 {
		server.WriteError(w, 400, "invalid_request", "invalid pagination")
		return true
	}
	effective := min(limit, bookmarks.MaxPageLimit)
	list, err := a.bm.ListDuplicates(r.Context(), sc, int(page), int(effective))
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	groups := make([]map[string]any, 0, len(list.Groups))
	for _, g := range list.Groups {
		items := make([]map[string]any, 0, len(g.Bookmarks))
		for _, b := range g.Bookmarks {
			items = append(items, bookmarkJSON(b))
		}
		groups = append(groups, map[string]any{"url": g.URL, "bookmarks": items})
	}
	writeJSON(w, 200, map[string]any{"count": list.Count, "groups": groups, "page": page, "limit": effective, "hasMore": list.HasMore})
	return true
}

// serveLinkChecks queues a public-only link check (POST) and reports one job (GET). Jobs
// are scoped to the caller and the collection or share they were submitted in; submitting
// needs write authority in that scope, so a view-only share cannot queue checks.
func (a *API) serveLinkChecks(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p != "/api/v1/link-checks" && !strings.HasPrefix(p, "/api/v1/link-checks/") {
		return false
	}
	if a.lc == nil {
		server.WriteError(w, 503, "unavailable", "link checking is not available")
		return true
	}
	write := r.Method == http.MethodPost
	sc, err := a.scope(r, write)
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	if write && p == "/api/v1/link-checks" {
		var in struct {
			BookmarkIDs []int64 `json:"bookmarkIds"`
		}
		if !decodeJSON(w, r, &in) {
			return true
		}
		job, err := a.lc.SubmitCheck(r.Context(), sc, in.BookmarkIDs)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		writeJSON(w, 202, linkJobJSON(job))
		return true
	}
	if r.Method == http.MethodGet && p != "/api/v1/link-checks" {
		id, e := requestID(r, "id")
		if e != nil {
			writeDomainError(w, bookmarks.ErrNotFound)
			return true
		}
		job, err := a.lc.CheckStatus(r.Context(), sc, id)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		writeJSON(w, 200, linkJobJSON(job))
		return true
	}
	return false
}

func linkJobJSON(j linkcheck.Job) map[string]any {
	when := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	results := make([]map[string]any, 0, len(j.Results))
	for _, r := range j.Results {
		var httpStatus any
		if r.HTTPStatus != 0 {
			httpStatus = r.HTTPStatus
		}
		results = append(results, map[string]any{"bookmarkId": r.BookmarkID, "url": r.URL, "status": r.Status,
			"httpStatus": httpStatus, "detail": r.Detail, "redirects": r.Redirects, "checkedAt": when(r.CheckedAt)})
	}
	return map[string]any{"id": j.ID, "status": j.Status, "total": j.Total, "done": j.Done,
		"createdAt": when(j.CreatedAt), "startedAt": when(j.StartedAt), "finishedAt": when(j.FinishedAt), "results": results}
}

func (a *API) serveTrash(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p != "/api/v1/trash" && !strings.HasPrefix(p, "/api/v1/trash/") {
		return false
	}
	if r.URL.Query().Has("share") {
		server.WriteError(w, 400, "invalid_request", "trash is available only for a private collection")
		return true
	}
	sc, err := a.scope(r, r.Method != http.MethodGet)
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	if p == "/api/v1/trash" {
		if r.Method == http.MethodGet {
			limit, e1 := queryInt(r, "limit", 100)
			page, e2 := queryInt(r, "page", 0)
			if e1 != nil || e2 != nil || limit < 1 || page < 0 || page > 1_000_000 {
				server.WriteError(w, 400, "invalid_request", "invalid pagination")
				return true
			}
			effective := min(limit, 1000)
			items, err := a.bm.ListTrash(r.Context(), sc, int(effective+1), int(page*effective))
			if err != nil {
				writeDomainError(w, err)
				return true
			}
			hasMore := int64(len(items)) > effective
			if hasMore {
				items = items[:effective]
			}
			out := make([]map[string]any, 0, len(items))
			for _, it := range items {
				out = append(out, map[string]any{"type": it.Type, "id": it.ID, "title": it.Title, "url": it.URL, "deletedAt": it.DeletedAt, "purgeAt": it.PurgeAt})
			}
			writeJSON(w, 200, map[string]any{"items": out, "page": page, "limit": effective, "hasMore": hasMore})
			return true
		}
		if r.Method == http.MethodDelete {
			if err := a.bm.EmptyTrash(r.Context(), sc); err != nil {
				writeDomainError(w, err)
			} else {
				empty(w)
			}
			return true
		}
	}
	if strings.HasSuffix(p, "/restore") && r.Method == http.MethodPost {
		v := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/trash/"), "/restore")
		parts := strings.Split(v, "/")
		if len(parts) != 2 {
			server.WriteError(w, 400, "invalid_request", "invalid trash item")
			return true
		}
		id, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil {
			server.WriteError(w, 400, "invalid_request", "invalid trash item")
			return true
		}
		n, err := a.bm.RestoreTrash(r.Context(), sc, parts[0], id)
		if err != nil {
			writeDomainError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"item": treeJSON([]bookmarks.Node{n})[0]})
		}
		return true
	}
	if r.Method == http.MethodDelete {
		v := strings.TrimPrefix(p, "/api/v1/trash/")
		parts := strings.Split(v, "/")
		if len(parts) != 2 {
			server.WriteError(w, 400, "invalid_request", "invalid trash item")
			return true
		}
		id, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil {
			server.WriteError(w, 400, "invalid_request", "invalid trash item")
			return true
		}
		if err := a.bm.PurgeTrash(r.Context(), sc, parts[0], id); err != nil {
			writeDomainError(w, err)
		} else {
			empty(w)
		}
		return true
	}
	return false
}

func shareJSON(s bookmarks.Share) map[string]any {
	return map[string]any{"id": s.ID, "ownerId": s.OwnerID, "folderId": s.FolderID, "recipientId": s.RecipientID, "mode": s.Mode, "folderTitle": s.FolderTitle}
}
func (a *API) serveShares(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/shares") {
		return false
	}
	me := principalFrom(r).user
	if p == "/api/v1/shares/owned" && r.Method == http.MethodGet {
		ss, err := a.bm.ListShares(r.Context(), me.ID)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		out := make([]map[string]any, 0, len(ss))
		for _, s := range ss {
			out = append(out, shareJSON(s))
		}
		writeJSON(w, 200, map[string]any{"shares": out})
		return true
	}
	if p == "/api/v1/shares/incoming" && r.Method == http.MethodGet {
		ss, err := a.bm.ListIncomingShares(r.Context(), me.ID)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		out := make([]map[string]any, 0, len(ss))
		for _, s := range ss {
			out = append(out, shareJSON(s))
		}
		writeJSON(w, 200, map[string]any{"shares": out})
		return true
	}
	if p == "/api/v1/shares" && r.Method == http.MethodPost {
		var in struct {
			FolderID  int64         `json:"folderId"`
			Recipient string        `json:"recipient"`
			Mode      identity.Mode `json:"mode"`
		}
		if !decodeJSON(w, r, &in) {
			return true
		}
		users, err := a.ids.ListUsers(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		var recipient *identity.UserInfo
		for i := range users {
			if strings.EqualFold(users[i].Username, in.Recipient) && users[i].Active {
				recipient = &users[i]
				break
			}
		}
		if recipient == nil {
			writeDomainError(w, bookmarks.ErrNotFound)
			return true
		}
		s, err := a.bm.GrantShare(r.Context(), me.ID, in.FolderID, recipient.ID, in.Mode)
		if err != nil {
			writeDomainError(w, err)
		} else {
			a.queueUserNotification(me.ID, "sharing")
			a.queueUserNotification(recipient.ID, "sharing")
			writeJSON(w, 201, shareJSON(s))
		}
		return true
	}
	if strings.HasPrefix(p, "/api/v1/shares/") {
		id, e := requestID(r, "id")
		if e != nil {
			writeDomainError(w, bookmarks.ErrNotFound)
			return true
		}
		s, err := a.bm.ShareByID(r.Context(), id)
		if err != nil {
			writeDomainError(w, err)
			return true
		}
		if r.Method == http.MethodPatch {
			if s.OwnerID != me.ID {
				writeDomainError(w, bookmarks.ErrNotFound)
				return true
			}
			var in struct {
				Mode identity.Mode `json:"mode"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			updated, err := a.bm.UpdateShare(r.Context(), me.ID, id, in.Mode)
			if err != nil {
				writeDomainError(w, err)
			} else {
				a.queueUserNotification(s.OwnerID, "sharing")
				a.queueUserNotification(s.RecipientID, "sharing")
				writeJSON(w, 200, shareJSON(updated))
			}
			return true
		}
		if r.Method == http.MethodDelete {
			if s.RecipientID == me.ID {
				err = a.bm.LeaveShare(r.Context(), me.ID, id)
			} else if s.OwnerID == me.ID {
				err = a.bm.RevokeShare(r.Context(), me.ID, s.FolderID, s.RecipientID)
			} else {
				err = bookmarks.ErrNotFound
			}
			if err != nil {
				writeDomainError(w, err)
			} else {
				a.queueUserNotification(s.OwnerID, "sharing")
				a.queueUserNotification(s.RecipientID, "sharing")
				empty(w)
			}
			return true
		}
	}
	return false
}
