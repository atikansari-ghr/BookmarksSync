package updates

import (
	"context"
	"errors"
)

var ErrRunnerUnavailable = errors.New("host update runner is unavailable")

type UpdateRunner interface {
	Start(context.Context, string) (string, error)
}
