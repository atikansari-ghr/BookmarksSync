package identity

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/network"
	_ "time/tzdata" // IANA zones for ui.timezone validation on every platform
	"unicode"
	"unicode/utf8"

	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck/exclude"
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

// ListUserThemeDefaults returns the account theme overrides in one query for the
// administrator's paginated account list. Missing entries inherit the account's
// regular theme preference.
func (s *Service) ListUserThemeDefaults(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,value FROM user_setting_defaults WHERE key='ui.theme'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]string)
	for rows.Next() {
		var userID int64
		var theme string
		if err := rows.Scan(&userID, &theme); err != nil {
			return nil, err
		}
		out[userID] = theme
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
	// Link-check exclusions only ever skip checks; the checker's public-only rules still apply.
	exclude.SettingKey: exclude.Valid,
	"dashboard.layout": validDashboardLayout,
	"ui.theme": func(v string) bool {
		return v == "classic" || v == "ocean-glass" || v == "midnight-glass" || v == "warm-glass"
	},
	"ui.color-scheme":         func(v string) bool { return v == "light" || v == "dark" },
	"ui.reduced-transparency": func(v string) bool { return v == "true" || v == "false" },
	"ui.timezone": func(v string) bool {
		if v == "" || len(v) > 64 || v == "Local" || strings.Contains(v, "..") || strings.HasPrefix(v, "/") {
			return false
		}
		_, err := time.LoadLocation(v)
		return err == nil
	},
}

var defaultSettingKeys = map[string]bool{
	"bookmarks.view": true, "bookmarks.sort": true, "dashboard.layout": true,
	"ui.theme": true, "ui.color-scheme": true, "ui.reduced-transparency": true,
}

var systemSettingValidators = map[string]func(string) bool{
	network.SettingGeoMode:      func(v string) bool { return v == "allow" || v == "block" },
	network.SettingGeoCountries: func(v string) bool { _, err := network.ParseCountryCodes(v); return err == nil },
	"branding.app-title":        func(v string) bool { return validPlainSetting(v, 80) },
	"branding.signin-title":     func(v string) bool { return validPlainSetting(v, 100) },
	"branding.tagline":          func(v string) bool { return validPlainSetting(v, 160) },
	"branding.supporting-line":  func(v string) bool { return validPlainSetting(v, 240) },
	"branding.primary-color":    validHexColor,
	"branding.accent-color":     validHexColor,
	"branding.background-color": validHexColor,
	"branding.login-layout":     func(v string) bool { return v == "split" || v == "centered" || v == "plain" },
	"branding.logo-id":          validBrandingAssetID,
	"branding.background-id":    validBrandingAssetID,
}

func validBrandingAssetID(v string) bool {
	if len(v) != 32 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validPlainSetting(v string, max int) bool {
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max || strings.TrimSpace(v) != v || strings.ContainsAny(v, "<>") {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validHexColor(v string) bool {
	if len(v) != 7 || v[0] != '#' {
		return false
	}
	for _, r := range v[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

type dashboardWidgetSetting struct {
	ID      string `json:"id"`
	Visible bool   `json:"visible"`
	Order   int    `json:"order"`
	Size    string `json:"size"`
}

func validDashboardLayout(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	var layout struct {
		Widgets []dashboardWidgetSetting `json:"widgets"`
	}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&layout); err != nil || len(layout.Widgets) != 4 {
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return false
	}
	seenID, seenOrder := map[string]bool{}, map[int]bool{}
	for _, w := range layout.Widgets {
		if (w.ID != "total" && w.ID != "recent" && w.ID != "sync" && w.ID != "duplicates") || seenID[w.ID] || w.Order < 0 || w.Order > 3 || seenOrder[w.Order] ||
			(w.Size != "small" && w.Size != "medium" && w.Size != "large") {
			return false
		}
		seenID[w.ID], seenOrder[w.Order] = true, true
	}
	return true
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	defaults, err := s.db.QueryContext(ctx, `SELECT key, value FROM user_setting_defaults WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer defaults.Close()
	for defaults.Next() {
		var k, v string
		if err := defaults.Scan(&k, &v); err != nil {
			return nil, err
		}
		if _, valid := settingValidators[k]; valid {
			if _, override := out[k]; !override {
				out[k] = v
			}
		}
	}
	return out, defaults.Err()
}

// UpdateUserSettingDefaults sets the values an administrator has chosen for one user.
// User-owned settings take precedence and deleting a user override reveals the default.
func (s *Service) UpdateUserSettingDefaults(ctx context.Context, userID int64, changes map[string]*string) (map[string]string, error) {
	if userID <= 0 || len(changes) == 0 {
		return nil, fmt.Errorf("%w: no default settings given", ErrInvalid)
	}
	for k, v := range changes {
		valid, ok := settingValidators[k]
		if !defaultSettingKeys[k] || !ok || v != nil && !valid(*v) {
			return nil, fmt.Errorf("%w: invalid default setting %q", ErrInvalid, k)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	for k, v := range changes {
		if v == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_setting_defaults WHERE user_id = ? AND key = ?`, userID, k); err != nil {
				return nil, err
			}
		} else if _, err := tx.ExecContext(ctx, `INSERT INTO user_setting_defaults(user_id,key,value,updated_at) VALUES (?,?,?,?) ON CONFLICT(user_id,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, userID, k, *v, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.UserSettingDefaults(ctx, userID)
}

func (s *Service) UserSettingDefaults(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM user_setting_defaults WHERE user_id = ?`, userID)
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
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Service) SystemSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM system_settings`)
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
		if _, ok := systemSettingValidators[k]; ok {
			out[k] = v
		}
	}
	return out, rows.Err()
}

func (s *Service) UpdateSystemSettings(ctx context.Context, changes map[string]*string) (map[string]string, error) {
	if len(changes) == 0 {
		return nil, fmt.Errorf("%w: no system settings given", ErrInvalid)
	}
	for k, v := range changes {
		valid, ok := systemSettingValidators[k]
		if !ok || v != nil && !valid(*v) {
			return nil, fmt.Errorf("%w: invalid system setting %q", ErrInvalid, k)
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
			if _, err := tx.ExecContext(ctx, `DELETE FROM system_settings WHERE key=?`, k); err != nil {
				return nil, err
			}
		} else if _, err := tx.ExecContext(ctx, `INSERT INTO system_settings(key,value,updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, k, *v, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.SystemSettings(ctx)
}

type AdminActivity struct {
	ID         int64
	ActorID    int64
	Action     string
	TargetType string
	TargetID   *int64
	CreatedAt  time.Time
}

var adminActivityTargets = map[string]map[string]bool{
	"user_created": {"user": true}, "user_updated": {"user": true}, "user_settings_updated": {"user": true},
	"connection_revoked": {"user": true}, "password_reset": {"user": true}, "factor_reset": {"user": true},
	"system_settings_updated": {"system": true}, "branding_asset_uploaded": {"system": true, "asset": true},
	"backup_schedule_updated": {"backup": true}, "backup_run_started": {"backup": true},
	"backup_destination_created": {"backup_destination": true}, "backup_destination_updated": {"backup_destination": true},
	"backup_destination_deleted": {"backup_destination": true},
	"software_update_approved":   {"system": true},
}

// RecordAdminActivity stores only a fixed action code and numeric target identifiers.
// Callers cannot attach usernames, bookmark content, request data, or secrets.
func (s *Service) RecordAdminActivity(ctx context.Context, actorID int64, action, targetType string, targetID int64) error {
	allowedTargets := adminActivityTargets[action]
	if actorID <= 0 || !allowedTargets[targetType] ||
		((targetType == "system" || targetType == "backup" || targetType == "backup_destination") && targetID != 0) ||
		((targetType == "user" || targetType == "asset") && targetID <= 0) {
		return fmt.Errorf("%w: invalid activity event", ErrInvalid)
	}
	var target any
	if targetID > 0 {
		target = targetID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_activity(actor_id,action,target_type,target_id,created_at) VALUES (?,?,?,?,?)`, actorID, action, targetType, target, s.now().UnixMilli())
	return err
}

// RecordSystemActivity records the approved local geo-policy reset without a
// signed-in administrator; the existing action code and schema stay compatible.
func (s *Service) RecordSystemActivity(ctx context.Context, action, targetType string, targetID int64) error {
	if action != "system_settings_updated" || targetType != "system" || targetID != 0 {
		return fmt.Errorf("%w: invalid system activity event", ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_activity(actor_id,action,target_type,target_id,created_at) VALUES (NULL,?,?,NULL,?)`, action, targetType, s.now().UnixMilli())
	return err
}

func (s *Service) ListAdminActivity(ctx context.Context, limit int) ([]AdminActivity, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: invalid activity limit", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(actor_id,0),action,target_type,target_id,created_at FROM admin_activity ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminActivity{}
	for rows.Next() {
		var v AdminActivity
		var target sql.NullInt64
		var at int64
		if err := rows.Scan(&v.ID, &v.ActorID, &v.Action, &v.TargetType, &target, &at); err != nil {
			return nil, err
		}
		if target.Valid {
			id := target.Int64
			v.TargetID = &id
		}
		v.CreatedAt = time.UnixMilli(at).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateSettings applies changes (nil removes a key) atomically: any unknown key or
// invalid value rejects the whole batch with ErrInvalid.
func (s *Service) UpdateSettings(ctx context.Context, userID int64, changes map[string]*string) (map[string]string, error) {
	if len(changes) == 0 {
		return nil, fmt.Errorf("%w: no settings given", ErrInvalid)
	}
	if err := ValidateSettingsChanges(changes); err != nil {
		return nil, err
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

// ValidateSettingsChanges validates an override batch without applying it.
func ValidateSettingsChanges(changes map[string]*string) error {
	if len(changes) == 0 {
		return fmt.Errorf("%w: no settings given", ErrInvalid)
	}
	for k, v := range changes {
		valid, ok := settingValidators[k]
		if !ok {
			return fmt.Errorf("%w: unknown setting %q", ErrInvalid, k)
		}
		if v != nil && !valid(*v) {
			return fmt.Errorf("%w: invalid value for %s", ErrInvalid, k)
		}
	}
	return nil
}
