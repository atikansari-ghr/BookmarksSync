//go:build windows

package windowsupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWindowsHostWaitReadyRequiresLocalReadyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ops := &WindowsHostOperations{}
	if err := ops.WaitReady(context.Background(), server.URL+"/readyz"); err != nil {
		t.Fatal(err)
	}
	if err := ops.WaitReady(context.Background(), strings.Replace(server.URL, "127.0.0.1", "localhost", 1)+"/readyz"); err == nil {
		t.Fatal("non-literal loopback host accepted")
	}
}

func TestWindowsHostWaitReadyHonorsContextOnUnhealthyService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := (&WindowsHostOperations{}).WaitReady(ctx, server.URL+"/readyz"); err == nil {
		t.Fatal("unhealthy service reported ready")
	}
}
