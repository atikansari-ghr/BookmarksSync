// Package bookmarks holds the bookmark domain: SQL repositories and transactional
// operations over folders, bookmarks, placements, shares and scope locks. It is
// protocol-agnostic; the floccus adapter and later /api/v1 and UI reuse it.
//
// Every operation takes a Scope. A Scope is either the owner's whole private
// collection or one shared folder subtree, read-only or editable. Ids outside
// the scope behave exactly like ids that do not exist.
package bookmarks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// RootID is the scope-relative id of the root. In a private scope it is the
// virtual root holding the canonical folders; in a share scope it aliases the
// shared folder.
const RootID int64 = -1

// Bounds that keep memory and recursion finite.
const (
	MaxTitleBytes  = 4096
	MaxURLBytes    = 8192
	MaxJSURLBytes  = 32768
	MaxTags        = 100
	MaxTagBytes    = 200
	MaxFolderDepth = 256
	MaxPageLimit   = 1000
	MaxListAll     = 50000
	MaxImportNodes = 2000
	MaxImportDepth = 32
	MaxChildLayers = 1000
	defaultLockTTL = 5 * time.Minute
	maxListPageIdx = 1_000_000
	canonicalBar   = "Bookmarks Bar"
	canonicalOther = "Other Bookmarks"
	kindFolder     = "folder"
	kindBar        = "bar"
	kindOther      = "other"
	typeFolder     = "folder"
	typeBookmark   = "bookmark"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrInvalid   = errors.New("invalid")
	ErrConflict  = errors.New("conflict")
	ErrLocked    = errors.New("locked")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Scope is the authority of one authenticated connection.
type Scope struct {
	OwnerID      int64 // owner of the collection the data lives in
	RootFolderID int64 // 0 = whole private collection; else the shared folder
	Writable     bool
	Holder       int64 // credential id used as lock owner; 0 when none
}

// out presents a real folder id the way this scope addresses it.
func (sc Scope) out(id int64) int64 {
	if sc.RootFolderID != 0 && id == sc.RootFolderID {
		return RootID
	}
	return id
}

// Service is the bookmark domain service.
type Service struct {
	db      *sql.DB
	now     func() time.Time
	lockTTL time.Duration
}

type Option func(*Service)

func WithClock(f func() time.Time) Option { return func(s *Service) { s.now = f } }

func WithLockTTL(d time.Duration) Option { return func(s *Service) { s.lockTTL = d } }

func New(db *sql.DB, opts ...Option) *Service {
	s := &Service{db: db, now: time.Now, lockTTL: defaultLockTTL}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Service) ms() int64 { return s.now().UnixMilli() }

// queryer is satisfied by *sql.DB, *sql.Tx and *sql.Conn.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// read runs fn against one consistent snapshot (a deferred read transaction on a
// single connection) so multi-query reads never see half of a concurrent write.
func (s *Service) read(ctx context.Context, fn func(q queryer) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	return fn(conn)
}

// write runs fn in one transaction (BEGIN IMMEDIATE via the database DSN). It
// refuses read-only scopes and conflicting locks held by other credentials, and
// commits only if fn returns nil, so a failure never leaves a partial write.
func (s *Service) write(ctx context.Context, sc Scope, fn func(tx *sql.Tx) error) error {
	if !sc.Writable {
		return fmt.Errorf("%w: read-only scope", ErrForbidden)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.checkLock(ctx, tx, sc); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsureCollection creates the canonical Bookmarks Bar / Other Bookmarks roots for
// a user. It is idempotent.
func (s *Service) EnsureCollection(ctx context.Context, ownerID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.ms()
	for i, c := range []struct{ kind, title string }{{kindBar, canonicalBar}, {kindOther, canonicalOther}} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO folders(owner_id, parent_id, kind, title, position, created_at, updated_at)
			SELECT ?1, NULL, ?2, ?3, ?4, ?5, ?5
			WHERE NOT EXISTS (SELECT 1 FROM folders WHERE owner_id = ?1 AND kind = ?2)`,
			ownerID, c.kind, c.title, i, now); err != nil {
			return fmt.Errorf("ensure collection: %w", err)
		}
	}
	return tx.Commit()
}

// ResolveScope turns an authenticated principal into a Scope using live state:
// a share credential needs a current grant to this very user on a live folder.
// Granting, downgrading or revoking therefore applies on the next request.
func (s *Service) ResolveScope(ctx context.Context, p identity.Principal) (Scope, error) {
	switch p.Kind {
	case identity.KindPrivate:
		if err := s.EnsureCollection(ctx, p.User.ID); err != nil {
			return Scope{}, err
		}
		return Scope{OwnerID: p.User.ID, Writable: p.Mode == identity.ModeEdit, Holder: p.CredentialID}, nil
	case identity.KindShare:
		var owner, folder, recipient int64
		var mode string
		var folderDeleted sql.NullInt64
		var active int
		err := s.db.QueryRowContext(ctx, `
			SELECT sh.owner_id, sh.folder_id, sh.recipient_id, sh.mode, f.deleted_at, u.active
			FROM shares sh
			JOIN folders f ON f.id = sh.folder_id
			JOIN users u ON u.id = sh.recipient_id
			WHERE sh.id = ?`, p.ShareID).Scan(&owner, &folder, &recipient, &mode, &folderDeleted, &active)
		if errors.Is(err, sql.ErrNoRows) {
			return Scope{}, identity.ErrUnauthorized
		}
		if err != nil {
			return Scope{}, err
		}
		if recipient != p.User.ID || folderDeleted.Valid || active != 1 {
			return Scope{}, identity.ErrUnauthorized
		}
		return Scope{
			OwnerID: owner, RootFolderID: folder, Holder: p.CredentialID,
			Writable: mode == string(identity.ModeEdit) && p.Mode == identity.ModeEdit,
		}, nil
	}
	return Scope{}, identity.ErrUnauthorized
}

func validTitle(kind, s string) error {
	if len(s) > MaxTitleBytes {
		return invalid("%s too long (max %d bytes)", kind, MaxTitleBytes)
	}
	if !utf8.ValidString(s) {
		return invalid("%s is not valid UTF-8", kind)
	}
	return nil
}
