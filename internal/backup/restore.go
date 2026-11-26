package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

// Restore verifies the complete archive before publishing its contents. The
// destination must be absent; restore never merges into an existing instance.
func Restore(ctx context.Context, archivePath, targetDir string) error {
	if archivePath == "" || targetDir == "" {
		return errors.New("archive and restore destination are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := filepath.Abs(targetDir)
	if err != nil {
		return errors.New("restore destination is invalid")
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect restore destination: %w", err)
	}
	parent := filepath.Dir(target)
	if err := securefile.MkdirAll(parent); err != nil {
		return fmt.Errorf("create restore parent directory: %w", err)
	}
	stage, err := securefile.CreateTempDir(parent, ".bookmarksync-restore-*")
	if err != nil {
		return fmt.Errorf("create restore staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if _, err := verifyToStage(ctx, archivePath, stage); err != nil {
		return err
	}
	if err := moveToDataRoot(stage, filepath.FromSlash(databaseName), "bookmarksync.db"); err != nil {
		return fmt.Errorf("prepare restored database: %w", err)
	}
	if err := moveToDataRoot(stage, filepath.FromSlash(runtimeName), "runtime.json"); err != nil {
		return fmt.Errorf("prepare restored runtime configuration: %w", err)
	}
	if err := disableRestoredBackupTargets(ctx, filepath.Join(stage, "bookmarksync.db")); err != nil {
		return fmt.Errorf("prepare restored backup settings: %w", err)
	}
	_ = os.Remove(filepath.Join(stage, "database"))
	_ = os.Remove(filepath.Join(stage, "config"))
	if err := ctx.Err(); err != nil {
		return err
	}
	runtimeCfg, err := config.LoadRuntime(config.RuntimePath(stage))
	if err != nil {
		return errors.New("restored runtime configuration is invalid")
	}
	keyPath := filepath.Join(stage, "instance.key")
	if _, err := os.Lstat(keyPath); err == nil {
		key, err := os.ReadFile(keyPath)
		if err != nil {
			return errors.New("restored instance key cannot be read")
		}
		if err := os.Remove(keyPath); err != nil {
			return errors.New("restored instance key cannot be secured")
		}
		if err := identity.RestoreInstanceKey(stage, key); err != nil {
			for i := range key {
				key[i] = 0
			}
			return errors.New("restored instance key is invalid or could not be secured")
		}
		for i := range key {
			key[i] = 0
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("restored instance key cannot be checked")
	} else if runtimeCfg.PublicOrigin != "" {
		return errors.New("backup with browser sign-in is missing its instance key")
	}
	if err := filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return securefile.CheckDirectory(path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := securefile.Check(path, info); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("secure restored file permissions: %w", err)
	}
	if err := syncTreeDirs(stage); err != nil {
		return fmt.Errorf("sync restored staging directories: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("restore destination appeared during restore")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("recheck restore destination: %w", err)
	}
	if err := securefile.CheckDirectory(stage); err != nil {
		return fmt.Errorf("restore staging directory is not private: %w", err)
	}
	if err := securefile.PublishDir(stage, target); err != nil {
		return fmt.Errorf("publish restored data directory: %w", err)
	}
	if err := securefile.SyncDir(parent); err != nil {
		return fmt.Errorf("sync restore parent directory: %w", err)
	}
	return nil
}

func disableRestoredBackupTargets(ctx context.Context, dbPath string) error {
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.SQL().ExecContext(ctx, `UPDATE backup_schedule SET weekly_enabled=0,monthly_enabled=0`); err != nil {
		return err
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE backup_destinations SET enabled=0`); err != nil {
		return err
	}
	_, err = db.SQL().ExecContext(ctx, `UPDATE backup_runs SET state='failed',error_code='cancelled',finished_at=COALESCE(finished_at,?) WHERE state='running'`, time.Now().UTC().UnixMilli())
	return err
}

func syncTreeDirs(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := securefile.SyncDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

func moveToDataRoot(root, relative, name string) error {
	if err := os.Rename(filepath.Join(root, relative), filepath.Join(root, name)); err != nil {
		return err
	}
	return nil
}
