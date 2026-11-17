package bookmarks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// Folder is a folder as addressed by a scope. ParentID is RootID for children of
// the (virtual or share) root and 0 for the root itself.
type Folder struct {
	ID        int64
	ParentID  int64
	Title     string
	Position  int
	Canonical bool
}

// Node is a child in a tree listing. Loaded distinguishes a folder whose children
// were included (possibly none) from one that was not expanded.
type Node struct {
	Type     string // "folder" or "bookmark"
	ID       int64
	Title    string
	URL      string
	Loaded   bool
	Children []Node
}

// ChildRef names a child for SetChildOrder.
type ChildRef struct {
	Type string
	ID   int64
}

// frow is a folder row. id 0 is the owner's virtual root.
type frow struct {
	id, owner int64
	parent    int64 // 0 = child of virtual root
	kind      string
	title     string
	position  int
	virtual   bool // the private virtual root
	scopeRoot bool // the share root (addressed as -1)
}

func (f frow) isRoot() bool { return f.virtual || f.scopeRoot }

// visibleFolder resolves a scope-relative id to a live folder inside the scope.
// Anything else, including other users' folders, is ErrNotFound.
func (s *Service) visibleFolder(ctx context.Context, q queryer, sc Scope, id int64) (frow, error) {
	if id == RootID {
		if sc.RootFolderID == 0 {
			return frow{virtual: true, owner: sc.OwnerID}, nil
		}
		id = sc.RootFolderID
	}
	if id <= 0 {
		return frow{}, ErrNotFound
	}
	var f frow
	var parent, deleted sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT id, owner_id, parent_id, kind, title, position, deleted_at FROM folders WHERE id = ?`, id).
		Scan(&f.id, &f.owner, &parent, &f.kind, &f.title, &f.position, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return frow{}, ErrNotFound
	}
	if err != nil {
		return frow{}, err
	}
	if f.owner != sc.OwnerID || deleted.Valid {
		return frow{}, ErrNotFound
	}
	f.parent = parent.Int64
	if sc.RootFolderID != 0 {
		if f.id == sc.RootFolderID {
			f.scopeRoot = true
		} else if ok, err := s.isWithin(ctx, q, f.id, sc.RootFolderID); err != nil {
			return frow{}, err
		} else if !ok {
			return frow{}, ErrNotFound
		}
	}
	return f, nil
}

// isWithin reports whether folderID equals ancestorID or lies below it.
func (s *Service) isWithin(ctx context.Context, q queryer, folderID, ancestorID int64) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `
		WITH RECURSIVE up(id) AS (
			SELECT ?1 UNION SELECT f.parent_id FROM folders f JOIN up ON f.id = up.id WHERE f.parent_id IS NOT NULL)
		SELECT 1 FROM up WHERE id = ?2 LIMIT 1`, folderID, ancestorID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) depthOf(ctx context.Context, q queryer, folderID int64) (int, error) {
	if folderID == 0 {
		return 0, nil
	}
	var n int
	err := q.QueryRowContext(ctx, `
		WITH RECURSIVE up(id) AS (
			SELECT ?1 UNION SELECT f.parent_id FROM folders f JOIN up ON f.id = up.id WHERE f.parent_id IS NOT NULL)
		SELECT COUNT(*) FROM up`, folderID).Scan(&n)
	return n, err
}

// subtreeHeight is the number of folder levels in the subtree rooted at id (>=1).
func (s *Service) subtreeHeight(ctx context.Context, q queryer, id int64) (int, error) {
	var h int
	err := q.QueryRowContext(ctx, `
		WITH RECURSIVE sub(id, d) AS (
			SELECT ?1, 1 UNION ALL
			SELECT f.id, sub.d + 1 FROM folders f JOIN sub ON f.parent_id = sub.id WHERE f.deleted_at IS NULL)
		SELECT MAX(d) FROM sub`, id).Scan(&h)
	return h, err
}

func (s *Service) checkDepth(ctx context.Context, q queryer, parentReal int64, below int) error {
	d, err := s.depthOf(ctx, q, parentReal)
	if err != nil {
		return err
	}
	if d+below > MaxFolderDepth {
		return invalid("folders nested deeper than %d levels", MaxFolderDepth)
	}
	return nil
}

// nextPosition is the position after every live child (folder or bookmark) of a
// folder. parentReal 0 means the owner's virtual root.
func (s *Service) nextPosition(ctx context.Context, q queryer, owner, parentReal int64) (int, error) {
	var pos sql.NullInt64
	var err error
	if parentReal == 0 {
		err = q.QueryRowContext(ctx, `SELECT MAX(position) FROM folders WHERE owner_id = ? AND parent_id IS NULL AND deleted_at IS NULL`, owner).Scan(&pos)
	} else {
		err = q.QueryRowContext(ctx, `
			SELECT MAX(p) FROM (
				SELECT MAX(position) AS p FROM folders WHERE parent_id = ?1 AND deleted_at IS NULL
				UNION ALL
				SELECT MAX(pl.position) FROM placements pl JOIN bookmarks b ON b.id = pl.bookmark_id
				WHERE pl.folder_id = ?1 AND pl.deleted_at IS NULL AND b.deleted_at IS NULL)`, parentReal).Scan(&pos)
	}
	if err != nil {
		return 0, err
	}
	if !pos.Valid {
		return 0, nil
	}
	return int(pos.Int64) + 1, nil
}

func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// ---- tree loading -------------------------------------------------------

type tnode struct {
	folder   bool
	id       int64
	title    string
	url      string
	position int
}

type tree struct {
	folders map[int64]*frow
	kids    map[int64][]tnode // key: parent real id, 0 = virtual root
}

func sortKids(ks []tnode) {
	sort.SliceStable(ks, func(i, j int) bool {
		if ks[i].position != ks[j].position {
			return ks[i].position < ks[j].position
		}
		if ks[i].folder != ks[j].folder {
			return ks[i].folder
		}
		return ks[i].id < ks[j].id
	})
}

// loadTree loads every live folder and placement below rootReal (0 = the owner's
// whole collection) in two queries.
func (s *Service) loadTree(ctx context.Context, q queryer, owner, rootReal int64) (*tree, error) {
	t := &tree{folders: map[int64]*frow{}, kids: map[int64][]tnode{}}
	var frows *sql.Rows
	var err error
	const fcols = `f.id, f.parent_id, f.kind, f.title, f.position`
	if rootReal == 0 {
		frows, err = q.QueryContext(ctx, `SELECT `+fcols+` FROM folders f WHERE f.owner_id = ? AND f.deleted_at IS NULL`, owner)
	} else {
		frows, err = q.QueryContext(ctx, `
			WITH RECURSIVE sub(id) AS (
				SELECT ?1 UNION ALL SELECT c.id FROM folders c JOIN sub ON c.parent_id = sub.id WHERE c.deleted_at IS NULL)
			SELECT `+fcols+` FROM folders f WHERE f.id IN (SELECT id FROM sub) AND f.owner_id = ?2 AND f.deleted_at IS NULL`, rootReal, owner)
	}
	if err != nil {
		return nil, err
	}
	for frows.Next() {
		var f frow
		var parent sql.NullInt64
		if err := frows.Scan(&f.id, &parent, &f.kind, &f.title, &f.position); err != nil {
			frows.Close()
			return nil, err
		}
		f.parent, f.owner = parent.Int64, owner
		t.folders[f.id] = &f
		if f.id != rootReal {
			t.kids[f.parent] = append(t.kids[f.parent], tnode{folder: true, id: f.id, title: f.title, position: f.position})
		}
	}
	if err := frows.Err(); err != nil {
		frows.Close()
		return nil, err
	}
	frows.Close()

	var prows *sql.Rows
	const pcols = `p.folder_id, p.position, b.id, b.title, b.url`
	if rootReal == 0 {
		prows, err = q.QueryContext(ctx, `
			SELECT `+pcols+` FROM placements p JOIN bookmarks b ON b.id = p.bookmark_id JOIN folders f ON f.id = p.folder_id
			WHERE f.owner_id = ? AND f.deleted_at IS NULL AND p.deleted_at IS NULL AND b.deleted_at IS NULL`, owner)
	} else {
		prows, err = q.QueryContext(ctx, `
			WITH RECURSIVE sub(id) AS (
				SELECT ?1 UNION ALL SELECT c.id FROM folders c JOIN sub ON c.parent_id = sub.id WHERE c.deleted_at IS NULL)
			SELECT `+pcols+` FROM placements p JOIN bookmarks b ON b.id = p.bookmark_id
			WHERE p.folder_id IN (SELECT id FROM sub) AND p.deleted_at IS NULL AND b.deleted_at IS NULL AND b.owner_id = ?2`, rootReal, owner)
	}
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var folder int64
		var n tnode
		if err := prows.Scan(&folder, &n.position, &n.id, &n.title, &n.url); err != nil {
			return nil, err
		}
		t.kids[folder] = append(t.kids[folder], n)
	}
	if err := prows.Err(); err != nil {
		return nil, err
	}
	for k := range t.kids {
		sortKids(t.kids[k])
	}
	return t, nil
}

// build converts children of parent into Nodes. remaining is the number of levels to
// return (-1 = all); folders at the last level are not expanded.
func (t *tree) build(sc Scope, parent int64, remaining int, foldersOnly bool) []Node {
	out := []Node{}
	for _, k := range t.kids[parent] {
		if !k.folder {
			if !foldersOnly {
				out = append(out, Node{Type: typeBookmark, ID: k.id, Title: k.title, URL: k.url})
			}
			continue
		}
		n := Node{Type: typeFolder, ID: sc.out(k.id), Title: k.title}
		if remaining == -1 || remaining > 1 {
			next := remaining
			if remaining > 0 {
				next--
			}
			n.Loaded = true
			n.Children = t.build(sc, k.id, next, foldersOnly)
		}
		out = append(out, n)
	}
	return out
}

func realRoot(f frow) int64 {
	if f.virtual {
		return 0
	}
	return f.id
}

// Children lists the children of a folder. layers is -1 (everything below), or
// 1..MaxChildLayers levels; folders beyond that depth are returned unexpanded.
func (s *Service) Children(ctx context.Context, sc Scope, id int64, layers int) ([]Node, error) {
	if layers != -1 && (layers < 1 || layers > MaxChildLayers) {
		return nil, invalid("layers must be -1 or between 1 and %d", MaxChildLayers)
	}
	var out []Node
	err := s.read(ctx, func(q queryer) error {
		f, err := s.visibleFolder(ctx, q, sc, id)
		if err != nil {
			return err
		}
		t, err := s.loadTree(ctx, q, sc.OwnerID, realRoot(f))
		if err != nil {
			return err
		}
		out = t.build(sc, realRoot(f), layers, false)
		return nil
	})
	return out, err
}

// FolderTree lists only folders below id. layers 0 returns the direct child folders
// unexpanded; -1 expands everything; N>=1 expands N levels.
func (s *Service) FolderTree(ctx context.Context, sc Scope, id int64, layers int) ([]Node, error) {
	if layers != -1 && (layers < 0 || layers > MaxChildLayers) {
		return nil, invalid("layers must be -1 or between 0 and %d", MaxChildLayers)
	}
	remaining := layers
	if layers == 0 {
		remaining = 1
	}
	var out []Node
	err := s.read(ctx, func(q queryer) error {
		f, err := s.visibleFolder(ctx, q, sc, id)
		if err != nil {
			return err
		}
		t, err := s.loadTree(ctx, q, sc.OwnerID, realRoot(f))
		if err != nil {
			return err
		}
		out = t.build(sc, realRoot(f), remaining, true)
		return nil
	})
	return out, err
}

func (s *Service) presentFolder(sc Scope, f frow) Folder {
	out := Folder{ID: sc.out(f.id), Title: f.title, Position: f.position, Canonical: f.kind != kindFolder}
	switch {
	case f.virtual:
		out.ID, out.ParentID = RootID, 0
	case f.scopeRoot:
		out.ParentID = 0
	case f.parent == 0:
		out.ParentID = RootID
	default:
		out.ParentID = sc.out(f.parent)
	}
	return out
}

func (s *Service) GetFolder(ctx context.Context, sc Scope, id int64) (Folder, error) {
	var out Folder
	err := s.read(ctx, func(q queryer) error {
		f, err := s.visibleFolder(ctx, q, sc, id)
		if err != nil {
			return err
		}
		out = s.presentFolder(sc, f)
		return nil
	})
	return out, err
}

func isCanonicalTitle(t string) bool { return t == canonicalBar || t == canonicalOther }

func (s *Service) CreateFolder(ctx context.Context, sc Scope, parentID int64, title string) (Folder, error) {
	if err := validTitle("folder title", title); err != nil {
		return Folder{}, err
	}
	var out Folder
	err := s.write(ctx, sc, func(tx *sql.Tx) error {
		parent, err := s.visibleFolder(ctx, tx, sc, parentID)
		if err != nil {
			return err
		}
		if parent.virtual && isCanonicalTitle(title) {
			return invalid("%q is reserved for the canonical root folders", title)
		}
		if err := s.checkDepth(ctx, tx, realRoot(parent), 1); err != nil {
			return err
		}
		pos, err := s.nextPosition(ctx, tx, sc.OwnerID, realRoot(parent))
		if err != nil {
			return err
		}
		now := s.ms()
		res, err := tx.ExecContext(ctx, `INSERT INTO folders(owner_id, parent_id, kind, title, position, created_at, updated_at) VALUES (?, ?, 'folder', ?, ?, ?, ?)`,
			sc.OwnerID, nullable(realRoot(parent)), title, pos, now, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		out = Folder{ID: id, ParentID: sc.out(parent.id), Title: title, Position: pos}
		if parent.virtual || parent.scopeRoot {
			out.ParentID = RootID
		}
		return nil
	})
	return out, err
}

// UpdateFolder renames and/or moves a folder. A rename keeps the position; a move
// appends at the end of the new parent. Cycles, canonical roots, the scope root and
// out-of-scope parents are rejected before anything is written.
func (s *Service) UpdateFolder(ctx context.Context, sc Scope, id int64, parentID *int64, title *string) (Folder, error) {
	if title != nil {
		if err := validTitle("folder title", *title); err != nil {
			return Folder{}, err
		}
	}
	var out Folder
	err := s.write(ctx, sc, func(tx *sql.Tx) error {
		f, err := s.visibleFolder(ctx, tx, sc, id)
		if err != nil {
			return err
		}
		newTitle := f.title
		if title != nil {
			newTitle = *title
		}
		newParent := f.parent
		var newParentRow *frow
		if parentID != nil {
			p, err := s.visibleFolder(ctx, tx, sc, *parentID)
			if err != nil {
				return err
			}
			newParentRow = &p
			newParent = realRoot(p)
		}
		changed := newTitle != f.title || newParent != f.parent
		if f.isRoot() || (f.kind != kindFolder && changed) {
			return invalid("the root and canonical folders cannot be renamed, moved or deleted")
		}
		if newParentRow != nil && newParent != f.parent {
			if newParent == f.id {
				return invalid("a folder cannot be its own parent")
			}
			if newParent != 0 {
				if cyc, err := s.isWithin(ctx, tx, newParent, f.id); err != nil {
					return err
				} else if cyc {
					return invalid("moving a folder below itself would create a cycle")
				}
			}
		}
		atVirtualRoot := newParent == 0
		if atVirtualRoot && isCanonicalTitle(newTitle) && changed {
			return invalid("%q is reserved for the canonical root folders", newTitle)
		}
		if newParent != f.parent {
			h, err := s.subtreeHeight(ctx, tx, f.id)
			if err != nil {
				return err
			}
			if err := s.checkDepth(ctx, tx, newParent, h); err != nil {
				return err
			}
		}
		pos := f.position
		if newParent != f.parent {
			if pos, err = s.nextPosition(ctx, tx, sc.OwnerID, newParent); err != nil {
				return err
			}
		}
		if changed {
			if _, err := tx.ExecContext(ctx, `UPDATE folders SET title = ?, parent_id = ?, position = ?, updated_at = ? WHERE id = ?`,
				newTitle, nullable(newParent), pos, s.ms(), f.id); err != nil {
				return err
			}
		}
		f.title, f.parent, f.position = newTitle, newParent, pos
		out = s.presentFolder(sc, f)
		return nil
	})
	return out, err
}

// DeleteFolder soft-deletes a folder subtree (Trash): folders and their placements keep
// their rows for restore. A bookmark that loses its last live placement is trashed too;
// placements in folders outside the subtree stay alive.
func (s *Service) DeleteFolder(ctx context.Context, sc Scope, id int64) error {
	return s.write(ctx, sc, func(tx *sql.Tx) error {
		f, err := s.visibleFolder(ctx, tx, sc, id)
		if err != nil {
			return err
		}
		if f.isRoot() || f.kind != kindFolder {
			return invalid("the root and canonical folders cannot be renamed, moved or deleted")
		}
		return s.trashSubtree(ctx, tx, f.id)
	})
}

func (s *Service) trashSubtree(ctx context.Context, tx *sql.Tx, rootID int64) error {
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE sub(id) AS (
			SELECT ?1 UNION ALL SELECT c.id FROM folders c JOIN sub ON c.parent_id = sub.id WHERE c.deleted_at IS NULL)
		SELECT id FROM sub`, rootID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	list, _ := json.Marshal(ids)
	now := s.ms()
	// Bookmarks touched by the subtree, before their placements are trashed.
	if _, err := tx.ExecContext(ctx, `UPDATE placements SET deleted_at = ?1 WHERE deleted_at IS NULL AND folder_id IN (SELECT value FROM json_each(?2))`, now, string(list)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE folders SET deleted_at = ?1, updated_at = ?1 WHERE deleted_at IS NULL AND id IN (SELECT value FROM json_each(?2))`, now, string(list)); err != nil {
		return err
	}
	return s.trashOrphanedBookmarks(ctx, tx, now)
}

// trashOrphanedBookmarks soft-deletes live bookmarks that no longer have a live
// placement. Only bookmarks that have (trashed) placements are considered.
func (s *Service) trashOrphanedBookmarks(ctx context.Context, q queryer, now int64) error {
	_, err := q.ExecContext(ctx, `
		UPDATE bookmarks SET deleted_at = ?1, updated_at = ?1
		WHERE deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM placements p WHERE p.bookmark_id = bookmarks.id AND p.deleted_at IS NULL)
		  AND EXISTS (SELECT 1 FROM placements p WHERE p.bookmark_id = bookmarks.id AND p.deleted_at = ?1)`, now)
	return err
}

// SetChildOrder applies an explicit order to the children of a folder. Every entry must
// be a live child; duplicates or foreign entries reject the whole request. Children not
// listed follow the listed ones in their existing order.
func (s *Service) SetChildOrder(ctx context.Context, sc Scope, id int64, order []ChildRef) error {
	return s.write(ctx, sc, func(tx *sql.Tx) error {
		f, err := s.visibleFolder(ctx, tx, sc, id)
		if err != nil {
			return err
		}
		t, err := s.loadChildren(ctx, tx, sc.OwnerID, realRoot(f))
		if err != nil {
			return err
		}
		have := map[ChildRef]bool{}
		for _, k := range t {
			if k.folder {
				have[ChildRef{typeFolder, k.id}] = true
			} else {
				have[ChildRef{typeBookmark, k.id}] = true
			}
		}
		seen := map[ChildRef]bool{}
		final := make([]ChildRef, 0, len(t))
		for _, r := range order {
			if r.Type != typeFolder && r.Type != typeBookmark {
				return invalid("unknown child type %q", r.Type)
			}
			if !have[r] {
				return invalid("%s %d is not a child of this folder", r.Type, r.ID)
			}
			if seen[r] {
				return invalid("duplicate %s %d in order", r.Type, r.ID)
			}
			seen[r] = true
			final = append(final, r)
		}
		for _, k := range t {
			r := ChildRef{typeBookmark, k.id}
			if k.folder {
				r.Type = typeFolder
			}
			if !seen[r] {
				final = append(final, r)
			}
		}
		for i, r := range final {
			var err error
			if r.Type == typeFolder {
				_, err = tx.ExecContext(ctx, `UPDATE folders SET position = ? WHERE id = ?`, i, r.ID)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE placements SET position = ? WHERE bookmark_id = ? AND folder_id = ? AND deleted_at IS NULL`, i, r.ID, realRoot(f))
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// loadChildren returns the sorted live children of one folder (0 = virtual root).
func (s *Service) loadChildren(ctx context.Context, q queryer, owner, parentReal int64) ([]tnode, error) {
	var out []tnode
	var rows *sql.Rows
	var err error
	if parentReal == 0 {
		rows, err = q.QueryContext(ctx, `SELECT id, title, position FROM folders WHERE owner_id = ? AND parent_id IS NULL AND deleted_at IS NULL`, owner)
	} else {
		rows, err = q.QueryContext(ctx, `SELECT id, title, position FROM folders WHERE owner_id = ?1 AND parent_id = ?2 AND deleted_at IS NULL`, owner, parentReal)
	}
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		n := tnode{folder: true}
		if err := rows.Scan(&n.id, &n.title, &n.position); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if parentReal != 0 {
		prow, err := q.QueryContext(ctx, `
			SELECT b.id, b.title, b.url, p.position FROM placements p JOIN bookmarks b ON b.id = p.bookmark_id
			WHERE p.folder_id = ? AND p.deleted_at IS NULL AND b.deleted_at IS NULL`, parentReal)
		if err != nil {
			return nil, err
		}
		defer prow.Close()
		for prow.Next() {
			var n tnode
			if err := prow.Scan(&n.id, &n.title, &n.url, &n.position); err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		if err := prow.Err(); err != nil {
			return nil, err
		}
	}
	sortKids(out)
	return out, nil
}
