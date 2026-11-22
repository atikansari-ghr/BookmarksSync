package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

// RestoreUser merges the selected user's private bookmarks and saved setting
// overrides from a verified full archive into an existing instance. Authentication,
// sessions, shares, system policy, and other users are never imported.
func RestoreUser(ctx context.Context, archivePath string, live *database.DB, username string) (bool, error) {
	if archivePath == "" || live == nil || username == "" {
		return false, errors.New("user recovery requires an archive, live database, and username")
	}
	archiveHash, err := hashArchive(ctx, archivePath)
	if err != nil {
		return false, err
	}
	stage, err := securefile.CreateTempDir(os.TempDir(), "bookmarksync-user-restore-*")
	if err != nil {
		return false, fmt.Errorf("create private recovery staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if _, err := verifyToStage(ctx, archivePath, stage); err != nil {
		return false, fmt.Errorf("verify recovery archive: %w", err)
	}
	source, err := database.Open(ctx, filepath.Join(stage, filepath.FromSlash(databaseName)))
	if err != nil {
		return false, fmt.Errorf("open archived database: %w", err)
	}
	defer source.Close()
	var sourceUserID int64
	if err := source.SQL().QueryRowContext(ctx, `SELECT id FROM users WHERE username = ? COLLATE NOCASE`, username).Scan(&sourceUserID); errors.Is(err, sql.ErrNoRows) {
		return false, identity.ErrNotFound
	} else if err != nil {
		return false, err
	}
	var barID, otherID int64
	if err := source.SQL().QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id = ? AND kind = 'bar' AND deleted_at IS NULL`, sourceUserID).Scan(&barID); err != nil {
		return false, fmt.Errorf("archived user bookmarks are unavailable")
	}
	if err := source.SQL().QueryRowContext(ctx, `SELECT id FROM folders WHERE owner_id = ? AND kind = 'other' AND deleted_at IS NULL`, sourceUserID).Scan(&otherID); err != nil {
		return false, fmt.Errorf("archived user bookmarks are unavailable")
	}
	sourceBookmarks := bookmarks.New(source.SQL())
	sourceScope := bookmarks.Scope{OwnerID: sourceUserID, Actor: sourceUserID}
	barExport, err := sourceBookmarks.ExportTree(ctx, sourceScope, barID)
	if err != nil {
		return false, err
	}
	otherExport, err := sourceBookmarks.ExportTree(ctx, sourceScope, otherID)
	if err != nil {
		return false, err
	}
	barTree, otherTree := toImportNodes(barExport), toImportNodes(otherExport)
	sourceSettings, err := readExplicitSettings(ctx, source.SQL(), sourceUserID)
	if err != nil {
		return false, err
	}

	targetIDs := identity.New(live.SQL())
	targetUser, err := targetIDs.GetUser(ctx, username)
	if err != nil {
		return false, err
	}
	targetBookmarks := bookmarks.New(live.SQL())
	targetScope, err := targetBookmarks.PrivateScope(ctx, targetUser.ID, true)
	if err != nil {
		return false, err
	}
	currentSettings, err := readExplicitSettings(ctx, live.SQL(), targetUser.ID)
	if err != nil {
		return false, err
	}
	changes := make(map[string]*string, len(currentSettings)+len(sourceSettings))
	for key := range currentSettings {
		if _, exists := sourceSettings[key]; !exists {
			changes[key] = nil
		}
	}
	for key, value := range sourceSettings {
		v := value
		changes[key] = &v
	}
	if len(changes) > 0 {
		if err := identity.ValidateSettingsChanges(changes); err != nil {
			return false, fmt.Errorf("archived user settings are invalid: %w", err)
		}
	}
	importScope := targetScope
	importScope.Actor = targetUser.ID
	imported, err := targetBookmarks.MergeUserRestore(ctx, importScope, archiveHash, barTree, otherTree)
	if err != nil {
		return false, err
	}
	if len(changes) > 0 {
		if _, err := targetIDs.UpdateSettings(ctx, targetUser.ID, changes); err != nil {
			return imported, fmt.Errorf("restore user settings: %w", err)
		}
	}
	return imported, nil
}

func hashArchive(ctx context.Context, path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 22 || info.Size() > maxArchiveSize {
		return "", errors.New("recovery archive must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("recovery archive cannot be opened")
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, err := h.Write(buf[:n]); err != nil {
				return "", errors.New("recovery archive cannot be hashed")
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", errors.New("recovery archive cannot be hashed")
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readExplicitSettings(ctx context.Context, db *sql.DB, userID int64) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM user_settings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func toImportNodes(nodes []bookmarks.ExportNode) []bookmarks.ImportNode {
	out := make([]bookmarks.ImportNode, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, bookmarks.ImportNode{Folder: node.Folder, Title: node.Title, URL: node.URL, Tags: node.Tags, Favorite: node.Favorite, Children: toImportNodes(node.Children)})
	}
	return out
}
