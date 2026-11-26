package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
)

// serveLog is the stderr capture shared by the serve test fixtures. Writes and
// reads are serialized because serve can log from goroutines it does not join.
type serveLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newServeLog() *serveLog { return &serveLog{} }

func (l *serveLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *serveLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// serve does not join purgeTrashLoop, so its first purge can fail on a canceled
// context or closed database and log after run has returned. The capture read by
// stop() must stay synchronized with such a late write (CI run 45603988386).
func TestServeLogCaptureSynchronizesWritesThatOutliveServe(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	bm := bookmarks.New(db.SQL())
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logs := newServeLog()
	log := slog.New(slog.NewTextHandler(logs, nil))
	purged := make(chan struct{})
	go func() {
		defer close(purged)
		purgeExpiredTrash(ctx, bm, log)
	}()
	_ = logs.String() // what stop() does without waiting for the purge goroutine
	<-purged
	if got := logs.String(); !strings.Contains(got, "automatic trash cleanup failed") {
		t.Fatalf("late purge failure was not captured: %q", got)
	}
}
