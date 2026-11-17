package config_test

import (
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
)

const (
	googleSecret    = "google-client-secret-value"
	authentikSecret = "authentik-client-secret-value"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func fullOIDCEnv() map[string]string {
	return map[string]string{
		config.EnvPublicURL:                 "https://bookmarks.example.test",
		config.EnvOIDCGoogleClientID:        "google-client",
		config.EnvOIDCGoogleClientSecret:    googleSecret,
		config.EnvOIDCAuthentikIssuer:       "https://auth.example.test/application/o/bookmarks/",
		config.EnvOIDCAuthentikClientID:     "authentik-client",
		config.EnvOIDCAuthentikClientSecret: authentikSecret,
	}
}

func TestOIDCProvidersComeFromTheEnvironment(t *testing.T) {
	cfg, err := config.Parse(nil, envOf(fullOIDCEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OIDCProviders) != 2 {
		t.Fatalf("providers = %+v", cfg.OIDCProviders)
	}
	g, a := cfg.OIDCProviders[0], cfg.OIDCProviders[1]
	if g.ID != "google" || g.Name != "Google" || g.Issuer != config.GoogleIssuer || config.GoogleIssuer != "https://accounts.google.com" ||
		g.ClientID != "google-client" || g.ClientSecret != googleSecret {
		t.Fatalf("google = %+v", g)
	}
	// The Authentik issuer is used exactly as configured (per-application path).
	if a.ID != "authentik" || a.Name != "Authentik" || a.Issuer != "https://auth.example.test/application/o/bookmarks/" ||
		a.ClientID != "authentik-client" || a.ClientSecret != authentikSecret {
		t.Fatalf("authentik = %+v", a)
	}
	// Either provider alone is fine.
	only := fullOIDCEnv()
	delete(only, config.EnvOIDCAuthentikIssuer)
	delete(only, config.EnvOIDCAuthentikClientID)
	delete(only, config.EnvOIDCAuthentikClientSecret)
	cfg, err = config.Parse(nil, envOf(only))
	if err != nil || len(cfg.OIDCProviders) != 1 || cfg.OIDCProviders[0].ID != "google" {
		t.Fatalf("google only = %+v, %v", cfg.OIDCProviders, err)
	}
	// None configured is fine too.
	cfg, err = config.Parse(nil, envOf(map[string]string{config.EnvPublicURL: "https://bookmarks.example.test"}))
	if err != nil || len(cfg.OIDCProviders) != 0 {
		t.Fatalf("none = %+v, %v", cfg.OIDCProviders, err)
	}
}

func TestPartialOrUnusableOIDCConfigurationIsRejectedWithoutLeakingSecrets(t *testing.T) {
	cases := map[string]func(m map[string]string){
		"google id only":           func(m map[string]string) { delete(m, config.EnvOIDCGoogleClientSecret) },
		"google secret only":       func(m map[string]string) { delete(m, config.EnvOIDCGoogleClientID) },
		"authentik without issuer": func(m map[string]string) { delete(m, config.EnvOIDCAuthentikIssuer) },
		"authentik without id":     func(m map[string]string) { delete(m, config.EnvOIDCAuthentikClientID) },
		"authentik without secret": func(m map[string]string) { delete(m, config.EnvOIDCAuthentikClientSecret) },
		"no public url":            func(m map[string]string) { delete(m, config.EnvPublicURL) },
		"http issuer":              func(m map[string]string) { m[config.EnvOIDCAuthentikIssuer] = "http://auth.example.test/app/" },
		"issuer with query":        func(m map[string]string) { m[config.EnvOIDCAuthentikIssuer] = "https://auth.example.test/app/?x=1" },
		"issuer with fragment":     func(m map[string]string) { m[config.EnvOIDCAuthentikIssuer] = "https://auth.example.test/app/#f" },
		"issuer with userinfo":     func(m map[string]string) { m[config.EnvOIDCAuthentikIssuer] = "https://u:p@auth.example.test/app/" },
		"relative issuer":          func(m map[string]string) { m[config.EnvOIDCAuthentikIssuer] = "auth.example.test/app" },
		"issuer without host":      func(m map[string]string) { m[config.EnvOIDCAuthentikIssuer] = "https:///app" },
		"whitespace secret":        func(m map[string]string) { m[config.EnvOIDCGoogleClientSecret] = "  " },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			m := fullOIDCEnv()
			mod(m)
			_, err := config.Parse(nil, envOf(m))
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), googleSecret) || strings.Contains(err.Error(), authentikSecret) || strings.Contains(err.Error(), "u:p@") {
				t.Fatalf("error leaks a secret: %v", err)
			}
		})
	}
}

func TestLoopbackHTTPIssuerIsAllowedForDevelopment(t *testing.T) {
	m := fullOIDCEnv()
	m[config.EnvOIDCAuthentikIssuer] = "http://127.0.0.1:9000/application/o/dev/"
	cfg, err := config.Parse(nil, envOf(m))
	if err != nil || cfg.OIDCProviders[1].Issuer != "http://127.0.0.1:9000/application/o/dev/" {
		t.Fatalf("loopback issuer = %+v, %v", cfg.OIDCProviders, err)
	}
}

// Client secrets are environment-only: there is no flag that would put them in the
// process list.
func TestOIDCSecretsHaveNoFlagForm(t *testing.T) {
	for _, flag := range []string{"--oidc-google-client-secret", "--oidc-authentik-client-secret", "--google-client-secret"} {
		if _, err := config.Parse([]string{flag, "x"}, noEnv); err == nil {
			t.Fatalf("flag %s accepted", flag)
		}
	}
}
