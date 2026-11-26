package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/backup"
	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

func TestBackupCLIEndToEndAndExistingTargetProtection(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO app_metadata(key,value) VALUES('cli-backup','works')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "snapshot.zip")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"backup", "create", "--data-dir", dir, "--output", archive}, strings.NewReader(""), &out, &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("create exit %d: %s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run(context.Background(), []string{"backup", "verify", "--file", archive}, strings.NewReader(""), &out, &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("verify exit %d: %s", code, errb.String())
	}
	key := bytes.Repeat([]byte{0x5a}, backup.RecoveryKeySize)
	encrypted := filepath.Join(t.TempDir(), "snapshot.zip.enc")
	if err := backup.EncryptArchive(context.Background(), archive, encrypted, key); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errbuf := &bytes.Buffer{}
	if code := run(context.Background(), []string{"backup", "verify", "--file", encrypted}, strings.NewReader(""), &out, errbuf, func(string) string { return "" }); code != 1 {
		t.Fatalf("encrypted verify without key exit %d: %s", code, errbuf.String())
	}
	keyFile := filepath.Join(t.TempDir(), "recovery-key.txt")
	keyOutput, err := securefile.Create(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(keyOutput, fmt.Sprintf("%x\n", key)); err != nil {
		t.Fatal(err)
	}
	if err := keyOutput.Close(); err != nil {
		t.Fatal(err)
	}
	errbuf.Reset()
	if code := run(context.Background(), []string{"backup", "verify", "--file", encrypted, "--key-file", keyFile}, strings.NewReader(""), &out, errbuf, func(string) string { return "" }); code != 0 {
		t.Fatalf("encrypted verify exit %d: %s", code, errbuf.String())
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if code := run(context.Background(), []string{"backup", "restore", "--file", encrypted, "--key-file", keyFile, "--target-dir", restored}, strings.NewReader(""), io.Discard, &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("restore exit %d: %s", code, errb.String())
	}
	check, err := database.Open(context.Background(), filepath.Join(restored, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := check.SQL().QueryRow(`SELECT value FROM app_metadata WHERE key='cli-backup'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	check.Close()
	if value != "works" {
		t.Fatalf("restored value = %q", value)
	}
	if code := run(context.Background(), []string{"backup", "restore", "--file", encrypted, "--key-file", keyFile, "--target-dir", restored}, strings.NewReader(""), io.Discard, &errb, func(string) string { return "" }); code == 0 {
		t.Fatal("restore accepted existing target")
	}
}

func TestBackupRestoreUserCLIRequiresTargetAndArchivedUser(t *testing.T) {
	ctx := context.Background()
	archiveDir := t.TempDir()
	archiveDB, err := database.Open(ctx, filepath.Join(archiveDir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRuntime(config.RuntimePath(archiveDir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if code := run(ctx, []string{"backup", "create", "--data-dir", archiveDir, "--output", archive}, strings.NewReader(""), io.Discard, io.Discard, func(string) string { return "" }); code != 0 {
		t.Fatalf("create exit %d", code)
	}
	if err := archiveDB.Close(); err != nil {
		t.Fatal(err)
	}

	if code := run(ctx, []string{"backup", "restore-user", "--file", archive}, strings.NewReader(""), io.Discard, io.Discard, func(string) string { return "" }); code != 2 {
		t.Fatalf("missing username exit %d, want usage error", code)
	}
	dataDir := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(dataDir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.New(db.SQL(), identity.WithPasswordCost(64, 1, 1)).CreateUser(ctx, "alice", "long-enough-password", false); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := run(ctx, []string{"backup", "restore-user", "--file", archive, "--username", "alice", "--data-dir", dataDir}, strings.NewReader(""), io.Discard, &stderr, func(string) string { return "" }); code != 1 || !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("missing archived user exit %d: %s", code, stderr.String())
	}
}

func TestBackupCLIDoesNotMigrateTheSourceDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`DELETE FROM schema_migrations WHERE version=?`, database.LatestSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "before-upgrade.zip")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"backup", "create", "--data-dir", dir, "--output", archive}, strings.NewReader(""), &out, &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("create exit %d: %s", code, errb.String())
	}
	readOnly, err := database.OpenReadOnly(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	version, err := readOnly.SchemaVersion(context.Background())
	if err != nil || version != database.LatestSchemaVersion-1 {
		t.Fatalf("source database schema = %d, %v; backup must not migrate the source", version, err)
	}
}
