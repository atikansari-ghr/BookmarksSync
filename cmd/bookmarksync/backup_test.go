package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
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
	restored := filepath.Join(t.TempDir(), "restored")
	if code := run(context.Background(), []string{"backup", "restore", "--file", archive, "--target-dir", restored}, strings.NewReader(""), io.Discard, &errb, func(string) string { return "" }); code != 0 {
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
	if code := run(context.Background(), []string{"backup", "restore", "--file", archive, "--target-dir", restored}, strings.NewReader(""), io.Discard, &errb, func(string) string { return "" }); code == 0 {
		t.Fatal("restore accepted existing target")
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
