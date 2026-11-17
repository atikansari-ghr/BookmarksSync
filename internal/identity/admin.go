package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones for ui.timezone validation on every platform
)

// ErrLastAdmin refuses a change that would leave no active administrator.
var ErrLastAdmin = errors.New("the last active administrator cannot be deactivated or demoted")

// UserInfo is a user with account metadata (never secrets).
type UserInfo struct {
	User
	CreatedAt     time.Time
	LastContactAt time.Time
}

func scanUserInfo(sc interface{ Scan(...any) error }) (UserInfo, error) {
	var u UserInfo
	var admin, active int
	var created int64
	var lastContact sql.NullInt64
	if err := sc.Scan(&u.ID, &u.Username, &admin, &active, &created, &lastContact); err != nil {
		return UserInfo{}, err
	}
	u.IsAdmin, u.Active, u.CreatedAt = admin == 1, active == 1, time.UnixMilli(created).UTC()
	if lastContact.Valid {
		u.LastContactAt = time.UnixMilli(lastContact.Int64).UTC()
	}
	return u, nil
}

const userInfoCols = `id, username, is_admin, active, created_at,
	(SELECT MAX(activity) FROM (
		SELECT last_seen_at AS activity FROM web_sessions WHERE user_id = users.id
		UNION ALL SELECT last_used_at FROM api_tokens WHERE user_id = users.id
		UNION ALL SELECT last_used_at FROM credentials WHERE user_id = users.id
	))`

// ListUsers returns every account in id order.
func (s *Service) ListUsers(ctx context.Context) ([]UserInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userInfoCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserInfo{}
	for rows.Next() {
		u, err := scanUserInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListUsersPage returns at most limit users in stable id order. Offset is a row
// offset so callers can request limit+1 to determine whether another page exists.
func (s *Service) ListUsersPage(ctx context.Context, limit, offset int) ([]UserInfo, error) {
	if limit < 1 || limit > 201 || offset < 0 || offset > 200_000_000 {
		return nil, fmt.Errorf("%w: invalid user page", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userInfoCols+` FROM users ORDER BY id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserInfo{}
	for rows.Next() {
		u, err := scanUserInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUserByID returns one account (ErrNotFound if unknown).
func (s *Service) GetUserByID(ctx context.Context, id int64) (UserInfo, error) {
	u, err := scanUserInfo(s.db.QueryRowContext(ctx, `SELECT `+userInfoCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return UserInfo{}, ErrNotFound
	}
	return u, err
}

// UpdateUser changes a user's active flag and/or administrator role in one transaction.
// Deactivation revokes credentials, sessions and API tokens (as SetActive). A change
// that would leave no active administrator is ErrLastAdmin and changes nothing; the
// check runs inside the (immediate) transaction, so concurrent changes cannot race
// past it.
func (s *Service) UpdateUser(ctx context.Context, userID int64, active, admin *bool) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	before, err := s.userByID(ctx, tx, userID)
	if err != nil {
		return User{}, err
	}
	if admin != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ?`, b2i(*admin), userID); err != nil {
			return User{}, err
		}
	}
	if active != nil {
		if err := s.setActiveTx(ctx, tx, userID, *active); err != nil {
			return User{}, err
		}
	}
	u, err := s.userByID(ctx, tx, userID)
	if err != nil {
		return User{}, err
	}
	// Only a change that takes away an active administrator can leave none.
	if before.IsAdmin && before.Active && !(u.IsAdmin && u.Active) {
		var admins int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_admin = 1 AND active = 1`).Scan(&admins); err != nil {
			return User{}, err
		}
		if admins == 0 {
			return User{}, ErrLastAdmin
		}
	}
	return u, tx.Commit()
}

func validPassword(p string) error {
	if len(p) < minPasswordLen || len(p) > maxPasswordBytes {
		return fmt.Errorf("%w: password must be %d-%d bytes", ErrInvalid, minPasswordLen, maxPasswordBytes)
	}
	return nil
}

// ChangePassword sets a new password for the signed-in user (the caller has verified
// the current password). In one transaction it revokes every personal API token and
// every other session, and rotates the acting session.
func (s *Service) ChangePassword(ctx context.Context, userID, sessionID int64, newPassword string) (SessionRotation, error) {
	if err := validPassword(newPassword); err != nil {
		return SessionRotation{}, err
	}
	hash, err := s.hashPassword(newPassword)
	if err != nil {
		return SessionRotation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRotation{}, err
	}
	defer tx.Rollback()
	rot, err := s.rotateActingSession(ctx, tx, userID, sessionID)
	if err != nil {
		return SessionRotation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ? AND active = 1`, hash, userID); err != nil {
		return SessionRotation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, s.now().UnixMilli(), userID); err != nil {
		return SessionRotation{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionRotation{}, err
	}
	return rot, nil
}

// revokeSessionsAndTokens revokes every web session and personal API token of a user.
func (s *Service) revokeSessionsAndTokens(ctx context.Context, q querier, userID int64) error {
	now := s.now().UnixMilli()
	for _, table := range []string{"web_sessions", "api_tokens"} {
		if _, err := q.ExecContext(ctx, `UPDATE `+table+` SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID); err != nil {
			return err
		}
	}
	return nil
}

// ResetPassword is an administrator's explicit password reset. It revokes every web
// session and personal API token of the user; floccus app credentials are kept.
func (s *Service) ResetPassword(ctx context.Context, userID int64, newPassword string) error {
	if err := validPassword(newPassword); err != nil {
		return err
	}
	hash, err := s.hashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_flows WHERE user_id = ? AND kind = ? AND consumed_at IS NULL`, userID, flowSecondFactor); err != nil {
		return err
	}
	if err := s.revokeSessionsAndTokens(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// FactorReset selects the sign-in factors an administrator removes.
type FactorReset struct {
	TOTP     bool // TOTP seed and recovery codes
	Passkeys bool
	OIDC     bool // linked provider identities
}

// ResetFactors removes the selected factors of a user and revokes every web session
// and personal API token. At least one factor must be selected.
func (s *Service) ResetFactors(ctx context.Context, userID int64, r FactorReset) error {
	if !r.TOTP && !r.Passkeys && !r.OIDC {
		return fmt.Errorf("%w: choose at least one factor to reset", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.userByID(ctx, tx, userID); err != nil {
		return err
	}
	var stmts []string
	if r.TOTP {
		stmts = append(stmts, `DELETE FROM totp_secrets WHERE user_id = ?`, `DELETE FROM recovery_codes WHERE user_id = ?`,
			`DELETE FROM auth_flows WHERE user_id = ? AND kind = 'second_factor'`)
	}
	if r.Passkeys {
		stmts = append(stmts, `DELETE FROM webauthn_credentials WHERE user_id = ?`)
	}
	if r.OIDC {
		stmts = append(stmts, `DELETE FROM oidc_identities WHERE user_id = ?`)
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	if err := s.revokeSessionsAndTokens(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeUserCredential revokes one of userID's app credentials and drops its lock;
// another user's credential is ErrNotFound. Revoking twice is a no-op.
func (s *Service) RevokeUserCredential(ctx context.Context, userID, credID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM credentials WHERE id = ? AND user_id = ?`, credID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credentials SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, s.now().UnixMilli(), credID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE credential_id = ?`, credID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- user settings (whitelisted) ----

var settingValidators = map[string]func(string) bool{
	"bookmarks.view": func(v string) bool { return v == "list" || v == "grid" },
	"bookmarks.sort": func(v string) bool { return v == "manual" || v == "title" || v == "created" },
	"ui.timezone": func(v string) bool {
		if v == "" || len(v) > 64 || v == "Local" || strings.Contains(v, "..") || strings.HasPrefix(v, "/") {
			return false
		}
		_, err := time.LoadLocation(v)
		return err == nil
	},
}

// Settings returns the user's whitelisted settings.
func (s *Service) Settings(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM user_settings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if _, ok := settingValidators[k]; ok {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// UpdateSettings applies changes (nil removes a key) atomically: any unknown key or
// invalid value rejects the whole batch with ErrInvalid.
func (s *Service) UpdateSettings(ctx context.Context, userID int64, changes map[string]*string) (map[string]string, error) {
	if len(changes) == 0 {
		return nil, fmt.Errorf("%w: no settings given", ErrInvalid)
	}
	for k, v := range changes {
		valid, ok := settingValidators[k]
		if !ok {
			return nil, fmt.Errorf("%w: unknown setting %q", ErrInvalid, k)
		}
		if v != nil && !valid(*v) {
			return nil, fmt.Errorf("%w: invalid value for %s", ErrInvalid, k)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	for k, v := range changes {
		if v == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_settings WHERE user_id = ? AND key = ?`, userID, k); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_settings(user_id, key, value, updated_at) SELECT ?, ?, ?, ? WHERE `+activeUserSQL+`
			ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			userID, k, *v, now, userID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Settings(ctx, userID)
}
