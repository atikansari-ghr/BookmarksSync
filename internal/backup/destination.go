package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

type DestinationDriver interface {
	Test(context.Context, Destination) error
	Publish(context.Context, Destination, string, string) error
	Delete(context.Context, Destination, string) error
}

type FilesystemDriver struct{}

var archiveObjectPattern = regexp.MustCompile(`^bookmarksync-(manual|weekly|monthly)-[0-9]{8}T[0-9]{6}\.[0-9]{3}Z-[0-9a-f]{32}\.zip$`)

func backupArchiveName(trigger Trigger, at time.Time, batchID string) string {
	if (trigger != TriggerManual && trigger != TriggerWeekly && trigger != TriggerMonthly) || len(batchID) != 32 || !hexString.MatchString(batchID) {
		return ""
	}
	return fmt.Sprintf("bookmarksync-%s-%s-%s.zip", trigger, at.UTC().Format("20060102T150405.000Z"), batchID)
}

func validArchiveName(name string) bool {
	return archiveObjectPattern.MatchString(name)
}

func (FilesystemDriver) Test(ctx context.Context, d Destination) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := destinationDirectory(d)
	if err != nil {
		return err
	}
	probe, err := securefile.CreateTemp(dir, ".bookmarksync-write-check-*.tmp")
	if err != nil {
		return errors.New("backup destination is not writable")
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if closeErr != nil || removeErr != nil {
		return errors.New("backup destination write check could not be cleaned up")
	}
	return nil
}

func (FilesystemDriver) Publish(ctx context.Context, d Destination, sourcePath, name string) error {
	if !validArchiveName(name) {
		return errors.New("backup archive name is invalid")
	}
	dir, err := destinationDirectory(d)
	if err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return errors.New("verified backup archive cannot be opened")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("verified backup archive is not a regular file")
	}
	tmp, err := securefile.CreateTemp(dir, ".bookmarksync-upload-*.tmp")
	if err != nil {
		return errors.New("backup destination cannot create a private temporary file")
	}
	tmpPath := tmp.Name()
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := copyContext(ctx, tmp, source); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.New("backup destination temporary file could not be synced")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("backup destination temporary file could not be closed")
	}
	if _, err := destinationDirectory(d); err != nil {
		return err
	}
	finalPath := filepath.Join(dir, name)
	if err := securefile.PublishFile(tmpPath, finalPath); err != nil {
		return errors.New("backup archive could not be published without replacement")
	}
	published = true
	if err := securefile.SyncDir(dir); err != nil {
		return errors.New("backup destination directory could not be synced")
	}
	return nil
}

func (FilesystemDriver) Delete(ctx context.Context, d Destination, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validArchiveName(name) {
		return errors.New("backup archive name is invalid")
	}
	dir, err := destinationDirectory(d)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("old backup archive could not be removed")
	}
	if err := securefile.SyncDir(dir); err != nil {
		return errors.New("backup destination directory could not be synced")
	}
	return nil
}

func destinationDirectory(d Destination) (string, error) {
	if d.Kind != DestinationLocal && d.Kind != DestinationNFS || !filepath.IsAbs(d.Reference) || filepath.Clean(d.Reference) != d.Reference {
		return "", errors.New("filesystem backup destination path is invalid")
	}
	info, err := os.Lstat(d.Reference)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("backup destination must be an existing directory")
	}
	resolved, err := filepath.EvalSymlinks(d.Reference)
	if err != nil {
		return "", errors.New("backup destination path cannot be resolved")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("backup destination path cannot be resolved")
	}
	if d.Kind == DestinationNFS {
		mounted, err := isNFSMount(resolved)
		if err != nil || !mounted {
			return "", errors.New("NFS destination is not an active NFS mount")
		}
	}
	return resolved, nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return errors.New("backup archive could not be copied to destination")
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read backup archive failed: %w", readErr)
		}
	}
}
