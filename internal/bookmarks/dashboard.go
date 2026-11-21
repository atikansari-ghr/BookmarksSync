package bookmarks

import (
	"context"
	"fmt"
	"time"
)

// DashboardChange is one record that changed inside the dashboard window, described by its
// CURRENT state. Type is "deleted" (the record is in the Trash and was deleted in the
// window), else "created" (created in the window), else "updated" (edited in the window).
// At is the timestamp that decided the type. Each record appears once.
type DashboardChange struct {
	Type  string
	ID    int64
	Title string
	URL   string
	At    time.Time
}

// RecentChanges summarises the window starting at Since (inclusive). The counts cover the
// whole window; Items is one page ordered newest first (then by id, descending).
type RecentChanges struct {
	Since                     time.Time
	Created, Updated, Deleted int
	Items                     []DashboardChange
	HasMore                   bool
}

// DashboardSummary is the owner's overview. TotalBookmarks counts distinct live records
// (a record placed in several folders counts once).
type DashboardSummary struct {
	TotalBookmarks int
	Recent         RecentChanges
}

// Dashboard summarises the caller's own private collection. Only a private scope is
// accepted: share scopes (and so shared or other users' data) are refused. It reports the
// current state of records, not a full event history; placement-only edits do not change a
// record's timestamps and so are not reported. page is zero-based; limit is 1..MaxPageLimit
// (larger values are clamped).
func (s *Service) Dashboard(ctx context.Context, sc Scope, since time.Time, page, limit int) (DashboardSummary, error) {
	if sc.RootFolderID != 0 {
		return DashboardSummary{}, fmt.Errorf("%w: the dashboard is only available for your own collection", ErrForbidden)
	}
	if page < 0 || page > maxListPageIdx || limit < 1 {
		return DashboardSummary{}, invalid("bad dashboard page")
	}
	limit = min(limit, MaxPageLimit)
	sinceMs := since.UnixMilli()

	const visible = `EXISTS (SELECT 1 FROM placements p JOIN folders f ON f.id = p.folder_id
		WHERE p.bookmark_id = b.id AND p.deleted_at IS NULL AND f.deleted_at IS NULL AND f.owner_id = ?1)`
	const recent = `SELECT CASE WHEN b.deleted_at IS NOT NULL THEN 'deleted' WHEN b.created_at >= ?2 THEN 'created' ELSE 'updated' END AS typ,
			b.id AS id, b.title AS title, b.url AS url,
			CASE WHEN b.deleted_at IS NOT NULL THEN b.deleted_at WHEN b.created_at >= ?2 THEN b.created_at ELSE b.updated_at END AS at
		FROM bookmarks b WHERE b.owner_id = ?1 AND (
			(b.deleted_at IS NOT NULL AND b.deleted_at >= ?2)
			OR (b.deleted_at IS NULL AND (b.created_at >= ?2 OR b.updated_at >= ?2) AND ` + visible + `))`

	out := DashboardSummary{Recent: RecentChanges{Since: since, Items: []DashboardChange{}}}
	err := s.read(ctx, func(q queryer) error {
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM bookmarks b WHERE b.owner_id = ?1 AND b.deleted_at IS NULL AND `+visible,
			sc.OwnerID).Scan(&out.TotalBookmarks); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT typ, count(*) FROM (`+recent+`) GROUP BY typ`, sc.OwnerID, sinceMs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var typ string
			var n int
			if err := rows.Scan(&typ, &n); err != nil {
				rows.Close()
				return err
			}
			switch typ {
			case "created":
				out.Recent.Created = n
			case "updated":
				out.Recent.Updated = n
			case "deleted":
				out.Recent.Deleted = n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = q.QueryContext(ctx, `SELECT typ, id, title, url, at FROM (`+recent+`) ORDER BY at DESC, id DESC LIMIT ?3 OFFSET ?4`,
			sc.OwnerID, sinceMs, limit+1, page*limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c DashboardChange
			var at int64
			if err := rows.Scan(&c.Type, &c.ID, &c.Title, &c.URL, &at); err != nil {
				return err
			}
			c.At = time.UnixMilli(at).UTC()
			out.Recent.Items = append(out.Recent.Items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return DashboardSummary{}, err
	}
	if len(out.Recent.Items) > limit {
		out.Recent.HasMore = true
		out.Recent.Items = out.Recent.Items[:limit]
	}
	return out, nil
}
