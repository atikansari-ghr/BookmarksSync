// Package windowsupdate contains the privileged Windows updater workflow.
package windowsupdate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
)

const AppServiceName = "BookmarksSync"

var (
	ErrUpdateUnavailable     = errors.New("approved update could not be applied")
	ErrUpdateRolledBack      = errors.New("update activation failed; previous version restored and ready")
	ErrRollbackFailed        = errors.New("update failed and rollback could not be completed")
	ErrServiceRecoveryFailed = errors.New("update stopped before replacement and the application service could not be restarted")
)

type RollbackFailure struct{ SnapshotPath string }

func (e *RollbackFailure) Error() string {
	return fmt.Sprintf("%s; verified snapshot retained at %s", ErrRollbackFailed, e.SnapshotPath)
}
func (e *RollbackFailure) Unwrap() error { return ErrRollbackFailed }

type VerifiedRelease struct {
	Tag               string
	Commit            string
	ServerExecutable  string
	UpdaterExecutable string
	BuildInfo         string
	SHA256Sums        string
	GeoLiteUpdater    string
	GeoLiteLicense    string
}

func (r VerifiedRelease) IsComplete() bool {
	return filepath.IsAbs(r.ServerExecutable) && filepath.IsAbs(r.UpdaterExecutable) && filepath.IsAbs(r.BuildInfo) && filepath.IsAbs(r.SHA256Sums) && filepath.IsAbs(r.GeoLiteUpdater) && filepath.IsAbs(r.GeoLiteLicense)
}

type Snapshot struct {
	Tag            string
	Path           string
	ManifestSHA256 string
	ArchiveSHA256  string
	Verified       bool
}

type HostOperations interface {
	VerifyRelease(context.Context, string) (VerifiedRelease, error)
	StopService(context.Context, string) error
	StartService(context.Context, string) error
	CreateVerifiedSnapshot(context.Context, string) (Snapshot, error)
	ReplaceInstalledFiles(context.Context, VerifiedRelease) error
	WaitReady(context.Context, string) error
	RestoreSnapshot(context.Context, Snapshot) error
}

func ApplyUpdate(ctx context.Context, tag string, ops HostOperations) error {
	if ops == nil || updates.ValidateApprovedTag(tag) != nil {
		return ErrUpdateUnavailable
	}
	release, err := ops.VerifyRelease(ctx, tag)
	if err != nil || release.Tag != tag || release.Commit != tag[len("build-"):] || !release.IsComplete() {
		return ErrUpdateUnavailable
	}
	if err := ops.StopService(ctx, AppServiceName); err != nil {
		cleanupCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if ops.StartService(cleanupCtx, AppServiceName) != nil {
			return ErrServiceRecoveryFailed
		}
		return ErrUpdateUnavailable
	}
	snapshot, err := ops.CreateVerifiedSnapshot(ctx, tag)
	if err != nil || snapshot.Tag != tag || !snapshot.Verified || !filepath.IsAbs(snapshot.Path) || len(snapshot.ManifestSHA256) != 64 || len(snapshot.ArchiveSHA256) != 64 {
		cleanupCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if ops.StartService(cleanupCtx, AppServiceName) != nil {
			return ErrServiceRecoveryFailed
		}
		return ErrUpdateUnavailable
	}
	if err := ops.ReplaceInstalledFiles(ctx, release); err != nil {
		return rollback(ctx, ops, snapshot)
	}
	if err := ops.StartService(ctx, AppServiceName); err != nil {
		return rollback(ctx, ops, snapshot)
	}
	if err := ops.WaitReady(ctx, ""); err != nil {
		return rollback(ctx, ops, snapshot)
	}
	return nil
}

func rollback(ctx context.Context, ops HostOperations, snapshot Snapshot) error {
	cleanupCtx, cancel := rollbackContext(ctx)
	defer cancel()
	if ops.StopService(cleanupCtx, AppServiceName) != nil {
		return &RollbackFailure{SnapshotPath: snapshot.Path}
	}
	if ops.RestoreSnapshot(cleanupCtx, snapshot) != nil {
		return &RollbackFailure{SnapshotPath: snapshot.Path}
	}
	if ops.StartService(cleanupCtx, AppServiceName) != nil || ops.WaitReady(cleanupCtx, "") != nil {
		return &RollbackFailure{SnapshotPath: snapshot.Path}
	}
	return ErrUpdateRolledBack
}

func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
}
