package bookmarks

import (
	"context"
	"database/sql"
	"errors"
)

// MaxBulkMove is the most bookmarks one MoveBookmarks call accepts.
const MaxBulkMove = 100

// MoveBookmarks moves the placement each listed record has in sourceFolderID to
// destinationFolderID, atomically. Placements in other folders are left alone. The moved
// records are appended after the destination's existing children in the submitted order; a
// record already placed in the destination is not duplicated, it is moved to its submitted
// place at the end. Source and destination must be distinct real folders visible in the scope
// (the private virtual root is neither), ids must be 1..MaxBulkMove unique positive numbers,
// and every record must be visible with a live placement in the source. The write rules are
// those of any other write: read-only scopes, Floccus locks and share revalidation apply, and
// a duplicate exact URL in the destination (another record) is refused with ErrConflict. Any
// failure leaves every record and position untouched.
func (s *Service) MoveBookmarks(ctx context.Context, sc Scope, ids []int64, sourceFolderID, destinationFolderID int64) error {
	if len(ids) < 1 || len(ids) > MaxBulkMove {
		return invalid("bookmarkIds must hold 1 to 100 ids")
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id < 1 {
			return invalid("bookmarkIds must be positive ids")
		}
		if seen[id] {
			return invalid("bookmarkIds must be unique")
		}
		seen[id] = true
	}
	return s.write(ctx, sc, func(tx *sql.Tx) error {
		src, err := s.visibleFolder(ctx, tx, sc, sourceFolderID)
		if err != nil {
			return err
		}
		if src.virtual {
			return invalid("the source must be a folder, not the root")
		}
		dsts, err := s.resolveTargets(ctx, tx, sc, []int64{destinationFolderID})
		if err != nil {
			return err
		}
		dst := dsts[0]
		if src.id == dst {
			return invalid("source and destination folders must differ")
		}
		now := s.ms()
		for _, id := range ids {
			if _, err := s.getVisible(ctx, tx, sc, id); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE placements SET deleted_at = ? WHERE bookmark_id = ? AND folder_id = ? AND deleted_at IS NULL`, now, id, src.id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrNotFound
			}
			var pid int64
			err = tx.QueryRowContext(ctx, `SELECT id FROM placements WHERE bookmark_id = ? AND folder_id = ? AND deleted_at IS NULL`, id, dst).Scan(&pid)
			switch {
			case err == nil:
				pos, err := s.nextPosition(ctx, tx, sc.OwnerID, dst)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE placements SET position = ? WHERE id = ?`, pos, pid); err != nil {
					return err
				}
			case errors.Is(err, sql.ErrNoRows):
				if _, err := s.addPlacement(ctx, tx, sc.OwnerID, id, dst); err != nil {
					return err
				}
			default:
				return err
			}
		}
		return nil
	})
}
