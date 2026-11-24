//go:build windows

package updates

import (
	"context"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
	"github.com/atikansari-ghr/BookmarksSync/internal/updateipc"
)

type windowsGeoLiteRunner struct{}

func InstalledGeoLiteRunner() GeoLiteRunner { return windowsGeoLiteRunner{} }

func (windowsGeoLiteRunner) Status(ctx context.Context) (geolite.Status, error) {
	response, err := sendGeoLite(ctx, updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationStatus})
	if err != nil {
		return geolite.Status{}, err
	}
	if response.Status == nil {
		return geolite.Status{}, ErrGeoLiteUnavailable
	}
	return *response.Status, nil
}
func (windowsGeoLiteRunner) Configure(ctx context.Context, c geolite.Config) error {
	_, err := sendGeoLite(ctx, updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationConfigure, Config: &c})
	return err
}
func (windowsGeoLiteRunner) Clear(ctx context.Context) error {
	_, err := sendGeoLite(ctx, updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationClear})
	return err
}
func (windowsGeoLiteRunner) Run(ctx context.Context) (string, error) {
	response, err := sendGeoLite(ctx, updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationRun})
	return response.Job, err
}

func sendGeoLite(ctx context.Context, request updateipc.GeoLiteRequest) (updateipc.GeoLiteResponse, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	pipe, err := updateipc.DialPipe(ctx)
	if err != nil {
		return updateipc.GeoLiteResponse{}, ErrGeoLiteUnavailable
	}
	defer pipe.Close()
	if err := updateipc.EncodeGeoLiteRequest(pipe, request); err != nil {
		return updateipc.GeoLiteResponse{}, ErrGeoLiteInvalid
	}
	response, err := updateipc.DecodeGeoLiteResponse(pipe)
	if err != nil {
		return updateipc.GeoLiteResponse{}, ErrGeoLiteUnavailable
	}
	if response.OK {
		return response, nil
	}
	switch response.ErrorCode {
	case "busy":
		return response, ErrGeoLiteBusy
	case "invalid_request":
		return response, ErrGeoLiteInvalid
	default:
		return response, ErrGeoLiteUnavailable
	}
}

var _ GeoLiteRunner = windowsGeoLiteRunner{}
