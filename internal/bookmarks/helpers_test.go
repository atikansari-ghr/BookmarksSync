package bookmarks_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

var ctx = context.Background()

type env struct {
	t    *testing.T
	db   *database.DB
	path string
	svc  *bookmarks.Service
	ids  *identity.Service

	mu  sync.Mutex
	now time.Time
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	e.path = filepath.Join(t.TempDir(), "bm.db")
	e.open()
	return e
}

func (e *env) open() {
	e.t.Helper()
	db, err := database.Open(ctx, e.path)
	if err != nil {
		e.t.Fatalf("open db: %v", err)
	}
	e.t.Cleanup(func() { db.Close() })
	e.db = db
	e.ids = identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(e.clock))
	e.svc = bookmarks.New(db.SQL(), bookmarks.WithClock(e.clock), bookmarks.WithLockTTL(5*time.Minute))
}

// reopen closes and reopens the same database file (durability check).
func (e *env) reopen() {
	e.t.Helper()
	if err := e.db.Close(); err != nil {
		e.t.Fatal(err)
	}
	e.open()
}

func (e *env) user(name string) identity.User {
	e.t.Helper()
	u, err := e.ids.CreateUser(ctx, name, "a perfectly fine password", false)
	if err != nil {
		e.t.Fatalf("create user %s: %v", name, err)
	}
	if err := e.svc.EnsureCollection(ctx, u.ID); err != nil {
		e.t.Fatalf("ensure collection: %v", err)
	}
	return u
}

// private returns a scope over u's whole private collection.
func (e *env) private(u identity.User, mode identity.Mode) bookmarks.Scope {
	e.t.Helper()
	c, _, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: u.ID, Label: "t", Mode: mode})
	if err != nil {
		e.t.Fatal(err)
	}
	return e.scopeOf(u, c)
}

func (e *env) scopeOf(u identity.User, c identity.Credential) bookmarks.Scope {
	e.t.Helper()
	sc, err := e.svc.ResolveScope(ctx, identity.Principal{CredentialID: c.ID, User: u, Kind: c.Kind, ShareID: c.ShareID, Mode: c.Mode})
	if err != nil {
		e.t.Fatalf("ResolveScope: %v", err)
	}
	return sc
}

// shared grants folder (owned by owner) to recipient and returns the recipient's
// share-bound scope plus the credential.
func (e *env) shared(owner identity.User, folderID int64, recip identity.User, grant, credMode identity.Mode) (bookmarks.Scope, identity.Credential, bookmarks.Share) {
	e.t.Helper()
	sh, err := e.svc.GrantShare(ctx, owner.ID, folderID, recip.ID, grant)
	if err != nil {
		e.t.Fatalf("GrantShare: %v", err)
	}
	c, _, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: recip.ID, Kind: identity.KindShare, ShareID: sh.ID, Mode: credMode})
	if err != nil {
		e.t.Fatal(err)
	}
	return e.scopeOf(recip, c), c, sh
}

func (e *env) roots(sc bookmarks.Scope) (bar, other int64) {
	e.t.Helper()
	nodes, err := e.svc.Children(ctx, sc, bookmarks.RootID, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	if len(nodes) < 2 {
		e.t.Fatalf("root has %d children", len(nodes))
	}
	return nodes[0].ID, nodes[1].ID
}

func (e *env) folder(sc bookmarks.Scope, parent int64, title string) int64 {
	e.t.Helper()
	f, err := e.svc.CreateFolder(ctx, sc, parent, title)
	if err != nil {
		e.t.Fatalf("CreateFolder(%q): %v", title, err)
	}
	return f.ID
}

func (e *env) bm(sc bookmarks.Scope, url, title string, folders ...int64) bookmarks.Bookmark {
	e.t.Helper()
	b, err := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: url, Title: title, Folders: folders})
	if err != nil {
		e.t.Fatalf("CreateBookmark(%s): %v", url, err)
	}
	return b
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// snapshot is a deep, comparable dump of everything visible in a scope, used to
// prove failed operations wrote nothing.
func (e *env) snapshot(sc bookmarks.Scope) string {
	e.t.Helper()
	nodes, err := e.svc.Children(ctx, sc, bookmarks.RootID, -1)
	if err != nil {
		e.t.Fatal(err)
	}
	var out string
	var walk func(ns []bookmarks.Node, depth int)
	walk = func(ns []bookmarks.Node, depth int) {
		for _, n := range ns {
			out += string(rune('0'+depth)) + "|" + n.Type + "|" + itoa(n.ID) + "|" + n.Title + "|" + n.URL + "\n"
			walk(n.Children, depth+1)
		}
	}
	walk(nodes, 0)
	list, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Page: -1})
	if err != nil {
		e.t.Fatal(err)
	}
	for _, b := range list {
		out += "B" + itoa(b.ID) + b.URL + b.Title
		for _, f := range b.Folders {
			out += ",f" + itoa(f)
		}
		for _, tg := range b.Tags {
			out += ",t" + tg
		}
		out += "\n"
	}
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
