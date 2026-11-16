package bookmarks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// Share is an owner's grant of one folder subtree to another user.
type Share struct {
	ID          int64
	OwnerID     int64
	FolderID    int64
	RecipientID int64
	Mode        identity.Mode
}

// GrantShare grants (or changes the mode of) a folder to an existing active user.
// Canonical roots, the virtual root and other users' folders cannot be shared.
func (s *Service) GrantShare(ctx context.Context, ownerID, folderID, recipientID int64, mode identity.Mode) (Share, error) {
	if mode != identity.ModeView && mode != identity.ModeEdit {
		return Share{}, invalid("mode must be view or edit")
	}
	if folderID <= 0 {
		return Share{}, invalid("choose a folder to share (the root cannot be shared)")
	}
	if ownerID == recipientID {
		return Share{}, invalid("cannot share with yourself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Share{}, err
	}
	defer tx.Rollback()
	var owner int64
	var kind string
	var deleted sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT owner_id, kind, deleted_at FROM folders WHERE id = ?`, folderID).Scan(&owner, &kind, &deleted)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (owner != ownerID || deleted.Valid) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	if kind != kindFolder {
		return Share{}, invalid("canonical root folders cannot be shared; share a folder inside them")
	}
	var active int
	err = tx.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, recipientID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, fmt.Errorf("%w: recipient", ErrNotFound)
	}
	if err != nil {
		return Share{}, err
	}
	if active != 1 {
		return Share{}, invalid("recipient is inactive")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO shares(owner_id, folder_id, recipient_id, mode, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(folder_id, recipient_id) DO UPDATE SET mode = excluded.mode`,
		ownerID, folderID, recipientID, string(mode), s.ms()); err != nil {
		return Share{}, err
	}
	sh := Share{OwnerID: ownerID, FolderID: folderID, RecipientID: recipientID, Mode: mode}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM shares WHERE folder_id = ? AND recipient_id = ?`, folderID, recipientID).Scan(&sh.ID); err != nil {
		return Share{}, err
	}
	return sh, tx.Commit()
}

// RevokeShare removes a grant. Credentials bound to it are deleted with it (cascade),
// so they stop working at once.
func (s *Service) RevokeShare(ctx context.Context, ownerID, folderID, recipientID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE owner_id = ? AND folder_id = ? AND recipient_id = ?`, ownerID, folderID, recipientID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) ListShares(ctx context.Context, ownerID int64) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, folder_id, recipient_id, mode FROM shares WHERE owner_id = ? ORDER BY id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		var sh Share
		var mode string
		if err := rows.Scan(&sh.ID, &sh.OwnerID, &sh.FolderID, &sh.RecipientID, &mode); err != nil {
			return nil, err
		}
		sh.Mode = identity.Mode(mode)
		out = append(out, sh)
	}
	return out, rows.Err()
}

// FindShare returns the grant of folderID to recipientID.
func (s *Service) FindShare(ctx context.Context, ownerID, folderID, recipientID int64) (Share, error) {
	sh := Share{OwnerID: ownerID, FolderID: folderID, RecipientID: recipientID}
	var mode string
	err := s.db.QueryRowContext(ctx, `SELECT id, mode FROM shares WHERE owner_id = ? AND folder_id = ? AND recipient_id = ?`, ownerID, folderID, recipientID).Scan(&sh.ID, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	sh.Mode = identity.Mode(mode)
	return sh, err
}

// ---- locks --------------------------------------------------------------

// overlaps reports whether two physical scopes (0 = the owner's whole collection,
// else a folder subtree) intersect.
func (s *Service) overlaps(ctx context.Context, q queryer, a, b int64) (bool, error) {
	if a == 0 || b == 0 || a == b {
		return true, nil
	}
	if in, err := s.isWithin(ctx, q, a, b); err != nil || in {
		return in, err
	}
	return s.isWithin(ctx, q, b, a)
}

// conflictingLock reports whether another credential holds an unexpired lock whose
// physical scope overlaps sc.
func (s *Service) conflictingLock(ctx context.Context, q queryer, sc Scope) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT root_folder_id FROM locks WHERE owner_id = ? AND expires_at > ? AND credential_id <> ?`,
		sc.OwnerID, s.ms(), sc.Holder)
	if err != nil {
		return false, err
	}
	var roots []int64
	for rows.Next() {
		var r int64
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return false, err
		}
		roots = append(roots, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, r := range roots {
		if ov, err := s.overlaps(ctx, q, sc.RootFolderID, r); err != nil || ov {
			return ov, err
		}
	}
	return false, nil
}

func (s *Service) checkLock(ctx context.Context, q queryer, sc Scope) error {
	if busy, err := s.conflictingLock(ctx, q, sc); err != nil {
		return err
	} else if busy {
		return fmt.Errorf("%w: another connection is syncing this collection", ErrLocked)
	}
	return nil
}

// AcquireLock takes or renews the scope lock for sc.Holder with the service TTL. It
// fails with ErrLocked while another credential holds an overlapping, unexpired lock.
// Read-only credentials may lock: a download-only sync still does.
func (s *Service) AcquireLock(ctx context.Context, sc Scope) error {
	if sc.Holder == 0 {
		return invalid("a credential is required to lock")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE expires_at <= ?`, s.ms()); err != nil {
		return err
	}
	if err := s.checkLock(ctx, tx, sc); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO locks(credential_id, owner_id, root_folder_id, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(credential_id) DO UPDATE SET owner_id = excluded.owner_id,
			root_folder_id = excluded.root_folder_id, expires_at = excluded.expires_at`,
		sc.Holder, sc.OwnerID, sc.RootFolderID, s.ms()+s.lockTTL.Milliseconds()); err != nil {
		return err
	}
	return tx.Commit()
}

// ReleaseLock drops the caller's own lock. Releasing when nothing is held is a no-op;
// attempting to release while only someone else's overlapping lock exists is ErrLocked
// and removes nothing.
func (s *Service) ReleaseLock(ctx context.Context, sc Scope) error {
	if sc.Holder == 0 {
		return invalid("a credential is required to unlock")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE credential_id = ?`, sc.Holder)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if err := s.checkLock(ctx, tx, sc); err != nil {
			return err
		}
	}
	return tx.Commit()
}
