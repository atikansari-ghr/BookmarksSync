//go:build windows

package updates

import (
	"context"
	"errors"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updateipc"
)

type windowsPipeRunner struct{}

func InstalledHostRunner(string) UpdateRunner { return windowsPipeRunner{} }

func (windowsPipeRunner) Start(ctx context.Context, tag string) (string, error) {
	if ValidateApprovedTag(tag) != nil {
		return "", errors.New("invalid release tag")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	pipe, err := updateipc.DialPipe(ctx)
	if err != nil {
		return "", ErrRunnerUnavailable
	}
	done := make(chan struct {
		job string
		err error
	}, 1)
	go func() {
		defer close(done)
		defer pipe.Close()
		if err := updateipc.EncodeRequest(pipe, tag); err != nil {
			done <- struct {
				job string
				err error
			}{err: ErrRunnerUnavailable}
			return
		}
		response, err := updateipc.DecodeResponse(pipe)
		if err != nil || !response.Accepted {
			done <- struct {
				job string
				err error
			}{err: ErrRunnerUnavailable}
			return
		}
		want := "bookmarksync-update-" + tag[len("build-"):]
		if response.Job != want {
			done <- struct {
				job string
				err error
			}{err: ErrRunnerUnavailable}
			return
		}
		done <- struct {
			job string
			err error
		}{job: response.Job}
	}()
	select {
	case result := <-done:
		return result.job, result.err
	case <-ctx.Done():
		_ = pipe.Close()
		return "", ErrRunnerUnavailable
	}
}
