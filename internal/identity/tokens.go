package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Scope is a personal API token permission. Scopes are exact: no scope implies another.
type Scope string

const (
	ScopeBookmarksRead  Scope = "bookmarks:read"
	ScopeBookmarksWrite Scope = "bookmarks:write"
	ScopeSharesRead     Scope = "shares:read"
	ScopeSharesManage   Scope = "shares:manage"
)

// tokenPrefix marks personal API tokens so they are recognisable to secret scanners
// and cannot be confused with session secrets or floccus app credentials.
const tokenPrefix = "bmt1_"

var tokenScopes = []Scope{ScopeBookmarksRead, ScopeBookmarksWrite, ScopeSharesRead, ScopeSharesManage}

// TokenScopes returns every grantable scope in canonical order. There is deliberately
// no admin scope: personal API tokens never authorize administrator operations.
func TokenScopes() []Scope { return append([]Scope(nil), tokenScopes...) }

func validScope(sc Scope) bool {
	for _, v := range tokenScopes {
		if sc == v {
			return true
		}
	}
	return false
}

// APIToken is the non-secret metadata of a personal API token.
type APIToken struct {
	ID         int64
	UserID     int64
	Label      string
	Scopes     []Scope // canonical order
	CreatedAt  time.Time
	ExpiresAt  time.Time // zero: never expires
	LastUsedAt time.Time // zero: never used
	Revoked    bool
	Expired    bool
}

type NewAPIToken struct {
	UserID    int64
	Label     string
	Scopes    []Scope
	ExpiresAt time.Time // zero: never expires; otherwise must be in the future
}

// TokenPrincipal is the caller behind a valid personal API token.
type TokenPrincipal struct {
	Token APIToken
	User  User
}

// Allows reports whether the token carries exactly this scope.
func (p TokenPrincipal) Allows(sc Scope) bool {
	if !validScope(sc) {
		return false
	}
	for _, have := range p.Token.Scopes {
		if have == sc {
			return true
		}
	}
	return false
}

// AuthorizesAdmin is always false: administrator endpoints require an admin web
// session, whatever the token owner's role.
func (p TokenPrincipal) AuthorizesAdmin() bool { return false }

func canonicalScopes(in []Scope) ([]Scope, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrInvalid)
	}
	seen := map[Scope]bool{}
	for _, sc := range in {
		if !validScope(sc) {
			return nil, fmt.Errorf("%w: unknown scope %q", ErrInvalid, sc)
		}
		if seen[sc] {
			return nil, fmt.Errorf("%w: duplicate scope %q", ErrInvalid, sc)
		}
		seen[sc] = true
	}
	out := []Scope{}
	for _, sc := range tokenScopes {
		if seen[sc] {
			out = append(out, sc)
		}
	}
	return out, nil
}

func joinScopes(scopes []Scope) string {
	parts := make([]string, len(scopes))
	for i, sc := range scopes {
		parts[i] = string(sc)
	}
	return strings.Join(parts, " ")
}

func splitScopes(stored string) []Scope {
	out := []Scope{}
	for _, f := range strings.Fields(stored) {
		if validScope(Scope(f)) {
			out = append(out, Scope(f))
		}
	}
	return out
}

// CreateAPIToken mints a personal API token for an active user. The plaintext token is
// returned exactly once; only its SHA-256 is stored and no other method returns it.
func (s *Service) CreateAPIToken(ctx context.Context, nt NewAPIToken) (APIToken, string, error) {
	scopes, err := canonicalScopes(nt.Scopes)
	if err != nil {
		return APIToken{}, "", err
	}
	if !utf8.ValidString(nt.Label) || utf8.RuneCountInString(nt.Label) > maxLabelRunes {
		return APIToken{}, "", fmt.Errorf("%w: label must be valid UTF-8 of at most %d characters", ErrInvalid, maxLabelRunes)
	}
	now := s.now()
	var expiresArg any
	if !nt.ExpiresAt.IsZero() {
		if !nt.ExpiresAt.After(now) {
			return APIToken{}, "", fmt.Errorf("%w: expiry must be in the future", ErrInvalid)
		}
		expiresArg = nt.ExpiresAt.UnixMilli()
	}
	if err := requireActiveUser(ctx, s.db, nt.UserID); err != nil {
		return APIToken{}, "", err
	}
	random, err := newOpaqueSecret()
	if err != nil {
		return APIToken{}, "", err
	}
	secret := tokenPrefix + random
	res, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(user_id, label, secret_hash, scopes, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, nt.UserID, nt.Label, hashSecret(secret), joinScopes(scopes), now.UnixMilli(), expiresArg)
	if err != nil {
		return APIToken{}, "", err
	}
	id, _ := res.LastInsertId()
	tok := APIToken{ID: id, UserID: nt.UserID, Label: nt.Label, Scopes: scopes, CreatedAt: msTime(now)}
	if expiresArg != nil {
		tok.ExpiresAt = msTime(nt.ExpiresAt)
	}
	return tok, secret, nil
}

const tokenCols = `t.id, t.user_id, t.label, t.scopes, t.created_at, t.expires_at, t.last_used_at, t.revoked_at`

func (s *Service) scanToken(sc interface{ Scan(...any) error }, extra ...any) (APIToken, error) {
	var tok APIToken
	var scopes string
	var created int64
	var expires, lastUsed, revoked sql.NullInt64
	dest := append([]any{&tok.ID, &tok.UserID, &tok.Label, &scopes, &created, &expires, &lastUsed, &revoked}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return APIToken{}, err
	}
	tok.Scopes = splitScopes(scopes)
	tok.CreatedAt = time.UnixMilli(created).UTC()
	if expires.Valid {
		tok.ExpiresAt = time.UnixMilli(expires.Int64).UTC()
		tok.Expired = s.now().UnixMilli() >= expires.Int64
	}
	if lastUsed.Valid {
		tok.LastUsedAt = time.UnixMilli(lastUsed.Int64).UTC()
	}
	tok.Revoked = revoked.Valid
	return tok, nil
}

// ResolveAPIToken validates a presented personal API token against live state: it must
// exist, be unrevoked and unexpired, and belong to an active user. Every failure is
// ErrUnauthorized. Use is recorded at most once per minute.
func (s *Service) ResolveAPIToken(ctx context.Context, secret string) (TokenPrincipal, error) {
	body, ok := strings.CutPrefix(secret, tokenPrefix)
	if !ok || len(body) != 43 {
		return TokenPrincipal{}, ErrUnauthorized
	}
	var p TokenPrincipal
	var admin, active int
	row := s.db.QueryRowContext(ctx, `SELECT `+tokenCols+`, u.username, u.is_admin, u.active
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.secret_hash = ?`, hashSecret(secret))
	tok, err := s.scanToken(row, &p.User.Username, &admin, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenPrincipal{}, ErrUnauthorized
	}
	if err != nil {
		return TokenPrincipal{}, err
	}
	if tok.Revoked || tok.Expired || active != 1 || len(tok.Scopes) == 0 {
		return TokenPrincipal{}, ErrUnauthorized
	}
	now := s.now().UnixMilli()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
		now, tok.ID, now-lastUsedThrottle.Milliseconds()); err != nil {
		return TokenPrincipal{}, err
	}
	p.Token = tok
	p.User.ID, p.User.IsAdmin, p.User.Active = tok.UserID, admin == 1, true
	return p, nil
}

// ListAPITokens returns every token of the user (including revoked and expired ones)
// in creation order, without secrets or hashes.
func (s *Service) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens t WHERE t.user_id = ? ORDER BY t.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		tok, err := s.scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	return out, rows.Err()
}

// RevokeAPIToken revokes one of the user's tokens. A token id that does not belong to
// userID is ErrNotFound. Revoking twice is a no-op.
func (s *Service) RevokeAPIToken(ctx context.Context, userID, tokenID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ?
		WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, s.now().UnixMilli(), tokenID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM api_tokens WHERE id = ? AND user_id = ?`, tokenID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// RevokeUserAPITokens revokes every token of the user (admin reset, account recovery).
func (s *Service) RevokeUserAPITokens(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		s.now().UnixMilli(), userID)
	return err
}
