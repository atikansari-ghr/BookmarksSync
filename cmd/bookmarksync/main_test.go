package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionFlagPrintsBuildInfo(t *testing.T) {
	for _, arg := range []string{"--version", "version"} {
		var out, errb bytes.Buffer
		code := run(context.Background(), []string{arg}, &out, &errb, func(string) string { return "" })
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %q", arg, code, errb.String())
		}
		if !strings.HasPrefix(out.String(), "bookmarksync ") {
			t.Errorf("%s: output = %q", arg, out.String())
		}
	}
}

func TestUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":      {},
		"unknown command": {"frobnicate"},
		"bad listen":      {"serve", "--listen", "nonsense"},
		"bad flag":        {"serve", "--nope"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(context.Background(), args, &out, &errb, func(string) string { return "" }); code == 0 {
				t.Fatalf("exit 0 for %v", args)
			}
			if errb.Len() == 0 {
				t.Error("expected a message on stderr")
			}
		})
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestServeStartsAnsweringAndStopsOnCancel(t *testing.T) {
	addr := freePort(t)
	dir := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var errb bytes.Buffer
	go func() {
		done <- run(ctx, []string{"serve", "--listen", addr, "--data-dir", dir}, io.Discard, &errb, func(string) string { return "" })
	}()

	url := fmt.Sprintf("http://%s/readyz", addr)
	var ok bool
	for i := 0; i < 100 && !ok; i++ {
		if resp, err := http.Get(url); err == nil {
			ok = resp.StatusCode == http.StatusOK
			resp.Body.Close()
		}
		if !ok {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !ok {
		cancel()
		t.Fatalf("server never became ready; stderr: %s", errb.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code %d after cancel, stderr %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not exit after cancel")
	}
}
