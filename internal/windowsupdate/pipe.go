package windowsupdate

import (
	"context"
	"io"

	"github.com/atikansari-ghr/BookmarksSync/internal/updateipc"
)

func HandlePipeRequest(ctx context.Context, conn io.ReadWriteCloser, manager *JobManager) {
	defer conn.Close()
	request, err := updateipc.DecodeRequest(conn)
	if err != nil || ctx.Err() != nil || manager == nil {
		_ = updateipc.EncodeResponse(conn, updateipc.Response{})
		return
	}
	job, err := manager.Submit(request.Tag)
	if err != nil {
		_ = updateipc.EncodeResponse(conn, updateipc.Response{})
		return
	}
	_ = updateipc.EncodeResponse(conn, updateipc.Response{Accepted: true, Job: job})
}
