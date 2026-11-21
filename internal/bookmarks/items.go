package bookmarks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Bookmark is one bookmark record as seen through a scope. Folders lists only the
// placements visible in the scope (the share root is reported as RootID).
type Bookmark struct {
	ID      int64
	URL     string
	Title   string
	Tags    []string
	Folders []int64
	// Favorite is the scope actor's own favorite state for this record (false for
	// actorless scopes such as Floccus credentials). It is per user and per record, so
	// it is the same across every placement.
	Favorite bool
}

// BookmarkInput creates a bookmark (or adds placements to the visible record with the
// same exact URL). Tags nil means "leave existing tags alone".
type BookmarkInput struct {
	URL     string
	Title   string
	Folders []int64
	Tags    *[]string
}

// BookmarkUpdate changes only the fields that are non-nil.
type BookmarkUpdate struct {
	URL     *string
	Title   *string
	Folders *[]int64
	Tags    *[]string
}

// ListQuery selects visible bookmarks. Page -1 returns every match (bounded by
// MaxListAll); otherwise Limit is 1..MaxPageLimit (larger values are clamped).
type ListQuery struct {
	Page     int
	Limit    int
	Offset   *int     // optional row offset, useful when Limit includes a lookahead row
	FolderID int64    // restrict results to bookmarks placed in this visible folder; zero disables the filter
	URL      string   // exact match when non-empty
	Search   []string // every term must occur in url, title or a tag (case-insensitive)
	Tags     []string // every tag must be present exactly
	Favorite *bool    // when non-nil, only records whose actor favorite state equals *Favorite
}

var allowedSchemes = map[string]bool{"http": true, "https": true, "ftp": true, "ftps": true, "file": true, "javascript": true}

// ValidateURL reports whether u uses a supported scheme and size. It never rewrites u.
func ValidateURL(u string) error { return validateURL(u) }

// validateURL accepts only the documented schemes. It never rewrites the URL.
func validateURL(u string) error {
	if u == "" || strings.TrimSpace(u) == "" {
		return invalid("url is required")
	}
	if !utf8.ValidString(u) || strings.ContainsRune(u, 0) {
		return invalid("url contains invalid characters")
	}
	i := strings.IndexByte(u, ':')
	if i <= 0 {
		return invalid("url must start with a scheme (supported: http, https, ftp, ftps, file, javascript)")
	}
	scheme := u[:i]
	for j := 0; j < len(scheme); j++ {
		c := scheme[j]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (j > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'))
		if !ok {
			return invalid("url must start with a scheme (supported: http, https, ftp, ftps, file, javascript)")
		}
	}
	lower := strings.ToLower(scheme)
	if !allowedSchemes[lower] {
		return invalid("unsupported url scheme %q (supported: http, https, ftp, ftps, file, javascript)", lower)
	}
	max := MaxURLBytes
	if lower == "javascript" {
		max = MaxJSURLBytes
	}
	if len(u) > max {
		return invalid("url longer than %d bytes", max)
	}
	return nil
}

func validTags(tags *[]string) error {
	if tags == nil {
		return nil
	}
	if len(*tags) > MaxTags {
		return invalid("more than %d tags", MaxTags)
	}
	for _, t := range *tags {
		if t == "" || len(t) > MaxTagBytes || !utf8.ValidString(t) {
			return invalid("tags must be 1-%d bytes of valid UTF-8", MaxTagBytes)
		}
	}
	return nil
}

// scopeFilter returns the optional CTE prefix, the condition on folder alias f that
// restricts folders to the scope, and the arguments (CTE arg first).
func scopeFilter(sc Scope) (cte, cond string, args []any) {
	if sc.RootFolderID == 0 {
		return "", "f.owner_id = ?", []any{sc.OwnerID}
	}
	return `WITH RECURSIVE sub(id) AS (
		SELECT ? UNION ALL SELECT c.id FROM folders c JOIN sub ON c.parent_id = sub.id WHERE c.deleted_at IS NULL) `,
		"f.id IN (SELECT id FROM sub)", []any{sc.RootFolderID}
}

type placementRow struct {
	id, bookmark, folder int64
	position             int
}

// visiblePlacements returns live placements of the bookmarks inside the scope.
func (s *Service) visiblePlacements(ctx context.Context, q queryer, sc Scope, ids []int64) (map[int64][]placementRow, error) {
	out := map[int64][]placementRow{}
	if len(ids) == 0 {
		return out, nil
	}
	list, _ := json.Marshal(ids)
	cte, cond, args := scopeFilter(sc)
	args = append(args, string(list))
	rows, err := q.QueryContext(ctx, cte+`
		SELECT p.id, p.bookmark_id, p.folder_id, p.position
		FROM placements p JOIN folders f ON f.id = p.folder_id
		WHERE `+cond+` AND f.deleted_at IS NULL AND p.deleted_at IS NULL
		  AND p.bookmark_id IN (SELECT value FROM json_each(?))
		ORDER BY p.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p placementRow
		if err := rows.Scan(&p.id, &p.bookmark, &p.folder, &p.position); err != nil {
			return nil, err
		}
		out[p.bookmark] = append(out[p.bookmark], p)
	}
	return out, rows.Err()
}

// hydrate builds Bookmarks (in the order of ids) for records already known visible.
func (s *Service) hydrate(ctx context.Context, q queryer, sc Scope, ids []int64) ([]Bookmark, error) {
	if len(ids) == 0 {
		return []Bookmark{}, nil
	}
	list, _ := json.Marshal(ids)
	meta := map[int64]*Bookmark{}
	rows, err := q.QueryContext(ctx, `SELECT id, url, title FROM bookmarks WHERE id IN (SELECT value FROM json_each(?)) AND deleted_at IS NULL`, string(list))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		b := &Bookmark{Tags: []string{}, Folders: []int64{}}
		if err := rows.Scan(&b.ID, &b.URL, &b.Title); err != nil {
			rows.Close()
			return nil, err
		}
		meta[b.ID] = b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	trows, err := q.QueryContext(ctx, `SELECT bookmark_id, tag FROM bookmark_tags WHERE bookmark_id IN (SELECT value FROM json_each(?)) ORDER BY tag`, string(list))
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var id int64
		var tag string
		if err := trows.Scan(&id, &tag); err != nil {
			trows.Close()
			return nil, err
		}
		if b := meta[id]; b != nil {
			b.Tags = append(b.Tags, tag)
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, err
	}
	if sc.Actor != 0 {
		frows, err := q.QueryContext(ctx, `SELECT bookmark_id FROM bookmark_favorites WHERE user_id = ? AND bookmark_id IN (SELECT value FROM json_each(?))`, sc.Actor, string(list))
		if err != nil {
			return nil, err
		}
		for frows.Next() {
			var id int64
			if err := frows.Scan(&id); err != nil {
				frows.Close()
				return nil, err
			}
			if b := meta[id]; b != nil {
				b.Favorite = true
			}
		}
		frows.Close()
		if err := frows.Err(); err != nil {
			return nil, err
		}
	}
	pl, err := s.visiblePlacements(ctx, q, sc, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Bookmark, 0, len(ids))
	for _, id := range ids {
		b := meta[id]
		if b == nil {
			continue
		}
		for _, p := range pl[id] {
			b.Folders = append(b.Folders, sc.out(p.folder))
		}
		out = append(out, *b)
	}
	return out, nil
}

// visibleIDs returns ids of live bookmarks of the owner with at least one visible
// placement, optionally filtered, ordered by id. A non-nil favorite keeps only records
// whose favorite state for sc.Actor equals *favorite (actorless scopes have none).
func (s *Service) visibleIDs(ctx context.Context, q queryer, sc Scope, folderID int64, url string, search []string, favorite *bool, limit, offset int, tags ...string) ([]int64, error) {
	cte, cond, args := scopeFilter(sc)
	where := ` WHERE b.owner_id = ? AND b.deleted_at IS NULL AND EXISTS (
		SELECT 1 FROM placements p JOIN folders f ON f.id = p.folder_id
		WHERE p.bookmark_id = b.id AND p.deleted_at IS NULL AND f.deleted_at IS NULL AND ` + cond + `)`
	// cte args precede everything; the owner arg precedes cond args.
	qargs := []any{}
	if cte != "" {
		qargs = append(qargs, args[0])
		args = args[1:]
	}
	qargs = append(qargs, sc.OwnerID)
	qargs = append(qargs, args...)
	if folderID != 0 {
		folderScope := "ff.owner_id = ?"
		if sc.RootFolderID != 0 {
			folderScope = "ff.id IN (SELECT id FROM sub)"
		}
		where += ` AND EXISTS (
			SELECT 1 FROM placements fp JOIN folders ff ON ff.id = fp.folder_id
			WHERE fp.bookmark_id = b.id AND fp.deleted_at IS NULL AND ff.deleted_at IS NULL
			AND ff.id = ? AND ` + folderScope + `)`
		qargs = append(qargs, folderID)
		if sc.RootFolderID == 0 {
			qargs = append(qargs, sc.OwnerID)
		}
	}
	if url != "" {
		where += ` AND b.url = ?`
		qargs = append(qargs, url)
	}
	for _, tag := range tags {
		where += ` AND EXISTS (SELECT 1 FROM bookmark_tags t WHERE t.bookmark_id = b.id AND t.tag = ?)`
		qargs = append(qargs, tag)
	}
	if favorite != nil {
		switch {
		case sc.Actor == 0 && *favorite:
			where += ` AND 0`
		case sc.Actor != 0:
			not := ""
			if !*favorite {
				not = "NOT "
			}
			where += ` AND ` + not + `EXISTS (SELECT 1 FROM bookmark_favorites bf WHERE bf.user_id = ? AND bf.bookmark_id = b.id)`
			qargs = append(qargs, sc.Actor)
		}
	}
	for _, term := range search {
		like := "%" + escapeLike(term) + "%"
		where += ` AND (b.url LIKE ? ESCAPE '\' OR b.title LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM bookmark_tags t WHERE t.bookmark_id = b.id AND t.tag LIKE ? ESCAPE '\'))`
		qargs = append(qargs, like, like, like)
	}
	qargs = append(qargs, limit, offset)
	rows, err := q.QueryContext(ctx, cte+`SELECT b.id FROM bookmarks b`+where+` ORDER BY b.id LIMIT ? OFFSET ?`, qargs...)
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

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (s *Service) ListBookmarks(ctx context.Context, sc Scope, lq ListQuery) ([]Bookmark, error) {
	if len(lq.Search) > MaxSearchTerms {
		return nil, invalid("at most %d search terms", MaxSearchTerms)
	}
	for _, t := range lq.Search {
		if len(t) > MaxSearchTermBytes {
			return nil, invalid("search terms are limited to %d bytes", MaxSearchTermBytes)
		}
	}
	if len(lq.Tags) > MaxSearchTerms {
		return nil, invalid("at most %d tag filters", MaxSearchTerms)
	}
	if err := validTags(&lq.Tags); err != nil {
		return nil, err
	}
	limit, offset := 0, 0
	switch {
	case lq.Page == -1:
		limit = MaxListAll + 1
	case lq.Page < -1 || lq.Page > maxListPageIdx:
		return nil, invalid("page must be -1 or a non-negative page number")
	case lq.Limit < 1:
		return nil, invalid("limit must be at least 1")
	default:
		limit = lq.Limit
		if limit > MaxPageLimit {
			limit = MaxPageLimit
		}
		offset = lq.Page * limit
		if lq.Offset != nil {
			if *lq.Offset < 0 {
				return nil, invalid("offset must be non-negative")
			}
			offset = *lq.Offset
		}
	}
	var out []Bookmark
	err := s.read(ctx, func(q queryer) error {
		ids, err := s.visibleIDs(ctx, q, sc, lq.FolderID, lq.URL, lq.Search, lq.Favorite, limit, offset, lq.Tags...)
		if err != nil {
			return err
		}
		if lq.Page == -1 && len(ids) > MaxListAll {
			return invalid("more than %d matches; use paging", MaxListAll)
		}
		out, err = s.hydrate(ctx, q, sc, ids)
		return err
	})
	return out, err
}

func (s *Service) GetBookmark(ctx context.Context, sc Scope, id int64) (Bookmark, error) {
	var out Bookmark
	err := s.read(ctx, func(q queryer) error {
		var err error
		out, err = s.getVisible(ctx, q, sc, id)
		return err
	})
	return out, err
}

// getVisible returns the bookmark if alive, owned by the scope's collection and
// placed somewhere visible; otherwise ErrNotFound.
func (s *Service) getVisible(ctx context.Context, q queryer, sc Scope, id int64) (Bookmark, error) {
	var owner int64
	var deleted sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT owner_id, deleted_at FROM bookmarks WHERE id = ?`, id).Scan(&owner, &deleted)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (owner != sc.OwnerID || deleted.Valid) {
		return Bookmark{}, ErrNotFound
	}
	if err != nil {
		return Bookmark{}, err
	}
	bs, err := s.hydrate(ctx, q, sc, []int64{id})
	if err != nil {
		return Bookmark{}, err
	}
	if len(bs) == 0 || len(bs[0].Folders) == 0 {
		return Bookmark{}, ErrNotFound
	}
	return bs[0], nil
}

// resolveTargets maps scope-relative folder ids to distinct real folder ids that may
// hold bookmarks (the private virtual root may not).
func (s *Service) resolveTargets(ctx context.Context, q queryer, sc Scope, folders []int64) ([]int64, error) {
	if len(folders) == 0 {
		return nil, invalid("at least one folder is required")
	}
	var out []int64
	seen := map[int64]bool{}
	for _, id := range folders {
		f, err := s.visibleFolder(ctx, q, sc, id)
		if err != nil {
			return nil, err
		}
		if f.virtual {
			return nil, invalid("bookmarks cannot be placed in the root; use a folder")
		}
		if !seen[f.id] {
			seen[f.id] = true
			out = append(out, f.id)
		}
	}
	return out, nil
}

func (s *Service) setTags(ctx context.Context, tx *sql.Tx, id int64, tags []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM bookmark_tags WHERE bookmark_id = ?`, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, t := range tags {
		if seen[t] {
			continue
		}
		seen[t] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO bookmark_tags(bookmark_id, tag) VALUES (?, ?)`, id, t); err != nil {
			return err
		}
	}
	return nil
}

// addPlacement puts the bookmark into the folder unless a live placement exists.
// It reports whether a placement was created.
func (s *Service) addPlacement(ctx context.Context, tx *sql.Tx, owner, bookmarkID, folderID int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM placements WHERE bookmark_id = ? AND folder_id = ? AND deleted_at IS NULL`, bookmarkID, folderID).Scan(&one)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// Invariant: a folder never holds two live placements of records with the same exact
	// URL. Both records are visible to any caller that can address this folder, so refusing
	// reveals nothing hidden; silently merging the records would change ids.
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM placements p JOIN bookmarks b ON b.id = p.bookmark_id
		WHERE p.folder_id = ?1 AND p.deleted_at IS NULL AND b.deleted_at IS NULL AND b.id <> ?2
		  AND b.url = (SELECT url FROM bookmarks WHERE id = ?2) LIMIT 1`, folderID, bookmarkID).Scan(&one)
	if err == nil {
		return false, errURLInFolder
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	pos, err := s.nextPosition(ctx, tx, owner, folderID)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO placements(bookmark_id, folder_id, position) VALUES (?, ?, ?)`, bookmarkID, folderID, pos)
	return err == nil, err
}

// errURLInFolder is deliberately generic: it must not reveal which record, folder or title
// caused the collision; collisions are only ever judged among records visible to the writer.
var errURLInFolder = fmt.Errorf("%w: another bookmark with this URL is already in that folder", ErrConflict)

// findVisibleByURL finds the record with this exact URL that is visible in the scope.
// Deterministic policy with no candidate cap: the lowest-id record already placed in one of
// the target folders wins (so a single-folder POST never duplicates a placement), otherwise
// the lowest-id visible record. Target folders are visible by construction.
func (s *Service) findVisibleByURL(ctx context.Context, q queryer, sc Scope, url string, targets []int64) (int64, bool, error) {
	if len(targets) > 0 {
		tl, _ := json.Marshal(targets)
		var id int64
		err := q.QueryRowContext(ctx, `
			SELECT b.id FROM bookmarks b
			WHERE b.owner_id = ? AND b.deleted_at IS NULL AND b.url = ? AND EXISTS (
				SELECT 1 FROM placements p JOIN folders f ON f.id = p.folder_id
				WHERE p.bookmark_id = b.id AND p.deleted_at IS NULL AND f.deleted_at IS NULL
				  AND p.folder_id IN (SELECT value FROM json_each(?)))
			ORDER BY b.id LIMIT 1`, sc.OwnerID, url, string(tl)).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
	}
	ids, err := s.visibleIDs(ctx, q, sc, 0, url, nil, nil, 1, 0)
	if err != nil || len(ids) == 0 {
		return 0, false, err
	}
	return ids[0], true, nil
}

// CreateBookmark creates a bookmark in the given folders. If a record with the exact
// same URL is already visible in this scope it gains the missing placements (and the
// given title and tags) instead; records hidden from the scope are never reused.
func (s *Service) CreateBookmark(ctx context.Context, sc Scope, in BookmarkInput) (Bookmark, error) {
	if err := validateURL(in.URL); err != nil {
		return Bookmark{}, err
	}
	if err := validTitle("title", in.Title); err != nil {
		return Bookmark{}, err
	}
	if err := validTags(in.Tags); err != nil {
		return Bookmark{}, err
	}
	var out Bookmark
	err := s.write(ctx, sc, func(tx *sql.Tx) error {
		targets, err := s.resolveTargets(ctx, tx, sc, in.Folders)
		if err != nil {
			return err
		}
		id, found, err := s.findVisibleByURL(ctx, tx, sc, in.URL, targets)
		if err != nil {
			return err
		}
		now := s.ms()
		if found {
			if _, err := tx.ExecContext(ctx, `UPDATE bookmarks SET title = ?, updated_at = ? WHERE id = ?`, in.Title, now, id); err != nil {
				return err
			}
		} else {
			res, err := tx.ExecContext(ctx, `INSERT INTO bookmarks(owner_id, url, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, sc.OwnerID, in.URL, in.Title, now, now)
			if err != nil {
				return err
			}
			id, _ = res.LastInsertId()
		}
		if in.Tags != nil {
			if err := s.setTags(ctx, tx, id, *in.Tags); err != nil {
				return err
			}
		}
		for _, f := range targets {
			if _, err := s.addPlacement(ctx, tx, sc.OwnerID, id, f); err != nil {
				return err
			}
		}
		out, err = s.getVisible(ctx, tx, sc, id)
		return err
	})
	return out, err
}

// UpdateBookmark changes a visible bookmark. Folders is the complete list of desired
// placements inside the scope; placements outside the scope are never touched.
func (s *Service) UpdateBookmark(ctx context.Context, sc Scope, id int64, up BookmarkUpdate) (Bookmark, error) {
	if up.URL != nil {
		if err := validateURL(*up.URL); err != nil {
			return Bookmark{}, err
		}
	}
	if up.Title != nil {
		if err := validTitle("title", *up.Title); err != nil {
			return Bookmark{}, err
		}
	}
	if err := validTags(up.Tags); err != nil {
		return Bookmark{}, err
	}
	var out Bookmark
	err := s.write(ctx, sc, func(tx *sql.Tx) error {
		cur, err := s.getVisible(ctx, tx, sc, id)
		if err != nil {
			return err
		}
		var targets []int64
		if up.Folders != nil {
			if targets, err = s.resolveTargets(ctx, tx, sc, *up.Folders); err != nil {
				return err
			}
		}
		if up.URL != nil && *up.URL != cur.URL {
			// A URL edit of an already-visible record applies to every reference of it, like
			// title and tags, including placements this scope cannot see (their rows stay).
			// It must not collide with another record visible to this scope (generic 409).
			others, err := s.visibleIDs(ctx, tx, sc, 0, *up.URL, nil, nil, 2, 0)
			if err != nil {
				return err
			}
			for _, o := range others {
				if o != id {
					return errURLInFolder
				}
			}
			// Deliberately no look at folders or records hidden from this scope: the outcome
			// must be identical to a fresh URL. A resulting duplicate inside a hidden folder
			// is allowed; the owner sees it and resolves it in duplicate review.
		}
		now := s.ms()
		if up.URL != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE bookmarks SET url = ?, updated_at = ? WHERE id = ?`, *up.URL, now, id); err != nil {
				return err
			}
		}
		if up.Title != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE bookmarks SET title = ?, updated_at = ? WHERE id = ?`, *up.Title, now, id); err != nil {
				return err
			}
		}
		if up.Tags != nil {
			if err := s.setTags(ctx, tx, id, *up.Tags); err != nil {
				return err
			}
		}
		if up.Folders != nil {
			pl, err := s.visiblePlacements(ctx, tx, sc, []int64{id})
			if err != nil {
				return err
			}
			want := map[int64]bool{}
			for _, f := range targets {
				want[f] = true
			}
			have := map[int64]bool{}
			for _, p := range pl[id] {
				have[p.folder] = true
				if !want[p.folder] {
					if _, err := tx.ExecContext(ctx, `UPDATE placements SET deleted_at = ? WHERE id = ?`, now, p.id); err != nil {
						return err
					}
				}
			}
			for _, f := range targets {
				if !have[f] {
					if _, err := s.addPlacement(ctx, tx, sc.OwnerID, id, f); err != nil {
						return err
					}
				}
			}
		}
		out, err = s.getVisible(ctx, tx, sc, id)
		return err
	})
	return out, err
}

// RemovePlacement removes a bookmark from one folder. The bookmark is trashed when
// it has no placement left anywhere.
func (s *Service) RemovePlacement(ctx context.Context, sc Scope, folderID, bookmarkID int64) error {
	return s.write(ctx, sc, func(tx *sql.Tx) error {
		f, err := s.visibleFolder(ctx, tx, sc, folderID)
		if err != nil {
			return err
		}
		if f.virtual {
			return ErrNotFound
		}
		src, err := s.getVisible(ctx, tx, sc, bookmarkID)
		if err != nil {
			return err
		}
		now := s.ms()
		res, err := tx.ExecContext(ctx, `UPDATE placements SET deleted_at = ? WHERE bookmark_id = ? AND folder_id = ? AND deleted_at IS NULL`, now, bookmarkID, f.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if err := s.mergeIntoSoleVisibleDuplicate(ctx, tx, sc, src, now); err != nil {
			return err
		}
		return s.trashOrphanedBookmarks(ctx, tx, now)
	})
}

// mergeIntoSoleVisibleDuplicate preserves the metadata of a record whose last placement was
// just removed: stock floccus resolves a same-folder duplicate by deleting one copy without
// saying whether it was a move, so the title and tags of the removed copy win on the single
// remaining copy. Deliberately narrow (approved policy): it only acts when the source has no
// live placement anywhere (hidden scopes included) and exactly one other record with the exact
// URL is visible to this caller's scope. Candidates are found with the scope-visible query only,
// so records hidden from the caller are never inspected; with several visible candidates nothing
// is guessed. The survivor keeps its id and placements.
func (s *Service) mergeIntoSoleVisibleDuplicate(ctx context.Context, tx *sql.Tx, sc Scope, src Bookmark, now int64) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM placements WHERE bookmark_id = ? AND deleted_at IS NULL LIMIT 1`, src.ID).Scan(&one)
	if err == nil {
		return nil // still placed somewhere: not an orphan
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	ids, err := s.visibleIDs(ctx, tx, sc, 0, src.URL, nil, nil, 3, 0)
	if err != nil {
		return err
	}
	var survivors []int64
	for _, id := range ids {
		if id != src.ID {
			survivors = append(survivors, id)
		}
	}
	if len(survivors) != 1 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bookmarks SET title = ?, updated_at = ? WHERE id = ?`, src.Title, now, survivors[0]); err != nil {
		return err
	}
	// Each user's favorite follows the bookmark to the survivor for that same user only; the
	// source rows stay so a Trash restore of the source keeps its favorite state.
	if _, err := tx.ExecContext(ctx, `INSERT INTO bookmark_favorites(user_id, bookmark_id, created_at)
		SELECT user_id, ?, created_at FROM bookmark_favorites WHERE bookmark_id = ?
		ON CONFLICT(user_id, bookmark_id) DO NOTHING`, survivors[0], src.ID); err != nil {
		return err
	}
	return s.setTags(ctx, tx, survivors[0], src.Tags)
}

// DeleteBookmark removes every placement visible in the scope; placements outside the
// scope survive, and the record is trashed only when none remain.
func (s *Service) DeleteBookmark(ctx context.Context, sc Scope, id int64) error {
	return s.write(ctx, sc, func(tx *sql.Tx) error {
		if _, err := s.getVisible(ctx, tx, sc, id); err != nil {
			return err
		}
		pl, err := s.visiblePlacements(ctx, tx, sc, []int64{id})
		if err != nil {
			return err
		}
		now := s.ms()
		for _, p := range pl[id] {
			if _, err := tx.ExecContext(ctx, `UPDATE placements SET deleted_at = ? WHERE id = ?`, now, p.id); err != nil {
				return err
			}
		}
		return s.trashOrphanedBookmarks(ctx, tx, now)
	})
}
