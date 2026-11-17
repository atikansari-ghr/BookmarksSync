package identity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
)

func mustOpenDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(dir, "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.SQL()
}

func hasParams(encoded string, memKiB, iter uint32) bool {
	return strings.Contains(encoded, fmt.Sprintf("m=%d,t=%d,", memKiB, iter))
}
