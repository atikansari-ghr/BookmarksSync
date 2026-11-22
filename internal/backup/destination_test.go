package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesystemDestinationPublishesWithoutReplacement(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("verified backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := Destination{Kind: DestinationLocal, Reference: dir}
	name := backupArchiveName(TriggerWeekly, time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC), strings.Repeat("a", 32))
	driver := FilesystemDriver{}
	if err := driver.Test(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	if err := driver.Publish(context.Background(), destination, source, name); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != "verified backup" {
		t.Fatalf("published file = %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := driver.Publish(context.Background(), destination, source, name); err == nil {
		t.Fatal("Publish replaced an existing archive")
	}
	got, err = os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing destination after collision = %q, %v", got, err)
	}
}

func TestFilesystemDestinationRejectsMissingOrInvalidPaths(t *testing.T) {
	driver := FilesystemDriver{}
	for _, destination := range []Destination{
		{Kind: DestinationLocal, Reference: filepath.Join(t.TempDir(), "missing")},
		{Kind: DestinationLocal, Reference: "relative/path"},
		{Kind: DestinationNFS, Reference: t.TempDir()},
	} {
		if err := driver.Test(context.Background(), destination); err == nil {
			t.Errorf("Test accepted invalid destination: %#v", destination)
		}
	}
}

func TestFilesystemDestinationRejectsUnsafeArchiveNames(t *testing.T) {
	driver := FilesystemDriver{}
	destination := Destination{Kind: DestinationLocal, Reference: t.TempDir()}
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape.zip", "notes.txt", "bookmarksync-weekly-20261122T030000Z-abc.zip"} {
		if err := driver.Publish(context.Background(), destination, source, name); err == nil {
			t.Errorf("Publish accepted archive name %q", name)
		}
	}
	if err := driver.Delete(context.Background(), destination, "../escape.zip"); err == nil {
		t.Fatal("Delete accepted a traversal path")
	}
}

func TestFilesystemDestinationDeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	driver := FilesystemDriver{}
	destination := Destination{Kind: DestinationLocal, Reference: dir}
	name := backupArchiveName(TriggerManual, time.Now(), strings.Repeat("b", 32))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := driver.Delete(context.Background(), destination, name); err != nil {
		t.Fatal(err)
	}
	if err := driver.Delete(context.Background(), destination, name); err != nil {
		t.Fatalf("deleting already removed archive: %v", err)
	}
}
