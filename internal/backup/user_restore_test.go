package backup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestRestoreUserMergesOnlySelectedUserAndIsRepeatSafe(t *testing.T) {
	ctx := context.Background()
	archivedDir := t.TempDir()
	archivedDB, err := database.Open(ctx, filepath.Join(archivedDir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archivedDB.Close() })
	archivedIDs := identity.New(archivedDB.SQL(), identity.WithPasswordCost(64, 1, 1))
	archivedAlice, err := archivedIDs.CreateUser(ctx, "alice", "long-enough-password", false)
	if err != nil {
		t.Fatal(err)
	}
	archivedBob, err := archivedIDs.CreateUser(ctx, "bob", "long-enough-password", false)
	if err != nil {
		t.Fatal(err)
	}
	archivedBookmarks := bookmarks.New(archivedDB.SQL())
	archivedAliceScope, err := archivedBookmarks.PrivateScope(ctx, archivedAlice.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var archivedBar int64
	if err := archivedDB.SQL().QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id=? AND kind='bar'`, archivedAlice.ID).Scan(&archivedBar); err != nil {
		t.Fatal(err)
	}
	if _, err := archivedBookmarks.ImportTree(ctx, archivedAliceScope, archivedBar, []bookmarks.ImportNode{{Folder: true, Title: "Work", Children: []bookmarks.ImportNode{{Title: "Docs", URL: "https://docs.example/", Tags: []string{"work"}}}}}); err != nil {
		t.Fatal(err)
	}
	var archivedDocID int64
	if err := archivedDB.SQL().QueryRowContext(ctx, `SELECT id FROM bookmarks WHERE url=? AND owner_id=?`, "https://docs.example/", archivedAlice.ID).Scan(&archivedDocID); err != nil {
		t.Fatal(err)
	}
	if err := archivedBookmarks.SetFavorite(ctx, archivedAliceScope, archivedDocID, true); err != nil {
		t.Fatal(err)
	}
	archivedTheme := "ocean-glass"
	if _, err := archivedIDs.UpdateSettings(ctx, archivedAlice.ID, map[string]*string{"ui.theme": &archivedTheme}); err != nil {
		t.Fatal(err)
	}
	archivedBobScope, err := archivedBookmarks.PrivateScope(ctx, archivedBob.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var bobBar int64
	if err := archivedDB.SQL().QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id=? AND kind='bar'`, archivedBob.ID).Scan(&bobBar); err != nil {
		t.Fatal(err)
	}
	if _, err := archivedBookmarks.ImportTree(ctx, archivedBobScope, bobBar, []bookmarks.ImportNode{{Title: "Private", URL: "https://bob.example/"}}); err != nil {
		t.Fatal(err)
	}
	secret := "warm-glass"
	if _, err := archivedIDs.UpdateSettings(ctx, archivedBob.ID, map[string]*string{"ui.theme": &secret}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRuntime(config.RuntimePath(archivedDir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "full.zip")
	if _, err := Create(ctx, Options{DB: archivedDB, DataDir: archivedDir, Output: archivePath}); err != nil {
		t.Fatal(err)
	}
	if _, err := archivedDB.SQL().ExecContext(ctx, `UPDATE user_settings SET value='invalid-theme' WHERE user_id=? AND key='ui.theme'`, archivedAlice.ID); err != nil {
		t.Fatal(err)
	}
	invalidSettingsArchive := filepath.Join(t.TempDir(), "invalid-settings.zip")
	if _, err := Create(ctx, Options{DB: archivedDB, DataDir: archivedDir, Output: invalidSettingsArchive}); err != nil {
		t.Fatal(err)
	}
	if err := archivedDB.Close(); err != nil {
		t.Fatal(err)
	}

	liveDB, err := database.Open(ctx, filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer liveDB.Close()
	liveIDs := identity.New(liveDB.SQL(), identity.WithPasswordCost(64, 1, 1))
	liveAlice, err := liveIDs.CreateUser(ctx, "alice", "different-current-password", false)
	if err != nil {
		t.Fatal(err)
	}
	liveBob, err := liveIDs.CreateUser(ctx, "bob", "different-current-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := liveIDs.CreateSession(ctx, liveAlice.ID, "current-session"); err != nil {
		t.Fatal(err)
	}
	var livePasswordHash string
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=?`, liveAlice.ID).Scan(&livePasswordHash); err != nil {
		t.Fatal(err)
	}
	liveBookmarks := bookmarks.New(liveDB.SQL())
	aliceScope, err := liveBookmarks.PrivateScope(ctx, liveAlice.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var liveBar int64
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id=? AND kind='bar'`, liveAlice.ID).Scan(&liveBar); err != nil {
		t.Fatal(err)
	}
	if _, err := liveBookmarks.ImportTree(ctx, aliceScope, liveBar, []bookmarks.ImportNode{{Title: "Current", URL: "https://current.example/"}}); err != nil {
		t.Fatal(err)
	}
	oldTheme := "classic"
	oldSort := "title"
	if _, err := liveIDs.UpdateSettings(ctx, liveAlice.ID, map[string]*string{"ui.theme": &oldTheme, "bookmarks.sort": &oldSort}); err != nil {
		t.Fatal(err)
	}
	if imported, err := RestoreUser(ctx, invalidSettingsArchive, liveDB, "alice"); err == nil || imported {
		t.Fatalf("invalid archived settings imported=%v err=%v", imported, err)
	}
	var beforeRecoveryCount int
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT count(*) FROM bookmarks WHERE owner_id=?`, liveAlice.ID).Scan(&beforeRecoveryCount); err != nil || beforeRecoveryCount != 1 {
		t.Fatalf("invalid settings changed bookmarks: count=%d err=%v", beforeRecoveryCount, err)
	}

	imported, err := RestoreUser(ctx, archivePath, liveDB, "ALICE")
	if err != nil || !imported {
		t.Fatalf("RestoreUser first call imported=%v err=%v", imported, err)
	}
	imported, err = RestoreUser(ctx, archivePath, liveDB, "alice")
	if err != nil || imported {
		t.Fatalf("RestoreUser repeat imported=%v err=%v", imported, err)
	}
	aliceScope, err = liveBookmarks.PrivateScope(ctx, liveAlice.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	barTree, err := liveBookmarks.ExportTree(ctx, aliceScope, liveBar)
	if err != nil {
		t.Fatal(err)
	}
	if len(barTree) != 2 || barTree[0].Title != "Current" || barTree[1].Title != "Work" || barTree[1].Children[0].Tags[0] != "work" {
		t.Fatalf("recovered target tree = %#v", barTree)
	}
	settings, err := liveIDs.Settings(ctx, liveAlice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settings["ui.theme"] != "ocean-glass" {
		t.Fatalf("Alice theme = %q", settings["ui.theme"])
	}
	var favoriteCount int
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM bookmark_favorites f JOIN bookmarks b ON b.id=f.bookmark_id WHERE f.user_id=? AND b.url=?`, liveAlice.ID, "https://docs.example/").Scan(&favoriteCount); err != nil || favoriteCount != 1 {
		t.Fatalf("restored favorite count = %d, %v", favoriteCount, err)
	}
	if _, exists := settings["bookmarks.sort"]; exists {
		t.Fatalf("stale setting was not removed: %#v", settings)
	}
	var recoveredPasswordHash string
	var sessionCount int
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=?`, liveAlice.ID).Scan(&recoveredPasswordHash); err != nil {
		t.Fatal(err)
	}
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT count(*) FROM web_sessions WHERE user_id=?`, liveAlice.ID).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if recoveredPasswordHash != livePasswordHash || sessionCount != 1 {
		t.Fatalf("account identity changed: password preserved=%v sessions=%d", recoveredPasswordHash == livePasswordHash, sessionCount)
	}
	bobScope, err := liveBookmarks.PrivateScope(ctx, liveBob.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var liveBobBar int64
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id=? AND kind='bar'`, liveBob.ID).Scan(&liveBobBar); err != nil {
		t.Fatal(err)
	}
	bobTree, err := liveBookmarks.ExportTree(ctx, bobScope, liveBobBar)
	if err != nil {
		t.Fatal(err)
	}
	if len(bobTree) != 0 {
		t.Fatalf("Bob received archived content: %#v", bobTree)
	}
	bobSettings, err := liveIDs.Settings(ctx, liveBob.ID)
	if err != nil || bobSettings["ui.theme"] != "" {
		t.Fatalf("Bob settings changed: %#v (%v)", bobSettings, err)
	}
}
