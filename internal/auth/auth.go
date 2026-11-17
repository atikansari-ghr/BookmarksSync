// Package auth serves the browser sign-in JSON API under /api/v1/auth: password login,
// logout, the current session, CSRF bootstrap, session listing/revocation, and recent
// reauthentication. Sessions are opaque server-side records (internal/identity) carried
// in a __Host- cookie. Unsafe methods require an exact Origin match against the
// configured public origin and a session-bound CSRF token. Personal API tokens are
// never accepted here.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

const (
	// SessionCookieName uses the __Host- prefix: browsers only accept it with Secure,
	// Path=/ and no Domain, so it is host-only and cannot be set by a sibling domain.
	SessionCookieName = "__Host-bookmarksync_session"
	// CSRFHeader carries the session-bound CSRF token on unsafe requests.
	CSRFHeader = "X-CSRF-Token"
	// ReauthWindow is how long a (re)authentication satisfies RequireRecentAuth.
	ReauthWindow = 5 * time.Minute

	maxAuthBodyBytes = 8 << 10
	csrfContext      = "bookmarksync-csrf-v1"
)

// Identity is the subset of *identity.Service the handlers use.
type Identity interface {
	VerifyPassword(ctx context.Context, username, password string) (identity.User, error)
	CreateSession(ctx context.Context, userID int64, label string) (identity.Session, string, error)
	ResolveSession(ctx context.Context, secret string) (identity.Session, identity.User, error)
	RotateSession(ctx context.Context, secret string) (identity.Session, string, error)
	ListSessions(ctx context.Context, userID int64) ([]identity.Session, error)
	RevokeSession(ctx context.Context, userID, sessionID int64) error
}

// Options configures New.
type Options struct {
	Identity Identity
	// PublicOrigin is the normalized origin from config (BOOKMARKSYNC_PUBLIC_URL).
	PublicOrigin string
	// Now is the clock for reauthentication age and rate limits; it must agree with the
	// identity service's clock. Defaults to time.Now.
	Now    func() time.Time
	Logger *slog.Logger
	Limits Limits
	// Factors enables passkeys, TOTP and recovery codes (nil: password only).
	Factors Factors
	// FactorLimits bounds second-factor failures per account across all peers; only
	// UserFailures and Window apply. Zero fields take DefaultLimits.
	FactorLimits Limits
	// PasskeyBeginLimits bounds anonymous passkey ceremonies started per direct peer;
	// only PeerFailures, Window and MaxKeys apply. Zero fields take
	// DefaultPasskeyBeginLimits.
	PasskeyBeginLimits Limits
	// OIDC configures Google/Authentik sign-in and linking (no providers: disabled).
	OIDC OIDCOptions
	// OIDCBeginLimits bounds OIDC flows started per direct peer; only PeerFailures,
	// Window and MaxKeys apply. Zero fields take DefaultOIDCBeginLimits.
	OIDCBeginLimits Limits
}

// Handler serves the auth API and provides session middleware for other routes.
type Handler struct {
	ids     Identity
	origin  string
	now     func() time.Time
	log     *slog.Logger
	limiter *Limiter

	factors Factors
	// factorLimiter counts second-factor failures per account across peers.
	factorLimiter *Limiter
	// passkeyBeginLimiter bounds anonymous passkey ceremonies started per direct peer.
	passkeyBeginLimiter *Limiter

	oidc            map[string]*oidcRP // configured providers by ID
	oidcOrder       []string           // configuration order, for the public list
	oidcStore       OIDCStore
	oidcClient      *http.Client
	oidcTimeout     time.Duration
	oidcBeginLimits *Limiter
}

// CheckPassword verifies a recent-authentication proof using the same per-account
// and per-peer limiter as password sign-in. It writes a generic authentication error
// or rate-limit response on failure and returns false.
func (h *Handler) CheckPassword(w http.ResponseWriter, r *http.Request, username, password string) bool {
	res, wait := h.limiter.Reserve(username, r.RemoteAddr)
	if res == nil {
		h.log.Warn("auth password proof rate limited", "peer", peerKey(r.RemoteAddr))
		writeRateLimited(w, wait)
		return false
	}
	defer res.Cancel()
	_, err := h.ids.VerifyPassword(r.Context(), username, password)
	if err != nil {
		if !errors.Is(err, identity.ErrUnauthorized) {
			h.internal(w, "verify password proof", err)
			return false
		}
		res.Fail()
		writeInvalidCredentials(w)
		return false
	}
	res.Success()
	return true
}

// New validates opts and returns a Handler.
func New(opts Options) (*Handler, error) {
	if opts.Identity == nil {
		return nil, errors.New("auth: identity service is required")
	}
	o, err := config.ParsePublicOrigin(opts.PublicOrigin)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	if o != opts.PublicOrigin {
		return nil, fmt.Errorf("auth: public origin %q is not normalized (want %q)", opts.PublicOrigin, o)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	h := &Handler{ids: opts.Identity, origin: o, now: opts.Now, log: opts.Logger,
		limiter: NewLimiter(opts.Limits, opts.Now), factors: opts.Factors}
	if err := h.configureOIDC(opts); err != nil {
		return nil, err
	}
	if opts.Factors != nil {
		h.factorLimiter = NewLimiter(factorLimits(opts.FactorLimits), opts.Now)
		h.passkeyBeginLimiter = NewLimiter(passkeyBeginLimits(opts.PasskeyBeginLimits), opts.Now)
	}
	return h, nil
}

// Register adds the /api/v1/auth routes. Every response, including errors and empty
// mutations, is marked Cache-Control: no-store by the handler itself.
func (h *Handler) Register(r server.Registrar) {
	r.Handle("POST /api/v1/auth/login", noStore(http.HandlerFunc(h.login)))
	r.Handle("POST /api/v1/auth/logout", h.RequireSession(http.HandlerFunc(h.logout)))
	r.Handle("GET /api/v1/auth/session", h.RequireSession(http.HandlerFunc(h.current)))
	r.Handle("GET /api/v1/auth/csrf", h.RequireSession(http.HandlerFunc(h.csrf)))
	r.Handle("GET /api/v1/auth/sessions", h.RequireSession(http.HandlerFunc(h.listSessions)))
	r.Handle("DELETE /api/v1/auth/sessions/{id}", h.RequireSession(http.HandlerFunc(h.revokeSession)))
	r.Handle("POST /api/v1/auth/reauthenticate", h.RequireSession(http.HandlerFunc(h.reauthenticate)))
	if h.factors != nil {
		h.registerFactors(r)
	}
	if len(h.oidc) > 0 {
		h.registerOIDC(r)
	}
}

// noStore forbids caching of personalized or credential-bearing responses.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type ctxKey struct{}

type authState struct {
	sess   identity.Session
	user   identity.User
	secret string
}

// FromContext returns the session and user established by RequireSession.
func FromContext(ctx context.Context) (identity.Session, identity.User, bool) {
	st, ok := ctx.Value(ctxKey{}).(*authState)
	if !ok {
		return identity.Session{}, identity.User{}, false
	}
	return st.sess, st.user, true
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin requires exactly one Origin header equal to the configured origin. Host
// and forwarded headers are deliberately ignored.
func (h *Handler) sameOrigin(r *http.Request) bool {
	v := r.Header.Values("Origin")
	return len(v) == 1 && subtle.ConstantTimeCompare([]byte(v[0]), []byte(h.origin)) == 1
}

// csrfToken derives the session's CSRF token as HMAC-SHA256 keyed by the 256-bit
// session secret. It is bound to the session, changes when the session rotates, needs
// no extra storage, and does not reveal the HttpOnly secret.
func csrfToken(secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(csrfContext))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// RequireSession authenticates the request from the session cookie only. For unsafe
// methods it also requires an exact same-origin Origin header and the session's CSRF
// token. On success the session is available through FromContext. Responses are
// marked Cache-Control: no-store.
func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		unsafe := !safeMethod(r.Method)
		if unsafe && !h.sameOrigin(r) {
			server.WriteError(w, http.StatusForbidden, "origin_rejected", "cross-origin request rejected")
			return
		}
		c, err := r.Cookie(SessionCookieName)
		if err != nil || c.Value == "" {
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		sess, user, err := h.ids.ResolveSession(r.Context(), c.Value)
		if err != nil {
			if !errors.Is(err, identity.ErrUnauthorized) {
				h.internal(w, "resolve session", err)
				return
			}
			h.clearCookie(w)
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		if unsafe {
			got := r.Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(csrfToken(c.Value))) != 1 {
				server.WriteError(w, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
				return
			}
		}
		st := &authState{sess: sess, user: user, secret: c.Value}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, st)))
	})
}

// RequireRecentAuth must wrap a handler inside RequireSession. It refuses with 403
// reauthentication_required unless the session (re)authenticated within ReauthWindow.
func (h *Handler) RequireRecentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, _, ok := FromContext(r.Context())
		if !ok {
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		if h.now().Sub(sess.AuthenticatedAt) > ReauthWindow {
			server.WriteError(w, http.StatusForbidden, "reauthentication_required", "confirm your password to continue")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) internal(w http.ResponseWriter, what string, err error) {
	h.log.Error("auth: "+what+" failed", "error", err)
	server.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}

func (h *Handler) setCookie(w http.ResponseWriter, sess identity.Session, secret string) {
	maxAge := int(math.Ceil(sess.ExpiresAt.Sub(h.now()).Seconds()))
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: secret, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// ApplyRotation installs a freshly rotated session secret in the session cookie and
// returns the matching CSRF token for JSON APIs that change authenticators.
func (h *Handler) ApplyRotation(w http.ResponseWriter, rot identity.SessionRotation) string {
	if rot.Secret == "" {
		return ""
	}
	h.setCookie(w, rot.Session, rot.Secret)
	return csrfToken(rot.Secret)
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// decodeBody reads a small JSON body, writing 413/400 itself on failure.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)).Decode(v)
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

func writeRateLimited(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	server.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
}

func writeInvalidCredentials(w http.ResponseWriter) {
	server.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
}

type userJSON struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
}

type sessionJSON struct {
	ID              int64     `json:"id"`
	Label           string    `json:"label"`
	CreatedAt       time.Time `json:"createdAt"`
	LastSeenAt      time.Time `json:"lastSeenAt"`
	AuthenticatedAt time.Time `json:"authenticatedAt"`
	IdleExpiresAt   time.Time `json:"idleExpiresAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Current         bool      `json:"current"`
}

type stateJSON struct {
	User          userJSON    `json:"user"`
	Session       sessionJSON `json:"session"`
	RecentAuthEnd time.Time   `json:"recentAuthUntil"`
	CSRFToken     string      `json:"csrfToken"`
}

func toSessionJSON(s identity.Session, current bool) sessionJSON {
	return sessionJSON{ID: s.ID, Label: s.Label, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt,
		AuthenticatedAt: s.AuthenticatedAt, IdleExpiresAt: s.LastSeenAt.Add(identity.SessionIdleTimeout),
		ExpiresAt: s.ExpiresAt, Current: current}
}

func toState(u identity.User, s identity.Session, secret string) stateJSON {
	return stateJSON{User: userJSON{ID: u.ID, Username: u.Username, IsAdmin: u.IsAdmin},
		Session: toSessionJSON(s, true), RecentAuthEnd: s.AuthenticatedAt.Add(ReauthWindow),
		CSRFToken: csrfToken(secret)}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		server.WriteError(w, http.StatusForbidden, "origin_rejected", "cross-origin request rejected")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	// Capacity is reserved before the Argon2id work so concurrent attempts cannot all
	// pass before earlier failures are recorded.
	res, wait := h.limiter.Reserve(req.Username, r.RemoteAddr)
	if res == nil {
		h.log.Warn("auth login rate limited", "peer", peerKey(r.RemoteAddr))
		writeRateLimited(w, wait)
		return
	}
	defer res.Cancel() // no-op once finalized; releases capacity on internal errors
	// VerifyPassword performs Argon2id work for unknown, inactive and wrong-password
	// accounts alike; every failure gets the same public response.
	user, err := h.ids.VerifyPassword(r.Context(), req.Username, req.Password)
	if err != nil {
		if !errors.Is(err, identity.ErrUnauthorized) {
			h.internal(w, "verify password", err)
			return
		}
		res.Fail()
		h.log.Info("auth login failed", "peer", peerKey(r.RemoteAddr))
		writeInvalidCredentials(w)
		return
	}
	res.Success()
	if h.factors != nil {
		if h.requireSecondFactor(w, r, user) {
			return
		}
	}
	h.issueSession(w, r, user, "auth login succeeded")
}

// issueSession creates a fresh session for a verified user, retires any presented
// session, sets the cookie and writes the session state. An account suspended since
// verification gets the generic invalid-credentials response.
func (h *Handler) issueSession(w http.ResponseWriter, r *http.Request, user identity.User, event string) {
	sess, secret, err := h.startSession(w, r, user, event)
	if err != nil {
		if errors.Is(err, identity.ErrInvalid) || errors.Is(err, identity.ErrNotFound) {
			// Suspended between verification and issuance.
			h.log.Info("auth login failed", "peer", peerKey(r.RemoteAddr))
			writeInvalidCredentials(w)
			return
		}
		h.internal(w, "create session", err)
		return
	}
	server.WriteJSON(w, http.StatusOK, toState(user, sess, secret))
}

// startSession creates a fresh session for a verified user (CreateSession re-checks
// that the account is active), retires any presented session (never adopted:
// fixation) and sets the cookie. It writes nothing else; the caller answers.
func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, user identity.User, event string) (identity.Session, string, error) {
	sess, secret, err := h.ids.CreateSession(r.Context(), user.ID, r.UserAgent())
	if err != nil {
		return identity.Session{}, "", err
	}
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		if old, owner, err := h.ids.ResolveSession(r.Context(), c.Value); err == nil {
			if err := h.ids.RevokeSession(r.Context(), owner.ID, old.ID); err != nil {
				h.log.Warn("auth: revoke replaced session failed", "error", err)
			}
		}
	}
	h.log.Info(event, "user_id", user.ID, "session_id", sess.ID, "peer", peerKey(r.RemoteAddr))
	h.setCookie(w, sess, secret)
	return sess, secret, nil
}

func state(r *http.Request) *authState { return r.Context().Value(ctxKey{}).(*authState) }

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	if err := h.ids.RevokeSession(r.Context(), st.user.ID, st.sess.ID); err != nil {
		h.internal(w, "logout", err)
		return
	}
	h.log.Info("auth logout", "user_id", st.user.ID, "session_id", st.sess.ID)
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) current(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	server.WriteJSON(w, http.StatusOK, toState(st.user, st.sess, st.secret))
}

func (h *Handler) csrf(w http.ResponseWriter, r *http.Request) {
	server.WriteJSON(w, http.StatusOK, map[string]string{"csrfToken": csrfToken(state(r).secret)})
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	list, err := h.ids.ListSessions(r.Context(), st.user.ID)
	if err != nil {
		h.internal(w, "list sessions", err)
		return
	}
	out := make([]sessionJSON, 0, len(list))
	for _, s := range list {
		out = append(out, toSessionJSON(s, s.ID == st.sess.ID))
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		server.WriteError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err := h.ids.RevokeSession(r.Context(), st.user.ID, id); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			server.WriteError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		h.internal(w, "revoke session", err)
		return
	}
	h.log.Info("auth session revoked", "user_id", st.user.ID, "session_id", id)
	if id == st.sess.ID {
		h.clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reauthenticate(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	var req struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	res, wait := h.limiter.Reserve(st.user.Username, r.RemoteAddr)
	if res == nil {
		h.log.Warn("auth reauthentication rate limited", "user_id", st.user.ID, "peer", peerKey(r.RemoteAddr))
		writeRateLimited(w, wait)
		return
	}
	defer res.Cancel()
	user, err := h.ids.VerifyPassword(r.Context(), st.user.Username, req.Password)
	if err != nil || user.ID != st.user.ID {
		if err != nil && !errors.Is(err, identity.ErrUnauthorized) {
			h.internal(w, "verify password", err)
			return
		}
		res.Fail()
		h.log.Info("auth reauthentication failed", "user_id", st.user.ID, "peer", peerKey(r.RemoteAddr))
		writeInvalidCredentials(w)
		return
	}
	res.Success()
	sess, secret, err := h.ids.RotateSession(r.Context(), st.secret)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			h.clearCookie(w)
			server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
			return
		}
		h.internal(w, "rotate session", err)
		return
	}
	h.log.Info("auth reauthentication succeeded", "user_id", user.ID, "session_id", sess.ID)
	h.setCookie(w, sess, secret)
	server.WriteJSON(w, http.StatusOK, toState(user, sess, secret))
}
