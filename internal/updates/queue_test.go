package updates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerQueueRunnerWritesOnlyExactBuildRequests(t *testing.T) {
	dir := t.TempDir()
	runner := &DockerQueueRunner{Directory: dir}
	tag := "build-" + strings.Repeat("a", 40)
	job, err := runner.Start(context.Background(), tag)
	if err != nil || job != "bookmarksync-docker-update-"+strings.Repeat("a", 40) {
		t.Fatalf("queue: %q %v", job, err)
	}
	info, err := os.Lstat(filepath.Join(dir, tag))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("unsafe queue entry: %v %v", info, err)
	}
	if _, err := runner.Start(context.Background(), tag); !errors.Is(err, ErrRunnerUnavailable) {
		t.Fatalf("duplicate request accepted: %v", err)
	}
	for _, invalid := range []string{"latest", "../build-" + strings.Repeat("b", 40), "build-" + strings.Repeat("A", 40), tag + ";id"} {
		if _, err := runner.Start(context.Background(), invalid); err == nil {
			t.Fatalf("invalid request %q accepted", invalid)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("invalid request created an entry")
	}
}

func TestDockerQueueRunnerBoundsQueueAndRejectsUnavailablePaths(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 64; i++ {
		file, err := os.CreateTemp(dir, "entry-")
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}
	runner := &DockerQueueRunner{Directory: dir}
	if _, err := runner.Start(context.Background(), "build-"+strings.Repeat("b", 40)); !errors.Is(err, ErrRunnerUnavailable) {
		t.Fatalf("full queue: %v", err)
	}
	for _, directory := range []string{"relative", filepath.Join(t.TempDir(), "missing")} {
		if _, err := (&DockerQueueRunner{Directory: directory}).Start(context.Background(), "build-"+strings.Repeat("b", 40)); !errors.Is(err, ErrRunnerUnavailable) {
			t.Fatalf("unavailable queue: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&DockerQueueRunner{Directory: t.TempDir()}).Start(ctx, "build-"+strings.Repeat("b", 40)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request queued: %v", err)
	}
}
