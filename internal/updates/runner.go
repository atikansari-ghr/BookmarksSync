package updates

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"time"
)

var ErrRunnerUnavailable = errors.New("host update runner is unavailable")

type UpdateRunner interface {
	Start(context.Context, string) (string, error)
}

// HostRunner can ask a fixed, root-owned helper to schedule one systemd job.
// It does not invoke a shell and never accepts a command from the request.
type HostRunner struct {
	SudoPath   string
	HelperPath string
	Timeout    time.Duration
	Run        func(context.Context, string, ...string) error
}

func (r HostRunner) Start(ctx context.Context, tag string) (string, error) {
	if ValidateApprovedTag(tag) != nil {
		return "", errors.New("invalid release tag")
	}
	if !filepath.IsAbs(r.SudoPath) || !filepath.IsAbs(r.HelperPath) {
		return "", ErrRunnerUnavailable
	}
	timeout := r.Timeout
	if timeout <= 0 || timeout > 20*time.Second {
		timeout = 10 * time.Second
	}
	run := r.Run
	if run == nil {
		run = func(ctx context.Context, path string, args ...string) error {
			return exec.CommandContext(ctx, path, args...).Run()
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := run(ctx, r.SudoPath, "-n", r.HelperPath, "schedule", "--tag", tag); err != nil {
		return "", ErrRunnerUnavailable
	}
	return "bookmarksync-update-" + tag[len("build-"):], nil
}
