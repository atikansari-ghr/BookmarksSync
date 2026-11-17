package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Short-lived authentication flows (auth_flows): WebAuthn ceremonies and pending
// second-factor logins. The browser holds an opaque 256-bit handle; only its SHA-256 is
// stored. A flow is bound to a kind and, where relevant, to a user and a web session,
// is single-use (consumed_at) and expires. Suspension does not delete pending flows
// (Task 1 ruling); instead every completion re-checks that the account is active
// before anything is issued.

const (
	flowSecondFactor     = "second_factor"
	flowPasskeyLogin     = "webauthn_login"
	flowPasskeyRegister  = "webauthn_register"
	maxPendingLoginFlows = 10000
)

// ErrTooManyFlows reports that too many anonymous flows are pending.
var ErrTooManyFlows = errors.New("too many pending authentication flows")

type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// SessionRotation is the acting session after an authenticator change: the same
// session (same ID, authenticated_at and expiry) with a fresh secret, returned once.
// Zero when no acting session was given.
type SessionRotation struct {
	Session Session
	Secret  string
}

type flowRow struct {
	ID     int64
	UserID int64
	Data   string
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// createFlow stores a new flow through q (the database or an open transaction) and
// returns its handle once. A user-bound flow is only created for an active user
// (checked in the same statement).
func (s *Service) createFlow(ctx context.Context, q querier, kind string, userID, sessionID int64, data string, ttl time.Duration) (string, time.Time, error) {
	handle, err := newOpaqueSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	expires := msTime(now.Add(ttl))
	// Opportunistic cleanup keeps the table small.
	if _, err := q.ExecContext(ctx, `DELETE FROM auth_flows WHERE expires_at <= ? OR consumed_at IS NOT NULL`, now.UnixMilli()); err != nil {
		return "", time.Time{}, err
	}
	if userID == 0 {
		res, err := q.ExecContext(ctx, `INSERT INTO auth_flows(flow_hash, kind, data, created_at, expires_at)
			SELECT ?1, ?2, ?3, ?4, ?5 WHERE (SELECT count(*) FROM auth_flows WHERE user_id IS NULL AND kind = ?2) < ?6`,
			hashSecret(handle), kind, data, now.UnixMilli(), expires.UnixMilli(), maxPendingLoginFlows)
		if err != nil {
			return "", time.Time{}, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", time.Time{}, ErrTooManyFlows
		}
		return handle, expires, nil
	}
	res, err := q.ExecContext(ctx, `INSERT INTO auth_flows(flow_hash, kind, user_id, session_id, data, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE `+activeUserSQL,
		hashSecret(handle), kind, userID, nullID(sessionID), data, now.UnixMilli(), expires.UnixMilli(), userID)
	if err != nil {
		return "", time.Time{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if err := requireActiveUser(ctx, q, userID); err != nil {
			return "", time.Time{}, err
		}
		return "", time.Time{}, fmt.Errorf("%w: user is inactive", ErrInvalid)
	}
	return handle, expires, nil
}

// consumeFlow atomically marks a live flow of kind, bound to exactly userID and
// sessionID (0 = unbound), as used and returns it. A flow bound to someone else is not
// consumed. Any failure is ErrUnauthorized.
func (s *Service) consumeFlow(ctx context.Context, q querier, handle, kind string, userID, sessionID int64) (flowRow, error) {
	if handle == "" {
		return flowRow{}, ErrUnauthorized
	}
	now := s.now().UnixMilli()
	var f flowRow
	var uid sql.NullInt64
	err := q.QueryRowContext(ctx, `UPDATE auth_flows SET consumed_at = ?1
		WHERE flow_hash = ?2 AND kind = ?3 AND consumed_at IS NULL AND expires_at > ?1
		AND user_id IS ?4 AND session_id IS ?5
		RETURNING id, user_id, data`,
		now, hashSecret(handle), kind, nullID(userID), nullID(sessionID)).Scan(&f.ID, &uid, &f.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return flowRow{}, ErrUnauthorized
	}
	if err != nil {
		return flowRow{}, err
	}
	f.UserID = uid.Int64
	return f, nil
}

// isActive reports whether the user exists and is active.
func isActive(ctx context.Context, q querier, userID int64) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ? AND active = 1`, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) userByID(ctx context.Context, q querier, userID int64) (User, error) {
	var u User
	var admin, active int
	err := q.QueryRowContext(ctx, `SELECT id, username, is_admin, active FROM users WHERE id = ?`, userID).
		Scan(&u.ID, &u.Username, &admin, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.IsAdmin, u.Active = admin == 1, active == 1
	return u, nil
}

// rotateActingSession is the session side of an authenticator change, run inside the
// change's transaction: the acting session (sessionID, which must be a live session of
// userID, else ErrUnauthorized and the caller rolls back) gets a fresh secret, and every
// other session of the user is revoked. authenticated_at and the absolute expiry are
// unchanged: rotation is not a new authentication. sessionID 0 (no acting session, e.g.
// an operator action) revokes every session and returns a zero rotation.
func (s *Service) rotateActingSession(ctx context.Context, q querier, userID, sessionID int64) (SessionRotation, error) {
	var rot SessionRotation
	now := s.now().UnixMilli()
	if sessionID != 0 {
		fresh, err := newOpaqueSecret()
		if err != nil {
			return SessionRotation{}, err
		}
		row := q.QueryRowContext(ctx, `UPDATE web_sessions AS ws SET secret_hash = ?4, last_seen_at = ?1
			WHERE ws.id = ?3 AND ws.user_id = ?5 AND `+liveSessionSQL+`
			RETURNING `+strings.ReplaceAll(sessionCols, "ws.", ""),
			now, SessionIdleTimeout.Milliseconds(), sessionID, hashSecret(fresh), userID)
		sess, err := scanSession(row)
		if errors.Is(err, sql.ErrNoRows) {
			return SessionRotation{}, ErrUnauthorized
		}
		if err != nil {
			return SessionRotation{}, err
		}
		rot = SessionRotation{Session: sess, Secret: fresh}
	}
	if _, err := q.ExecContext(ctx, `UPDATE web_sessions SET revoked_at = ?
		WHERE user_id = ? AND id <> ? AND revoked_at IS NULL`, now, userID, sessionID); err != nil {
		return SessionRotation{}, err
	}
	return rot, nil
}

// requireLiveSession returns ErrUnauthorized unless sessionID is a live session of userID.
func (s *Service) requireLiveSession(ctx context.Context, q querier, userID, sessionID int64) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM web_sessions ws WHERE ws.id = ?3 AND ws.user_id = ?4 AND `+liveSessionSQL,
		s.now().UnixMilli(), SessionIdleTimeout.Milliseconds(), sessionID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	return err
}
