package floccus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/floccus"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

const R = "/index.php/apps/bookmarks/public/rest/v2"

var bg = context.Background()

type rig struct {
	t    *testing.T
	db   *database.DB
	ids  *identity.Service
	svc  *bookmarks.Service
	ts   *httptest.Server
	logs *syncBuf

	mu  sync.Mutex
	now time.Time
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func (r *rig) clock() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC), logs: &syncBuf{}}
	db, err := database.Open(bg, filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	r.db = db
	r.ids = identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(r.clock))
	r.svc = bookmarks.New(db.SQL(), bookmarks.WithClock(r.clock), bookmarks.WithLockTTL(5*time.Minute))
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ad := floccus.New(r.ids, r.svc, log)
	h := server.NewHandler(server.Options{Version: version.Current(), Ready: db, Register: ad.Register, Logger: log})
	r.ts = httptest.NewServer(h)
	t.Cleanup(func() { r.ts.Close(); db.Close() })
	return r
}

// account is a user plus one app credential and its plaintext secret.
type account struct {
	user   identity.User
	cred   identity.Credential
	secret string
	scope  bookmarks.Scope
}

func (r *rig) account(name string, mode identity.Mode) account {
	r.t.Helper()
	u, err := r.ids.GetUser(bg, name)
	if err != nil {
		if u, err = r.ids.CreateUser(bg, name, "website password "+name, false); err != nil {
			r.t.Fatal(err)
		}
	}
	return r.credFor(u, identity.NewCredential{UserID: u.ID, Label: "test", Mode: mode})
}

func (r *rig) credFor(u identity.User, nc identity.NewCredential) account {
	r.t.Helper()
	c, secret, err := r.ids.CreateCredential(bg, nc)
	if err != nil {
		r.t.Fatal(err)
	}
	sc, err := r.svc.ResolveScope(bg, identity.Principal{CredentialID: c.ID, User: u, Kind: c.Kind, ShareID: c.ShareID, Mode: c.Mode})
	if err != nil {
		r.t.Fatal(err)
	}
	return account{user: u, cred: c, secret: secret, scope: sc}
}

// shareAccount grants folder to recipient and returns a share-bound credential.
func (r *rig) shareAccount(owner account, folder int64, recipient identity.User, grant, credMode identity.Mode) account {
	r.t.Helper()
	sh, err := r.svc.GrantShare(bg, owner.user.ID, folder, recipient.ID, grant)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.credFor(recipient, identity.NewCredential{UserID: recipient.ID, Kind: identity.KindShare, ShareID: sh.ID, Mode: credMode})
}

type resp struct {
	status int
	header http.Header
	raw    string
	body   map[string]any
}

func (r *rig) client() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (r *rig) do(a *account, method, path string, body any) resp {
	r.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd, ct = strings.NewReader(b), "application/json"
	case []byte:
		rd = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			r.t.Fatal(err)
		}
		rd, ct = bytes.NewReader(buf), "application/json"
	}
	return r.send(a, method, path, rd, ct)
}

func (r *rig) send(a *account, method, path string, rd io.Reader, contentType string) resp {
	r.t.Helper()
	req, err := http.NewRequest(method, r.ts.URL+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if a != nil {
		req.SetBasicAuth(a.user.Username, a.secret)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := r.client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	if strings.Contains(out.raw, `"ticket"`) {
		r.t.Errorf("%s %s emitted a ticket field: %s", method, path, out.raw)
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		r.t.Errorf("%s %s redirected (%d)", method, path, res.StatusCode)
	}
	return out
}

func (rs resp) ok(t *testing.T) resp {
	t.Helper()
	if rs.status != 200 || rs.body["status"] != "success" {
		t.Fatalf("status %d body %s", rs.status, rs.raw)
	}
	return rs
}

func (rs resp) fail(t *testing.T, status int) resp {
	t.Helper()
	if rs.status != status || rs.body["status"] != "error" {
		t.Fatalf("want error %d, got %d body %s", status, rs.status, rs.raw)
	}
	return rs
}

func (rs resp) data() []any {
	l, _ := rs.body["data"].([]any)
	return l
}

func (rs resp) item() map[string]any {
	m, _ := rs.body["item"].(map[string]any)
	return m
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func idStr(id int64) string { return fmt.Sprintf("%d", id) }

// roots returns the canonical Bar and Other ids for the account's scope.
func (r *rig) roots(a account) (bar, other int64) {
	r.t.Helper()
	d := r.do(&a, "GET", R+"/folder/-1/children?layers=1", nil).ok(r.t).data()
	if len(d) < 2 {
		r.t.Fatalf("roots = %v", d)
	}
	return num(d[0].(map[string]any)["id"]), num(d[1].(map[string]any)["id"])
}

func (r *rig) mkFolder(a account, parent int64, title string) int64 {
	r.t.Helper()
	return num(r.do(&a, "POST", R+"/folder", map[string]any{"parent_folder": parent, "title": title}).ok(r.t).item()["id"])
}

func (r *rig) mkBookmark(a account, url, title string, folders ...int64) int64 {
	r.t.Helper()
	return num(r.do(&a, "POST", R+"/bookmark", map[string]any{"url": url, "title": title, "folders": folders}).ok(r.t).item()["id"])
}

func (r *rig) multipart(a *account, path, field, filename, content string) resp {
	r.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	h.Set("Content-Type", "text/html")
	p, _ := w.CreatePart(h)
	p.Write([]byte(content))
	w.Close()
	return r.send(a, "POST", path, &buf, w.FormDataContentType())
}

func bookmarksInput(url, title string, folders []int64, tags []string) bookmarks.BookmarkInput {
	in := bookmarks.BookmarkInput{URL: url, Title: title, Folders: folders}
	if tags != nil {
		in.Tags = &tags
	}
	return in
}
