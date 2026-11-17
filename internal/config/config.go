// Package config parses and validates server configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// DefaultListenAddr binds to loopback only; exposing the server is an explicit choice.
	DefaultListenAddr = "127.0.0.1:8080"
	DefaultDataDir    = "data"

	EnvListen    = "BOOKMARKSYNC_LISTEN"
	EnvDataDir   = "BOOKMARKSYNC_DATA_DIR"
	EnvPublicURL = "BOOKMARKSYNC_PUBLIC_URL"

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
}

// Parse builds a Config from defaults, then environment, then flags (flags win).
// args are the arguments following the "serve" command.
func Parse(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{ListenAddr: DefaultListenAddr, DataDir: DefaultDataDir}
	if v := getenv(EnvListen); v != "" {
		cfg.ListenAddr = v
	}
	if v := getenv(EnvDataDir); v != "" {
		cfg.DataDir = v
	}
	publicURL := getenv(EnvPublicURL)

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "address to listen on")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for the database and local data")
	fs.StringVar(&publicURL, "public-url", publicURL, "public origin of the web interface")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if publicURL != "" {
		o, err := ParsePublicOrigin(publicURL)
		if err != nil {
			return Config{}, err
		}
		cfg.PublicOrigin = o
	}
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
