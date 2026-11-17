package auth

// OpenID Connect sign-in and linking (Google, Authentik) under /api/v1/auth/oidc.
//
// Authorization-code flow with PKCE (S256), state and nonce. The state is an opaque
// single-use server-side transaction (identity auth_flows, stored hashed, 10 minutes),
// bound to the browser by a __Host- cookie and, for linking, to the signed-in account
// and session. The redirect URI is built only from the configured public origin. ID
// tokens are verified with go-oidc (pinned v3.15.0): signature against the provider's
// JWKS, exact issuer, audience containing the client ID, expiry/nbf with the service
// clock. Checked here: the nonce, a non-empty subject, and azp (required with several
// audiences; whenever present it must equal the client ID, OIDC Core 3.1.3.7). A link
// is keyed by verified issuer+subject only; accounts are never created or matched by
// email. Discovery is lazy and cached; every provider call has a deadline. Codes,
// state, nonce, verifier, tokens, subjects, binding cookies and client secrets are
// never logged or reflected.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

const (
	// OIDCBindingCookieName binds an OIDC transaction to the browser that began it.
	OIDCBindingCookieName = "__Host-bookmarksync_oidc"
	// oidcCompletePath is the Task 6 page every callback redirects to.
	oidcCompletePath   = "/auth/oidc/complete"
	defaultOIDCTimeout = 10 * time.Second
	oidcBeginKey       = "\x00oidc-begin"
	maxSubjectBytes    = 255
)

// DefaultOIDCBeginLimits: each begin stores a pending transaction, so a direct peer may
// start at most 30 per 5 minutes (the global pending-flow cap still applies).
var DefaultOIDCBeginLimits = Limits{MaxKeys: 10000, Window: 5 * time.Minute, PeerFailures: 30}

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// OIDCProvider is one operator-configured provider. ClientSecret is never logged.
type OIDCProvider struct {
	ID           string
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret string
}

// String hides the client secret if a provider is ever formatted.
func (p OIDCProvider) String() string {
	return fmt.Sprintf("OIDCProvider{%s %s client=%s}", p.ID, p.Issuer, p.ClientID)
}

// OIDCOptions configures OIDC. Store defaults to Options.Identity when it implements
// OIDCStore.
type OIDCOptions struct {
	Providers []OIDCProvider
	// Timeout bounds each provider call (discovery, JWKS, token). Default 10s.
	Timeout time.Duration
	Store   OIDCStore
}

// OIDCStore is the subset of *identity.Service used for OIDC.
type OIDCStore interface {
	BeginOIDCFlow(ctx context.Context, st identity.OIDCFlowStart) (string, time.Time, error)
	ConsumeOIDCFlow(ctx context.Context, handle, provider, binding string, userID, sessionID int64) (identity.OIDCFlow, error)
	LoginWithOIDC(ctx context.Context, issuer, subject string) (identity.User, error)
	LinkOIDCIdentity(ctx context.Context, req identity.OIDCLinkRequest) (identity.OIDCIdentity, identity.SessionRotation, error)
	ListOIDCIdentities(ctx context.Context, userID int64) ([]identity.OIDCIdentity, error)
	UnlinkOIDCIdentity(ctx context.Context, userID, identityID, sessionID int64) (identity.SessionRotation, error)
}

// oidcRP is a provider plus its lazily discovered metadata.
type oidcRP struct {
	cfg         OIDCProvider
	redirectURI string

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

func (h *Handler) configureOIDC(opts Options) error {
	if len(opts.OIDC.Providers) == 0 {
		return nil
	}
	store := opts.OIDC.Store
	if store == nil {
		s, ok := opts.Identity.(OIDCStore)
		if !ok {
			return errors.New("auth: OIDC providers need an OIDC store")
		}
		store = s
	}
	h.oidcStore = store
	h.oidcTimeout = opts.OIDC.Timeout
	if h.oidcTimeout <= 0 {
		h.oidcTimeout = defaultOIDCTimeout
	}
	h.oidcClient = &http.Client{
		Timeout: h.oidcTimeout,
		// Provider endpoints come from discovery; never follow redirects elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	h.oidcBeginLimits = NewLimiter(oidcBeginLimits(opts.OIDCBeginLimits), opts.Now)
	h.oidc = map[string]*oidcRP{}
	for _, p := range opts.OIDC.Providers {
		if !providerIDPattern.MatchString(p.ID) || p.Name == "" || p.ClientID == "" || p.ClientSecret == "" {
			return fmt.Errorf("auth: OIDC provider %q is incomplete", p.ID)
		}
		if _, err := config.ParseOIDCIssuer(p.Issuer); err != nil {
			return fmt.Errorf("auth: OIDC provider %q: %w", p.ID, err)
		}
		if _, dup := h.oidc[p.ID]; dup {
			return fmt.Errorf("auth: OIDC provider %q configured twice", p.ID)
		}
		h.oidc[p.ID] = &oidcRP{cfg: p, redirectURI: h.origin + "/api/v1/auth/oidc/" + p.ID + "/callback"}
		h.oidcOrder = append(h.oidcOrder, p.ID)
	}
	return nil
}

func oidcBeginLimits(l Limits) Limits {
	if l.PeerFailures <= 0 {
		l.PeerFailures = DefaultOIDCBeginLimits.PeerFailures
	}
	if l.Window <= 0 {
		l.Window = DefaultOIDCBeginLimits.Window
	}
	if l.MaxKeys <= 0 {
		l.MaxKeys = DefaultOIDCBeginLimits.MaxKeys
	}
	l.UserFailures = math.MaxInt32 // per peer only
	return l.withDefaults()
}

func (h *Handler) registerOIDC(r server.Registrar) {
	recent := func(f http.HandlerFunc) http.Handler { return h.RequireSession(h.RequireRecentAuth(f)) }
	r.Handle("GET /api/v1/auth/oidc/providers", noStore(http.HandlerFunc(h.oidcProviders)))
	r.Handle("POST /api/v1/auth/oidc/{provider}/login", noStore(http.HandlerFunc(h.oidcLoginBegin)))
	r.Handle("POST /api/v1/auth/oidc/{provider}/link", recent(h.oidcLinkBegin))
	r.Handle("GET /api/v1/auth/oidc/{provider}/callback", noStore(http.HandlerFunc(h.oidcCallback)))
	r.Handle("GET /api/v1/auth/oidc/identities", h.RequireSession(http.HandlerFunc(h.oidcIdentities)))
	r.Handle("DELETE /api/v1/auth/oidc/identities/{id}", recent(h.oidcUnlink))
}

func (h *Handler) oidcProviders(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]entry, 0, len(h.oidcOrder))
	for _, id := range h.oidcOrder {
		out = append(out, entry{ID: id, Name: h.oidc[id].cfg.Name})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// discover returns the provider metadata and verifier, discovering once (bounded).
func (h *Handler) discover(ctx context.Context, p *oidcRP) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, p.verifier, nil
	}
	ctx, cancel := context.WithTimeout(ctx, h.oidcTimeout)
	defer cancel()
	prov, err := oidc.NewProvider(oidc.ClientContext(ctx, h.oidcClient), p.cfg.Issuer)
	if err != nil {
		return nil, nil, err
	}
	// JWKS requests use the bounded client; Verify callers add their own deadline.
	p.verifier = prov.VerifierContext(oidc.ClientContext(context.Background(), h.oidcClient),
		&oidc.Config{ClientID: p.cfg.ClientID, Now: h.now})
	p.provider = prov
	return prov, p.verifier, nil
}

func (p *oidcRP) oauth(prov *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{ClientID: p.cfg.ClientID, ClientSecret: p.cfg.ClientSecret, Endpoint: prov.Endpoint(),
		RedirectURL: p.redirectURI, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (h *Handler) oidcLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		server.WriteError(w, http.StatusForbidden, "origin_rejected", "cross-origin request rejected")
		return
	}
	h.oidcBegin(w, r, identity.OIDCLogin, 0, 0)
}

func (h *Handler) oidcLinkBegin(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	h.oidcBegin(w, r, identity.OIDCLink, st.user.ID, st.sess.ID)
}

func (h *Handler) oidcBegin(w http.ResponseWriter, r *http.Request, kind identity.OIDCFlowKind, userID, sessionID int64) {
	p := h.oidc[r.PathValue("provider")]
	if p == nil {
		server.WriteError(w, http.StatusNotFound, "not_found", "unknown sign-in provider")
		return
	}
	res, wait := h.oidcBeginLimits.Reserve(oidcBeginKey, r.RemoteAddr)
	if res == nil {
		h.log.Warn("auth oidc begin rate limited", "provider", p.cfg.ID, "peer", peerKey(r.RemoteAddr))
		writeRateLimited(w, wait)
		return
	}
	res.Fail() // every begin counts
	prov, _, err := h.discover(r.Context(), p)
	if err != nil {
		h.log.Warn("auth oidc discovery failed", "provider", p.cfg.ID, "error_class", errorClass(r.Context(), err))
		server.WriteError(w, http.StatusServiceUnavailable, "provider_unavailable", "the sign-in provider is unavailable; try again later")
		return
	}
	nonce, err1 := randomToken()
	binding, err2 := randomToken()
	if err := errors.Join(err1, err2); err != nil {
		h.internal(w, "oidc randomness", err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	stateHandle, expires, err := h.oidcStore.BeginOIDCFlow(r.Context(), identity.OIDCFlowStart{Kind: kind, Provider: p.cfg.ID,
		UserID: userID, SessionID: sessionID, Nonce: nonce, Verifier: verifier, Binding: binding})
	switch {
	case errors.Is(err, identity.ErrTooManyFlows):
		writeRateLimited(w, time.Minute)
		return
	case errors.Is(err, identity.ErrUnauthorized), errors.Is(err, identity.ErrInvalid), errors.Is(err, identity.ErrNotFound):
		h.clearCookie(w)
		server.WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign-in required")
		return
	case err != nil:
		h.internal(w, "begin oidc flow", err)
		return
	}
	authURL := p.oauth(prov).AuthCodeURL(stateHandle, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	maxAge := int(math.Ceil(expires.Sub(h.now()).Seconds()))
	http.SetCookie(w, &http.Cookie{Name: OIDCBindingCookieName, Value: binding, Path: "/", MaxAge: max(maxAge, 1),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	h.log.Info("auth oidc flow started", "provider", p.cfg.ID, "kind", string(kind), "user_id", userID, "peer", peerKey(r.RemoteAddr))
	server.WriteJSON(w, http.StatusOK, map[string]any{"authorizationUrl": authURL, "expiresAt": expires})
}

// errorClass describes a provider failure without its content.
func errorClass(ctx context.Context, err error) string {
	if unavailable(ctx, err) {
		return "unavailable"
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return "token_endpoint_rejected"
	}
	return "invalid"
}

// unavailable reports a provider timeout or network failure, as opposed to a protocol
// or verification failure.
func unavailable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var ue *url.Error
	return errors.As(err, &ue)
}

// oidcRedirect ends a callback with a 303 to the fixed completion page. Only closed
// result/error codes are carried; fragment is the second-factor handle, if any.
func (h *Handler) oidcRedirect(w http.ResponseWriter, r *http.Request, key, value, fragment string) {
	loc := oidcCompletePath + "?" + url.Values{key: {value}}.Encode()
	if fragment != "" {
		loc += "#" + url.Values{"flowId": {fragment}}.Encode()
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

func (h *Handler) oidcFail(w http.ResponseWriter, r *http.Request, p *oidcRP, code, class string) {
	provider := ""
	if p != nil {
		provider = p.cfg.ID
	}
	h.log.Info("auth oidc callback failed", "provider", provider, "result", code, "error_class", class, "peer", peerKey(r.RemoteAddr))
	h.oidcRedirect(w, r, "error", code, "")
}

// currentSession returns the live session presented with the request, if any.
func (h *Handler) currentSession(r *http.Request) (identity.Session, identity.User, bool) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return identity.Session{}, identity.User{}, false
	}
	sess, user, err := h.ids.ResolveSession(r.Context(), c.Value)
	return sess, user, err == nil
}

func (h *Handler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	// The binding cookie is single-use whatever happens.
	http.SetCookie(w, &http.Cookie{Name: OIDCBindingCookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	p := h.oidc[r.PathValue("provider")]
	q := r.URL.Query()
	stateHandle, code := q.Get("state"), q.Get("code")
	binding := ""
	if c, err := r.Cookie(OIDCBindingCookieName); err == nil {
		binding = c.Value
	}
	var uid, sid int64
	if sess, user, ok := h.currentSession(r); ok {
		uid, sid = user.ID, sess.ID
	}
	if q.Has("error") {
		// The user declined or the provider failed: end the transaction, reflect nothing.
		if p != nil && stateHandle != "" && binding != "" {
			_, _ = h.oidcStore.ConsumeOIDCFlow(r.Context(), stateHandle, p.cfg.ID, binding, uid, sid)
		}
		h.oidcFail(w, r, p, "access_denied", "provider_error")
		return
	}
	if stateHandle == "" || code == "" || binding == "" {
		h.oidcFail(w, r, p, "invalid_request", "missing_parameter")
		return
	}
	if p == nil {
		h.oidcFail(w, r, nil, "expired", "unknown_provider") // no live transaction can match
		return
	}
	flow, err := h.oidcStore.ConsumeOIDCFlow(r.Context(), stateHandle, p.cfg.ID, binding, uid, sid)
	if err != nil {
		if !errors.Is(err, identity.ErrUnauthorized) {
			h.log.Error("auth: consume oidc flow failed", "error", err)
		}
		h.oidcFail(w, r, p, "expired", "no_transaction")
		return
	}
	prov, verifier, err := h.discover(r.Context(), p)
	if err != nil {
		h.oidcFail(w, r, p, "unavailable", "discovery")
		return
	}
	exCtx, cancel := context.WithTimeout(context.WithValue(r.Context(), oauth2.HTTPClient, h.oidcClient), h.oidcTimeout)
	tok, err := p.oauth(prov).Exchange(exCtx, code, oauth2.VerifierOption(flow.Verifier))
	exErr := exCtx.Err()
	cancel()
	if err != nil {
		if exErr != nil || unavailable(context.Background(), err) {
			h.oidcFail(w, r, p, "unavailable", "token_exchange")
		} else {
			h.oidcFail(w, r, p, "invalid_request", "token_exchange")
		}
		return
	}
	rawIDToken, _ := tok.Extra("id_token").(string)
	if rawIDToken == "" {
		h.oidcFail(w, r, p, "invalid_request", "no_id_token")
		return
	}
	vCtx, vCancel := context.WithTimeout(r.Context(), h.oidcTimeout)
	idt, err := verifier.Verify(vCtx, rawIDToken)
	vErr := vCtx.Err()
	vCancel()
	if err != nil {
		if vErr != nil {
			h.oidcFail(w, r, p, "unavailable", "jwks")
		} else {
			h.oidcFail(w, r, p, "invalid_request", "id_token")
		}
		return
	}
	var claims struct {
		AuthorizedParty   string `json:"azp"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	if err := idt.Claims(&claims); err != nil {
		h.oidcFail(w, r, p, "invalid_request", "claims")
		return
	}
	switch {
	case idt.Nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(flow.Nonce)) != 1:
		h.oidcFail(w, r, p, "invalid_request", "nonce")
		return
	case idt.Subject == "" || len(idt.Subject) > maxSubjectBytes:
		h.oidcFail(w, r, p, "invalid_request", "subject")
		return
	// OIDC Core 3.1.3.7: with several audiences azp must be present; whenever azp is
	// present it must be this client's ID.
	case (len(idt.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != p.cfg.ClientID:
		h.oidcFail(w, r, p, "invalid_request", "azp")
		return
	}
	display := claims.Name
	if display == "" {
		display = claims.PreferredUsername
	}
	if display == "" {
		display = claims.Email // display only; never used for matching
	}
	// The configured issuer is the stable key (go-oidc verified the token's issuer
	// against it).
	switch flow.Kind {
	case identity.OIDCLogin:
		h.oidcLogin(w, r, p, idt.Subject)
	case identity.OIDCLink:
		h.oidcLink(w, r, p, flow, idt.Subject, display)
	default:
		h.oidcFail(w, r, p, "invalid_request", "flow_kind")
	}
}

func (h *Handler) oidcLogin(w http.ResponseWriter, r *http.Request, p *oidcRP, subject string) {
	user, err := h.oidcStore.LoginWithOIDC(r.Context(), p.cfg.Issuer, subject)
	if err != nil {
		if !errors.Is(err, identity.ErrUnauthorized) {
			h.log.Error("auth: oidc login lookup failed", "error", err)
		}
		h.oidcFail(w, r, p, "not_linked", "no_active_link")
		return
	}
	// A provider login never bypasses the account's second factor.
	if h.factors != nil {
		st, err := h.factors.TOTPStatus(r.Context(), user.ID)
		if err != nil {
			h.log.Error("auth: totp status failed", "error", err)
			h.oidcFail(w, r, p, "unavailable", "totp_status")
			return
		}
		if st.Enabled {
			if res, _ := h.factorLimiter.Reserve(accountKey(user.ID), accountPeer); res == nil {
				h.oidcFail(w, r, p, "rate_limited", "second_factor_locked")
				return
			} else {
				res.Cancel()
			}
			flow, err := h.factors.BeginSecondFactor(r.Context(), user.ID)
			switch {
			case err == nil:
				h.log.Info("auth oidc login pending second factor", "provider", p.cfg.ID, "user_id", user.ID, "peer", peerKey(r.RemoteAddr))
				h.oidcRedirect(w, r, "result", "second_factor_required", flow.ID)
				return
			case errors.Is(err, identity.ErrNotFound):
				// TOTP disabled meanwhile: continue to a session.
			case errors.Is(err, identity.ErrInvalid):
				h.oidcFail(w, r, p, "not_linked", "inactive")
				return
			default:
				h.log.Error("auth: begin second factor failed", "error", err)
				h.oidcFail(w, r, p, "unavailable", "second_factor")
				return
			}
		}
	}
	if _, _, err := h.startSession(w, r, user, "auth oidc login succeeded"); err != nil {
		if errors.Is(err, identity.ErrInvalid) || errors.Is(err, identity.ErrNotFound) {
			h.oidcFail(w, r, p, "not_linked", "inactive")
			return
		}
		h.log.Error("auth: create session failed", "error", err)
		h.oidcFail(w, r, p, "unavailable", "session")
		return
	}
	h.oidcRedirect(w, r, "result", "signed_in", "")
}

func (h *Handler) oidcLink(w http.ResponseWriter, r *http.Request, p *oidcRP, flow identity.OIDCFlow, subject, display string) {
	link, rot, err := h.oidcStore.LinkOIDCIdentity(r.Context(), identity.OIDCLinkRequest{UserID: flow.UserID, SessionID: flow.SessionID,
		Provider: p.cfg.ID, Issuer: p.cfg.Issuer, Subject: subject, DisplayName: display})
	switch {
	case errors.Is(err, identity.ErrExists):
		h.oidcFail(w, r, p, "already_linked", "conflict")
		return
	case errors.Is(err, identity.ErrUnauthorized):
		h.oidcFail(w, r, p, "expired", "session_gone")
		return
	case errors.Is(err, identity.ErrInvalid):
		h.oidcFail(w, r, p, "invalid_request", "identity")
		return
	case err != nil:
		h.log.Error("auth: link oidc identity failed", "error", err)
		h.oidcFail(w, r, p, "unavailable", "link")
		return
	}
	h.setCookie(w, rot.Session, rot.Secret)
	h.log.Info("auth oidc linked", "provider", p.cfg.ID, "user_id", flow.UserID, "link_id", link.ID, "session_id", rot.Session.ID)
	h.oidcRedirect(w, r, "result", "linked", "")
}

type oidcIdentityJSON struct {
	ID          int64      `json:"id"`
	Provider    string     `json:"provider"`
	DisplayName string     `json:"displayName"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
}

func (h *Handler) oidcIdentities(w http.ResponseWriter, r *http.Request) {
	list, err := h.oidcStore.ListOIDCIdentities(r.Context(), state(r).user.ID)
	if err != nil {
		h.internal(w, "list oidc identities", err)
		return
	}
	out := make([]oidcIdentityJSON, 0, len(list))
	for _, o := range list {
		j := oidcIdentityJSON{ID: o.ID, Provider: o.Provider, DisplayName: o.DisplayName, CreatedAt: o.CreatedAt}
		if !o.LastLoginAt.IsZero() {
			t := o.LastLoginAt
			j.LastLoginAt = &t
		}
		out = append(out, j)
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"identities": out})
}

func (h *Handler) oidcUnlink(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		server.WriteError(w, http.StatusNotFound, "not_found", "linked identity not found")
		return
	}
	rot, err := h.oidcStore.UnlinkOIDCIdentity(r.Context(), st.user.ID, id, st.sess.ID)
	switch {
	case errors.Is(err, identity.ErrNotFound):
		server.WriteError(w, http.StatusNotFound, "not_found", "linked identity not found")
		return
	case errors.Is(err, identity.ErrUnauthorized):
		h.sessionGone(w)
		return
	case err != nil:
		h.internal(w, "unlink oidc identity", err)
		return
	}
	tok, ok := h.rotated(w, rot)
	if !ok {
		return
	}
	h.log.Info("auth oidc unlinked", "user_id", st.user.ID, "link_id", id, "session_id", st.sess.ID)
	server.WriteJSON(w, http.StatusOK, map[string]any{"csrfToken": tok})
}
