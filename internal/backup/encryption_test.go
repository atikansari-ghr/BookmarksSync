package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

func TestEncryptDecryptArchiveStreamsMultipleAuthenticatedChunks(t *testing.T) {
	dir := t.TempDir()
	plain := make([]byte, 2*(1<<20)+317)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.zip")
	encrypted := filepath.Join(dir, "source.zip.enc")
	decrypted := filepath.Join(dir, "restored.zip")
	if err := os.WriteFile(source, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x4a}, RecoveryKeySize)
	if err := EncryptArchive(context.Background(), source, encrypted, key); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(encrypted); err != nil || bytes.Contains(got, plain[:128]) {
		t.Fatalf("encrypted file contains source data or cannot be read: %v", err)
	}
	if err := DecryptArchive(context.Background(), encrypted, decrypted, key); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(decrypted)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypted data mismatch: %v", err)
	}
}

func TestEncryptArchiveCancellationRemovesPartialOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.zip")
	if err := os.WriteFile(source, bytes.Repeat([]byte{1}, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(dir, "cancelled.enc")
	if err := EncryptArchive(ctx, source, output, bytes.Repeat([]byte{2}, RecoveryKeySize)); err == nil {
		t.Fatal("cancelled encryption succeeded")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("cancelled encryption left a partial output: %v", err)
	}
}

func TestDecryptArchiveRejectsWrongKeyTamperingAndTruncation(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{1}, RecoveryKeySize)
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, bytes.Repeat([]byte("data"), 700_000), 0o600); err != nil {
		t.Fatal(err)
	}
	container := filepath.Join(dir, "archive.enc")
	if err := EncryptArchive(context.Background(), source, container, key); err != nil {
		t.Fatal(err)
	}
	wrongOutput := filepath.Join(dir, "wrong-output")
	if err := DecryptArchive(context.Background(), container, wrongOutput, bytes.Repeat([]byte{2}, RecoveryKeySize)); err == nil {
		t.Fatal("wrong recovery key was accepted")
	}
	if _, err := os.Stat(wrongOutput); !os.IsNotExist(err) {
		t.Fatalf("failed decrypt left output behind: %v", err)
	}

	data, err := os.ReadFile(container)
	if err != nil {
		t.Fatal(err)
	}
	for name, damaged := range map[string][]byte{
		"tampered":  func() []byte { v := append([]byte(nil), data...); v[len(v)/2] ^= 0x80; return v }(),
		"truncated": append([]byte(nil), data[:len(data)-7]...),
	} {
		path := filepath.Join(dir, name+".enc")
		if err := os.WriteFile(path, damaged, 0o600); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(dir, name+".out")
		if err := DecryptArchive(context.Background(), path, output, key); err == nil {
			t.Errorf("%s data was accepted", name)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Errorf("%s decrypt left output behind: %v", name, err)
		}
	}
}

func TestDestinationRecoveryKeyIsStablePrivateAndExportable(t *testing.T) {
	dir := t.TempDir()
	const id = "0123456789abcdef0123456789abcdef"
	first, created, err := ensureDestinationKey(dir, id)
	if err != nil || !created || len(first) != RecoveryKeySize {
		t.Fatalf("first key created=%v len=%d err=%v", created, len(first), err)
	}
	second, created, err := ensureDestinationKey(dir, id)
	if err != nil || created || !bytes.Equal(first, second) {
		t.Fatalf("second key created=%v stable=%v err=%v", created, bytes.Equal(first, second), err)
	}
	exported, err := ExportDestinationRecoveryKey(dir, id)
	if err != nil || exported != fmt.Sprintf("%x", first) {
		t.Fatalf("export=%q err=%v", exported, err)
	}
	info, err := os.Stat(destinationKeyPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := securefile.Check(destinationKeyPath(dir, id), info); err != nil {
		t.Fatalf("key file is not private: %v", err)
	}
	keyFile := filepath.Join(dir, "recovery.key")
	f, err := securefile.Create(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(exported + "\n")); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadRecoveryKeyFile(keyFile)
	if err != nil || !bytes.Equal(loaded, first) {
		t.Fatalf("loaded recovery key matches=%v err=%v", bytes.Equal(loaded, first), err)
	}
}
