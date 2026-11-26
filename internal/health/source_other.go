//go:build !windows && !linux

package health

import (
	"context"
	"errors"
)

type platformSource struct{}

func (platformSource) CPUPercent(context.Context) (float64, error) {
	return 0, errors.New("unsupported platform")
}
func (platformSource) Memory(context.Context) (Usage, error) {
	return Usage{}, errors.New("unsupported platform")
}
func (platformSource) Disk(context.Context, string) (Usage, error) {
	return Usage{}, errors.New("unsupported platform")
}
func (platformSource) Uptime(context.Context) (uint64, error) {
	return 0, errors.New("unsupported platform")
}
