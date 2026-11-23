//go:build windows

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/windowsupdate"
)

func TestParseWorkerArgsRequiresExactInternalArguments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "release")
	valid := []string{"worker", "--tag", "build-0123456789abcdef0123456789abcdef01234567", "--release-dir", dir}
	if tag, gotDir, err := parseWorkerArgs(valid); err != nil || tag != valid[2] || gotDir != dir {
		t.Fatalf("parseWorkerArgs() = %q, %q, %v", tag, gotDir, err)
	}
	for _, args := range [][]string{
		{"worker", "--tag", "latest", "--release-dir", dir},
		{"worker", "--tag", valid[2], "--release-dir", "relative"},
		{"worker", "--tag", valid[2], "--release-dir", dir, "--extra"},
		{"service", "run", "--tag", valid[2]},
	} {
		if _, _, err := parseWorkerArgs(args); err == nil {
			t.Errorf("parseWorkerArgs(%q) succeeded", args)
		}
	}
}

func TestUpdateEventResultContainsOutcomeButNoPrivateErrorDetails(t *testing.T) {
	cases := []struct {
		name   string
		result error
		want   string
		avoid  string
	}{
		{"success", nil, "completed", ""},
		{"rolled back", windowsupdate.ErrUpdateRolledBack, "previous verified snapshot was restored", ""},
		{"rollback failure", errors.Join(&windowsupdate.RollbackFailure{SnapshotPath: filepath.Join(t.TempDir(), "snapshot-retained.zip")}, errors.New("password=secret")), "snapshot retained at", "secret"},
		{"verification failure", windowsupdate.ErrUpdateUnavailable, "could not be applied", ""},
		{"service recovery failure", windowsupdate.ErrServiceRecoveryFailed, "application service did not restart", ""},
	}
	programData := t.TempDir()
	t.Setenv("ProgramData", programData)
	for _, tc := range cases {
		if tc.name == "rollback failure" {
			tc.result = errors.Join(&windowsupdate.RollbackFailure{SnapshotPath: filepath.Join(programData, "BookmarksSync", "snapshots", "snapshot-retained.zip")}, errors.New("password=secret"))
		}
		_, _, message := updateEventMessage("build-0123456789abcdef0123456789abcdef01234567", tc.result)
		if !strings.Contains(message, tc.want) || (tc.avoid != "" && strings.Contains(message, tc.avoid)) {
			t.Errorf("event message %q does not safely describe %s", message, tc.name)
		}
	}
}
