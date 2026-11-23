package windowsupdate

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/updateipc"
)

func TestHandlePipeRequestAcknowledgesOnlyAcceptedTags(t *testing.T) {
	manager := NewJobManager(context.Background(), func(context.Context, string) error { return nil })
	defer manager.Close()
	var request bytes.Buffer
	if err := updateipc.EncodeRequest(&request, validTag); err != nil {
		t.Fatal(err)
	}
	responsePipe := &duplexBuffer{in: bytes.NewReader(request.Bytes())}
	HandlePipeRequest(context.Background(), responsePipe, manager)
	response, err := updateipc.DecodeResponse(bytes.NewReader(responsePipe.out.Bytes()))
	if err != nil || !response.Accepted || response.Job == "" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	var invalid bytes.Buffer
	_ = updateipc.EncodeRequest(&invalid, "latest")
	invalidPipe := &duplexBuffer{in: bytes.NewReader(invalid.Bytes())}
	HandlePipeRequest(context.Background(), invalidPipe, manager)
	response, err = updateipc.DecodeResponse(bytes.NewReader(invalidPipe.out.Bytes()))
	if err != nil || response.Accepted || response.Job != "" {
		t.Fatalf("invalid response=%#v err=%v", response, err)
	}
}

type duplexBuffer struct {
	in  *bytes.Reader
	out bytes.Buffer
}

func (d *duplexBuffer) Read(p []byte) (int, error)  { return d.in.Read(p) }
func (d *duplexBuffer) Write(p []byte) (int, error) { return d.out.Write(p) }
func (d *duplexBuffer) Close() error                { return nil }

var _ io.ReadWriteCloser = (*duplexBuffer)(nil)
