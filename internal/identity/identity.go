// Package identity provides minimal user and per-device app-credential
// provisioning and verification. It is reusable by the later website login.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrExists       = errors.New("already exists")
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
)

// Mode is the permission level of a credential or grant.
type Mode string

const (
	ModeView Mode = "view"
	ModeEdit Mode = "edit"
)

// Kind is the scope kind a credential is bound to.
type Kind string

const (
	KindPrivate Kind = "private"
	KindShare   Kind = "share"
)

const (
	minPasswordLen   = 12
	maxPasswordBytes = 1024
	maxUsernameRunes = 64
	maxLabelRunes    = 200
	secretPrefix     = "bms1"
	lastUsedThrottle = time.Minute
)

type User struct {
	ID       int64
	Username string
	IsAdmin  bool
	Active   bool
}

type Credential struct {
	ID      int64
	UserID  int64
	Label   string
	Kind    Kind
	ShareID int64 // 0 for private
	Mode    Mode
}

type NewCredential struct {
	UserID  int64
	Label   string
	Kind    Kind // defaults to KindPrivate
	ShareID int64
	Mode    Mode
}

// Principal is the authenticated caller behind a valid app credential.
type Principal struct {
	CredentialID int64
	User         User
	Kind         Kind
	ShareID      int64
	Mode         Mode
}

type Service struct {
	db   *sql.DB
	now  func() time.Time
	mem  uint32 // KiB
	iter uint32
	par  uint8
}

type Option func(*Service)

// WithClock overrides the time source (tests).
func WithClock(f func() time.Time) Option { return func(s *Service) { s.now = f } }

// WithPasswordCost overrides the Argon2id cost (memoryKiB, iterations, parallelism).
// Intended for tests; the defaults follow the RFC 9106 second recommended option scaled
// to 64 MiB.
func WithPasswordCost(memoryKiB, iterations uint32, parallelism uint8) Option {
	return func(s *Service) { s.mem, s.iter, s.par = memoryKiB, iterations, parallelism }
}

func New(db *sql.DB, opts ...Option) *Service {
	s := &Service{db: db, now: time.Now, mem: 64 * 1024, iter: 3, par: 2}
	for _, o := range opts {
		o(s)
	}
	return s
}

func validUsername(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxUsernameRunes || !utf8.ValidString(name) {
		return false
	}
	if strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		// ':' would break the HTTP Basic user:secret split.
		if r == ':' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// CreateUser stores a new active user with an Argon2id-hashed password.
func (s *Service) CreateUser(ctx context.Context, username, password string, admin bool) (User, error) {
	if !validUsername(username) {
		return User{}, fmt.Errorf("%w: username must be 1-%d characters, without ':' control characters or surrounding space", ErrInvalid, maxUsernameRunes)
	}
	if len(password) < minPasswordLen || len(password) > maxPasswordBytes {
		return User{}, fmt.Errorf("%w: password must be %d-%d bytes", ErrInvalid, minPasswordLen, maxPasswordBytes)
	}
	hash, err := s.hashPassword(password)
	if err != nil {
		return User{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users(username, password_hash, is_admin, active, created_at) VALUES (?, ?, ?, 1, ?)`,
		username, hash, b2i(admin), s.now().UnixMilli())
	if err != nil {
		if isUnique(err) {
			return User{}, fmt.Errorf("%w: user %q", ErrExists, username)
		}
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, IsAdmin: admin, Active: true}, nil
}

func (s *Service) GetUser(ctx context.Context, username string) (User, error) {
	var u User
	var admin, active int
	err := s.db.QueryRowContext(ctx, `SELECT id, username, is_admin, active FROM users WHERE username = ?`, username).
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

// SetActive enables or disables a user. Disabled users fail every authentication
// immediately because credentials are checked against the live row.
func (s *Service) SetActive(ctx context.Context, username string, active bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET active = ? WHERE username = ?`, b2i(active), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// dummyHash lets VerifyPassword spend comparable time for unknown users.
var dummyHash string

// VerifyPassword checks an interactive password. Never use it for floccus auth.
func (s *Service) VerifyPassword(ctx context.Context, username, password string) (User, error) {
	var u User
	var admin, active int
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, username, is_admin, active, password_hash FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &admin, &active, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		if dummyHash == "" {
			dummyHash, _ = s.hashPassword("dummy password for timing")
		}
		verifyHash(dummyHash, password)
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	ok := verifyHash(hash, password)
	if !ok || active != 1 {
		return User{}, ErrUnauthorized
	}
	u.IsAdmin, u.Active = admin == 1, true
	return u, nil
}

func (s *Service) hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, s.iter, s.mem, s.par, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, s.mem, s.iter, s.par,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyHash(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m > 1<<20 || t > 100 || p == 0 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// CreateCredential mints a random 256-bit app secret, stores only its SHA-256, and
// returns the plaintext once. The caller must show it to the user once and discard it.
func (s *Service) CreateCredential(ctx context.Context, nc NewCredential) (Credential, string, error) {
	if nc.Kind == "" {
		nc.Kind = KindPrivate
	}
	if nc.Mode != ModeView && nc.Mode != ModeEdit {
		return Credential{}, "", fmt.Errorf("%w: mode must be view or edit", ErrInvalid)
	}
	switch {
	case nc.Kind == KindPrivate && nc.ShareID != 0, nc.Kind == KindShare && nc.ShareID == 0,
		nc.Kind != KindPrivate && nc.Kind != KindShare:
		return Credential{}, "", fmt.Errorf("%w: share id is required for share credentials only", ErrInvalid)
	}
	if utf8.RuneCountInString(nc.Label) > maxLabelRunes {
		return Credential{}, "", fmt.Errorf("%w: label too long", ErrInvalid)
	}
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, nc.UserID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, "", ErrNotFound
	}
	if err != nil {
		return Credential{}, "", err
	}
	if active != 1 {
		return Credential{}, "", fmt.Errorf("%w: user is inactive", ErrInvalid)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return Credential{}, "", err
	}
	randPart := base64.RawURLEncoding.EncodeToString(random)
	var shareArg any
	if nc.ShareID != 0 {
		shareArg = nc.ShareID
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO credentials(user_id, label, secret_hash, kind, share_id, mode, created_at) VALUES (?,?,?,?,?,?,?)`,
		nc.UserID, nc.Label, hashSecret(randPart), string(nc.Kind), shareArg, string(nc.Mode), s.now().UnixMilli())
	if err != nil {
		return Credential{}, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	id, _ := res.LastInsertId()
	secret := secretPrefix + "." + strconv.FormatInt(id, 10) + "." + randPart
	return Credential{ID: id, UserID: nc.UserID, Label: nc.Label, Kind: nc.Kind, ShareID: nc.ShareID, Mode: nc.Mode}, secret, nil
}

func hashSecret(randPart string) string {
	sum := sha256.Sum256([]byte(randPart))
	return hex.EncodeToString(sum[:])
}

// RevokeCredential permanently disables a credential. Revoking twice is a no-op.
func (s *Service) RevokeCredential(ctx context.Context, id int64) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM credentials WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, s.now().UnixMilli(), id)
	return err
}

// zeroHash stands in for the stored hash when the credential id is unknown so that
// failed lookups still perform the same comparison.
var zeroHash = strings.Repeat("0", 64)

// Authenticate verifies an HTTP Basic username and app secret against live state:
// the credential must exist and be unrevoked, belong to that active user, and the
// secret hash must match (constant-time comparison).
func (s *Service) Authenticate(ctx context.Context, username, secret string) (Principal, error) {
	parts := strings.Split(secret, ".")
	var credID int64
	var randPart string
	if len(parts) == 3 && parts[0] == secretPrefix && len(parts[2]) == 43 {
		if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil && id > 0 {
			credID, randPart = id, parts[2]
		}
	}
	storedHash := zeroHash
	var p Principal
	var uname string
	var admin, active int
	var revoked sql.NullInt64
	var shareID sql.NullInt64
	found := false
	if credID != 0 {
		err := s.db.QueryRowContext(ctx, `
			SELECT c.secret_hash, c.kind, c.share_id, c.mode, c.revoked_at, u.id, u.username, u.is_admin, u.active
			FROM credentials c JOIN users u ON u.id = c.user_id WHERE c.id = ?`, credID).
			Scan(&storedHash, &p.Kind, &shareID, &p.Mode, &revoked, &p.User.ID, &uname, &admin, &active)
		if err == nil {
			found = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Principal{}, err
		} else {
			storedHash = zeroHash
		}
	}
	presented := hashSecret(randPart)
	secretOK := subtle.ConstantTimeCompare([]byte(presented), []byte(storedHash)) == 1
	nameOK := subtle.ConstantTimeCompare([]byte(strings.ToLower(username)), []byte(strings.ToLower(uname))) == 1
	if !found || !secretOK || !nameOK || revoked.Valid || active != 1 || username == "" {
		return Principal{}, ErrUnauthorized
	}
	p.CredentialID = credID
	p.User.Username, p.User.IsAdmin, p.User.Active = uname, admin == 1, true
	p.ShareID = shareID.Int64
	s.touch(ctx, credID)
	return p, nil
}

// touch records last use at most once per throttle window to avoid a write per request.
func (s *Service) touch(ctx context.Context, id int64) {
	now := s.now().UnixMilli()
	_, _ = s.db.ExecContext(ctx,
		`UPDATE credentials SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
		now, id, now-lastUsedThrottle.Milliseconds())
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CredentialInfo is the non-secret metadata of a credential.
type CredentialInfo struct {
	Credential
	CreatedAt  time.Time
	LastUsedAt time.Time // zero if never used
	Revoked    bool
}

// ListCredentials returns every credential of a user (including revoked ones) in
// creation order. It never includes secrets or their hashes.
func (s *Service) ListCredentials(ctx context.Context, userID int64) ([]CredentialInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, label, kind, share_id, mode, created_at, last_used_at, revoked_at
		FROM credentials WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredentialInfo{}
	for rows.Next() {
		var ci CredentialInfo
		var share, lastUsed, revoked sql.NullInt64
		var created int64
		if err := rows.Scan(&ci.ID, &ci.UserID, &ci.Label, &ci.Kind, &share, &ci.Mode, &created, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		ci.ShareID = share.Int64
		ci.CreatedAt = time.UnixMilli(created).UTC()
		if lastUsed.Valid {
			ci.LastUsedAt = time.UnixMilli(lastUsed.Int64).UTC()
		}
		ci.Revoked = revoked.Valid
		out = append(out, ci)
	}
	return out, rows.Err()
}
