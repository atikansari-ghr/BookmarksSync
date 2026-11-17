package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
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
	return startServeIn(t, filepath.Join(t.TempDir(), "data"), env)
}

// startServeIn is startServe with an explicit data directory.
func startServeIn(t *testing.T, dir string, env map[string]string) (string, func()) {
	t.Helper()
	addr := freePort(t)
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
				var once sync.Once
				return base, func() { once.Do(func() { cancel(); <-done }) }
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

func request(t *testing.T, method, url, origin string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestServeWiresFactorsAndCreatesThePrivateInstanceKey(t *testing.T) {
	origin := "http://localhost:1"
	dir := filepath.Join(t.TempDir(), "data")
	base, stop := startServeIn(t, dir, map[string]string{"BOOKMARKSYNC_PUBLIC_URL": origin})
	defer stop()
	key, err := os.ReadFile(filepath.Join(dir, identity.InstanceKeyFile))
	if err != nil || len(key) != identity.InstanceKeySize {
		t.Fatalf("instance key: %d bytes, %v", len(key), err)
	}
	if code := request(t, "POST", base+"/api/v1/auth/passkey/login/begin", origin); code != http.StatusOK {
		t.Fatalf("passkey login begin: %d", code)
	}
	if code := request(t, "GET", base+"/api/v1/auth/totp", ""); code != http.StatusUnauthorized {
		t.Fatalf("totp status without session: %d, want 401", code)
	}
	stop()
	// A restart reuses the same key.
	_, stop2 := startServeIn(t, dir, map[string]string{"BOOKMARKSYNC_PUBLIC_URL": origin})
	defer stop2()
	again, _ := os.ReadFile(filepath.Join(dir, identity.InstanceKeyFile))
	if !bytes.Equal(key, again) {
		t.Fatal("restart replaced the instance key")
	}
}

func TestServeWithIPOriginKeepsTOTPButDisablesPasskeys(t *testing.T) {
	origin := "http://127.0.0.1:1"
	base, stop := startServe(t, map[string]string{"BOOKMARKSYNC_PUBLIC_URL": origin})
	defer stop()
	if code := request(t, "POST", base+"/api/v1/auth/passkey/login/begin", origin); code != http.StatusNotFound {
		t.Fatalf("passkeys with an IP origin: %d, want 404", code)
	}
	if code := request(t, "GET", base+"/api/v1/auth/totp", ""); code != http.StatusUnauthorized {
		t.Fatalf("totp status: %d, want 401", code)
	}
}

func TestServeFailsClosedOnAMalformedInstanceKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, identity.InstanceKeyFile)
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	code := run(context.Background(), []string{"serve", "--listen", freePort(t), "--data-dir", dir, "--public-url", "http://localhost:1"},
		strings.NewReader(""), io.Discard, &errb, func(string) string { return "" })
	if code == 0 || !strings.Contains(errb.String(), "instance key") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
	if got, _ := os.ReadFile(path); string(got) != "short" {
		t.Fatal("malformed key was replaced")
	}
}

func TestServeWithoutPublicURLCreatesNoInstanceKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	_, stop := startServeIn(t, dir, nil)
	stop()
	if _, err := os.Stat(filepath.Join(dir, identity.InstanceKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("instance key created with browser sign-in disabled: %v", err)
	}
}
