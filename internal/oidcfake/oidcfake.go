// Package oidcfake is a test-only local OpenID Connect issuer: discovery and JWKS (via
// go-oidc's oidctest), an authorization endpoint simulated by Authorize, and a token
// endpoint that enforces client authentication, the exact redirect URI, single-use
// codes and PKCE S256. Knobs let tests forge ID-token claims, signatures and delays.
// It must never be imported by production code.
package oidcfake

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

// AuthRequest is what the browser carried to the authorization endpoint.
type AuthRequest struct {
	ClientID, RedirectURI, State, Nonce, Scope string
	CodeChallenge, CodeChallengeMethod         string
	ResponseType                               string
	Query                                      url.Values
}

// Server is a fake issuer. Exported fields may be changed between requests.
type Server struct {
	*httptest.Server
	Issuer       string
	ClientID     string
	ClientSecret string
	Key          *rsa.PrivateKey
	KeyID        string
	Now          func() time.Time

	// Claims, if set, edits the ID-token claims before signing (delete keys to omit).
	Claims func(claims map[string]any)
	// SignWith, if set, signs ID tokens with this key (unknown to JWKS) instead.
	SignWith *rsa.PrivateKey
	// Unsigned issues alg=none tokens.
	Unsigned bool
	// OmitIDToken leaves id_token out of the token response.
	OmitIDToken bool
	// Delay sleeps before answering the given path ("discovery", "keys", "token").
	Delay map[string]time.Duration

	mu            sync.Mutex
	codes         map[string]*grant
	TokenRequests []url.Values
	Discoveries   int
}

type grant struct {
	req     AuthRequest
	subject string
	used    bool
}

// New starts a fake issuer on a loopback HTTP listener.
func New(clientID, clientSecret string) *Server {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	s := &Server{ClientID: clientID, ClientSecret: clientSecret, Key: key, KeyID: "k1", Now: time.Now,
		codes: map[string]*grant{}, Delay: map[string]time.Duration{}}
	disc := &oidctest.Server{PublicKeys: []oidctest.PublicKey{{PublicKey: key.Public(), KeyID: "k1", Algorithm: "RS256"}}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			s.mu.Lock()
			s.Discoveries++
			s.mu.Unlock()
			s.sleep(r.Context(), "discovery")
			disc.ServeHTTP(w, r)
		case "/keys":
			s.sleep(r.Context(), "keys")
			disc.ServeHTTP(w, r)
		case "/token":
			s.sleep(r.Context(), "token")
			s.token(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	s.Issuer = s.Server.URL
	disc.SetIssuer(s.Issuer)
	return s
}

func (s *Server) sleep(ctx context.Context, what string) {
	s.mu.Lock()
	d := s.Delay[what]
	s.mu.Unlock()
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}
}

func random() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Authorize simulates the user approving the request at authURL as subject. It checks
// that authURL targets this issuer's authorization endpoint and returns the URL the
// provider would redirect the browser to (redirect_uri with code and state).
func (s *Server) Authorize(authURL, subject string) (string, AuthRequest, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", AuthRequest{}, err
	}
	if u.Scheme+"://"+u.Host != s.Issuer || u.Path != "/auth" {
		return "", AuthRequest{}, fmt.Errorf("oidcfake: authorization URL %q is not this issuer's /auth", u.Scheme+"://"+u.Host+u.Path)
	}
	q := u.Query()
	req := AuthRequest{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"),
		Nonce: q.Get("nonce"), Scope: q.Get("scope"), CodeChallenge: q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"), ResponseType: q.Get("response_type"), Query: q}
	if req.ClientID != s.ClientID {
		return "", req, errors.New("oidcfake: unknown client_id")
	}
	code := random()
	s.mu.Lock()
	s.codes[code] = &grant{req: req, subject: subject}
	s.mu.Unlock()
	cb, err := url.Parse(req.RedirectURI)
	if err != nil {
		return "", req, err
	}
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", req.State)
	cb.RawQuery = cq.Encode()
	return cb.String(), req, nil
}

func tokenError(w http.ResponseWriter, code int, kind string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": kind})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		tokenError(w, http.StatusMethodNotAllowed, "invalid_request")
		return
	}
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	s.mu.Lock()
	s.TokenRequests = append(s.TokenRequests, r.PostForm)
	s.mu.Unlock()
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != s.ClientID || secret != s.ClientSecret {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	s.mu.Lock()
	g := s.codes[r.PostForm.Get("code")]
	if g == nil || g.used {
		s.mu.Unlock()
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	g.used = true
	s.mu.Unlock()
	if r.PostForm.Get("redirect_uri") != g.req.RedirectURI {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if g.req.CodeChallengeMethod != "S256" || r.PostForm.Get("code_verifier") == "" ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.req.CodeChallenge {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	now := s.Now()
	claims := map[string]any{
		"iss": s.Issuer, "sub": g.subject, "aud": s.ClientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": g.req.Nonce, "name": "Name of " + g.subject, "email": g.subject + "@example.test",
	}
	if s.Claims != nil {
		s.Claims(claims)
	}
	raw, _ := json.Marshal(claims)
	var idToken string
	switch {
	case s.Unsigned:
		hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		idToken = hdr + "." + base64.RawURLEncoding.EncodeToString(raw) + "."
	case s.SignWith != nil:
		idToken = oidctest.SignIDToken(s.SignWith, s.KeyID, "RS256", string(raw))
	default:
		idToken = oidctest.SignIDToken(s.Key, s.KeyID, "RS256", string(raw))
	}
	resp := map[string]any{"access_token": random(), "token_type": "Bearer", "expires_in": 300}
	if !s.OmitIDToken {
		resp["id_token"] = idToken
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}

// LastTokenRequest returns the most recent token request form.
func (s *Server) LastTokenRequest() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.TokenRequests) == 0 {
		return nil
	}
	return s.TokenRequests[len(s.TokenRequests)-1]
}

// Discovered reports how many discovery requests were served.
func (s *Server) Discovered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Discoveries
}

// SetDelay sets a per-endpoint delay ("discovery", "keys", "token").
func (s *Server) SetDelay(what string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Delay[what] = d
}
