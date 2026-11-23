// Package config parses and validates server configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/network"
)

const (
	// DefaultListenAddr binds to loopback only; exposing the server is an explicit choice.
	DefaultListenAddr = "127.0.0.1:8080"
	DefaultDataDir    = "data"

	EnvListen          = "BOOKMARKSYNC_LISTEN"
	EnvDataDir         = "BOOKMARKSYNC_DATA_DIR"
	EnvPublicURL       = "BOOKMARKSYNC_PUBLIC_URL"
	EnvTrustedProxies  = "BOOKMARKSYNC_TRUSTED_PROXIES"
	EnvManagementCIDRs = "BOOKMARKSYNC_MANAGEMENT_CIDRS"
	EnvGeoIPDB         = "BOOKMARKSYNC_GEOIP_DB"

	databaseFile = "bookmarksync.db"
)

// Config is the validated runtime configuration.
type Config struct {
	ListenAddr string
	DataDir    string
	// PublicOrigin is the normalized browser origin (scheme://host[:port]) users reach
	// the server at, from BOOKMARKSYNC_PUBLIC_URL or --public-url. It is the only origin
	// accepted for browser authentication; Host and forwarded headers are never used.
	// Empty means browser sign-in is disabled.
	PublicOrigin string
	// OIDCProviders are the operator-configured OpenID Connect providers (Google,
	// Authentik), from environment variables only. They require PublicOrigin.
	OIDCProviders []OIDCProvider
	Notifications NotificationConfig
	// TrustedProxies are the only peers allowed to supply X-Forwarded-For.
	TrustedProxies []netip.Prefix
	// ManagementCIDRs are explicit resolved client networks exempt from country rules.
	ManagementCIDRs []netip.Prefix
	// GeoIPDBPath points to a separately provisioned MaxMind Country MMDB file.
	GeoIPDBPath string
}

// Parse builds a Config from defaults, then environment, then flags (flags win).
// args are the arguments following the "serve" command.
func Parse(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{ListenAddr: DefaultListenAddr, DataDir: DefaultDataDir}
	if v := getenv(EnvDataDir); v != "" {
		cfg.DataDir = v
	}
	// Resolve the data directory before applying other options so the saved
	// runtime settings form the default layer beneath environment and CLI values.
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if (args[i] == "--data-dir" || args[i] == "-data-dir") && i+1 < len(args) {
			i++
			cfg.DataDir = args[i]
		} else if strings.HasPrefix(args[i], "--data-dir=") {
			cfg.DataDir = strings.TrimPrefix(args[i], "--data-dir=")
		} else if strings.HasPrefix(args[i], "-data-dir=") {
			cfg.DataDir = strings.TrimPrefix(args[i], "-data-dir=")
		}
	}
	if len(args) > 0 && (args[len(args)-1] == "--data-dir" || args[len(args)-1] == "-data-dir") {
		return Config{}, errors.New("data directory flag requires a value")
	}
	if cfg.DataDir == "" {
		return Config{}, errors.New("data directory must not be empty")
	}
	runtimeCfg, runtimeErr := LoadRuntime(RuntimePath(cfg.DataDir))
	if runtimeErr != nil && !errors.Is(runtimeErr, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load runtime configuration: %w", runtimeErr)
	}
	if runtimeErr == nil {
		cfg.ListenAddr = runtimeCfg.ListenAddr
		cfg.Notifications = runtimeCfg.Notifications
	}
	if v := getenv(EnvListen); v != "" {
		cfg.ListenAddr = v
	}
	publicURL := ""
	if runtimeErr == nil {
		publicURL = runtimeCfg.PublicOrigin
	}
	if v := getenv(EnvPublicURL); v != "" {
		publicURL = v
	}
	disableOrigin := false
	disableGoogle := false
	disableAuthentik := false

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "address to listen on")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for the database and local data")
	fs.StringVar(&publicURL, "public-url", publicURL, "public origin of the web interface")
	fs.BoolVar(&disableOrigin, "disable-public-url", false, "disable browser sign-in and saved identity providers")
	fs.BoolVar(&disableGoogle, "disable-oidc-google", false, "disable the saved Google identity provider")
	fs.BoolVar(&disableAuthentik, "disable-oidc-authentik", false, "disable the saved Authentik identity provider")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if disableOrigin {
		publicURL = ""
	}
	if publicURL != "" {
		o, err := ParsePublicOrigin(publicURL)
		if err != nil {
			return Config{}, err
		}
		cfg.PublicOrigin = o
	}
	providerDefaults := map[string]OIDCProvider{}
	if runtimeErr == nil {
		for _, provider := range runtimeCfg.OIDCProviders {
			providerDefaults[provider.ID] = provider
		}
	}
	googleEnvSet := getenv(EnvOIDCGoogleClientID) != "" || getenv(EnvOIDCGoogleClientSecret) != ""
	authentikEnvSet := getenv(EnvOIDCAuthentikIssuer) != "" || getenv(EnvOIDCAuthentikClientID) != "" || getenv(EnvOIDCAuthentikClientSecret) != ""
	if disableOrigin && (googleEnvSet || authentikEnvSet) {
		return Config{}, errors.New("OIDC environment settings cannot be used with --disable-public-url")
	}
	providers, err := parseOIDC(func(key string) string {
		if disableOrigin || (disableGoogle && (key == EnvOIDCGoogleClientID || key == EnvOIDCGoogleClientSecret)) || (disableAuthentik && (key == EnvOIDCAuthentikIssuer || key == EnvOIDCAuthentikClientID || key == EnvOIDCAuthentikClientSecret)) {
			return ""
		}
		if value := getenv(key); value != "" {
			return value
		}
		if (googleEnvSet && (key == EnvOIDCGoogleClientID || key == EnvOIDCGoogleClientSecret)) || (authentikEnvSet && (key == EnvOIDCAuthentikIssuer || key == EnvOIDCAuthentikClientID || key == EnvOIDCAuthentikClientSecret)) {
			return ""
		}
		for _, p := range providerDefaults {
			switch key {
			case EnvOIDCGoogleClientID:
				if p.ID == "google" {
					return p.ClientID
				}
			case EnvOIDCGoogleClientSecret:
				if p.ID == "google" {
					return p.ClientSecret
				}
			case EnvOIDCAuthentikIssuer:
				if p.ID == "authentik" {
					return p.Issuer
				}
			case EnvOIDCAuthentikClientID:
				if p.ID == "authentik" {
					return p.ClientID
				}
			case EnvOIDCAuthentikClientSecret:
				if p.ID == "authentik" {
					return p.ClientSecret
				}
			}
		}
		return ""
	}, cfg.PublicOrigin)
	if err != nil {
		return Config{}, err
	}
	cfg.OIDCProviders = providers
	cfg.TrustedProxies, err = network.ParseCIDRs(getenv(EnvTrustedProxies))
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", EnvTrustedProxies, err)
	}
	cfg.ManagementCIDRs, err = network.ParseCIDRs(getenv(EnvManagementCIDRs))
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", EnvManagementCIDRs, err)
	}
	cfg.GeoIPDBPath = strings.TrimSpace(getenv(EnvGeoIPDB))
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ParsePublicOrigin validates a public URL and returns its normalized origin: lower-case
// scheme and host, default port removed, no path, query, fragment, or user info. HTTPS
// is required except for loopback hosts (localhost, 127.0.0.0/8, ::1) used in local
// development, where browsers treat http as a secure context.
func ParsePublicOrigin(raw string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("invalid public URL %q: %s", raw, why)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad(err.Error())
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return bad("scheme must be https (or http for loopback development)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") {
		return bad("must be an origin only, without path, query, fragment, or user info")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return bad("host is required")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return bad("port must be a number from 1 to 65535")
		}
		if (scheme == "https" && n == 443) || (scheme == "http" && n == 80) {
			port = ""
		} else {
			port = strconv.Itoa(n)
		}
	}
	if scheme == "http" && !isLoopback(host) {
		return bad("https is required except for loopback development")
	}
	hostport := host
	if strings.Contains(host, ":") {
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport = net.JoinHostPort(host, port)
	}
	return scheme + "://" + hostport, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate reports the first problem with the configuration.
func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.ListenAddr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid listen address %q: port must be a number from 1 to 65535", c.ListenAddr)
	}
	if c.DataDir == "" {
		return errors.New("data directory must not be empty")
	}
	if c.PublicOrigin != "" {
		if o, err := ParsePublicOrigin(c.PublicOrigin); err != nil {
			return err
		} else if o != c.PublicOrigin {
			return fmt.Errorf("public origin %q is not normalized (want %q)", c.PublicOrigin, o)
		}
	}
	return nil
}

// DatabasePath is the SQLite file location inside the data directory.
func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, databaseFile)
}
