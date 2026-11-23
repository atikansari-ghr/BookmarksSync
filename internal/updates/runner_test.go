package updates

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHostRunnerInvokesOnlyTheRestrictedHelperWithExactTag(t *testing.T) {
	var called int
	sudoPath := filepath.Join(t.TempDir(), "sudo.exe")
	helperPath := filepath.Join(t.TempDir(), "bookmarksync-update")
	runner := HostRunner{SudoPath: sudoPath, HelperPath: helperPath, Run: func(_ context.Context, path string, args ...string) error {
		called++
		if path != sudoPath || !reflect.DeepEqual(args, []string{"-n", helperPath, "schedule", "--tag", "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}) {
			t.Fatalf("command = %q %q", path, args)
		}
		return nil
	}}
	job, err := runner.Start(context.Background(), "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || job != "bookmarksync-update-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || called != 1 {
		t.Fatalf("Start = %q, %v, calls %d", job, err, called)
	}
}

func TestHostRunnerRejectsUnavailableAndInvalidRequestsWithoutExecution(t *testing.T) {
	if _, err := (HostRunner{}).Start(context.Background(), "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrRunnerUnavailable) {
		t.Fatalf("disabled runner error = %v", err)
	}
	called := false
	runner := HostRunner{SudoPath: "/usr/bin/sudo", HelperPath: "/usr/local/sbin/bookmarksync-update", Run: func(context.Context, string, ...string) error { called = true; return nil }}
	for _, tag := range []string{"latest", "build-aaaaaaaaaaaa", "build-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;id"} {
		if _, err := runner.Start(context.Background(), tag); err == nil {
			t.Errorf("invalid tag %q succeeded", tag)
		}
	}
	if called {
		t.Fatal("invalid request reached the privileged command")
	}
}

func TestHostRunnerHidesHelperOutput(t *testing.T) {
	runner := HostRunner{SudoPath: "/usr/bin/sudo", HelperPath: "/usr/local/sbin/bookmarksync-update", Run: func(context.Context, string, ...string) error { return errors.New("sensitive helper output") }}
	if _, err := runner.Start(context.Background(), "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil || err.Error() == "sensitive helper output" {
		t.Fatalf("unsafe helper error: %v", err)
	}
}
