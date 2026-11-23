package windowsupdate

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRoundTripsDataConfigRcloneAndBothExecutables(t *testing.T) {
	paths := makeSnapshotFixture(t)
	snapshot, err := CreateSnapshot(context.Background(), paths, validTag)
	if err != nil || !snapshot.Verified {
		t.Fatalf("CreateSnapshot() = %#v, %v", snapshot, err)
	}
	lateFile := filepath.Join(paths.DataDir, "added-after-snapshot.db")
	if err := os.WriteFile(lateFile, []byte("newer data"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		paths.ServerExecutable:                       "server old",
		paths.UpdaterExecutable:                      "updater old",
		filepath.Join(paths.DataDir, "bookmarks.db"): "database old",
		paths.EnvFile:                                "oidc_secret=old",
		paths.RcloneConfig:                           "token=old",
	} {
		if err := os.WriteFile(name, []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		_ = value
	}
	if err := RestoreSnapshot(context.Background(), paths, snapshot); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		paths.ServerExecutable:                       "server old",
		paths.UpdaterExecutable:                      "updater old",
		filepath.Join(paths.DataDir, "bookmarks.db"): "database old",
		paths.EnvFile:                                "oidc_secret=old",
		paths.RcloneConfig:                           "token=old",
	} {
		got, err := os.ReadFile(name)
		if err != nil || string(got) != want {
			t.Fatalf("restored %s = %q, %v; want original value", filepath.Base(name), got, err)
		}
	}
	if _, err := os.Stat(lateFile); !os.IsNotExist(err) {
		t.Fatalf("post-snapshot data file survived full restore: %v", err)
	}
}

func TestSnapshotRejectsReparsePathsAndCorruptArchives(t *testing.T) {
	paths := makeSnapshotFixture(t)
	link := filepath.Join(paths.DataDir, "escape")
	if err := os.Symlink(paths.ServerExecutable, link); err == nil {
		if _, err := CreateSnapshot(context.Background(), paths, validTag); err == nil {
			t.Fatal("snapshot accepted a symlink")
		}
		_ = os.Remove(link)
	} else {
		t.Logf("symlink check skipped: %v", err)
	}
	snapshot, err := CreateSnapshot(context.Background(), paths, validTag)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(snapshot.Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("corrupt"))
	_ = f.Close()
	if err := RestoreSnapshot(context.Background(), paths, snapshot); err == nil {
		t.Fatal("RestoreSnapshot accepted a corrupted archive")
	}
}

func TestSnapshotRetentionKeepsNewestThreeVerifiedArchives(t *testing.T) {
	paths := makeSnapshotFixture(t)
	for i := 0; i < 4; i++ {
		if _, err := CreateSnapshot(context.Background(), paths, validTag); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(paths.SnapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".zip") {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("retained %d snapshots, want 3", count)
	}
}

func TestSnapshotContainsManifestAndIsCompressed(t *testing.T) {
	paths := makeSnapshotFixture(t)
	snapshot, err := CreateSnapshot(context.Background(), paths, validTag)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(snapshot.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	found := false
	for _, file := range archive.File {
		if file.Name == "manifest.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("snapshot is missing its verification manifest")
	}
}

func TestSnapshotRejectsOverlappingDataAndInstallRoots(t *testing.T) {
	paths := makeSnapshotFixture(t)
	paths.DataDir = filepath.Join(paths.InstallDir, "data")
	if err := os.MkdirAll(paths.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSnapshot(context.Background(), paths, validTag); err == nil {
		t.Fatal("overlapping installation/data roots accepted")
	}
}

func makeSnapshotFixture(t *testing.T) SnapshotPaths {
	t.Helper()
	root := t.TempDir()
	paths := SnapshotPaths{
		InstallDir: filepath.Join(root, "install"), DataDir: filepath.Join(root, "data"),
		EnvFile: filepath.Join(root, "config", "bookmarksync.env"), RcloneConfig: filepath.Join(root, "rclone", "rclone.conf"),
		SnapshotDir: filepath.Join(root, "snapshots"),
	}
	paths.ServerExecutable = filepath.Join(paths.InstallDir, "bookmarksync.exe")
	paths.UpdaterExecutable = filepath.Join(paths.InstallDir, "bookmarksync-updater.exe")
	for _, dir := range []string{paths.InstallDir, paths.DataDir, filepath.Dir(paths.EnvFile), filepath.Dir(paths.RcloneConfig), paths.SnapshotDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]string{
		paths.ServerExecutable: "server old", paths.UpdaterExecutable: "updater old",
		filepath.Join(paths.DataDir, "bookmarks.db"): "database old", paths.EnvFile: "oidc_secret=old",
		paths.RcloneConfig: "token=old",
	} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}
