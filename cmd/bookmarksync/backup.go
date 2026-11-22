package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/atikansari-ghr/BookmarksSync/internal/backup"
	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

func backupCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: bookmarksync backup create|verify|restore|restore-user [options]")
		return 2
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dataDir := fs.String("data-dir", "data", "server data directory")
		output := fs.String("output", "", "destination ZIP archive")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if *output == "" || fs.NArg() != 0 {
			fmt.Fprintln(stderr, "backup create requires --output and no positional arguments")
			return 2
		}
		if _, err := config.LoadRuntime(config.RuntimePath(*dataDir)); err != nil {
			fmt.Fprintln(stderr, "backup create requires a server runtime config")
			return 1
		}
		databasePath := filepath.Join(*dataDir, "bookmarksync.db")
		info, err := os.Lstat(databasePath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintln(stderr, "backup create requires an existing regular database file")
			return 1
		}
		db, err := database.OpenReadOnly(ctx, databasePath)
		if err != nil {
			fmt.Fprintf(stderr, "backup create: %v\n", err)
			return 1
		}
		defer db.Close()
		result, err := backup.Create(ctx, backup.Options{DB: db, DataDir: *dataDir, Output: *output, Build: version.Current()})
		if err != nil {
			fmt.Fprintf(stderr, "backup create: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Backup created: %s (%d bytes, schema %d)\n", result.Path, result.Size, result.Manifest.SchemaVersion)
		return 0
	case "verify":
		fs := flag.NewFlagSet("backup verify", flag.ContinueOnError)
		fs.SetOutput(stderr)
		archivePath := fs.String("file", "", "backup archive to verify")
		keyPath := fs.String("key-file", "", "private hex recovery key file for an encrypted archive")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if *archivePath == "" || fs.NArg() != 0 {
			fmt.Fprintln(stderr, "backup verify requires --file and no positional arguments")
			return 2
		}
		var manifest backup.Manifest
		err := withPlainArchive(ctx, *archivePath, *keyPath, func(path string) error {
			var inspectErr error
			manifest, inspectErr = backup.Inspect(ctx, path)
			return inspectErr
		})
		if err != nil {
			fmt.Fprintf(stderr, "backup verify: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Backup verified: format %d, schema %d, %d files, created %s\n", manifest.FormatVersion, manifest.SchemaVersion, len(manifest.Entries), manifest.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"))
		return 0
	case "restore":
		fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
		fs.SetOutput(stderr)
		archivePath := fs.String("file", "", "backup archive to restore")
		target := fs.String("target-dir", "", "new, absent server data directory")
		keyPath := fs.String("key-file", "", "private hex recovery key file for an encrypted archive")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if *archivePath == "" || *target == "" || fs.NArg() != 0 {
			fmt.Fprintln(stderr, "backup restore requires --file and --target-dir")
			return 2
		}
		if err := withPlainArchive(ctx, *archivePath, *keyPath, func(path string) error { return backup.Restore(ctx, path, *target) }); err != nil {
			fmt.Fprintf(stderr, "backup restore: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Backup restored to %s\n", *target)
		return 0
	case "restore-user":
		fs := flag.NewFlagSet("backup restore-user", flag.ContinueOnError)
		fs.SetOutput(stderr)
		archivePath := fs.String("file", "", "full-system backup archive")
		username := fs.String("username", "", "existing account whose private data should be recovered")
		dataDir := fs.String("data-dir", "data", "existing live server data directory")
		keyPath := fs.String("key-file", "", "private hex recovery key file for an encrypted archive")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if *archivePath == "" || *username == "" || fs.NArg() != 0 {
			fmt.Fprintln(stderr, "backup restore-user requires --file, --username, and no positional arguments")
			return 2
		}
		databasePath := filepath.Join(*dataDir, "bookmarksync.db")
		info, err := os.Lstat(databasePath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintln(stderr, "backup restore-user requires an existing regular database file")
			return 1
		}
		db, err := database.Open(ctx, databasePath)
		if err != nil {
			fmt.Fprintf(stderr, "backup restore-user: %v\n", err)
			return 1
		}
		defer db.Close()
		var imported bool
		err = withPlainArchive(ctx, *archivePath, *keyPath, func(path string) error {
			var restoreErr error
			imported, restoreErr = backup.RestoreUser(ctx, path, db, *username)
			return restoreErr
		})
		if err != nil {
			fmt.Fprintf(stderr, "backup restore-user: %v\n", err)
			return 1
		}
		if imported {
			fmt.Fprintf(stdout, "User recovery applied for %s\n", *username)
		} else {
			fmt.Fprintf(stdout, "Bookmark recovery already applied for %s; settings checked\n", *username)
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: bookmarksync backup create|verify|restore|restore-user [options]")
		return 2
	}
}
