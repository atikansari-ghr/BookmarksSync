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

func TestPublicURLIsOptionalAndNormalizedToAnOrigin(t *testing.T) {
	cfg, err := config.Parse(nil, noEnv)
	if err != nil || cfg.PublicOrigin != "" {
		t.Fatalf("unset public URL: %+v %v", cfg, err)
	}
	cases := map[string]string{
		"https://Bookmarks.Example.com":      "https://bookmarks.example.com",
		"https://bookmarks.example.com/":     "https://bookmarks.example.com",
		"https://bookmarks.example.com:443":  "https://bookmarks.example.com",
		"https://bookmarks.example.com:8443": "https://bookmarks.example.com:8443",
		"http://127.0.0.1:8080":              "http://127.0.0.1:8080",
		"http://localhost:8080":              "http://localhost:8080",
		"http://[::1]:8080":                  "http://[::1]:8080",
		"http://localhost:80":                "http://localhost",
	}
	for in, want := range cases {
		cfg, err := config.Parse([]string{"--public-url", in}, noEnv)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if cfg.PublicOrigin != want {
			t.Errorf("%q -> %q, want %q", in, cfg.PublicOrigin, want)
		}
	}
	env := func(k string) string {
		if k == "BOOKMARKSYNC_PUBLIC_URL" {
			return "https://env.example.com"
		}
		return ""
	}
	if cfg, err := config.Parse(nil, env); err != nil || cfg.PublicOrigin != "https://env.example.com" {
		t.Errorf("env public URL: %+v %v", cfg, err)
	}
}

func TestInvalidPublicURLIsRejected(t *testing.T) {
	for _, in := range []string{
		"http://bookmarks.example.com",       // plain HTTP off loopback
		"http://192.168.1.5:8080",            // plain HTTP on a LAN address
		"http://localhost.example.com",       // not loopback
		"https://bookmarks.example.com/sync", // path
		"https://bookmarks.example.com?x=1",  // query
		"https://bookmarks.example.com#frag", // fragment
		"https://user:pw@bookmarks.example.com",
		"ftp://bookmarks.example.com",
		"bookmarks.example.com",
		"https://",
		"https://bookmarks.example.com:0",
		"https://bookmarks.example.com:99999",
	} {
		if _, err := config.Parse([]string{"--public-url", in}, noEnv); err == nil {
			t.Errorf("accepted public URL %q", in)
		}
	}
}
