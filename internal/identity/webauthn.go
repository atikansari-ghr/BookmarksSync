package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
)

// Passkeys use github.com/go-webauthn/webauthn (pinned v0.18.2). The relying party ID
// and the only accepted origin come from the configured public origin
// (BOOKMARKSYNC_PUBLIC_URL); request Host and forwarded headers never reach this code.
// Every ceremony requires user verification and a discoverable (resident) credential.
// Each ceremony's server state lives in a single-use, expiring auth_flows row whose
// browser handle is stored hashed; registration flows are bound to the account and the
// web session that began them.
const (
	PasskeyFlowTTL       = 5 * time.Minute
	maxPasskeysPerUser   = 20
	maxPasskeyNameRunes  = 64
	userHandleBytes      = 32
	defaultPasskeyName   = "Passkey"
	relyingPartyDispName = "BookmarksSync"
)

// RelyingParty is the WebAuthn configuration derived from the public origin.
type RelyingParty struct {
	wa     *webauthn.WebAuthn
	id     string
	origin string
}

// NewRelyingParty builds the relying party for a normalized public origin. The origin
// must be HTTPS (loopback names may use HTTP for development) and its host must be a
// domain name: WebAuthn RP IDs cannot be IP addresses.
func NewRelyingParty(publicOrigin string) (*RelyingParty, error) {
	o, err := config.ParsePublicOrigin(publicOrigin)
	if err != nil {
		return nil, fmt.Errorf("passkeys: %w", err)
	}
	if o != publicOrigin {
		return nil, fmt.Errorf("passkeys: public origin %q is not normalized (want %q)", publicOrigin, o)
	}
	u, err := url.Parse(o)
	if err != nil {
		return nil, fmt.Errorf("passkeys: %w", err)
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return nil, fmt.Errorf("passkeys: relying party ID cannot be the IP address %s; use a domain name", host)
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:                  host,
		RPDisplayName:         relyingPartyDispName,
		RPOrigins:             []string{o},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: PasskeyFlowTTL, TimeoutUVD: PasskeyFlowTTL},
			Registration: webauthn.TimeoutConfig{Timeout: PasskeyFlowTTL, TimeoutUVD: PasskeyFlowTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("passkeys: %w", err)
	}
	return &RelyingParty{wa: wa, id: host, origin: o}, nil
}

// WithRelyingParty enables passkeys.
func WithRelyingParty(rp *RelyingParty) Option { return func(s *Service) { s.rp = rp } }

func (s *Service) requireRP() error {
	if s.rp == nil {
		return fmt.Errorf("%w: passkeys need a public origin with a domain name", ErrNotConfigured)
	}
	return nil
}

// Passkey is the non-secret metadata of a registered credential.
type Passkey struct {
	ID             int64
	Name           string
	CreatedAt      time.Time
	LastUsedAt     time.Time // zero if never used
	BackupEligible bool
	BackupState    bool
	CloneWarning   bool
	Transports     []string
}

// Ceremony is a begun WebAuthn ceremony: Options is the JSON object (holding
// "publicKey") for navigator.credentials, FlowID the single-use handle to finish it.
type Ceremony struct {
	FlowID    string
	Options   json.RawMessage
	ExpiresAt time.Time
}

type waUser struct {
	id     int64
	name   string
	handle []byte
	creds  []webauthn.Credential
	rowIDs []int64
}

func (u *waUser) WebAuthnID() []byte                         { return u.handle }
func (u *waUser) WebAuthnName() string                       { return u.name }
func (u *waUser) WebAuthnDisplayName() string                { return u.name }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// loadWAUser loads an active user's handle and credentials (ErrUnauthorized otherwise).
func (s *Service) loadWAUser(ctx context.Context, q querier, where string, arg any) (*waUser, error) {
	u := &waUser{}
	err := q.QueryRowContext(ctx, `SELECT u.id, u.username, w.user_handle FROM webauthn_users w
		JOIN users u ON u.id = w.user_id WHERE u.active = 1 AND `+where, arg).Scan(&u.id, &u.name, &u.handle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, credential_id, public_key, attestation_type, transports, aaguid,
		sign_count, clone_warning, user_verified, backup_eligible, backup_state, attachment
		FROM webauthn_credentials WHERE user_id = ? ORDER BY id`, u.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c webauthn.Credential
		var id int64
		var transports, attachment string
		var count int64
		var clone, uv, be, bs int
		if err := rows.Scan(&id, &c.ID, &c.PublicKey, &c.AttestationType, &transports, &c.Authenticator.AAGUID,
			&count, &clone, &uv, &be, &bs, &attachment); err != nil {
			return nil, err
		}
		for _, t := range splitTransports(transports) {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
		c.Authenticator.SignCount = uint32(count)
		c.Authenticator.CloneWarning = clone == 1
		c.Authenticator.Attachment = protocol.AuthenticatorAttachment(attachment)
		c.Flags = webauthn.CredentialFlags{UserPresent: true, UserVerified: uv == 1, BackupEligible: be == 1, BackupState: bs == 1}
		u.creds = append(u.creds, c)
		u.rowIDs = append(u.rowIDs, id)
	}
	return u, rows.Err()
}

func splitTransports(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func validPasskeyName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxPasskeyNameRunes {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ensureUserHandle returns the user's random WebAuthn handle, creating it once.
func (s *Service) ensureUserHandle(ctx context.Context, userID int64) error {
	h := make([]byte, userHandleBytes)
	if _, err := rand.Read(h); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO webauthn_users(user_id, user_handle, created_at)
		SELECT ?, ?, ? WHERE `+activeUserSQL+` ON CONFLICT(user_id) DO NOTHING`,
		userID, h, s.now().UnixMilli(), userID)
	return err
}

func (s *Service) storeCeremony(ctx context.Context, kind string, userID, sessionID int64, options any, sd *webauthn.SessionData) (Ceremony, error) {
	data, err := json.Marshal(sd)
	if err != nil {
		return Ceremony{}, err
	}
	opts, err := json.Marshal(options)
	if err != nil {
		return Ceremony{}, err
	}
	id, exp, err := s.createFlow(ctx, s.db, kind, userID, sessionID, string(data), PasskeyFlowTTL)
	if err != nil {
		return Ceremony{}, err
	}
	return Ceremony{FlowID: id, Options: opts, ExpiresAt: exp}, nil
}

// BeginPasskeyRegistration starts adding a passkey for an active user from one of the
// user's live sessions. Existing credentials are excluded.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, userID, sessionID int64) (Ceremony, error) {
	if err := s.requireRP(); err != nil {
		return Ceremony{}, err
	}
	if sessionID == 0 {
		return Ceremony{}, fmt.Errorf("%w: a session is required", ErrInvalid)
	}
	if err := s.requireLiveSession(ctx, s.db, userID, sessionID); err != nil {
		return Ceremony{}, err
	}
	if err := s.ensureUserHandle(ctx, userID); err != nil {
		return Ceremony{}, err
	}
	u, err := s.loadWAUser(ctx, s.db, `u.id = ?`, userID)
	if err != nil {
		return Ceremony{}, fmt.Errorf("%w: user is unknown or inactive", ErrInvalid)
	}
	if len(u.creds) >= maxPasskeysPerUser {
		return Ceremony{}, fmt.Errorf("%w: at most %d passkeys", ErrInvalid, maxPasskeysPerUser)
	}
	creation, sd, err := s.rp.wa.BeginRegistration(u,
		webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if err != nil {
		return Ceremony{}, err
	}
	return s.storeCeremony(ctx, flowPasskeyRegister, userID, sessionID, creation, sd)
}

func backupFlagsValid(be, bs bool) bool { return be || !bs }

// FinishPasskeyRegistration verifies the authenticator's response to the ceremony
// flowID, which must belong to exactly this user and session, and stores the passkey.
// The flow is consumed even if verification fails. It is an authenticator change: the
// passkey is stored, the acting session rotated and the user's other sessions revoked in
// one transaction. Ceremony failures and a dead acting session are ErrUnauthorized
// (nothing stored); a credential already registered anywhere is ErrExists.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, userID, sessionID int64, flowID, name string, response []byte) (Passkey, SessionRotation, error) {
	var rot SessionRotation
	pk, err := s.finishPasskeyRegistration(ctx, userID, sessionID, flowID, name, response, &rot)
	if err != nil {
		return Passkey{}, SessionRotation{}, err
	}
	return pk, rot, nil
}

func (s *Service) finishPasskeyRegistration(ctx context.Context, userID, sessionID int64, flowID, name string, response []byte, rot *SessionRotation) (Passkey, error) {
	if err := s.requireRP(); err != nil {
		return Passkey{}, err
	}
	if name == "" {
		name = defaultPasskeyName
	}
	if !validPasskeyName(name) {
		return Passkey{}, fmt.Errorf("%w: passkey name must be 1-%d printable characters", ErrInvalid, maxPasskeyNameRunes)
	}
	f, err := s.consumeFlow(ctx, s.db, flowID, flowPasskeyRegister, userID, sessionID)
	if err != nil {
		return Passkey{}, err
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal([]byte(f.Data), &sd); err != nil {
		return Passkey{}, err
	}
	u, err := s.loadWAUser(ctx, s.db, `u.id = ?`, userID)
	if err != nil {
		return Passkey{}, ErrUnauthorized
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return Passkey{}, ErrUnauthorized
	}
	cred, err := s.rp.wa.CreateCredential(u, sd, parsed)
	if err != nil || !cred.Flags.UserVerified || !backupFlagsValid(cred.Flags.BackupEligible, cred.Flags.BackupState) {
		return Passkey{}, ErrUnauthorized
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Passkey{}, err
	}
	defer tx.Rollback()
	if ok, err := isActive(ctx, tx, userID); err != nil || !ok {
		return Passkey{}, errors.Join(ErrUnauthorized, err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return Passkey{}, err
	}
	if n >= maxPasskeysPerUser {
		return Passkey{}, fmt.Errorf("%w: at most %d passkeys", ErrInvalid, maxPasskeysPerUser)
	}
	now := s.now()
	res, err := tx.ExecContext(ctx, `INSERT INTO webauthn_credentials(user_id, credential_id, public_key, attestation_type,
		transports, aaguid, sign_count, user_verified, backup_eligible, backup_state, attachment, attestation, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
		userID, cred.ID, cred.PublicKey, cred.AttestationType, strings.Join(transports, ","), cred.Authenticator.AAGUID,
		int64(cred.Authenticator.SignCount), b2i(cred.Flags.BackupEligible), b2i(cred.Flags.BackupState),
		string(cred.Authenticator.Attachment), cred.Attestation.Object, name, now.UnixMilli())
	if err != nil {
		if isUnique(err) {
			return Passkey{}, fmt.Errorf("%w: credential already registered", ErrExists)
		}
		return Passkey{}, err
	}
	id, _ := res.LastInsertId()
	if *rot, err = s.rotateActingSession(ctx, tx, userID, sessionID); err != nil {
		return Passkey{}, err
	}
	if err := tx.Commit(); err != nil {
		return Passkey{}, err
	}
	return Passkey{ID: id, Name: name, CreatedAt: msTime(now), BackupEligible: cred.Flags.BackupEligible,
		BackupState: cred.Flags.BackupState, Transports: transports}, nil
}

// BeginPasskeyLogin starts a discoverable (username-less) passkey sign-in.
func (s *Service) BeginPasskeyLogin(ctx context.Context) (Ceremony, error) {
	if err := s.requireRP(); err != nil {
		return Ceremony{}, err
	}
	assertion, sd, err := s.rp.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return Ceremony{}, err
	}
	return s.storeCeremony(ctx, flowPasskeyLogin, 0, 0, assertion, sd)
}

// FinishPasskeyLogin verifies an assertion for the ceremony flowID (consumed even on
// failure) and returns the active account owning the credential. It checks the
// challenge, origin, RP ID hash, user verification, signature, credential ownership by
// the returned user handle, backup flags, and that the signature counter advanced
// (unless the authenticator never counts). Any failure is ErrUnauthorized.
func (s *Service) FinishPasskeyLogin(ctx context.Context, flowID string, response []byte) (User, error) {
	if err := s.requireRP(); err != nil {
		return User{}, err
	}
	f, err := s.consumeFlow(ctx, s.db, flowID, flowPasskeyLogin, 0, 0)
	if err != nil {
		return User{}, err
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal([]byte(f.Data), &sd); err != nil {
		return User{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return User{}, ErrUnauthorized
	}
	var owner *waUser
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		u, err := s.loadWAUser(ctx, s.db, `w.user_handle = ?`, userHandle)
		if err != nil {
			return nil, err
		}
		owner = u
		return u, nil
	}
	_, cred, err := s.rp.wa.ValidatePasskeyLogin(handler, sd, parsed)
	if err != nil || owner == nil || !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		return User{}, ErrUnauthorized
	}
	idx := -1
	for i, c := range owner.creds {
		if string(c.ID) == string(cred.ID) {
			idx = i
		}
	}
	if idx < 0 {
		return User{}, ErrUnauthorized
	}
	rowID, prev := owner.rowIDs[idx], owner.creds[idx].Authenticator.SignCount
	if cred.Authenticator.CloneWarning && !owner.creds[idx].Authenticator.CloneWarning ||
		cred.Authenticator.SignCount == prev && prev != 0 || parsed.Response.AuthenticatorData.Counter < prev {
		// The counter did not advance: a possible cloned authenticator. Refuse and record.
		_, _ = s.db.ExecContext(ctx, `UPDATE webauthn_credentials SET clone_warning = 1 WHERE id = ?`, rowID)
		return User{}, ErrUnauthorized
	}
	now := s.now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE webauthn_credentials
		SET sign_count = ?, backup_state = ?, last_used_at = ?
		WHERE id = ? AND user_id = ? AND sign_count = ?
		AND EXISTS (SELECT 1 FROM users WHERE id = ? AND active = 1)`,
		int64(parsed.Response.AuthenticatorData.Counter), b2i(cred.Flags.BackupState), now,
		rowID, owner.id, int64(prev), owner.id)
	if err != nil {
		return User{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return User{}, ErrUnauthorized
	}
	u, err := s.userByID(ctx, s.db, owner.id)
	if err != nil || !u.Active {
		return User{}, ErrUnauthorized
	}
	return u, nil
}

// ListPasskeys returns the user's passkeys in registration order (no key material).
func (s *Service) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, last_used_at, backup_eligible, backup_state,
		clone_warning, transports FROM webauthn_credentials WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		var created int64
		var used sql.NullInt64
		var be, bs, clone int
		var transports string
		if err := rows.Scan(&p.ID, &p.Name, &created, &used, &be, &bs, &clone, &transports); err != nil {
			return nil, err
		}
		p.CreatedAt = time.UnixMilli(created).UTC()
		if used.Valid {
			p.LastUsedAt = time.UnixMilli(used.Int64).UTC()
		}
		p.BackupEligible, p.BackupState, p.CloneWarning = be == 1, bs == 1, clone == 1
		p.Transports = splitTransports(transports)
		out = append(out, p)
	}
	return out, rows.Err()
}

// RenamePasskey renames one of the user's passkeys (ErrNotFound for anyone else's).
func (s *Service) RenamePasskey(ctx context.Context, userID, passkeyID int64, name string) error {
	if !validPasskeyName(name) {
		return fmt.Errorf("%w: passkey name must be 1-%d printable characters", ErrInvalid, maxPasskeyNameRunes)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE webauthn_credentials SET name = ? WHERE id = ? AND user_id = ?`, name, passkeyID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// DeletePasskey removes one of the user's passkeys. It is an authenticator change: in
// the same transaction the acting session is rotated and the user's other sessions are
// revoked. ErrNotFound for anyone else's passkey; ErrUnauthorized (nothing deleted) if
// the acting session is not live. The caller must have verified recent reauthentication.
func (s *Service) DeletePasskey(ctx context.Context, userID, passkeyID, sessionID int64) (SessionRotation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRotation{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, passkeyID, userID)
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

// PasskeysEnabled reports whether a relying party is configured.
func (s *Service) PasskeysEnabled() bool { return s.rp != nil }
