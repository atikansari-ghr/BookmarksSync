package windowsupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceInstalledBinariesStagesAndReplacesBothVerifiedAssets(t *testing.T) {
	releaseDir := t.TempDir()
	writeVerifiedReleaseFixture(t, releaseDir)
	release, err := VerifyReleaseFiles(releaseDir, validTag)
	if err != nil {
		t.Fatal(err)
	}
	installDir := t.TempDir()
	server := filepath.Join(installDir, "bookmarksync.exe")
	updater := filepath.Join(installDir, "bookmarksync-updater.exe")
	if err := os.WriteFile(server, []byte("old server"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updater, []byte("old updater"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceInstalledBinaries(installDir, server, updater, release); err != nil {
		t.Fatal(err)
	}
	for path, name := range map[string]string{server: WindowsServerAsset, updater: WindowsUpdaterAsset} {
		got, err := os.ReadFile(path)
		want, _ := os.ReadFile(filepath.Join(releaseDir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("installed %s = %q, %v; want verified release bytes", filepath.Base(path), got, err)
		}
	}
}

func TestReplaceInstalledBinariesRejectsTamperingWithoutPartialReplacement(t *testing.T) {
	releaseDir := t.TempDir()
	writeVerifiedReleaseFixture(t, releaseDir)
	release, err := VerifyReleaseFiles(releaseDir, validTag)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release.UpdaterExecutable, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	installDir := t.TempDir()
	server := filepath.Join(installDir, "bookmarksync.exe")
	updater := filepath.Join(installDir, "bookmarksync-updater.exe")
	_ = os.WriteFile(server, []byte("old server"), 0600)
	_ = os.WriteFile(updater, []byte("old updater"), 0600)
	if err := ReplaceInstalledBinaries(installDir, server, updater, release); err == nil {
		t.Fatal("tampered release accepted")
	}
	for path, want := range map[string]string{server: "old server", updater: "old updater"} {
		got, _ := os.ReadFile(path)
		if string(got) != want {
			t.Fatalf("%s changed despite validation failure: %q", filepath.Base(path), got)
		}
	}
}

func TestReplaceInstalledBinariesRollsBackFirstExecutableWhenSecondRenameFails(t *testing.T) {
	releaseDir := t.TempDir()
	writeVerifiedReleaseFixture(t, releaseDir)
	release, err := VerifyReleaseFiles(releaseDir, validTag)
	if err != nil {
		t.Fatal(err)
	}
	installDir := t.TempDir()
	server := filepath.Join(installDir, "bookmarksync.exe")
	updater := filepath.Join(installDir, "bookmarksync-updater.exe")
	_ = os.WriteFile(server, []byte("old server"), 0600)
	_ = os.WriteFile(updater, []byte("old updater"), 0600)
	rename := func(old, new string) error {
		if strings.Contains(old, "stage") && new == updater {
			return os.ErrPermission
		}
		return os.Rename(old, new)
	}
	if err := replaceInstalledBinaries(installDir, server, updater, release, rename); err == nil {
		t.Fatal("forced updater rename failure ignored")
	}
	for path, want := range map[string]string{server: "old server", updater: "old updater"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s after rollback = %q, %v", filepath.Base(path), got, err)
		}
	}
}

func TestReplaceInstalledBinariesRejectsReparseInstallDirectory(t *testing.T) {
	releaseDir := t.TempDir()
	writeVerifiedReleaseFixture(t, releaseDir)
	release, err := VerifyReleaseFiles(releaseDir, validTag)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "real-install")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(root, "install-link")
	if err := os.Symlink(target, installDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ReplaceInstalledBinaries(installDir, filepath.Join(installDir, "bookmarksync.exe"), filepath.Join(installDir, "bookmarksync-updater.exe"), release); err == nil {
		t.Fatal("replacement through reparse install directory accepted")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reparse target was modified: entries=%v err=%v", entries, err)
	}
}
