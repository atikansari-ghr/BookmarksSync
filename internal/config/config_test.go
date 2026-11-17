package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
)

func noEnv(string) string { return "" }

func TestDefaultsListenOnLoopback(t *testing.T) {
	cfg, err := config.Parse(nil, noEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:8080", cfg.ListenAddr)
	}
	if cfg.DataDir == "" {
		t.Error("DataDir must have a default")
	}
}

func TestFlagsOverrideDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Parse([]string{"--listen", "127.0.0.1:9191", "--data-dir", dir}, noEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9191" || cfg.DataDir != dir {
		t.Errorf("got %+v", cfg)
	}
}

func TestEnvironmentSuppliesDefaultsAndFlagsWin(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "BOOKMARKSYNC_LISTEN":
			return "127.0.0.1:7000"
		case "BOOKMARKSYNC_DATA_DIR":
			return "/from/env"
		}
		return ""
	}
	cfg, err := config.Parse(nil, env)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7000" || cfg.DataDir != "/from/env" {
		t.Errorf("env not applied: %+v", cfg)
	}
	cfg, err = config.Parse([]string{"--listen", "127.0.0.1:7001"}, env)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7001" {
		t.Errorf("flag should beat env, got %q", cfg.ListenAddr)
	}
}

func TestInvalidConfigIsRejected(t *testing.T) {
	cases := map[string][]string{
		"no port":             {"--listen", "127.0.0.1"},
		"garbage address":     {"--listen", "not an address"},
		"port out of range":   {"--listen", "127.0.0.1:70000"},
		"port zero":           {"--listen", "127.0.0.1:0"},
		"non numeric port":    {"--listen", "127.0.0.1:http"},
		"empty listen":        {"--listen", ""},
		"empty data dir":      {"--data-dir", ""},
		"unknown flag":        {"--bogus"},
		"unexpected argument": {"extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Parse(args, noEnv); err == nil {
				t.Fatalf("Parse(%v) succeeded, want error", args)
			}
		})
	}
}

func TestInvalidEnvironmentIsRejected(t *testing.T) {
	env := func(k string) string {
		if k == "BOOKMARKSYNC_LISTEN" {
			return "bad"
		}
		return ""
	}
	_, err := config.Parse(nil, env)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("want listen error, got %v", err)
	}
}

func TestDatabasePathIsInsideDataDir(t *testing.T) {
	cfg := config.Config{ListenAddr: "127.0.0.1:8080", DataDir: "d"}
	if got, want := cfg.DatabasePath(), filepath.Join("d", "bookmarksync.db"); got != want {
		t.Errorf("DatabasePath = %q, want %q", got, want)
	}
}
