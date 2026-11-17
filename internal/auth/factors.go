package auth

// Passkey, TOTP and recovery-code endpoints under /api/v1/auth. Ceremony and pending
// login endpoints that run before a session exists require the exact configured Origin;
// every other route runs behind RequireSession (cookie, exact Origin and CSRF for
// unsafe methods), and adding or removing an authenticator also requires a recent
// (re)authentication. Authenticator changes (TOTP confirm/disable, recovery-code
// regeneration, passkey add/remove) revoke the user's other sessions and rotate the
// acting session: the response sets a fresh cookie and returns the new csrfToken. Flow handles, TOTP seeds, codes and recovery codes are never
// logged.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

// Factors is the subset of *identity.Service used for passkeys, TOTP and recovery codes.
type Factors interface {
	PasskeysEnabled() bool
	TOTPStatus(ctx context.Context, userID int64) (identity.TOTPStatus, error)
	BeginTOTPEnrollment(ctx context.Context, userID int64) (identity.TOTPEnrollment, error)
	ConfirmTOTPEnrollment(ctx context.Context, userID, sessionID int64, code string) ([]string, identity.SessionRotation, error)
	VerifyTOTP(ctx context.Context, userID int64, code string) error
	ConsumeRecoveryCode(ctx context.Context, userID int64, code string) error
	DisableTOTP(ctx context.Context, userID, sessionID int64) (identity.SessionRotation, error)
	RegenerateRecoveryCodes(ctx context.Context, userID, sessionID int64) ([]string, identity.SessionRotation, error)
	BeginSecondFactor(ctx context.Context, userID int64) (identity.SecondFactorFlow, error)
	CompleteSecondFactor(ctx context.Context, flowID string, proof identity.SecondFactorProof) (identity.User, error)
	BeginPasskeyRegistration(ctx context.Context, userID, sessionID int64) (identity.Ceremony, error)
	FinishPasskeyRegistration(ctx context.Context, userID, sessionID int64, flowID, name string, response []byte) (identity.Passkey, identity.SessionRotation, error)
	BeginPasskeyLogin(ctx context.Context) (identity.Ceremony, error)
	FinishPasskeyLogin(ctx context.Context, flowID string, response []byte) (identity.User, error)
	ListPasskeys(ctx context.Context, userID int64) ([]identity.Passkey, error)
	RenamePasskey(ctx context.Context, userID, passkeyID int64, name string) error
	DeletePasskey(ctx context.Context, userID, passkeyID, sessionID int64) (identity.SessionRotation, error)
}

const (
	// maxCeremonyBodyBytes bounds WebAuthn responses (attestation objects can carry
	// certificate chains).
	maxCeremonyBodyBytes = 64 << 10
	// Limiter keys for second-factor attempts and passkey ceremonies. The NUL prefix
	// keeps them disjoint from any real username.
	secondFactorKey  = "\x00second-factor"
	passkeyBeginKey  = "\x00passkey-begin"
	factorProofKey   = "\x00factor-proof\x00"
	accountFactorKey = "\x00account\x00"
	accountPeer      = "account-wide"
)

// factorLimits applies DefaultLimits to zero fields and disables the per-peer bucket:
// the factor limiter counts per account across all peers.
func factorLimits(l Limits) Limits {
	l.PeerFailures = math.MaxInt32
	return l.withDefaults()
}

// DefaultPasskeyBeginLimits: each begin stores a pending flow for PasskeyFlowTTL, so a
// peer may start at most 30 ceremonies per 5 minutes. That bounds one peer's share of
// the global pending-flow cap (10,000) while leaving ample room for retries.
var DefaultPasskeyBeginLimits = Limits{MaxKeys: 10000, Window: 5 * time.Minute, PeerFailures: 30}

// passkeyBeginLimits applies DefaultPasskeyBeginLimits to zero fields and disables the
// per-pair bucket: only the direct peer is counted (peerKey, forwarded headers ignored).
func passkeyBeginLimits(l Limits) Limits {
	if l.PeerFailures <= 0 {
		l.PeerFailures = DefaultPasskeyBeginLimits.PeerFailures
	}
	if l.Window <= 0 {
		l.Window = DefaultPasskeyBeginLimits.Window
	}
	if l.MaxKeys <= 0 {
		l.MaxKeys = DefaultPasskeyBeginLimits.MaxKeys
	}
	l.UserFailures = math.MaxInt32
	return l.withDefaults()
}

func accountKey(userID int64) string { return accountFactorKey + strconv.FormatInt(userID, 10) }

func (h *Handler) registerFactors(r server.Registrar) {
	r.Handle("POST /api/v1/auth/login/second-factor", noStore(http.HandlerFunc(h.secondFactor)))

	session := func(f http.HandlerFunc) http.Handler { return h.RequireSession(f) }
	recent := func(f http.HandlerFunc) http.Handler { return h.RequireSession(h.RequireRecentAuth(f)) }
	r.Handle("GET /api/v1/auth/totp", session(h.totpStatus))
	r.Handle("POST /api/v1/auth/totp/enroll", recent(h.totpEnroll))
	r.Handle("POST /api/v1/auth/totp/confirm", recent(h.totpConfirm))
	r.Handle("POST /api/v1/auth/totp/disable", recent(h.totpDisable))
	r.Handle("POST /api/v1/auth/recovery-codes", recent(h.regenerateRecoveryCodes))

	if !h.factors.PasskeysEnabled() {
		h.log.Warn("passkeys disabled: the public origin's host must be a domain name")
		return
	}
	r.Handle("POST /api/v1/auth/passkey/login/begin", noStore(http.HandlerFunc(h.passkeyLoginBegin)))
	r.Handle("POST /api/v1/auth/passkey/login/finish", noStore(http.HandlerFunc(h.passkeyLoginFinish)))
	r.Handle("GET /api/v1/auth/passkeys", session(h.listPasskeys))
	r.Handle("POST /api/v1/auth/passkeys/register/begin", recent(h.passkeyRegisterBegin))
	r.Handle("POST /api/v1/auth/passkeys/register/finish", recent(h.passkeyRegisterFinish))
	r.Handle("PATCH /api/v1/auth/passkeys/{id}", session(h.renamePasskey))
	r.Handle("DELETE /api/v1/auth/passkeys/{id}", recent(h.deletePasskey))
}

func decodeBodyLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		server.WriteError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
	} else {
		server.WriteError(w, http.StatusBadRequest, "invalid_request", "request body must be a JSON object")
	}
	return false
}

// accountFactorLocked reports (and answers 429) when the account has too many recent
// second-factor failures.
func (h *Handler) accountFactorLocked(w http.ResponseWriter, userID int64) bool {
	res, wait := h.factorLimiter.Reserve(accountKey(userID), accountPeer)
	if res == nil {
		h.log.Warn("auth second factor locked for account", "user_id", userID)
		writeRateLimited(w, wait)
		return true
	}
	res.Cancel()
	return false
}

func (h *Handler) countAccountFactorFailure(userID int64) {
	if userID == 0 {
		return
	}
	if res, _ := h.factorLimiter.Reserve(accountKey(userID), accountPeer); res != nil {
		res.Fail()
	}
}

// requireSecondFactor answers a verified password login for a TOTP-enabled account
// with a pending second-factor challenge (202, no session). It reports whether it
// wrote a response; false means the account has no second factor and login proceeds.
func (h *Handler) requireSecondFactor(w http.ResponseWriter, r *http.Request, user identity.User) bool {
	st, err := h.factors.TOTPStatus(r.Context(), user.ID)
	if err != nil {
		h.internal(w, "totp status", err)
		return true
	}
	if !st.Enabled {
		return false
	}
	if h.accountFactorLocked(w, user.ID) {
		return true
	}
	flow, err := h.factors.BeginSecondFactor(r.Context(), user.ID)
	switch {
	case errors.Is(err, identity.ErrNotFound):
		return false // disabled meanwhile
	case errors.Is(err, identity.ErrInvalid):
		h.log.Info("auth login failed", "peer", peerKey(r.RemoteAddr))
		writeInvalidCredentials(w) // suspended meanwhile
		return true
	case err != nil:
		h.internal(w, "begin second factor", err)
		return true
	}
	h.log.Info("auth login pending second factor", "user_id", user.ID, "peer", peerKey(r.RemoteAddr))
	server.WriteJSON(w, http.StatusAccepted, map[string]any{
		"status":    "second_factor_required",
		"flowId":    flow.ID,
		"methods":   []string{"totp", "recoveryCode"},
		"expiresAt": flow.ExpiresAt,
	})
	return true
}

func (h *Handler) secondFactor(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		server.WriteError(w, http.StatusForbidden, "origin_rejected", "cross-origin request rejected")
		return
	}
	var req struct {
		FlowID       string `json:"flowId"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	res, wait := h.limiter.Reserve(secondFactorKey, r.RemoteAddr)
	if res == nil {
		h.log.Warn("auth second factor rate limited", "peer", peerKey(r.RemoteAddr))
		writeRateLimited(w, wait)
		return
	}
	defer res.Cancel()
	user, err := h.factors.CompleteSecondFactor(r.Context(), req.FlowID, identity.SecondFactorProof{TOTP: req.Code, RecoveryCode: req.RecoveryCode})
	if err != nil {
		if !errors.Is(err, identity.ErrUnauthorized) {
			h.internal(w, "complete second factor", err)
			return
		}
		res.Fail()
		h.countAccountFactorFailure(user.ID)
		h.log.Info("auth second factor failed", "user_id", user.ID, "peer", peerKey(r.RemoteAddr))
		server.WriteError(w, http.StatusUnauthorized, "invalid_second_factor", "invalid or expired verification code")
		return
	}
	res.Success()
	h.issueSession(w, r, user, "auth second factor succeeded")
}

func (h *Handler) totpStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.factors.TOTPStatus(r.Context(), state(r).user.ID)
	if err != nil {
		h.internal(w, "totp status", err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"enabled": st.Enabled, "recoveryCodesRemaining": st.RecoveryCodesRemaining})
}

func writeNotConfigured(w http.ResponseWriter) {
	server.WriteError(w, http.StatusServiceUnavailable, "not_configured", "this authentication method is not configured")
}

func (h *Handler) totpEnroll(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	enr, err := h.factors.BeginTOTPEnrollment(r.Context(), st.user.ID)
	switch {
	case errors.Is(err, identity.ErrExists):
		server.WriteError(w, http.StatusConflict, "totp_already_enabled", "an authenticator app is already enabled")
		return
	case errors.Is(err, identity.ErrNotConfigured):
		writeNotConfigured(w)
		return
	case err != nil:
		h.internal(w, "begin totp enrollment", err)
		return
	}
	h.log.Info("auth totp enrollment started", "user_id", st.user.ID)
	server.WriteJSON(w, http.StatusOK, map[string]any{"secret": enr.Secret, "provisioningUri": enr.URI,
		"algorithm": identity.TOTPAlgorithm, "digits": identity.TOTPDigits, "period": int(identity.TOTPPeriod / time.Second)})
}

// reserveProof rate-limits code checks on authenticated factor routes per user+peer
// and per peer, like password attempts.
func (h *Handler) reserveProof(w http.ResponseWriter, r *http.Request) *Reservation {
	st := state(r)
	res, wait := h.limiter.Reserve(factorProofKey+st.user.Username, r.RemoteAddr)
	if res == nil {
		h.log.Warn("auth factor proof rate limited", "user_id", st.user.ID, "peer", peerKey(r.RemoteAddr))
		writeRateLimited(w, wait)
	}
	return res
}

// rotated applies an authenticator change's session rotation to the response: the
// acting session's fresh secret goes into a new cookie, and the matching CSRF token is
// returned for the body. The pre-change cookie and CSRF token no longer work.
func (h *Handler) rotated(w http.ResponseWriter, rot identity.SessionRotation) (string, bool) {
	if rot.Secret == "" {
		h.internal(w, "rotate session", errors.New("authenticator change returned no rotated session"))
		return "", false
	}
	h.setCookie(w, rot.Session, rot.Secret)
	return csrfToken(rot.Secret), true
}

// sessionGone answers a change refused because the acting session died meanwhile.
func (h *Handler) sessionGone(w http.ResponseWriter) {
	h.clearCookie(w)
	server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
}

func writeInvalidCode(w http.ResponseWriter) {
	server.WriteError(w, http.StatusUnauthorized, "invalid_code", "invalid or expired verification code")
}

func (h *Handler) totpConfirm(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	var req struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	res := h.reserveProof(w, r)
	if res == nil {
		return
	}
	defer res.Cancel()
	codes, rot, err := h.factors.ConfirmTOTPEnrollment(r.Context(), st.user.ID, st.sess.ID, req.Code)
	switch {
	case errors.Is(err, identity.ErrUnauthorized):
		res.Fail()
		h.log.Info("auth totp confirmation failed", "user_id", st.user.ID)
		writeInvalidCode(w)
		return
	case errors.Is(err, identity.ErrNotConfigured):
		writeNotConfigured(w)
		return
	case err != nil:
		h.internal(w, "confirm totp", err)
		return
	}
	res.Success()
	tok, ok := h.rotated(w, rot)
	if !ok {
		return
	}
	h.log.Info("auth totp enabled", "user_id", st.user.ID, "session_id", st.sess.ID)
	server.WriteJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes, "csrfToken": tok})
}

// checkProof verifies exactly one of a TOTP code or a recovery code for the session's
// user, writing the failure response itself.
func (h *Handler) checkProof(w http.ResponseWriter, r *http.Request, code, recovery string) bool {
	st := state(r)
	if (code == "") == (recovery == "") {
		writeInvalidCode(w)
		return false
	}
	res := h.reserveProof(w, r)
	if res == nil {
		return false
	}
	defer res.Cancel()
	var err error
	if code != "" {
		err = h.factors.VerifyTOTP(r.Context(), st.user.ID, code)
	} else {
		err = h.factors.ConsumeRecoveryCode(r.Context(), st.user.ID, recovery)
	}
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			res.Fail()
			h.countAccountFactorFailure(st.user.ID)
			h.log.Info("auth factor proof failed", "user_id", st.user.ID)
			writeInvalidCode(w)
		} else if errors.Is(err, identity.ErrNotConfigured) {
			writeNotConfigured(w)
		} else {
			h.internal(w, "verify factor proof", err)
		}
		return false
	}
	res.Success()
	return true
}

func (h *Handler) totpDisable(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	var req struct {
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if !decodeBody(w, r, &req) || !h.checkProof(w, r, req.Code, req.RecoveryCode) {
		return
	}
	rot, err := h.factors.DisableTOTP(r.Context(), st.user.ID, st.sess.ID)
	switch {
	case errors.Is(err, identity.ErrNotFound):
		server.WriteError(w, http.StatusNotFound, "not_found", "no authenticator app is enabled")
		return
	case errors.Is(err, identity.ErrUnauthorized):
		h.sessionGone(w)
		return
	case err != nil:
		h.internal(w, "disable totp", err)
		return
	}
	tok, ok := h.rotated(w, rot)
	if !ok {
		return
	}
	h.log.Info("auth totp disabled", "user_id", st.user.ID, "session_id", st.sess.ID)
	server.WriteJSON(w, http.StatusOK, map[string]any{"csrfToken": tok})
}

func (h *Handler) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	var req struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &req) || !h.checkProof(w, r, req.Code, "") {
		return
	}
	codes, rot, err := h.factors.RegenerateRecoveryCodes(r.Context(), st.user.ID, st.sess.ID)
	switch {
	case errors.Is(err, identity.ErrNotFound):
		server.WriteError(w, http.StatusNotFound, "not_found", "no authenticator app is enabled")
		return
	case errors.Is(err, identity.ErrUnauthorized):
		h.sessionGone(w)
		return
	case err != nil:
		h.internal(w, "regenerate recovery codes", err)
		return
	}
	tok, ok := h.rotated(w, rot)
	if !ok {
		return
	}
	h.log.Info("auth recovery codes regenerated", "user_id", st.user.ID, "session_id", st.sess.ID)
	server.WriteJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes, "csrfToken": tok})
}

func writeCeremony(w http.ResponseWriter, c identity.Ceremony) {
	server.WriteJSON(w, http.StatusOK, map[string]any{"flowId": c.FlowID, "options": c.Options, "expiresAt": c.ExpiresAt})
}

func (h *Handler) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		server.WriteError(w, http.StatusForbidden, "origin_rejected", "cross-origin request rejected")
		return
	}
	// Every begin stores a pending flow, so begins are counted per direct peer before
	// any storage; the global pending-flow cap in identity still applies on top.
	res, wait := h.passkeyBeginLimiter.Reserve(passkeyBeginKey, r.RemoteAddr)
	if res == nil {
		h.log.Warn("auth passkey login begin rate limited", "peer", peerKey(r.RemoteAddr))
		writeRateLimited(w, wait)
		return
	}
	res.Fail() // counts the begin whatever happens next
	c, err := h.factors.BeginPasskeyLogin(r.Context())
	if err != nil {
		if errors.Is(err, identity.ErrTooManyFlows) {
			writeRateLimited(w, time.Minute)
			return
		}
		h.internal(w, "begin passkey login", err)
		return
	}
	writeCeremony(w, c)
}

type ceremonyRequest struct {
	FlowID     string          `json:"flowId"`
	Name       string          `json:"name"`
	Credential json.RawMessage `json:"credential"`
}

func (h *Handler) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		server.WriteError(w, http.StatusForbidden, "origin_rejected", "cross-origin request rejected")
		return
	}
	var req ceremonyRequest
	if !decodeBodyLimit(w, r, &req, maxCeremonyBodyBytes) {
		return
	}
	user, err := h.factors.FinishPasskeyLogin(r.Context(), req.FlowID, req.Credential)
	if err != nil {
		if !errors.Is(err, identity.ErrUnauthorized) {
			h.internal(w, "finish passkey login", err)
			return
		}
		h.log.Info("auth passkey login failed", "peer", peerKey(r.RemoteAddr))
		server.WriteError(w, http.StatusUnauthorized, "invalid_passkey", "passkey sign-in failed")
		return
	}
	h.issueSession(w, r, user, "auth passkey login succeeded")
}

type passkeyJSON struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	CreatedAt      time.Time  `json:"createdAt"`
	LastUsedAt     *time.Time `json:"lastUsedAt"`
	BackupEligible bool       `json:"backupEligible"`
	BackupState    bool       `json:"backupState"`
	CloneWarning   bool       `json:"cloneWarning"`
	Transports     []string   `json:"transports"`
}

func toPasskeyJSON(p identity.Passkey) passkeyJSON {
	out := passkeyJSON{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, BackupEligible: p.BackupEligible,
		BackupState: p.BackupState, CloneWarning: p.CloneWarning, Transports: p.Transports}
	if out.Transports == nil {
		out.Transports = []string{}
	}
	if !p.LastUsedAt.IsZero() {
		t := p.LastUsedAt
		out.LastUsedAt = &t
	}
	return out
}

func (h *Handler) listPasskeys(w http.ResponseWriter, r *http.Request) {
	list, err := h.factors.ListPasskeys(r.Context(), state(r).user.ID)
	if err != nil {
		h.internal(w, "list passkeys", err)
		return
	}
	out := make([]passkeyJSON, 0, len(list))
	for _, p := range list {
		out = append(out, toPasskeyJSON(p))
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"passkeys": out})
}

func (h *Handler) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	c, err := h.factors.BeginPasskeyRegistration(r.Context(), st.user.ID, st.sess.ID)
	if err != nil {
		if errors.Is(err, identity.ErrInvalid) {
			server.WriteError(w, http.StatusBadRequest, "invalid_request", "cannot add another passkey")
			return
		}
		h.internal(w, "begin passkey registration", err)
		return
	}
	writeCeremony(w, c)
}

func (h *Handler) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	var req ceremonyRequest
	if !decodeBodyLimit(w, r, &req, maxCeremonyBodyBytes) {
		return
	}
	pk, rot, err := h.factors.FinishPasskeyRegistration(r.Context(), st.user.ID, st.sess.ID, req.FlowID, req.Name, req.Credential)
	switch {
	case errors.Is(err, identity.ErrUnauthorized):
		h.log.Info("auth passkey registration failed", "user_id", st.user.ID)
		server.WriteError(w, http.StatusBadRequest, "invalid_passkey", "passkey registration failed; start again")
		return
	case errors.Is(err, identity.ErrExists):
		server.WriteError(w, http.StatusConflict, "passkey_exists", "this passkey is already registered")
		return
	case errors.Is(err, identity.ErrInvalid):
		server.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid passkey name or too many passkeys")
		return
	case err != nil:
		h.internal(w, "finish passkey registration", err)
		return
	}
	tok, ok := h.rotated(w, rot)
	if !ok {
		return
	}
	h.log.Info("auth passkey added", "user_id", st.user.ID, "passkey_id", pk.ID, "session_id", st.sess.ID)
	server.WriteJSON(w, http.StatusCreated, map[string]any{"passkey": toPasskeyJSON(pk), "csrfToken": tok})
}

func passkeyID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		server.WriteError(w, http.StatusNotFound, "not_found", "passkey not found")
		return 0, false
	}
	return id, true
}

func (h *Handler) renamePasskey(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	id, ok := passkeyID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	switch err := h.factors.RenamePasskey(r.Context(), st.user.ID, id, req.Name); {
	case errors.Is(err, identity.ErrInvalid):
		server.WriteError(w, http.StatusBadRequest, "invalid_request", "passkey name must be 1-64 printable characters")
	case errors.Is(err, identity.ErrNotFound):
		server.WriteError(w, http.StatusNotFound, "not_found", "passkey not found")
	case err != nil:
		h.internal(w, "rename passkey", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) deletePasskey(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	id, ok := passkeyID(w, r)
	if !ok {
		return
	}
	rot, err := h.factors.DeletePasskey(r.Context(), st.user.ID, id, st.sess.ID)
	switch {
	case errors.Is(err, identity.ErrNotFound):
		server.WriteError(w, http.StatusNotFound, "not_found", "passkey not found")
		return
	case errors.Is(err, identity.ErrUnauthorized):
		h.sessionGone(w)
		return
	case err != nil:
		h.internal(w, "delete passkey", err)
		return
	}
	tok, ok := h.rotated(w, rot)
	if !ok {
		return
	}
	h.log.Info("auth passkey removed", "user_id", st.user.ID, "passkey_id", id, "session_id", st.sess.ID)
	server.WriteJSON(w, http.StatusOK, map[string]any{"csrfToken": tok})
}
