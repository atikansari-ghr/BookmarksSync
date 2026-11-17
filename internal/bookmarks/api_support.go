package bookmarks

// Domain support for the /api/v1 user API: scopes for web-session and API-token
// callers (no floccus credential), actor revalidation inside writes, tag listing,
// Trash (list, restore, purge, retention), aggregate statistics and export. All SQL
// stays in this package; handlers only see Scope-checked results.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// TrashRetention is how long trashed items are kept before automatic purge.
const TrashRetention = 30 * 24 * time.Hour

// PrivateScope is the user's own collection for an /api/v1 caller. writable is false
// for read-only callers (an API token without bookmarks:write).
func (s *Service) PrivateScope(ctx context.Context, userID int64, writable bool) (Scope, error) {
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, userID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || err == nil && active != 1 {
		return Scope{}, identity.ErrUnauthorized
	}
	if err != nil {
		return Scope{}, err
	}
	if err := s.EnsureCollection(ctx, userID); err != nil {
		return Scope{}, err
	}
	return Scope{OwnerID: userID, Writable: writable, Actor: userID}, nil
}

// ShareScopeFor is the scope of a share for its recipient, from the live grant: the
// caller must be the recipient and the shared folder must be live. The scope is
// writable only for an edit grant and when allowWrite is true. Anything else is
// ErrNotFound.
func (s *Service) ShareScopeFor(ctx context.Context, recipientID, shareID int64, allowWrite bool) (Scope, error) {
	var owner, folder int64
	var mode string
	err := s.db.QueryRowContext(ctx, `SELECT sh.owner_id, sh.folder_id, sh.mode FROM shares sh
		JOIN folders f ON f.id = sh.folder_id
		JOIN users u ON u.id = sh.recipient_id
		WHERE sh.id = ? AND sh.recipient_id = ? AND f.deleted_at IS NULL AND u.active = 1`, shareID, recipientID).
		Scan(&owner, &folder, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return Scope{}, ErrNotFound
	}
	if err != nil {
		return Scope{}, err
	}
	return Scope{OwnerID: owner, RootFolderID: folder, Actor: recipientID,
		Writable: allowWrite && mode == string(identity.ModeEdit)}, nil
}

// revalidateActor re-derives an API scope's authority from committed state: the actor
// must be active; a private scope must still be the actor's own; a share scope needs
// the live grant (ErrNotFound when revoked or trashed) and, for writes, an edit grant.
func (s *Service) revalidateActor(ctx context.Context, q queryer, sc Scope, write bool) error {
	var active int
	err := q.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, sc.Actor).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || err == nil && active != 1 {
		return identity.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if sc.RootFolderID == 0 {
		if sc.OwnerID != sc.Actor {
			return identity.ErrUnauthorized
		}
		return nil
	}
	var mode string
	err = q.QueryRowContext(ctx, `SELECT sh.mode FROM shares sh JOIN folders f ON f.id = sh.folder_id
		WHERE sh.owner_id = ? AND sh.folder_id = ? AND sh.recipient_id = ? AND f.deleted_at IS NULL`,
		sc.OwnerID, sc.RootFolderID, sc.Actor).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if write && mode != string(identity.ModeEdit) {
		return fmt.Errorf("%w: read-only share", ErrForbidden)
	}
	return nil
}

// ListIncomingShares lists live grants to recipientID, with folder titles.
func (s *Service) ListIncomingShares(ctx context.Context, recipientID int64) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sh.id, sh.owner_id, sh.folder_id, sh.recipient_id, sh.mode, f.title
		FROM shares sh JOIN folders f ON f.id = sh.folder_id
		WHERE sh.recipient_id = ? AND f.deleted_at IS NULL ORDER BY sh.id`, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		var sh Share
		var mode string
		if err := rows.Scan(&sh.ID, &sh.OwnerID, &sh.FolderID, &sh.RecipientID, &mode, &sh.FolderTitle); err != nil {
			return nil, err
		}
		sh.Mode = identity.Mode(mode)
		out = append(out, sh)
	}
	return out, rows.Err()
}

// ShareByID returns a share with its folder title. Callers must check that the caller
// is its owner or recipient before revealing it.
func (s *Service) ShareByID(ctx context.Context, shareID int64) (Share, error) {
	var sh Share
	var mode string
	err := s.db.QueryRowContext(ctx, `SELECT sh.id, sh.owner_id, sh.folder_id, sh.recipient_id, sh.mode, f.title
		FROM shares sh JOIN folders f ON f.id = sh.folder_id WHERE sh.id = ?`, shareID).
		Scan(&sh.ID, &sh.OwnerID, &sh.FolderID, &sh.RecipientID, &mode, &sh.FolderTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	sh.Mode = identity.Mode(mode)
	return sh, nil
}

// TagCount is a tag with the number of visible bookmarks carrying it.
type TagCount struct {
	Name  string
	Count int
}

// ListTags returns the tags of the bookmarks visible in the scope, by name.
func (s *Service) ListTags(ctx context.Context, sc Scope) ([]TagCount, error) {
	cte, cond, args := scopeFilter(sc)
	qargs := []any{}
	if cte != "" {
		qargs = append(qargs, args[0])
		args = args[1:]
	}
	qargs = append(qargs, sc.OwnerID)
	qargs = append(qargs, args...)
	out := []TagCount{}
	err := s.read(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, cte+`SELECT t.tag, count(DISTINCT b.id) FROM bookmark_tags t
			JOIN bookmarks b ON b.id = t.bookmark_id
			WHERE b.owner_id = ? AND b.deleted_at IS NULL AND EXISTS (
				SELECT 1 FROM placements p JOIN folders f ON f.id = p.folder_id
				WHERE p.bookmark_id = b.id AND p.deleted_at IS NULL AND f.deleted_at IS NULL AND `+cond+`)
			GROUP BY t.tag ORDER BY t.tag`, qargs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var tc TagCount
			if err := rows.Scan(&tc.Name, &tc.Count); err != nil {
				return err
			}
			out = append(out, tc)
		}
		return rows.Err()
	})
	return out, err
}

// ---- Trash ----------------------------------------------------------------

// TrashItem is a top-level Trash entry: a trashed folder whose parent was not trashed
// with it (its subtree is part of the entry), or a trashed bookmark that was not
// trashed as part of a folder.
type TrashItem struct {
	Type      string // "folder" or "bookmark"
	ID        int64
	Title     string
	URL       string
	DeletedAt time.Time
	PurgeAt   time.Time
}

// requireTrashScope: Trash is the owner's own private collection only.
func requireTrashScope(sc Scope) error {
	if sc.RootFolderID != 0 {
		return fmt.Errorf("%w: the trash is only available for your own collection", ErrForbidden)
	}
	return nil
}

const trashFolderEntries = `SELECT 'folder', f.id, f.title, '', f.deleted_at FROM folders f
	LEFT JOIN folders p ON p.id = f.parent_id
	WHERE f.owner_id = ?1 AND f.deleted_at IS NOT NULL AND f.kind = 'folder'
	  AND (p.id IS NULL OR p.deleted_at IS NULL OR p.deleted_at <> f.deleted_at)`

const trashBookmarkEntries = `SELECT 'bookmark', b.id, b.title, b.url, b.deleted_at FROM bookmarks b
	WHERE b.owner_id = ?1 AND b.deleted_at IS NOT NULL
	  AND NOT EXISTS (SELECT 1 FROM placements pl JOIN folders f ON f.id = pl.folder_id
		WHERE pl.bookmark_id = b.id AND pl.deleted_at = b.deleted_at AND f.deleted_at = b.deleted_at)`

// ListTrash returns top-level Trash entries, newest first.
func (s *Service) ListTrash(ctx context.Context, sc Scope, limit, offset int) ([]TrashItem, error) {
	if err := requireTrashScope(sc); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageLimit+1 || offset < 0 {
		return nil, invalid("bad trash page")
	}
	out := []TrashItem{}
	err := s.read(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, trashFolderEntries+` UNION ALL `+trashBookmarkEntries+`
			ORDER BY 5 DESC, 2 DESC LIMIT ?2 OFFSET ?3`, sc.OwnerID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it TrashItem
			var deleted int64
			if err := rows.Scan(&it.Type, &it.ID, &it.Title, &it.URL, &deleted); err != nil {
				return err
			}
			it.DeletedAt = time.UnixMilli(deleted).UTC()
			it.PurgeAt = it.DeletedAt.Add(TrashRetention)
			out = append(out, it)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) otherRootID(ctx context.Context, q queryer, owner int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id = ? AND kind = 'other'`, owner).Scan(&id)
	return id, err
}

// RestoreTrash restores a Trash entry. A folder comes back with the subtree, placements
// and bookmarks that were trashed with it, under its original parent, or under Other
// Bookmarks when that parent is trashed or gone. A bookmark regains the placements it
// was trashed with whose folders are live, or a placement in Other Bookmarks. Restored
// items are appended at the end of their parent. Returns the restored item as a Node.
func (s *Service) RestoreTrash(ctx context.Context, sc Scope, typ string, id int64) (Node, error) {
	if err := requireTrashScope(sc); err != nil {
		return Node{}, err
	}
	var out Node
	err := s.write(ctx, sc, func(tx *sql.Tx) error {
		other, err := s.otherRootID(ctx, tx, sc.OwnerID)
		if err != nil {
			return err
		}
		now := s.ms()
		switch typ {
		case typeFolder:
			var title string
			var deleted int64
			var parent sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT title, deleted_at, parent_id FROM folders
				WHERE id = ? AND owner_id = ? AND kind = 'folder' AND deleted_at IS NOT NULL`, id, sc.OwnerID).Scan(&title, &deleted, &parent)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			target := parent.Int64
			var parentLive int
			if parent.Valid {
				err := tx.QueryRowContext(ctx, `SELECT 1 FROM folders WHERE id = ? AND deleted_at IS NULL`, parent.Int64).Scan(&parentLive)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			if parentLive != 1 {
				target = other
			}
			// The subtree trashed together with this folder.
			rows, err := tx.QueryContext(ctx, `WITH RECURSIVE sub(id) AS (
				SELECT ?1 UNION ALL SELECT c.id FROM folders c JOIN sub ON c.parent_id = sub.id WHERE c.deleted_at = ?2)
				SELECT id FROM sub`, id, deleted)
			if err != nil {
				return err
			}
			var ids []int64
			for rows.Next() {
				var fid int64
				if err := rows.Scan(&fid); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, fid)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			list, _ := json.Marshal(ids)
			height, err := s.subtreeHeightTrashed(ctx, tx, ids)
			if err != nil {
				return err
			}
			if err := s.checkDepth(ctx, tx, target, height); err != nil {
				return err
			}
			pos, err := s.nextPosition(ctx, tx, sc.OwnerID, target)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE folders SET deleted_at = NULL, updated_at = ?1
				WHERE id IN (SELECT value FROM json_each(?2))`, now, string(list)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE folders SET parent_id = ?, position = ? WHERE id = ?`, target, pos, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE bookmarks SET deleted_at = NULL, updated_at = ?1
				WHERE deleted_at = ?3 AND id IN (SELECT bookmark_id FROM placements
					WHERE deleted_at = ?3 AND folder_id IN (SELECT value FROM json_each(?2)))`, now, string(list), deleted); err != nil {
				return err
			}
			// Placements come back unless an identical live placement exists already.
			if _, err := tx.ExecContext(ctx, `UPDATE placements SET deleted_at = NULL
				WHERE deleted_at = ?2 AND folder_id IN (SELECT value FROM json_each(?1))
				AND NOT EXISTS (SELECT 1 FROM placements l WHERE l.bookmark_id = placements.bookmark_id
					AND l.folder_id = placements.folder_id AND l.deleted_at IS NULL)`, string(list), deleted); err != nil {
				return err
			}
			out = Node{Type: typeFolder, ID: id, Title: title}
		case typeBookmark:
			var title, url string
			var deleted int64
			err := tx.QueryRowContext(ctx, `SELECT title, url, deleted_at FROM bookmarks
				WHERE id = ? AND owner_id = ? AND deleted_at IS NOT NULL`, id, sc.OwnerID).Scan(&title, &url, &deleted)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE bookmarks SET deleted_at = NULL, updated_at = ? WHERE id = ?`, now, id); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE placements SET deleted_at = NULL
				WHERE bookmark_id = ?1 AND deleted_at = ?2
				AND folder_id IN (SELECT id FROM folders WHERE deleted_at IS NULL)
				AND NOT EXISTS (SELECT 1 FROM placements l WHERE l.bookmark_id = ?1
					AND l.folder_id = placements.folder_id AND l.deleted_at IS NULL)`, id, deleted)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				if _, err := s.addPlacement(ctx, tx, sc.OwnerID, id, other); err != nil {
					return err
				}
			}
			out = Node{Type: typeBookmark, ID: id, Title: title, URL: url}
		default:
			return invalid("type must be folder or bookmark")
		}
		return nil
	})
	return out, err
}

// subtreeHeightTrashed is the folder height of the given subtree ids (1 for a leaf).
func (s *Service) subtreeHeightTrashed(ctx context.Context, q queryer, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	list, _ := json.Marshal(ids)
	var h int
	err := q.QueryRowContext(ctx, `WITH RECURSIVE d(id, depth) AS (
		SELECT ?1, 1 UNION ALL SELECT c.id, d.depth + 1 FROM folders c JOIN d ON c.parent_id = d.id
			WHERE c.id IN (SELECT value FROM json_each(?2)))
		SELECT max(depth) FROM d`, ids[0], string(list)).Scan(&h)
	return h, err
}

// purgeFolders permanently deletes the given trashed folder subtrees (every descendant,
// whenever it was trashed), their placements, shares (and so share credentials), and
// bookmarks left with no placement that are themselves trashed.
func (s *Service) purgeFolders(ctx context.Context, tx *sql.Tx, owner int64, roots []int64) error {
	if len(roots) == 0 {
		return nil
	}
	rl, _ := json.Marshal(roots)
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE sub(id) AS (
		SELECT value FROM json_each(?1) UNION SELECT c.id FROM folders c JOIN sub ON c.parent_id = sub.id)
		SELECT f.id, f.deleted_at IS NULL FROM sub JOIN folders f ON f.id = sub.id WHERE f.owner_id = ?2`, string(rl), owner)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var live bool
		if err := rows.Scan(&id, &live); err != nil {
			rows.Close()
			return err
		}
		if live {
			rows.Close()
			return fmt.Errorf("%w: a live folder is inside a trashed folder", ErrConflict)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	list, _ := json.Marshal(ids)
	for _, q := range []string{
		`DELETE FROM shares WHERE folder_id IN (SELECT value FROM json_each(?1))`,
		`DELETE FROM placements WHERE folder_id IN (SELECT value FROM json_each(?1))`,
		`DELETE FROM folders WHERE id IN (SELECT value FROM json_each(?1))`,
	} {
		if _, err := tx.ExecContext(ctx, q, string(list)); err != nil {
			return err
		}
	}
	// Trashed bookmarks of this owner left with no placement at all.
	_, err = tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE owner_id = ? AND deleted_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM placements p WHERE p.bookmark_id = bookmarks.id)`, owner)
	return err
}

// PurgeTrash permanently deletes one Trash entry (a trashed folder subtree or a trashed
// bookmark). Live items are ErrNotFound.
func (s *Service) PurgeTrash(ctx context.Context, sc Scope, typ string, id int64) error {
	if err := requireTrashScope(sc); err != nil {
		return err
	}
	return s.write(ctx, sc, func(tx *sql.Tx) error {
		switch typ {
		case typeFolder:
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM folders WHERE id = ? AND owner_id = ? AND kind = 'folder' AND deleted_at IS NOT NULL`, id, sc.OwnerID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			return s.purgeFolders(ctx, tx, sc.OwnerID, []int64{id})
		case typeBookmark:
			res, err := tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ? AND owner_id = ? AND deleted_at IS NOT NULL`, id, sc.OwnerID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrNotFound
			}
			return nil
		default:
			return invalid("type must be folder or bookmark")
		}
	})
}

func (s *Service) trashedFolderRoots(ctx context.Context, q queryer, owner int64, before int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM folders WHERE owner_id = ? AND kind = 'folder'
		AND deleted_at IS NOT NULL AND deleted_at <= ?`, owner, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EmptyTrash permanently deletes every trashed folder and bookmark of the owner, and
// trashed placements of live bookmarks.
func (s *Service) EmptyTrash(ctx context.Context, sc Scope) error {
	if err := requireTrashScope(sc); err != nil {
		return err
	}
	return s.write(ctx, sc, func(tx *sql.Tx) error {
		return s.emptyBefore(ctx, tx, sc.OwnerID, s.ms())
	})
}

func (s *Service) emptyBefore(ctx context.Context, tx *sql.Tx, owner, before int64) error {
	roots, err := s.trashedFolderRoots(ctx, tx, owner, before)
	if err != nil {
		return err
	}
	if err := s.purgeFolders(ctx, tx, owner, roots); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE owner_id = ? AND deleted_at IS NOT NULL AND deleted_at <= ?`, owner, before); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM placements WHERE deleted_at IS NOT NULL AND deleted_at <= ?
		AND folder_id IN (SELECT id FROM folders WHERE owner_id = ?)`, before, owner)
	return err
}

// PurgeExpiredTrash permanently deletes trash older than TrashRetention for every
// owner and reports how many owners had something purged. It ignores locks: retention
// is server policy.
func (s *Service) PurgeExpiredTrash(ctx context.Context) (int, error) {
	cutoff := s.ms() - TrashRetention.Milliseconds()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT owner_id FROM folders WHERE deleted_at IS NOT NULL AND deleted_at <= ?1
		UNION SELECT DISTINCT owner_id FROM bookmarks WHERE deleted_at IS NOT NULL AND deleted_at <= ?1`, cutoff)
	if err != nil {
		return 0, err
	}
	var owners []int64
	for rows.Next() {
		var o int64
		if err := rows.Scan(&o); err != nil {
			rows.Close()
			return 0, err
		}
		owners = append(owners, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, o := range owners {
		if err := s.emptyBefore(ctx, tx, o, cutoff); err != nil {
			return 0, err
		}
	}
	return len(owners), tx.Commit()
}

// ---- statistics -------------------------------------------------------------

// UserStats are aggregate per-owner figures: no titles, URLs, tags or folder names.
type UserStats struct {
	Bookmarks    int
	Folders      int   // live non-canonical folders
	StorageBytes int64 // approximate: bytes of live URLs, titles, tags and folder titles
}

// Stats returns aggregate figures for every owner with data.
func (s *Service) Stats(ctx context.Context) (map[int64]UserStats, error) {
	out := map[int64]UserStats{}
	err := s.read(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT owner_id, count(*), coalesce(sum(length(CAST(url AS BLOB)) + length(CAST(title AS BLOB))), 0)
			FROM bookmarks WHERE deleted_at IS NULL GROUP BY owner_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var o int64
			var st UserStats
			if err := rows.Scan(&o, &st.Bookmarks, &st.StorageBytes); err != nil {
				rows.Close()
				return err
			}
			out[o] = st
		}
		rows.Close()
		for _, part := range []struct {
			sql     string
			folders bool // the count is a folder count
		}{
			{`SELECT b.owner_id, 0, coalesce(sum(length(CAST(t.tag AS BLOB))), 0) FROM bookmark_tags t JOIN bookmarks b ON b.id = t.bookmark_id
				WHERE b.deleted_at IS NULL GROUP BY b.owner_id`, false},
			{`SELECT owner_id, count(*), coalesce(sum(length(CAST(title AS BLOB))), 0) FROM folders
				WHERE deleted_at IS NULL AND kind = 'folder' GROUP BY owner_id`, true},
		} {
			rows, err := q.QueryContext(ctx, part.sql)
			if err != nil {
				return err
			}
			for rows.Next() {
				var o int64
				var n int
				var bytes int64
				if err := rows.Scan(&o, &n, &bytes); err != nil {
					rows.Close()
					return err
				}
				st := out[o]
				if part.folders {
					st.Folders = n
				}
				st.StorageBytes += bytes
				out[o] = st
			}
			rows.Close()
		}
		return nil
	})
	return out, err
}

// ---- export -----------------------------------------------------------------

// ExportNode is one item of an export tree (folders keep their order).
type ExportNode struct {
	Folder   bool
	Title    string
	URL      string
	Tags     []string
	Children []ExportNode
}

// ExportTree returns everything visible below folderID (RootID for the whole scope),
// with bookmark tags, for Netscape/XBEL export.
func (s *Service) ExportTree(ctx context.Context, sc Scope, folderID int64) ([]ExportNode, error) {
	var out []ExportNode
	err := s.read(ctx, func(q queryer) error {
		f, err := s.visibleFolder(ctx, q, sc, folderID)
		if err != nil {
			return err
		}
		t, err := s.loadTree(ctx, q, sc.OwnerID, realRoot(f))
		if err != nil {
			return err
		}
		nodes := t.build(sc, realRoot(f), -1, false)
		var ids []int64
		var collect func([]Node)
		collect = func(ns []Node) {
			for _, n := range ns {
				if n.Type == typeBookmark {
					ids = append(ids, n.ID)
				}
				collect(n.Children)
			}
		}
		collect(nodes)
		tags := map[int64][]string{}
		if len(ids) > 0 {
			list, _ := json.Marshal(ids)
			rows, err := q.QueryContext(ctx, `SELECT bookmark_id, tag FROM bookmark_tags WHERE bookmark_id IN (SELECT value FROM json_each(?))`, string(list))
			if err != nil {
				return err
			}
			for rows.Next() {
				var id int64
				var tag string
				if err := rows.Scan(&id, &tag); err != nil {
					rows.Close()
					return err
				}
				tags[id] = append(tags[id], tag)
			}
			rows.Close()
			for _, v := range tags {
				sort.Strings(v)
			}
		}
		var conv func([]Node) []ExportNode
		conv = func(ns []Node) []ExportNode {
			res := make([]ExportNode, 0, len(ns))
			for _, n := range ns {
				if n.Type == typeFolder {
					res = append(res, ExportNode{Folder: true, Title: n.Title, Children: conv(n.Children)})
				} else {
					res = append(res, ExportNode{Title: n.Title, URL: n.URL, Tags: tags[n.ID]})
				}
			}
			return res
		}
		out = conv(nodes)
		return nil
	})
	return out, err
}
