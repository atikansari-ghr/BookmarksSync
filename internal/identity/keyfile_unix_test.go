//go:build unix

package identity_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func writePrivateFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceKeyFileIsOwnerOnlyOnUnix(t *testing.T) {
	dir := t.TempDir()
	if _, err := identity.LoadOrCreateInstanceKey(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, identity.InstanceKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode %v, want 0600", perm)
	}
}

func TestInstanceKeyWithBroadPermissionsFailsClosedOnUnix(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o606} {
		dir := t.TempDir()
		key, err := identity.LoadOrCreateInstanceKey(dir)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, identity.InstanceKeyFile)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := identity.LoadOrCreateInstanceKey(dir); err == nil {
			t.Fatalf("mode %v accepted", mode)
		}
		if got, _ := os.ReadFile(path); string(got) != string(key) {
			t.Fatalf("mode %v: key was replaced", mode)
		}
	}
}

func TestInstanceKeySymlinkIsRejectedOnUnix(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	writePrivateFile(t, target, []byte("0123456789abcdef0123456789abcdef"))
	if err := os.Symlink(target, filepath.Join(dir, identity.InstanceKeyFile)); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := identity.LoadOrCreateInstanceKey(dir); err == nil {
		t.Fatal("symlinked key accepted")
	}
}
