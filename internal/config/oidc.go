package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// OpenID Connect providers are configured by the operator through environment
// variables only (never flags, so client secrets stay out of process listings). A
// provider is enabled when all of its variables are set; a partial set is an error.
const (
	EnvOIDCGoogleClientID        = "BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_ID"
	EnvOIDCGoogleClientSecret    = "BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET"
	EnvOIDCAuthentikIssuer       = "BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER"
	EnvOIDCAuthentikClientID     = "BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_ID"
	EnvOIDCAuthentikClientSecret = "BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_SECRET"

	// GoogleIssuer is Google's fixed OpenID Connect issuer.
	GoogleIssuer = "https://accounts.google.com"
)

// OIDCProvider is one configured OpenID Connect provider. ClientSecret must never be
// logged or shown.
type OIDCProvider struct {
	ID           string `json:"id"` // "google" or "authentik"; used in callback paths
	Name         string `json:"name"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// String hides the client secret if a provider is ever formatted.
func (p OIDCProvider) String() string {
	return fmt.Sprintf("OIDCProvider{%s %s client=%s}", p.ID, p.Issuer, p.ClientID)
}

func parseOIDC(getenv func(string) string, publicOrigin string) ([]OIDCProvider, error) {
	type group struct {
		id, name string
		issuer   string // fixed issuer, or "" when read from issuerVar
		vars     []string
	}
	groups := []group{
		{"google", "Google", GoogleIssuer, []string{EnvOIDCGoogleClientID, EnvOIDCGoogleClientSecret}},
		{"authentik", "Authentik", "", []string{EnvOIDCAuthentikIssuer, EnvOIDCAuthentikClientID, EnvOIDCAuthentikClientSecret}},
	}
	var out []OIDCProvider
	for _, g := range groups {
		vals := map[string]string{}
		var missing []string
		for _, v := range g.vars {
			raw := getenv(v)
			if raw != "" && strings.TrimSpace(raw) == "" {
				return nil, fmt.Errorf("%s must not be blank", v)
			}
			if raw == "" {
				missing = append(missing, v)
			}
			vals[v] = strings.TrimSpace(raw)
		}
		if len(missing) == len(g.vars) {
			continue // provider not configured
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("%s sign-in is partially configured: set %s", g.name, strings.Join(missing, ", "))
		}
		p := OIDCProvider{ID: g.id, Name: g.name, Issuer: g.issuer}
		switch g.id {
		case "google":
			p.ClientID, p.ClientSecret = vals[EnvOIDCGoogleClientID], vals[EnvOIDCGoogleClientSecret]
		case "authentik":
			iss, err := ParseOIDCIssuer(vals[EnvOIDCAuthentikIssuer])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", EnvOIDCAuthentikIssuer, err)
			}
			p.Issuer = iss
			p.ClientID, p.ClientSecret = vals[EnvOIDCAuthentikClientID], vals[EnvOIDCAuthentikClientSecret]
		}
		out = append(out, p)
	}
	if len(out) > 0 && publicOrigin == "" {
		return nil, fmt.Errorf("OpenID Connect sign-in requires %s (the exact callback is built from it)", EnvPublicURL)
	}
	return out, nil
}

// ParseOIDCIssuer validates an issuer URL and returns it unchanged (issuers are
// compared exactly, including any path). It must be absolute HTTPS (plain HTTP only on
// loopback, for development) without user info, query or fragment. Errors never echo
// the value, which could contain credentials.
func ParseOIDCIssuer(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("issuer must be an absolute URL")
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case u.Opaque != "" || u.Host == "" || u.Hostname() == "":
		return "", errors.New("issuer must be an absolute URL with a host")
	case u.User != nil:
		return "", errors.New("issuer must not contain user info")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#"):
		return "", errors.New("issuer must not contain a query or fragment")
	case scheme == "https":
	case scheme == "http" && isLoopback(strings.ToLower(u.Hostname())):
	default:
		return "", errors.New("issuer must use https (http only on loopback for development)")
	}
	return raw, nil
}
