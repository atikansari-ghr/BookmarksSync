package bookmarks

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// MergeUserRestore adds a backup's private canonical trees to one user's current
// collection. The archive hash is recorded atomically with both imports, making a
// repeated recovery a no-op. The caller must have resolved a private writable scope.
func (s *Service) MergeUserRestore(ctx context.Context, sc Scope, archiveSHA256 string, bar, other []ImportNode) (bool, error) {
	if sc.OwnerID <= 0 || sc.RootFolderID != 0 || sc.Holder != 0 || sc.Actor != 0 && sc.Actor != sc.OwnerID || !sc.Writable {
		return false, fmt.Errorf("%w: user recovery requires a private writable scope", ErrForbidden)
	}
	if len(archiveSHA256) != 64 {
		return false, invalid("archive hash must be SHA-256")
	}
	if _, err := hex.DecodeString(archiveSHA256); err != nil || strings.ToLower(archiveSHA256) != archiveSHA256 {
		return false, invalid("archive hash must be lowercase hexadecimal SHA-256")
	}
	for _, tree := range [][]ImportNode{bar, other} {
		total := 0
		if err := countAndCheck(tree, 1, &total); err != nil {
			return false, err
		}
	}
	imported := false
	err := s.write(ctx, sc, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_restore_imports WHERE user_id = ? AND archive_sha256 = ?`, sc.OwnerID, archiveSHA256).Scan(&exists)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		var barID, otherID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id = ? AND kind = 'bar' AND deleted_at IS NULL`, sc.OwnerID).Scan(&barID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id = ? AND kind = 'other' AND deleted_at IS NULL`, sc.OwnerID).Scan(&otherID); err != nil {
			return err
		}
		for _, part := range []struct {
			id   int64
			tree []ImportNode
		}{{barID, bar}, {otherID, other}} {
			parent, err := s.visibleFolder(ctx, tx, sc, part.id)
			if err != nil {
				return err
			}
			if err := s.checkDepth(ctx, tx, realRoot(parent), importHeight(part.tree)); err != nil {
				return err
			}
			if _, err := s.importInto(ctx, tx, sc, parent, part.tree); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_restore_imports(user_id, archive_sha256, imported_at) VALUES (?, ?, ?)`, sc.OwnerID, archiveSHA256, s.ms()); err != nil {
			return err
		}
		imported = true
		return nil
	})
	return imported, err
}
