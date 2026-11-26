package identity

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

const LoginFlowLifetime = 20 * time.Minute

// DefaultLoginFlowLabel names credentials from approvals that do not choose a
// connection name, matching the label used before names could be chosen.
const DefaultLoginFlowLabel = "Login Flow v2 connection"

var ErrLoginFlowCapacity = errors.New("login flow capacity reached")

// BeginLoginFlow bounds durable anonymous state and returns independent browser
// and poll secrets. Only their hashes are retained, and no app password is minted.
func (s *Service) BeginLoginFlow(ctx context.Context) (browser, poll string, err error) {
	browser, err = newOpaqueSecret()
	if err != nil {
		return
	}
	poll, err = newOpaqueSecret()
	if err != nil {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	now := s.now()
	if _, err = tx.ExecContext(ctx, `DELETE FROM login_flows_v2 WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return "", "", err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM login_flows_v2`).Scan(&count); err != nil {
		return "", "", err
	}
	if count >= 1024 {
		return "", "", ErrLoginFlowCapacity
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO login_flows_v2(browser_hash,poll_hash,expires_at) VALUES(?,?,?)`, hashSecret(browser), hashSecret(poll), now.Add(LoginFlowLifetime).UnixMilli())
	if err != nil {
		return "", "", err
	}
	err = tx.Commit()
	return
}

func (s *Service) LoginFlowPending(ctx context.Context, browser string) error {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM login_flows_v2 WHERE browser_hash=? AND expires_at>? AND session_id IS NULL`, hashSecret(browser), s.now().UnixMilli()).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ApproveLoginFlow binds consent to this live recently authenticated session and
// records the connection name the credential will receive. The label is trimmed
// and validated like a connection rename; an invalid label (ErrInvalid) leaves the
// flow pending. A second approval cannot transfer the flow to another account or
// session.
func (s *Service) ApproveLoginFlow(ctx context.Context, browser string, sessionID int64, label string) error {
	label, err := normalizeConnectionLabel(label)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE login_flows_v2 SET session_id=?3, credential_label=?5 WHERE browser_hash=?4 AND expires_at>?1 AND session_id IS NULL
 AND EXISTS(SELECT 1 FROM web_sessions ws WHERE ws.id=?3 AND `+liveSessionSQL+` AND ws.authenticated_at>?1-300000)`, now, SessionIdleTimeout.Milliseconds(), sessionID, hashSecret(browser), label)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// PollLoginFlow consumes an approved flow and mints a private edit credential in
// the same write transaction. Active user/session checks run at redemption, so
// revocation or suspension after consent prevents issuance. Concurrent pollers
// receive at most one secret. Plaintext never enters SQLite, including its WAL.
func (s *Service) PollLoginFlow(ctx context.Context, poll string) (username, secret string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var sid int64
	var label string
	err = tx.QueryRowContext(ctx, `DELETE FROM login_flows_v2 WHERE poll_hash=? AND expires_at>? AND session_id IS NOT NULL RETURNING session_id, credential_label`, hashSecret(poll), s.now().UnixMilli()).Scan(&sid, &label)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	var uid int64
	err = tx.QueryRowContext(ctx, `SELECT ws.user_id,u.username FROM web_sessions ws JOIN users u ON u.id=ws.user_id WHERE ws.id=?3 AND `+liveSessionSQL, s.now().UnixMilli(), SessionIdleTimeout.Milliseconds(), sid).Scan(&uid, &username)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return "", "", err
		}
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	random, err := newOpaqueSecret()
	if err != nil {
		return "", "", err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO credentials(user_id,label,secret_hash,kind,share_id,mode,created_at) VALUES(?,?,?,'private',NULL,'edit',?)`, uid, label, hashSecret(random), s.now().UnixMilli())
	if err != nil {
		return "", "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", "", err
	}
	if err = tx.Commit(); err != nil {
		return "", "", err
	}
	return username, secretPrefix + "." + strconv.FormatInt(id, 10) + "." + random, nil
}
