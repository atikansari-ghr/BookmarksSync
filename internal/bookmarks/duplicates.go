package bookmarks

import "context"

// DuplicateGroup is one exact URL held by two or more distinct live bookmark records that
// are visible to the caller. Bookmarks are ordered by id and carry only the placements
// visible in the scope.
type DuplicateGroup struct {
	URL       string
	Bookmarks []Bookmark
}

// DuplicateList is one page of duplicate groups. Count is the number of excess distinct
// records over every group visible to the caller (a URL held by three records adds two),
// independent of the page returned. Several folder placements of one record are
// intentional and never counted.
type DuplicateList struct {
	Count   int
	Groups  []DuplicateGroup
	HasMore bool
}

// ListDuplicates returns exact-URL duplicate groups among the records visible in the scope,
// ordered by each group's lowest record id. page is zero-based; limit is 1..MaxPageLimit
// (larger values are clamped). It is read-only: review never removes anything; a chosen
// record is removed with DeleteBookmark, which moves it to the Trash.
func (s *Service) ListDuplicates(ctx context.Context, sc Scope, page, limit int) (DuplicateList, error) {
	if page < 0 || page > maxListPageIdx || limit < 1 {
		return DuplicateList{}, invalid("bad duplicates page")
	}
	limit = min(limit, MaxPageLimit)

	cte, cond, args := scopeFilter(sc)
	qargs := []any{}
	if cte != "" {
		qargs = append(qargs, args[0])
		args = args[1:]
	}
	qargs = append(qargs, sc.OwnerID)
	qargs = append(qargs, args...)
	// Distinct live records (one bookmarks row each) with at least one visible placement.
	visible := ` FROM bookmarks b WHERE b.owner_id = ? AND b.deleted_at IS NULL AND EXISTS (
		SELECT 1 FROM placements p JOIN folders f ON f.id = p.folder_id
		WHERE p.bookmark_id = b.id AND p.deleted_at IS NULL AND f.deleted_at IS NULL AND ` + cond + `)
		GROUP BY b.url HAVING count(*) > 1`

	out := DuplicateList{Groups: []DuplicateGroup{}}
	err := s.read(ctx, func(q queryer) error {
		if err := q.QueryRowContext(ctx, cte+`SELECT coalesce(sum(n - 1), 0) FROM (SELECT count(*) AS n`+visible+`)`, qargs...).Scan(&out.Count); err != nil {
			return err
		}
		pageArgs := append(append([]any{}, qargs...), limit+1, page*limit)
		rows, err := q.QueryContext(ctx, cte+`SELECT b.url`+visible+` ORDER BY min(b.id) LIMIT ? OFFSET ?`, pageArgs...)
		if err != nil {
			return err
		}
		var urls []string
		for rows.Next() {
			var u string
			if err := rows.Scan(&u); err != nil {
				rows.Close()
				return err
			}
			urls = append(urls, u)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(urls) > limit {
			out.HasMore = true
			urls = urls[:limit]
		}
		for _, u := range urls {
			ids, err := s.visibleIDs(ctx, q, sc, 0, u, nil, nil, MaxListAll, 0)
			if err != nil {
				return err
			}
			bs, err := s.hydrate(ctx, q, sc, ids)
			if err != nil {
				return err
			}
			out.Groups = append(out.Groups, DuplicateGroup{URL: u, Bookmarks: bs})
		}
		return nil
	})
	if err != nil {
		return DuplicateList{}, err
	}
	return out, nil
}
