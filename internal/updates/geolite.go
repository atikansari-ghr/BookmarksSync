package updates

import (
	"context"
	"errors"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
)

var (
	ErrGeoLiteUnavailable = errors.New("GeoLite updater unavailable")
	ErrGeoLiteBusy        = errors.New("GeoLite updater is busy")
	ErrGeoLiteInvalid     = errors.New("invalid GeoLite configuration")
)

type GeoLiteRunner interface {
	Status(context.Context) (geolite.Status, error)
	Configure(context.Context, geolite.Config) error
	Clear(context.Context) error
	Run(context.Context) (string, error)
}
