package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TOTP follows RFC 6238 with the parameters authenticator apps universally support:
// HMAC-SHA1, 6 digits, a 30-second period from the Unix epoch, and a 160-bit random
// seed. A code is accepted for the current step or one step either side (±30 s of
// clock skew). Each accepted step is recorded atomically and only a strictly later
// step is accepted afterwards, so a code can never be replayed.
const (
	TOTPAlgorithm     = "SHA1"
	TOTPDigits        = 6
	TOTPPeriod        = 30 * time.Second
	TOTPSkewSteps     = 1
	TOTPEnrollmentTTL = 10 * time.Minute // a pending enrollment must be confirmed in time
	totpSeedBytes     = 20
	totpIssuer        = "BookmarksSync"

	// RecoveryCodeCount codes of 80 random bits each are issued together, shown once,
	// stored as SHA-256 digests and consumed once.
	RecoveryCodeCount = 10
	recoveryCodeBytes = 10

	// A password login for a TOTP user yields a pending second-factor flow that lasts
	// SecondFactorTTL and tolerates SecondFactorMaxAttempts wrong codes.
	SecondFactorTTL         = 5 * time.Minute
	SecondFactorMaxAttempts = 5
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// hotp is RFC 4226 HOTP with SHA-1 and dynamic truncation.
func hotp(seed []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, seed)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, v%mod)
}

func totpStep(t time.Time) int64 { return t.Unix() / int64(TOTPPeriod/time.Second) }

func wellFormedCode(code string) bool {
	if len(code) != TOTPDigits {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

// matchStep returns the step within the skew window, strictly after afterStep, whose
// code equals code; ok is false if none does. Every candidate is compared.
func matchStep(seed []byte, now time.Time, code string, afterStep int64) (int64, bool) {
	if !wellFormedCode(code) {
		return 0, false
	}
	cur := totpStep(now)
	var found int64
	ok := false
	for st := cur - TOTPSkewSteps; st <= cur+TOTPSkewSteps; st++ {
		eq := subtle.ConstantTimeCompare([]byte(hotp(seed, uint64(st), TOTPDigits)), []byte(code)) == 1
		if eq && st > afterStep && !ok {
			found, ok = st, true
		}
	}
	return found, ok
}

func totpBinding(userID int64) string { return "totp-seed/" + strconv.FormatInt(userID, 10) }

// TOTPEnrollment is shown once to the authenticated owner and never logged.
type TOTPEnrollment struct {
	Secret string // base32 seed for manual entry
	URI    string // otpauth:// provisioning URI
}

// TOTPStatus is the non-secret TOTP state of an account.
type TOTPStatus struct {
	Enabled                bool
	RecoveryCodesRemaining int
}

func (s *Service) requireBox() error {
	if s.box == nil {
		return fmt.Errorf("%w: TOTP needs an instance key", ErrNotConfigured)
	}
	return nil
}

// BeginTOTPEnrollment creates (or replaces) a pending TOTP seed for an active user and
// returns it once. It fails with ErrExists if TOTP is already enabled.
func (s *Service) BeginTOTPEnrollment(ctx context.Context, userID int64) (TOTPEnrollment, error) {
	if err := s.requireBox(); err != nil {
		return TOTPEnrollment{}, err
	}
	u, err := s.userByID(ctx, s.db, userID)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	if !u.Active {
		return TOTPEnrollment{}, fmt.Errorf("%w: user is inactive", ErrInvalid)
	}
	seed := make([]byte, totpSeedBytes)
	if _, err := rand.Read(seed); err != nil {
		return TOTPEnrollment{}, err
	}
	ct, err := s.box.seal(seed, totpBinding(userID))
	if err != nil {
		return TOTPEnrollment{}, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO totp_secrets(user_id, seed_ciphertext, key_version, created_at)
		SELECT ?, ?, 1, ? WHERE `+activeUserSQL+`
		ON CONFLICT(user_id) DO UPDATE SET seed_ciphertext = excluded.seed_ciphertext,
			key_version = excluded.key_version, created_at = excluded.created_at, last_used_step = NULL
		WHERE totp_secrets.confirmed_at IS NULL`,
		userID, ct, s.now().UnixMilli(), userID)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if st, err := s.TOTPStatus(ctx, userID); err == nil && st.Enabled {
			return TOTPEnrollment{}, fmt.Errorf("%w: TOTP is already enabled", ErrExists)
		}
		return TOTPEnrollment{}, fmt.Errorf("%w: user is inactive", ErrInvalid)
	}
	secret := b32.EncodeToString(seed)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", totpIssuer)
	q.Set("algorithm", TOTPAlgorithm)
	q.Set("digits", strconv.Itoa(TOTPDigits))
	q.Set("period", strconv.Itoa(int(TOTPPeriod/time.Second)))
	uri := (&url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + totpIssuer + ":" + u.Username, RawQuery: q.Encode()}).String()
	return TOTPEnrollment{Secret: secret, URI: uri}, nil
}

// ConfirmTOTPEnrollment enables a pending seed after a valid code, issues fresh
// recovery codes (returned once) and revokes the user's other sessions except
// keepSessionID. A wrong code or an expired pending enrollment is ErrUnauthorized.
func (s *Service) ConfirmTOTPEnrollment(ctx context.Context, userID, keepSessionID int64, code string) ([]string, error) {
	if err := s.requireBox(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var ct []byte
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT seed_ciphertext, created_at FROM totp_secrets
		WHERE user_id = ? AND confirmed_at IS NULL`, userID).Scan(&ct, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if !now.Before(time.UnixMilli(created).Add(TOTPEnrollmentTTL)) {
		return nil, ErrUnauthorized
	}
	if ok, err := isActive(ctx, tx, userID); err != nil || !ok {
		return nil, errors.Join(ErrUnauthorized, err)
	}
	seed, err := s.box.open(ct, totpBinding(userID))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot decrypt TOTP seed", ErrUnauthorized)
	}
	step, ok := matchStep(seed, now, code, -1<<62)
	if !ok {
		return nil, ErrUnauthorized
	}
	if _, err := tx.ExecContext(ctx, `UPDATE totp_secrets SET confirmed_at = ?, last_used_step = ?
		WHERE user_id = ? AND confirmed_at IS NULL`, now.UnixMilli(), step, userID); err != nil {
		return nil, err
	}
	codes, err := s.replaceRecoveryCodes(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.revokeOtherSessions(ctx, tx, userID, keepSessionID); err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

// VerifyTOTP accepts a code for an active user's enabled TOTP once. Any failure,
// including a replayed or earlier step, is ErrUnauthorized.
func (s *Service) VerifyTOTP(ctx context.Context, userID int64, code string) error {
	if err := s.requireBox(); err != nil {
		return err
	}
	return s.verifyTOTP(ctx, s.db, userID, code)
}

func (s *Service) verifyTOTP(ctx context.Context, q querier, userID int64, code string) error {
	var ct []byte
	var last sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT t.seed_ciphertext, t.last_used_step FROM totp_secrets t
		JOIN users u ON u.id = t.user_id
		WHERE t.user_id = ? AND t.confirmed_at IS NOT NULL AND u.active = 1`, userID).Scan(&ct, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	seed, err := s.box.open(ct, totpBinding(userID))
	if err != nil {
		return fmt.Errorf("%w: cannot decrypt TOTP seed", ErrUnauthorized)
	}
	after := int64(-1 << 62)
	if last.Valid {
		after = last.Int64
	}
	step, ok := matchStep(seed, s.now(), code, after)
	if !ok {
		return ErrUnauthorized
	}
	if s.beforeTOTPStepRecord != nil {
		s.beforeTOTPStepRecord()
	}
	// Recording the step is the atomic replay guard: only one request can advance it.
	res, err := q.ExecContext(ctx, `UPDATE totp_secrets SET last_used_step = ?
		WHERE user_id = ? AND confirmed_at IS NOT NULL AND (last_used_step IS NULL OR last_used_step < ?)`,
		step, userID, step)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrUnauthorized
	}
	return nil
}

func normalizeRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		switch {
		case r == '-' || r == ' ' || r == '\t':
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newRecoveryCode() (string, error) {
	raw := make([]byte, recoveryCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	s := strings.ToLower(b32.EncodeToString(raw)) // 16 characters
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16], nil
}

func (s *Service) replaceRecoveryCodes(ctx context.Context, q querier, userID int64) ([]string, error) {
	if _, err := q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	codes := make([]string, 0, RecoveryCodeCount)
	for len(codes) < RecoveryCodeCount {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO recovery_codes(user_id, code_hash, created_at) VALUES (?, ?, ?)`,
			userID, hashSecret(normalizeRecoveryCode(c)), now); err != nil {
			if isUnique(err) {
				continue
			}
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, nil
}

// ConsumeRecoveryCode spends one unused recovery code of an active user with enabled
// TOTP. Case, spaces and hyphens are ignored. Any failure is ErrUnauthorized.
func (s *Service) ConsumeRecoveryCode(ctx context.Context, userID int64, code string) error {
	return s.consumeRecoveryCode(ctx, s.db, userID, code)
}

func (s *Service) consumeRecoveryCode(ctx context.Context, q querier, userID int64, code string) error {
	norm := normalizeRecoveryCode(code)
	if len(norm) != 16 {
		return ErrUnauthorized
	}
	res, err := q.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ?
		WHERE user_id = ? AND code_hash = ? AND used_at IS NULL
		AND EXISTS (SELECT 1 FROM totp_secrets WHERE user_id = recovery_codes.user_id AND confirmed_at IS NOT NULL)
		AND EXISTS (SELECT 1 FROM users WHERE id = recovery_codes.user_id AND active = 1)`,
		s.now().UnixMilli(), userID, hashSecret(norm))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrUnauthorized
	}
	return nil
}

// TOTPStatus reports whether TOTP is enabled and how many recovery codes remain.
func (s *Service) TOTPStatus(ctx context.Context, userID int64) (TOTPStatus, error) {
	var st TOTPStatus
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM totp_secrets WHERE user_id = ?1 AND confirmed_at IS NOT NULL),
		(SELECT count(*) FROM recovery_codes WHERE user_id = ?1 AND used_at IS NULL)`, userID).
		Scan(&enabled, &st.RecoveryCodesRemaining)
	if err != nil {
		return TOTPStatus{}, err
	}
	st.Enabled = enabled == 1
	if !st.Enabled {
		st.RecoveryCodesRemaining = 0
	}
	return st, nil
}

// DisableTOTP removes the seed and recovery codes and revokes the user's other
// sessions except keepSessionID. ErrNotFound if TOTP is not enabled. The caller must
// have verified recent reauthentication and a current proof.
func (s *Service) DisableTOTP(ctx context.Context, userID, keepSessionID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM totp_secrets WHERE user_id = ? AND confirmed_at IS NOT NULL`, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_flows WHERE user_id = ? AND kind = ?`, userID, flowSecondFactor); err != nil {
		return err
	}
	if err := s.revokeOtherSessions(ctx, tx, userID, keepSessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// RegenerateRecoveryCodes replaces every recovery code and returns the new ones once.
// ErrNotFound if TOTP is not enabled.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM totp_secrets t JOIN users u ON u.id = t.user_id
		WHERE t.user_id = ? AND t.confirmed_at IS NOT NULL AND u.active = 1`, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	codes, err := s.replaceRecoveryCodes(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

// SecondFactorFlow is a pending password+TOTP login. Its ID is shown once to the
// client; it is not a session and authorizes nothing but CompleteSecondFactor.
type SecondFactorFlow struct {
	ID        string
	ExpiresAt time.Time
}

// SecondFactorProof carries exactly one of a TOTP code or a recovery code.
type SecondFactorProof struct {
	TOTP         string
	RecoveryCode string
}

// BeginSecondFactor starts a pending second-factor login for an active user with
// enabled TOTP: ErrNotFound if TOTP is not enabled, ErrInvalid if the user is inactive.
// Call it only after the password verified. There is at most one pending flow per
// account: checking TOTP and activity, discarding older pending flows and inserting the
// new one happen in one (immediate) transaction, so concurrent logins cannot leave two
// usable flows, and the per-flow attempt limit also bounds guesses per account.
func (s *Service) BeginSecondFactor(ctx context.Context, userID int64) (SecondFactorFlow, error) {
	if err := s.requireBox(); err != nil {
		return SecondFactorFlow{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecondFactorFlow{}, err
	}
	defer tx.Rollback()
	if err := requireActiveUser(ctx, tx, userID); err != nil {
		return SecondFactorFlow{}, err
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM totp_secrets WHERE user_id = ? AND confirmed_at IS NOT NULL`, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return SecondFactorFlow{}, ErrNotFound
	}
	if err != nil {
		return SecondFactorFlow{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_flows WHERE user_id = ? AND kind = ? AND consumed_at IS NULL`,
		userID, flowSecondFactor); err != nil {
		return SecondFactorFlow{}, err
	}
	id, exp, err := s.createFlow(ctx, tx, flowSecondFactor, userID, 0, `{"attempts":0}`, SecondFactorTTL)
	if err != nil {
		return SecondFactorFlow{}, err
	}
	if err := tx.Commit(); err != nil {
		return SecondFactorFlow{}, err
	}
	return SecondFactorFlow{ID: id, ExpiresAt: exp}, nil
}

// CompleteSecondFactor finishes a pending login with one proof. The flow, the proof
// and the account's active status are checked and the flow consumed in one
// transaction; a wrong proof counts an attempt, and the flow is discarded after
// SecondFactorMaxAttempts. Any failure is ErrUnauthorized; for a wrong proof on a live
// flow the returned User carries only the flow owner's ID (Active false) so callers can
// count failures per account. On success the user is active at completion; the caller
// issues the session (which re-checks activity atomically).
func (s *Service) CompleteSecondFactor(ctx context.Context, flowID string, proof SecondFactorProof) (User, error) {
	if err := s.requireBox(); err != nil {
		return User{}, err
	}
	if (proof.TOTP == "") == (proof.RecoveryCode == "") {
		return User{}, ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var f flowRow
	var uid sql.NullInt64
	err = tx.QueryRowContext(ctx, `UPDATE auth_flows SET consumed_at = ?1
		WHERE flow_hash = ?2 AND kind = ?3 AND consumed_at IS NULL AND expires_at > ?1 AND user_id IS NOT NULL
		RETURNING id, user_id`, s.now().UnixMilli(), hashSecret(flowID), flowSecondFactor).Scan(&f.ID, &uid)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	f.UserID = uid.Int64
	u, err := s.userByID(ctx, tx, f.UserID)
	if err != nil || !u.Active {
		return User{}, errors.Join(ErrUnauthorized, ignoreNotFound(err))
	}
	if proof.TOTP != "" {
		err = s.verifyTOTP(ctx, tx, f.UserID, proof.TOTP)
	} else {
		err = s.consumeRecoveryCode(ctx, tx, f.UserID, proof.RecoveryCode)
	}
	if s.secondFactorProofChecked != nil {
		s.secondFactorProofChecked()
	}
	if err != nil {
		if !errors.Is(err, ErrUnauthorized) {
			return User{}, err
		}
		// Count the attempt in the same (immediate) transaction that evaluated it, so
		// concurrent wrong proofs serialize: release the flow again unless this attempt
		// reaches the limit, in which case it stays consumed. A failed proof wrote
		// nothing, so committing here only records the attempt.
		if _, err := tx.ExecContext(ctx, `UPDATE auth_flows
			SET data = json_set(data, '$.attempts', coalesce(json_extract(data, '$.attempts'), 0) + 1),
			    consumed_at = CASE WHEN coalesce(json_extract(data, '$.attempts'), 0) + 1 >= ?1 THEN consumed_at END
			WHERE id = ?2`, SecondFactorMaxAttempts, f.ID); err != nil {
			return User{}, err
		}
		if err := tx.Commit(); err != nil {
			return User{}, err
		}
		// Only the id: lets the caller count failures per account. Not authenticated.
		return User{ID: f.UserID}, ErrUnauthorized
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

func ignoreNotFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
