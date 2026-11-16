// Package config parses and validates server configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
)

const (
	// DefaultListenAddr binds to loopback only; exposing the server is an explicit choice.
	DefaultListenAddr = "127.0.0.1:8080"
	DefaultDataDir    = "data"

	EnvListen  = "BOOKMARKSYNC_LISTEN"
	EnvDataDir = "BOOKMARKSYNC_DATA_DIR"

	databaseFile = "bookmarksync.db"
)

// Config is the validated runtime configuration.
type Config struct {
	ListenAddr string
	DataDir    string
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

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "address to listen on")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for the database and local data")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate reports the first problem with the configuration.
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.ListenAddr, err)
	}
	_ = host
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid listen address %q: port must be a number from 1 to 65535", c.ListenAddr)
	}
	if c.DataDir == "" {
		return errors.New("data directory must not be empty")
	}
	return nil
}

// DatabasePath is the SQLite file location inside the data directory.
func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, databaseFile)
}
