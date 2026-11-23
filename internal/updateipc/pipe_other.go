//go:build !windows

package updateipc

import (
	"context"
	"errors"
	"io"
)

type PipeHandler func(context.Context, io.ReadWriteCloser)

func ListenAndServe(context.Context, string, PipeHandler) error {
	return errors.New("Windows updater pipe is unavailable on this platform")
}

func DialPipe(context.Context) (io.ReadWriteCloser, error) {
	return nil, errors.New("Windows updater pipe is unavailable on this platform")
}
