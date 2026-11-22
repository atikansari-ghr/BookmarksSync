package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeFileRoundTripIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	want := RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://bookmarks.example", OIDCProviders: []OIDCProvider{{ID: "google", Name: "Google", Issuer: GoogleIssuer, ClientID: "client", ClientSecret: "secret-value"}}}
	if err := SaveRuntime(path, want); err != nil {
		t.Fatal(err)
	}
	want.ListenAddr = "127.0.0.1:8081"
	if err := SaveRuntime(path, want); err != nil {
		t.Fatalf("replace runtime file: %v", err)
	}
	got, err := LoadRuntime(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ListenAddr != want.ListenAddr || len(got.OIDCProviders) != 1 || got.OIDCProviders[0].ClientSecret != want.OIDCProviders[0].ClientSecret || got.PublicOrigin != want.PublicOrigin {
		t.Fatalf("runtime config round trip mismatch: %#v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("runtime file mode = %o", info.Mode().Perm())
		}
	}
}

func TestLoadRuntimeMissingFile(t *testing.T) {
	if _, err := LoadRuntime(filepath.Join(t.TempDir(), "missing.json")); !os.IsNotExist(err) {
		t.Fatalf("LoadRuntime error = %v", err)
	}
}

func TestParseUsesRuntimeDefaultsThenEnvironmentAndFlags(t *testing.T) {
	dir := t.TempDir()
	if err := SaveRuntime(RuntimePath(dir), RuntimeConfig{ListenAddr: "0.0.0.0:9090", PublicOrigin: "https://saved.example", OIDCProviders: []OIDCProvider{{ID: "google", Name: "Google", Issuer: GoogleIssuer, ClientID: "saved-id", ClientSecret: "saved-secret"}}}); err != nil {
		t.Fatal(err)
	}
	getenv := func(string) string { return "" }
	cfg, err := Parse([]string{"--data-dir", dir}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:9090" || cfg.PublicOrigin != "https://saved.example" || len(cfg.OIDCProviders) != 1 || cfg.OIDCProviders[0].ClientSecret != "saved-secret" {
		t.Fatalf("runtime defaults not loaded: %#v", cfg)
	}
	cfg, err = Parse([]string{"--data-dir", dir, "--listen", "127.0.0.1:8081", "--public-url", "https://flag.example"}, func(key string) string {
		if key == EnvListen {
			return "127.0.0.1:8082"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8081" || cfg.PublicOrigin != "https://flag.example" {
		t.Fatalf("environment/flags precedence failed: %#v", cfg)
	}
}

func TestParseDoesNotMixPartialOIDCEnvironmentWithSavedProvider(t *testing.T) {
	dir := t.TempDir()
	if err := SaveRuntime(RuntimePath(dir), RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://saved.example", OIDCProviders: []OIDCProvider{{ID: "google", Name: "Google", Issuer: GoogleIssuer, ClientID: "saved-id", ClientSecret: "saved-secret"}}}); err != nil {
		t.Fatal(err)
	}
	_, err := Parse([]string{"--data-dir", dir}, func(key string) string {
		if key == EnvOIDCGoogleClientID {
			return "replacement-id"
		}
		return ""
	})
	if err == nil || strings.Contains(err.Error(), "saved-secret") {
		t.Fatalf("partial provider override was mixed or leaked: %v", err)
	}
}

func TestParseCanDisableSavedPublicOriginAndProviders(t *testing.T) {
	dir := t.TempDir()
	if err := SaveRuntime(RuntimePath(dir), RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://saved.example", OIDCProviders: []OIDCProvider{{ID: "google", Name: "Google", Issuer: GoogleIssuer, ClientID: "saved-id", ClientSecret: "saved-secret"}}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]string{"--data-dir", dir, "--disable-public-url"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicOrigin != "" || len(cfg.OIDCProviders) != 0 {
		t.Fatalf("saved login settings were not disabled: %#v", cfg)
	}
	cfg, err = Parse([]string{"--data-dir", dir, "--disable-oidc-google"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OIDCProviders) != 0 {
		t.Fatalf("saved provider was not disabled: %#v", cfg.OIDCProviders)
	}
}

func TestParseSingleDashDataDirLoadsItsOwnRuntime(t *testing.T) {
	defaultDir, targetDir := t.TempDir(), t.TempDir()
	if err := SaveRuntime(RuntimePath(defaultDir), RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://wrong.example"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveRuntime(RuntimePath(targetDir), RuntimeConfig{ListenAddr: "127.0.0.1:8181"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]string{"-data-dir", targetDir}, func(key string) string {
		if key == EnvDataDir {
			return defaultDir
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != targetDir || cfg.ListenAddr != "127.0.0.1:8181" || cfg.PublicOrigin != "" {
		t.Fatalf("loaded wrong instance runtime config: %#v", cfg)
	}
	for _, flag := range []string{"--data-dir=" + targetDir, "-data-dir=" + targetDir} {
		cfg, err := Parse([]string{flag}, func(string) string { return "" })
		if err != nil {
			t.Fatalf("Parse(%q): %v", flag, err)
		}
		if cfg.DataDir != targetDir || cfg.ListenAddr != "127.0.0.1:8181" {
			t.Fatalf("Parse(%q) loaded wrong runtime: %#v", flag, cfg)
		}
	}
}
