package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// OpenID Connect persistence. A linked identity is keyed only by the verified issuer
// and subject (UNIQUE(issuer, subject) in oidc_identities); display fields are
// informational and never used for matching. Accounts are never created from an OIDC
// login. Authorization-code transactions are single-use auth_flows rows whose state
// (the flow handle) and browser-binding secret are stored only as SHA-256 digests.

// OIDCFlowTTL bounds the time between starting an OIDC transaction and its callback.
const OIDCFlowTTL = 10 * time.Minute

const (
	maxOIDCSubjectBytes   = 255
	maxOIDCIssuerBytes    = 2048
	maxOIDCDisplayRunes   = 200
	maxOIDCProviderLength = 32
)

// OIDCFlowKind is the purpose of an OIDC transaction.
type OIDCFlowKind string

const (
	OIDCLogin OIDCFlowKind = "oidc_login" // anonymous sign-in
	OIDCLink  OIDCFlowKind = "oidc_link"  // link to the signed-in account
)

// OIDCFlowStart describes a transaction to begin. Nonce and Verifier (PKCE) are kept
// server-side; Binding is the secret of the browser-binding cookie (stored hashed).
// Link transactions are bound to UserID and its live SessionID.
type OIDCFlowStart struct {
	Kind      OIDCFlowKind
	Provider  string
	UserID    int64
	SessionID int64
	Nonce     string
	Verifier  string
	Binding   string
}

// OIDCFlow is a consumed transaction.
type OIDCFlow struct {
	Kind      OIDCFlowKind
	Provider  string
	UserID    int64
	SessionID int64
	Nonce     string
	Verifier  string
}

type oidcFlowData struct {
	Provider string `json:"provider"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Binding  string `json:"binding"` // SHA-256 hex of the binding secret
}

// BeginOIDCFlow stores a transaction and returns its handle (used as OAuth state) once.
func (s *Service) BeginOIDCFlow(ctx context.Context, st OIDCFlowStart) (string, time.Time, error) {
	if st.Provider == "" || len(st.Provider) > maxOIDCProviderLength || st.Nonce == "" || st.Verifier == "" || st.Binding == "" {
		return "", time.Time{}, fmt.Errorf("%w: incomplete OIDC transaction", ErrInvalid)
	}
	data, err := json.Marshal(oidcFlowData{Provider: st.Provider, Nonce: st.Nonce, Verifier: st.Verifier, Binding: hashSecret(st.Binding)})
	if err != nil {
		return "", time.Time{}, err
	}
	switch st.Kind {
	case OIDCLogin:
		if st.UserID != 0 || st.SessionID != 0 {
			return "", time.Time{}, fmt.Errorf("%w: a login transaction has no account", ErrInvalid)
		}
		return s.createFlow(ctx, s.db, string(st.Kind), 0, 0, string(data), OIDCFlowTTL)
	case OIDCLink:
		if st.UserID == 0 || st.SessionID == 0 {
			return "", time.Time{}, fmt.Errorf("%w: a link transaction needs the account and session", ErrInvalid)
		}
		if err := s.requireLiveSession(ctx, s.db, st.UserID, st.SessionID); err != nil {
			return "", time.Time{}, err
		}
		return s.createFlow(ctx, s.db, string(st.Kind), st.UserID, st.SessionID, string(data), OIDCFlowTTL)
	default:
		return "", time.Time{}, fmt.Errorf("%w: unknown OIDC transaction kind", ErrInvalid)
	}
}

// ConsumeOIDCFlow atomically consumes the live transaction with this handle for
// provider, if the browser presented the matching binding secret and, for a link
// transaction, the same account and session that began it (userID/sessionID: the
// callback's current session, 0 if none). A mismatched attempt does not consume the
// transaction. Any failure is ErrUnauthorized.
func (s *Service) ConsumeOIDCFlow(ctx context.Context, handle, provider, binding string, userID, sessionID int64) (OIDCFlow, error) {
	if handle == "" || binding == "" || provider == "" {
		return OIDCFlow{}, ErrUnauthorized
	}
	now := s.now().UnixMilli()
	var f OIDCFlow
	var kind, data string
	var uid, sid sql.NullInt64
	err := s.db.QueryRowContext(ctx, `UPDATE auth_flows SET consumed_at = ?1
		WHERE flow_hash = ?2 AND consumed_at IS NULL AND expires_at > ?1
		AND json_extract(data, '$.provider') = ?3 AND json_extract(data, '$.binding') = ?4
		AND (
			(kind = 'oidc_login' AND user_id IS NULL)
			OR (kind = 'oidc_link' AND user_id = ?5 AND session_id = ?6)
		)
		RETURNING kind, user_id, session_id, data`,
		now, hashSecret(handle), provider, hashSecret(binding), userID, sessionID).Scan(&kind, &uid, &sid, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return OIDCFlow{}, ErrUnauthorized
	}
	if err != nil {
		return OIDCFlow{}, err
	}
	var d oidcFlowData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return OIDCFlow{}, err
	}
	f = OIDCFlow{Kind: OIDCFlowKind(kind), Provider: d.Provider, UserID: uid.Int64, SessionID: sid.Int64, Nonce: d.Nonce, Verifier: d.Verifier}
	return f, nil
}

// LoginWithOIDC returns the active account linked to exactly (issuer, subject) and
// records the login time. Unknown identities and inactive accounts are both
// ErrUnauthorized; no account is ever created or matched by any other attribute.
func (s *Service) LoginWithOIDC(ctx context.Context, issuer, subject string) (User, error) {
	if issuer == "" || subject == "" {
		return User{}, ErrUnauthorized
	}
	var u User
	var admin int
	var linkID int64
	err := s.db.QueryRowContext(ctx, `SELECT o.id, u.id, u.username, u.is_admin FROM oidc_identities o
		JOIN users u ON u.id = o.user_id WHERE o.issuer = ? AND o.subject = ? AND u.active = 1`, issuer, subject).
		Scan(&linkID, &u.ID, &u.Username, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE oidc_identities SET last_login_at = ? WHERE id = ?`, s.now().UnixMilli(), linkID); err != nil {
		return User{}, err
	}
	u.IsAdmin, u.Active = admin == 1, true
	return u, nil
}

// OIDCLinkRequest links a verified identity to the signed-in account.
type OIDCLinkRequest struct {
	UserID      int64
	SessionID   int64 // acting session: rotated; the user's other sessions are revoked
	Provider    string
	Issuer      string
	Subject     string
	DisplayName string // informational only
}

// OIDCIdentity is the non-secret view of a link (no issuer/subject).
type OIDCIdentity struct {
	ID          int64
	Provider    string
	DisplayName string
	CreatedAt   time.Time
	LastLoginAt time.Time // zero if never used to sign in
}

func sanitizeDisplay(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if r := []rune(s); len(r) > maxOIDCDisplayRunes {
		s = string(r[:maxOIDCDisplayRunes])
	}
	return strings.TrimSpace(s)
}

// LinkOIDCIdentity stores the link and, as an authenticator change, rotates the acting
// session and revokes the user's other sessions in the same transaction. An identity
// already linked to any account, or a second link for the same provider, is ErrExists;
// a dead acting session or inactive account is ErrUnauthorized (nothing stored).
func (s *Service) LinkOIDCIdentity(ctx context.Context, req OIDCLinkRequest) (OIDCIdentity, SessionRotation, error) {
	if req.Provider == "" || len(req.Provider) > maxOIDCProviderLength || req.Issuer == "" || len(req.Issuer) > maxOIDCIssuerBytes ||
		req.Subject == "" || len(req.Subject) > maxOIDCSubjectBytes {
		return OIDCIdentity{}, SessionRotation{}, fmt.Errorf("%w: issuer, subject (1-%d bytes) and provider are required", ErrInvalid, maxOIDCSubjectBytes)
	}
	display := sanitizeDisplay(req.DisplayName)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OIDCIdentity{}, SessionRotation{}, err
	}
	defer tx.Rollback()
	rot, err := s.rotateActingSession(ctx, tx, req.UserID, req.SessionID)
	if err != nil {
		return OIDCIdentity{}, SessionRotation{}, err
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM oidc_identities WHERE user_id = ? AND provider = ?`, req.UserID, req.Provider).Scan(&one)
	if err == nil {
		return OIDCIdentity{}, SessionRotation{}, fmt.Errorf("%w: a %s identity is already linked", ErrExists, req.Provider)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return OIDCIdentity{}, SessionRotation{}, err
	}
	now := s.now()
	res, err := tx.ExecContext(ctx, `INSERT INTO oidc_identities(user_id, provider, issuer, subject, display_name, created_at)
		SELECT ?, ?, ?, ?, ?, ? WHERE `+activeUserSQL,
		req.UserID, req.Provider, req.Issuer, req.Subject, display, now.UnixMilli(), req.UserID)
	if err != nil {
		if isUnique(err) {
			return OIDCIdentity{}, SessionRotation{}, fmt.Errorf("%w: identity already linked", ErrExists)
		}
		return OIDCIdentity{}, SessionRotation{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return OIDCIdentity{}, SessionRotation{}, ErrUnauthorized
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		if isUnique(err) {
			return OIDCIdentity{}, SessionRotation{}, fmt.Errorf("%w: identity already linked", ErrExists)
		}
		return OIDCIdentity{}, SessionRotation{}, err
	}
	return OIDCIdentity{ID: id, Provider: req.Provider, DisplayName: display, CreatedAt: msTime(now)}, rot, nil
}

// ListOIDCIdentities returns the user's links (no issuer/subject).
func (s *Service) ListOIDCIdentities(ctx context.Context, userID int64) ([]OIDCIdentity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, provider, display_name, created_at, last_login_at
		FROM oidc_identities WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OIDCIdentity{}
	for rows.Next() {
		var o OIDCIdentity
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&o.ID, &o.Provider, &o.DisplayName, &created, &last); err != nil {
			return nil, err
		}
		o.CreatedAt = time.UnixMilli(created).UTC()
		if last.Valid {
			o.LastLoginAt = time.UnixMilli(last.Int64).UTC()
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UnlinkOIDCIdentity removes one of the user's links (ErrNotFound for anyone else's)
// and, as an authenticator change, rotates the acting session and revokes the others.
// The caller must have verified recent reauthentication.
func (s *Service) UnlinkOIDCIdentity(ctx context.Context, userID, identityID, sessionID int64) (SessionRotation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRotation{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM oidc_identities WHERE id = ? AND user_id = ?`, identityID, userID)
	if err != nil {
		return SessionRotation{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return SessionRotation{}, ErrNotFound
	}
	rot, err := s.rotateActingSession(ctx, tx, userID, sessionID)
	if err != nil {
		return SessionRotation{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionRotation{}, err
	}
	return rot, nil
}
