package windowsupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestApplyUpdateVerifiesAndSnapshotsBeforeReplacement(t *testing.T) {
	ops := &fakeHostOps{}
	err := ApplyUpdate(context.Background(), validTag, ops)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"verify", "stop:BookmarksSync", "snapshot", "replace", "start:BookmarksSync", "ready"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("operations = %v, want %v", ops.calls, want)
	}
	if !ops.snapshotVerified {
		t.Fatal("replacement began before verified snapshot")
	}
}

func TestApplyUpdateDoesNotStopOnInvalidOrUntrustedRelease(t *testing.T) {
	for _, tc := range []struct {
		name string
		tag  string
		fail string
	}{{"invalid tag", "latest", ""}, {"unavailable release", validTag, "verify"}} {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeHostOps{failAt: tc.fail}
			if err := ApplyUpdate(context.Background(), tc.tag, ops); err == nil {
				t.Fatal("ApplyUpdate() succeeded; want failure")
			}
			if contains(ops.calls, "stop:BookmarksSync") || contains(ops.calls, "replace") {
				t.Fatalf("untrusted request changed services/files: %v", ops.calls)
			}
		})
	}
}

func TestApplyUpdateReadinessFailureRestoresSnapshot(t *testing.T) {
	ops := &fakeHostOps{failAt: "ready"}
	if err := ApplyUpdate(context.Background(), validTag, ops); !errors.Is(err, ErrUpdateRolledBack) {
		t.Fatalf("ApplyUpdate() error = %v, want ErrUpdateRolledBack", err)
	}
	want := []string{"verify", "stop:BookmarksSync", "snapshot", "replace", "start:BookmarksSync", "ready", "stop:BookmarksSync", "restore", "start:BookmarksSync", "ready"}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("operations = %v, want rollback sequence %v", ops.calls, want)
	}
}

func TestApplyUpdateRollbackFailureRetainsRecoverablePaths(t *testing.T) {
	ops := &fakeHostOps{failAt: "ready", restoreErr: errors.New("private path C:\\ProgramData\\secret")}
	err := ApplyUpdate(context.Background(), validTag, ops)
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("error = %v, want ErrRollbackFailed", err)
	}
	if !contains(ops.calls, "restore") || !ops.snapshotVerified {
		t.Fatalf("rollback did not retain verified snapshot: %v", ops.calls)
	}
	if containsText(err.Error(), "secret") {
		t.Fatalf("error leaked private operation details: %v", err)
	}
	if containsAfter(ops.calls, "restore", "start:BookmarksSync") {
		t.Fatalf("service restarted after a failed restore: %v", ops.calls)
	}
}

func TestApplyUpdateDoesNotRestoreWhileServiceStopFailed(t *testing.T) {
	ops := &fakeHostOps{failAt: "stop"}
	if err := ApplyUpdate(context.Background(), validTag, ops); err == nil {
		t.Fatal("stop failure ignored")
	}
	if contains(ops.calls, "snapshot") || contains(ops.calls, "replace") || contains(ops.calls, "restore") {
		t.Fatalf("files changed while service might be running: %v", ops.calls)
	}
}

func TestApplyUpdateReportsWhenAppCannotRestartAfterAbort(t *testing.T) {
	for _, tc := range []struct{ name, failAt string }{{"stop request", "stop"}, {"snapshot failure", "snapshot"}} {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeHostOps{failAt: tc.failAt, failStartAt: 1}
			if err := ApplyUpdate(context.Background(), validTag, ops); !errors.Is(err, ErrServiceRecoveryFailed) {
				t.Fatalf("ApplyUpdate() error = %v, want ErrServiceRecoveryFailed", err)
			}
		})
	}
}

func TestApplyUpdateDoesNotReplaceWhenSnapshotFails(t *testing.T) {
	ops := &fakeHostOps{failAt: "snapshot"}
	if err := ApplyUpdate(context.Background(), validTag, ops); err == nil {
		t.Fatal("ApplyUpdate() succeeded; want snapshot failure")
	}
	if contains(ops.calls, "replace") || !contains(ops.calls, "start:BookmarksSync") {
		t.Fatalf("replacement followed failed snapshot: %v", ops.calls)
	}
}

func TestApplyUpdateRejectsSnapshotForDifferentReleaseTag(t *testing.T) {
	ops := &fakeHostOps{snapshotTag: "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := ApplyUpdate(context.Background(), validTag, ops); err == nil {
		t.Fatal("snapshot for a different release was accepted")
	}
	if contains(ops.calls, "replace") {
		t.Fatalf("replacement used a different-tag snapshot: %v", ops.calls)
	}
}

const validTag = "build-0123456789abcdef0123456789abcdef01234567"

type fakeHostOps struct {
	calls            []string
	failAt           string
	restoreErr       error
	snapshotVerified bool
	readyCalls       int
	snapshotTag      string
	startCalls       int
	failStartAt      int
}

func (o *fakeHostOps) VerifyRelease(context.Context, string) (VerifiedRelease, error) {
	o.calls = append(o.calls, "verify")
	if o.failAt == "verify" {
		return VerifiedRelease{}, errors.New("untrusted release details")
	}
	root := os.TempDir()
	return VerifiedRelease{Tag: validTag, Commit: validTag[len("build-"):], ServerExecutable: filepath.Join(root, "server.exe"), UpdaterExecutable: filepath.Join(root, "updater.exe"), BuildInfo: filepath.Join(root, "BUILD_INFO.txt"), SHA256Sums: filepath.Join(root, "SHA256SUMS")}, nil
}
func (o *fakeHostOps) StopService(_ context.Context, name string) error {
	o.calls = append(o.calls, "stop:"+name)
	if o.failAt == "stop" {
		return errors.New("stop failed")
	}
	return nil
}
func (o *fakeHostOps) StartService(_ context.Context, name string) error {
	o.calls = append(o.calls, "start:"+name)
	o.startCalls++
	if o.startCalls == o.failStartAt {
		return errors.New("start failed")
	}
	return nil
}
func (o *fakeHostOps) CreateVerifiedSnapshot(context.Context, string) (Snapshot, error) {
	o.calls = append(o.calls, "snapshot")
	if o.failAt == "snapshot" {
		return Snapshot{}, errors.New("low disk space")
	}
	o.snapshotVerified = true
	tag := validTag
	if o.snapshotTag != "" {
		tag = o.snapshotTag
	}
	return Snapshot{Tag: tag, Path: filepath.Join(os.TempDir(), "snapshot-test.zip"), ManifestSHA256: strings.Repeat("a", 64), ArchiveSHA256: strings.Repeat("b", 64), Verified: true}, nil
}
func (o *fakeHostOps) ReplaceInstalledFiles(context.Context, VerifiedRelease) error {
	o.calls = append(o.calls, "replace")
	if !o.snapshotVerified {
		return errors.New("snapshot missing")
	}
	return nil
}
func (o *fakeHostOps) WaitReady(context.Context, string) error {
	o.calls = append(o.calls, "ready")
	o.readyCalls++
	if o.failAt == "ready" && o.readyCalls == 1 {
		return errors.New("service unavailable")
	}
	return nil
}
func (o *fakeHostOps) RestoreSnapshot(context.Context, Snapshot) error {
	o.calls = append(o.calls, "restore")
	return o.restoreErr
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func containsAfter(items []string, anchor, value string) bool {
	seen := false
	for _, item := range items {
		if item == anchor {
			seen = true
			continue
		}
		if seen && item == value {
			return true
		}
	}
	return false
}

func containsText(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
