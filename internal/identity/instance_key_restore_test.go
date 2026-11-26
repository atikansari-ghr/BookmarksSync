package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreInstanceKeyCreatesPlatformPrivateKey(t *testing.T) {
	dir := t.TempDir()
	want := make([]byte, InstanceKeySize)
	want[0] = 7
	if err := RestoreInstanceKey(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreateInstanceKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("restored instance key differs")
	}
	if err := RestoreInstanceKey(dir, want); err == nil {
		t.Fatal("RestoreInstanceKey replaced an existing key")
	}
}

func TestRestoreInstanceKeyRejectsMalformedKey(t *testing.T) {
	dir := t.TempDir()
	if err := RestoreInstanceKey(dir, []byte("bad")); err == nil {
		t.Fatal("accepted malformed key")
	}
	if _, err := os.Stat(filepath.Join(dir, InstanceKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("created a malformed key: %v", err)
	}
}
