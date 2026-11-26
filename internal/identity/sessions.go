package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Session policy (OWASP session management): an opaque server-side session ends after
// 12 hours without use or 30 days after creation, whichever comes first.
const (
	SessionIdleTimeout     = 12 * time.Hour
	SessionAbsoluteTimeout = 30 * 24 * time.Hour
)

// Session is the non-secret metadata of a web session.
type Session struct {
	ID              int64
	UserID          int64
	Label           string
	CreatedAt       time.Time
	LastSeenAt      time.Time
	AuthenticatedAt time.Time // last authentication or reauthentication
	ExpiresAt       time.Time // absolute expiry; never extended
}

// newOpaqueSecret returns 256 bits of cryptographic randomness, base64url-encoded.
func newOpaqueSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sanitizeLabel(label string) string {
	label = strings.ToValidUTF8(label, "")
	if r := []rune(label); len(r) > maxLabelRunes {
		label = string(r[:maxLabelRunes])
	}
	return label
}

// requireActiveUser returns ErrNotFound for an unknown user and ErrInvalid for an
// inactive one.
func requireActiveUser(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID int64) error {
	var active int
	err := q.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, userID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("%w: user is inactive", ErrInvalid)
	}
	return nil
}

// activeUserSQL guards an issuing INSERT … SELECT (its last bind parameter is the user
// id). Checking and inserting in one statement makes issuance atomic with respect to
// SetActive(false): SQLite serializes the statement against the suspension
// transaction, so a secret is either issued before suspension (and then revoked by it)
// or not issued at all.
const activeUserSQL = `EXISTS (SELECT 1 FROM users WHERE id = ? AND active = 1)`

// issued interprets the result of an activeUserSQL-guarded insert. No inserted row
// means the user is unknown (ErrNotFound) or inactive (ErrInvalid).
func (s *Service) issued(ctx context.Context, res sql.Result, err error, userID int64) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	if err := requireActiveUser(ctx, s.db, userID); err != nil {
		return err
	}
	return fmt.Errorf("%w: user is inactive", ErrInvalid) // reactivated after the insert was refused
}

// CreateSession starts a new web session for an active user and returns the opaque
// secret once. Only the SHA-256 of the secret is stored. label is a display hint (for
// example a user-agent summary); it is truncated and made valid UTF-8.
func (s *Service) CreateSession(ctx context.Context, userID int64, label string) (Session, string, error) {
	secret, err := newOpaqueSecret()
	if err != nil {
		return Session{}, "", err
	}
	now := s.now()
	sess := Session{
		UserID: userID, Label: sanitizeLabel(label),
		CreatedAt: msTime(now), LastSeenAt: msTime(now), AuthenticatedAt: msTime(now),
		ExpiresAt: msTime(now.Add(SessionAbsoluteTimeout)),
	}
	s.runBeforeIssue(ctx)
	res, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions
		(user_id, secret_hash, label, created_at, last_seen_at, authenticated_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE `+activeUserSQL,
		userID, hashSecret(secret), sess.Label, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), sess.ExpiresAt.UnixMilli(), userID)
	if err := s.issued(ctx, res, err, userID); err != nil {
		return Session{}, "", err
	}
	sess.ID, _ = res.LastInsertId()
	return sess, secret, nil
}

// liveSessionSQL is the predicate for a usable session at time ?1 (unix ms), given the
// idle window ?2 (ms): unrevoked, inside both expiry limits, and owned by an active user.
const liveSessionSQL = `ws.revoked_at IS NULL AND ws.expires_at > ?1 AND ws.last_seen_at > ?1 - ?2
	AND EXISTS (SELECT 1 FROM users u WHERE u.id = ws.user_id AND u.active = 1)`

const sessionCols = `ws.id, ws.user_id, ws.label, ws.created_at, ws.last_seen_at, ws.authenticated_at, ws.expires_at`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var ss Session
	var created, seen, authed, expires int64
	if err := sc.Scan(&ss.ID, &ss.UserID, &ss.Label, &created, &seen, &authed, &expires); err != nil {
		return Session{}, err
	}
	ss.CreatedAt, ss.LastSeenAt = time.UnixMilli(created).UTC(), time.UnixMilli(seen).UTC()
	ss.AuthenticatedAt, ss.ExpiresAt = time.UnixMilli(authed).UTC(), time.UnixMilli(expires).UTC()
	return ss, nil
}

// ResolveSession validates a presented session secret against live state and returns
// the session and its active user. Any failure (unknown, revoked, idle or absolutely
// expired, inactive user) is ErrUnauthorized. Use is recorded at most once per minute.
func (s *Service) ResolveSession(ctx context.Context, secret string) (Session, User, error) {
	if secret == "" {
		return Session{}, User{}, ErrUnauthorized
	}
	now := s.now().UnixMilli()
	var u User
	var admin int
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionCols+`, u.username, u.is_admin
		FROM web_sessions ws JOIN users u ON u.id = ws.user_id
		WHERE ws.secret_hash = ?3 AND `+liveSessionSQL,
		now, SessionIdleTimeout.Milliseconds(), hashSecret(secret))
	var sess Session
	var created, seen, authed, expires int64
	err := row.Scan(&sess.ID, &sess.UserID, &sess.Label, &created, &seen, &authed, &expires, &u.Username, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, User{}, err
	}
	sess.CreatedAt, sess.AuthenticatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(authed).UTC()
	sess.ExpiresAt, sess.LastSeenAt = time.UnixMilli(expires).UTC(), time.UnixMilli(seen).UTC()
	if now-seen >= lastUsedThrottle.Milliseconds() {
		if _, err := s.db.ExecContext(ctx, `UPDATE web_sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?`,
			now, sess.ID, now); err != nil {
			return Session{}, User{}, err
		}
		sess.LastSeenAt = time.UnixMilli(now).UTC()
	}
	u.ID, u.IsAdmin, u.Active = sess.UserID, admin == 1, true
	return sess, u, nil
}

// RotateSession atomically replaces a live session's secret, marks it freshly
// authenticated, and returns the new secret once. The old secret stops working
// immediately. The absolute expiry is unchanged. Call it on every (re)authentication
// to prevent session fixation.
func (s *Service) RotateSession(ctx context.Context, secret string) (Session, string, error) {
	if secret == "" {
		return Session{}, "", ErrUnauthorized
	}
	fresh, err := newOpaqueSecret()
	if err != nil {
		return Session{}, "", err
	}
	now := s.now().UnixMilli()
	row := s.db.QueryRowContext(ctx, `UPDATE web_sessions AS ws
		SET secret_hash = ?4, authenticated_at = ?1, last_seen_at = ?1
		WHERE ws.secret_hash = ?3 AND `+liveSessionSQL+`
		RETURNING `+strings.ReplaceAll(sessionCols, "ws.", ""),
		now, SessionIdleTimeout.Milliseconds(), hashSecret(secret), hashSecret(fresh))
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, "", ErrUnauthorized
	}
	if err != nil {
		return Session{}, "", err
	}
	return sess, fresh, nil
}

// ListSessions returns the user's currently usable sessions in creation order. It
// never includes secrets or their hashes.
func (s *Service) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM web_sessions ws
		WHERE ws.user_id = ?3 AND `+liveSessionSQL+` ORDER BY ws.id`,
		s.now().UnixMilli(), SessionIdleTimeout.Milliseconds(), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// RevokeSession revokes one of the user's sessions. A session id that does not belong
// to userID is ErrNotFound. Revoking twice is a no-op.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE web_sessions SET revoked_at = ?
		WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, s.now().UnixMilli(), sessionID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM web_sessions WHERE id = ? AND user_id = ?`, sessionID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// RevokeUserSessions revokes every session of the user except exceptSessionID (0 keeps
// none), for logout-everywhere and password/authenticator changes.
func (s *Service) RevokeUserSessions(ctx context.Context, userID, exceptSessionID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE web_sessions SET revoked_at = ?
		WHERE user_id = ? AND id <> ? AND revoked_at IS NULL`, s.now().UnixMilli(), userID, exceptSessionID)
	return err
}

// PurgeExpiredSessions deletes revoked and expired sessions and reports how many.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	now := s.now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions
		WHERE revoked_at IS NOT NULL OR expires_at <= ? OR last_seen_at <= ?`,
		now, now-SessionIdleTimeout.Milliseconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func msTime(t time.Time) time.Time { return time.UnixMilli(t.UnixMilli()).UTC() }
