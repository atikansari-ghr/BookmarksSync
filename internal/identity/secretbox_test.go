package identity_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestInstanceKeyIsCreatedOnceAndReloaded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	k1, err := identity.LoadOrCreateInstanceKey(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(k1) != identity.InstanceKeySize || identity.InstanceKeySize != 32 {
		t.Fatalf("key length %d, want 32", len(k1))
	}
	if bytes.Equal(k1, make([]byte, 32)) {
		t.Fatal("key is all zero")
	}
	raw, err := os.ReadFile(filepath.Join(dir, identity.InstanceKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, k1) {
		t.Fatal("key file does not hold exactly the returned key")
	}
	k2, err := identity.LoadOrCreateInstanceKey(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("reload returned a different key")
	}
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	k3, err := identity.LoadOrCreateInstanceKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k1, k3) {
		t.Fatal("two installs share a key")
	}
}

// A malformed existing key must fail closed and must never be replaced: replacing it
// would silently orphan every encrypted TOTP seed.
func TestMalformedInstanceKeyFailsClosedAndIsNeverReplaced(t *testing.T) {
	cases := map[string][]byte{
		"empty":     {},
		"short":     bytes.Repeat([]byte{7}, 31),
		"long":      bytes.Repeat([]byte{7}, 33),
		"all zeros": make([]byte, 32),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, identity.InstanceKeyFile)
			writePrivateFile(t, path, content)
			if _, err := identity.LoadOrCreateInstanceKey(dir); err == nil {
				t.Fatal("malformed key accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Fatal("malformed key file was modified")
			}
		})
	}
	t.Run("directory in its place", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, identity.InstanceKeyFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := identity.LoadOrCreateInstanceKey(dir); err == nil {
			t.Fatal("directory accepted as key")
		}
	})
	t.Run("missing data directory", func(t *testing.T) {
		if _, err := identity.LoadOrCreateInstanceKey(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("key created without a data directory")
		}
	})
}

func TestInstanceKeyErrorsNeverContainKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	content := []byte("SECRETSECRETSECRETSECRETSECRETX") // 31 bytes
	writePrivateFile(t, filepath.Join(dir, identity.InstanceKeyFile), content)
	_, err := identity.LoadOrCreateInstanceKey(dir)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}
