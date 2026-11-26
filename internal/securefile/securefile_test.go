package securefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateTempProducesCheckablePrivateFile(t *testing.T) {
	f, err := CreateTemp(t.TempDir(), "secret-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	if _, err := f.Write([]byte("secret")); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(path, info); err != nil {
		t.Fatalf("private file rejected: %v", err)
	}
}

func TestCheckRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(link, info); err == nil {
		t.Fatal("accepted symlink as a private file")
	}
}

func TestCreateTempDirIsPrivate(t *testing.T) {
	path, err := CreateTempDir(t.TempDir(), "restore-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckDirectory(path); err != nil {
		t.Fatalf("private directory rejected: %v", err)
	}
	nested := filepath.Join(path, "one", "two")
	if err := MkdirAll(nested); err != nil {
		t.Fatal(err)
	}
	if err := CheckDirectory(filepath.Dir(nested)); err != nil {
		t.Fatalf("nested private directory rejected: %v", err)
	}
}

func TestPublishFileNeverReplacesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "prepared")
	f, err := Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("new")); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "published")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishFile(source, destination); err == nil {
		t.Fatal("PublishFile replaced an existing destination")
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "keep" {
		t.Fatalf("destination changed: %q (%v)", got, err)
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := PublishFile(source, destination); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "new" {
		t.Fatalf("published content = %q (%v)", got, err)
	}
}

func TestPublishDirNeverReplacesExistingEmptyDirectory(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "stage")
	if err := MkdirPrivate(source); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "target")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PublishDir(source, destination); err == nil {
		t.Fatal("PublishDir replaced an existing empty directory")
	}
	if info, err := os.Stat(destination); err != nil || !info.IsDir() {
		t.Fatalf("destination directory changed: %v", err)
	}
}
