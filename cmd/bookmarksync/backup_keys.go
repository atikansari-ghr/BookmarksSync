package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/atikansari-ghr/BookmarksSync/internal/backup"
	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

func withPlainArchive(ctx context.Context, archivePath, keyPath string, run func(string) error) error {
	if keyPath == "" {
		isEncrypted, err := backup.IsEncryptedArchive(archivePath)
		if err != nil {
			return err
		}
		if isEncrypted {
			return errors.New("archive is encrypted; provide --key-file")
		}
		return run(archivePath)
	}
	key, err := backup.ReadRecoveryKeyFile(keyPath)
	if err != nil {
		return err
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	stage, err := securefile.CreateTempDir(os.TempDir(), "bookmarksync-cli-decrypt-*")
	if err != nil {
		return errors.New("private archive staging directory could not be created")
	}
	defer os.RemoveAll(stage)
	plainPath := filepath.Join(stage, "archive.zip")
	if err := backup.DecryptArchive(ctx, archivePath, plainPath, key); err != nil {
		return err
	}
	return run(plainPath)
}
