package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/oidcfake"
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
func startServe(t *testing.T, env map[string]string) (string, func() string) {
	t.Helper()
	return startServeIn(t, filepath.Join(t.TempDir(), "data"), env)
}

// startServeIn is startServe with an explicit data directory.
func startServeIn(t *testing.T, dir string, env map[string]string) (string, func() string) {
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
				return base, func() string {
					once.Do(func() { cancel(); <-done })
					return errb.String()
				}
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

func TestServeWiresAdminAndBookmarkAPI(t *testing.T) {
	admin := newCLI(t)
	admin.mustRun(pw+"\n", "user", "create", "--username", "api-owner", "--password-stdin", "--admin")
	origin := "http://127.0.0.1:1"
	base, stop := startServeIn(t, admin.dir, map[string]string{"BOOKMARKSYNC_PUBLIC_URL": origin})
	defer stop()
	loginBody, _ := json.Marshal(map[string]string{"username": "api-owner", "password": pw})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("login status %d: %s", resp.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, candidate := range resp.Cookies() {
		if candidate.Name == "__Host-bookmarksync_session" {
			cookie = candidate
			break
		}
	}
	resp.Body.Close()
	if cookie == nil {
		t.Fatal("login did not set the session cookie")
	}
	for _, path := range []string{"/api/v1/me", "/api/v1/admin/system", "/api/v1/bookmarks"} {
		apiReq, _ := http.NewRequest(http.MethodGet, base+path, nil)
		apiReq.AddCookie(cookie)
		apiResp, err := http.DefaultClient.Do(apiReq)
		if err != nil {
			t.Fatal(err)
		}
		if apiResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(apiResp.Body)
			apiResp.Body.Close()
			t.Errorf("GET %s: status %d: %s", path, apiResp.StatusCode, body)
			continue
		}
		apiResp.Body.Close()
	}
	if logs := stop(); strings.Contains(logs, pw) {
		t.Fatal("server logs contain the submitted password")
	}
}

func TestServeLeavesAuthAPIOffWithoutPublicURL(t *testing.T) {
	base, stop := startServe(t, nil)
	defer stop()
	if code := postLogin(t, base, base); code != http.StatusNotFound {
		t.Fatalf("login without public URL: %d, want 404", code)
	}
}

func TestServePurgesExpiredTrashWithoutPublicURL(t *testing.T) {
	admin := newCLI(t)
	admin.mustRun(pw+"\n", "user", "create", "--username", "trash-owner", "--password-stdin", "--admin")
	db := admin.db()
	defer db.Close()
	ids := identity.New(db.SQL())
	owner, err := ids.GetUser(context.Background(), "trash-owner")
	if err != nil {
		t.Fatal(err)
	}
	bm := bookmarks.New(db.SQL())
	sc, err := bm.PrivateScope(context.Background(), owner.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := bm.FolderTree(context.Background(), sc, bookmarks.RootID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var other int64
	for _, root := range roots {
		if root.Type == "folder" && root.Title == "Other Bookmarks" {
			other = root.ID
		}
	}
	created, err := bm.CreateBookmark(context.Background(), sc, bookmarks.BookmarkInput{URL: "https://expired.example", Title: "expired", Folders: []int64{other}})
	if err != nil {
		t.Fatal(err)
	}
	if err := bm.DeleteBookmark(context.Background(), sc, created.ID); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-bookmarks.TrashRetention - time.Hour).UnixMilli()
	if _, err := db.SQL().Exec(`UPDATE bookmarks SET deleted_at = ?, updated_at = ? WHERE id = ?`, old, old, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE placements SET deleted_at = ? WHERE bookmark_id = ?`, old, created.ID); err != nil {
		t.Fatal(err)
	}

	_, stop := startServeIn(t, admin.dir, nil)
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var found int
		err := db.SQL().QueryRow(`SELECT 1 FROM bookmarks WHERE id = ?`, created.ID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expired Trash item was not purged on a server without browser sign-in")
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

func TestServeWiresOIDCProvidersFromTheEnvironment(t *testing.T) {
	fake := oidcfake.New("client", "wiring-secret-value")
	defer fake.Close()
	origin := "http://localhost:1"
	env := map[string]string{
		"BOOKMARKSYNC_PUBLIC_URL":                   origin,
		"BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER":        fake.Issuer,
		"BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_ID":     "client",
		"BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_SECRET": "wiring-secret-value",
	}
	base, stop := startServe(t, env)
	defer stop()
	resp, err := http.Get(base + "/api/v1/auth/oidc/providers")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"id":"authentik"`) || strings.Contains(string(body), "wiring-secret-value") {
		t.Fatalf("providers: %d %s", resp.StatusCode, body)
	}
	if n := fake.Discovered(); n != 0 {
		t.Fatalf("startup contacted the provider (%d discoveries)", n)
	}
	if code := request(t, "POST", base+"/api/v1/auth/oidc/authentik/login", origin); code != http.StatusOK {
		t.Fatalf("oidc login begin: %d", code)
	}
}

func TestServeWithoutOIDCConfigurationHasNoOIDCRoutes(t *testing.T) {
	base, stop := startServe(t, map[string]string{"BOOKMARKSYNC_PUBLIC_URL": "http://localhost:1"})
	defer stop()
	if code := request(t, "GET", base+"/api/v1/auth/oidc/providers", ""); code != http.StatusNotFound {
		t.Fatalf("providers without OIDC: %d", code)
	}
}

func TestServeRejectsPartialOIDCConfigurationWithoutLeakingSecrets(t *testing.T) {
	var errb bytes.Buffer
	env := map[string]string{
		"BOOKMARKSYNC_PUBLIC_URL":                "http://localhost:1",
		"BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET": "partial-secret-value",
	}
	code := run(context.Background(), []string{"serve", "--listen", freePort(t), "--data-dir", t.TempDir()},
		strings.NewReader(""), io.Discard, &errb, func(k string) string { return env[k] })
	if code == 0 || !strings.Contains(errb.String(), "BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_ID") || strings.Contains(errb.String(), "partial-secret-value") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestServeServesTheSignInUIOnlyWithAPublicURL(t *testing.T) {
	base, stop := startServe(t, map[string]string{"BOOKMARKSYNC_PUBLIC_URL": "http://localhost:1"})
	defer stop()
	resp, err := http.Get(base + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `<div id="root">`) || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("sign-in page: %d %v", resp.StatusCode, resp.Header)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r2, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusFound || r2.Header.Get("Location") != "/auth/login" {
		t.Fatalf("root: %d %q", r2.StatusCode, r2.Header.Get("Location"))
	}

	plain, stopPlain := startServe(t, nil)
	defer stopPlain()
	if code := request(t, "GET", plain+"/auth/login", ""); code != http.StatusNotFound {
		t.Fatalf("UI without a public URL: %d, want 404", code)
	}
}
