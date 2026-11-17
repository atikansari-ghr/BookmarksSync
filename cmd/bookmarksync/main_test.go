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
		code := run(context.Background(), []string{arg}, strings.NewReader(""), &out, &errb, func(string) string { return "" })
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
			if code := run(context.Background(), args, strings.NewReader(""), &out, &errb, func(string) string { return "" }); code == 0 {
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
		done <- run(ctx, []string{"serve", "--listen", addr, "--data-dir", dir}, strings.NewReader(""), io.Discard, &errb, func(string) string { return "" })
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

// startServe runs serve with env and returns its base URL and a stop function.
func startServe(t *testing.T, env map[string]string) (string, func()) {
	t.Helper()
	addr := freePort(t)
	dir := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var errb bytes.Buffer
	go func() {
		done <- run(ctx, []string{"serve", "--listen", addr, "--data-dir", dir}, strings.NewReader(""), io.Discard, &errb,
			func(k string) string { return env[k] })
	}()
	base := "http://" + addr
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base, func() { cancel(); <-done }
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	t.Fatalf("server never became ready; stderr: %s", errb.String())
	return "", nil
}

func postLogin(t *testing.T, base, origin string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/v1/auth/login", strings.NewReader(`{"username":"nobody","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestServeWiresAuthAPIWhenPublicURLIsSet(t *testing.T) {
	origin := "http://127.0.0.1:1"
	base, stop := startServe(t, map[string]string{"BOOKMARKSYNC_PUBLIC_URL": origin})
	defer stop()
	if code := postLogin(t, base, origin); code != http.StatusUnauthorized {
		t.Fatalf("login with configured origin: %d, want 401", code)
	}
	if code := postLogin(t, base, base); code != http.StatusForbidden {
		t.Fatalf("login with Host-derived origin: %d, want 403", code)
	}
}

func TestServeLeavesAuthAPIOffWithoutPublicURL(t *testing.T) {
	base, stop := startServe(t, nil)
	defer stop()
	if code := postLogin(t, base, base); code != http.StatusNotFound {
		t.Fatalf("login without public URL: %d, want 404", code)
	}
}

func TestServeRejectsInvalidPublicURL(t *testing.T) {
	var errb bytes.Buffer
	code := run(context.Background(), []string{"serve", "--listen", freePort(t), "--data-dir", t.TempDir(), "--public-url", "http://example.com"},
		strings.NewReader(""), io.Discard, &errb, func(string) string { return "" })
	if code == 0 || !strings.Contains(errb.String(), "public") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}
