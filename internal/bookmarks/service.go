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
	// Search bounds keep the LIKE-per-term query cheap (checked before any SQL runs).
	MaxSearchTerms     = 10
	MaxSearchTermBytes = 256
	defaultLockTTL     = 5 * time.Minute
	maxListPageIdx     = 1_000_000
	canonicalBar       = "Bookmarks Bar"
	canonicalOther     = "Other Bookmarks"
	kindFolder         = "folder"
	kindBar            = "bar"
	kindOther          = "other"
	typeFolder         = "folder"
	typeBookmark       = "bookmark"
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
	// Actor is the /api/v1 user behind a scope without a credential (Holder 0). Writes
	// re-derive the actor's activity and, for a share scope, the live grant inside the
	// write transaction, so a suspension, revoke or downgrade cannot be outrun.
	Actor int64
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
	// The scope was resolved earlier in the request (possibly before a slow body was
	// read). Re-derive it from committed state inside this transaction, at its start and
	// again immediately before commit, so a revoke, deactivation, downgrade or trashed
	// share can never be outrun by a stale Scope. The write lock is held throughout, so
	// no such change can land between the last check and the commit.
	if err := s.revalidate(ctx, tx, sc, true); err != nil {
		return err
	}
	if err := s.checkLock(ctx, tx, sc); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := s.revalidate(ctx, tx, sc, true); err != nil {
		return err
	}
	return tx.Commit()
}

// revalidate re-derives the live authority of sc.Holder and refuses the operation if the
// credential is revoked, its user inactive, its share gone or trashed (identity.ErrUnauthorized),
// or, when write is true, if the live grant/credential no longer allows writing (ErrForbidden).
// Scopes without a Holder (internal callers such as the CLI) have no credential to check.
func (s *Service) revalidate(ctx context.Context, q queryer, sc Scope, write bool) error {
	if sc.Holder == 0 {
		if sc.Actor != 0 {
			return s.revalidateActor(ctx, q, sc, write)
		}
		return nil
	}
	live, _, err := s.liveScope(ctx, q, sc.Holder)
	if err != nil {
		return err
	}
	if live.OwnerID != sc.OwnerID || live.RootFolderID != sc.RootFolderID {
		return identity.ErrUnauthorized
	}
	if write && !live.Writable {
		return fmt.Errorf("%w: read-only scope", ErrForbidden)
	}
	return nil
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
	sc, userID, err := s.liveScope(ctx, s.db, p.CredentialID)
	if err != nil {
		return Scope{}, err
	}
	if userID != p.User.ID {
		return Scope{}, identity.ErrUnauthorized // a credential cannot be replayed by another user
	}
	if sc.RootFolderID == 0 {
		if err := s.EnsureCollection(ctx, sc.OwnerID); err != nil {
			return Scope{}, err
		}
	}
	return sc, nil
}

// liveScope derives the scope a credential has right now from committed state: the
// credential must be unrevoked and its user active; a share credential also needs its
// grant to exist for this very user on a live folder. The effective right is the lower of
// grant and credential. It also returns the credential's user id.
func (s *Service) liveScope(ctx context.Context, q queryer, credID int64) (Scope, int64, error) {
	var userID int64
	var kind, credMode string
	var shareID, revoked sql.NullInt64
	var active int
	err := q.QueryRowContext(ctx, `
		SELECT c.user_id, c.kind, c.share_id, c.mode, c.revoked_at, u.active
		FROM credentials c JOIN users u ON u.id = c.user_id WHERE c.id = ?`, credID).
		Scan(&userID, &kind, &shareID, &credMode, &revoked, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return Scope{}, 0, identity.ErrUnauthorized
	}
	if err != nil {
		return Scope{}, 0, err
	}
	if revoked.Valid || active != 1 {
		return Scope{}, 0, identity.ErrUnauthorized
	}
	switch identity.Kind(kind) {
	case identity.KindPrivate:
		return Scope{OwnerID: userID, Writable: credMode == string(identity.ModeEdit), Holder: credID}, userID, nil
	case identity.KindShare:
		var owner, folder, recipient int64
		var grant string
		var folderDeleted sql.NullInt64
		err := q.QueryRowContext(ctx, `
			SELECT sh.owner_id, sh.folder_id, sh.recipient_id, sh.mode, f.deleted_at
			FROM shares sh JOIN folders f ON f.id = sh.folder_id WHERE sh.id = ?`, shareID.Int64).
			Scan(&owner, &folder, &recipient, &grant, &folderDeleted)
		if errors.Is(err, sql.ErrNoRows) {
			return Scope{}, 0, identity.ErrUnauthorized
		}
		if err != nil {
			return Scope{}, 0, err
		}
		if recipient != userID || folderDeleted.Valid {
			return Scope{}, 0, identity.ErrUnauthorized
		}
		return Scope{
			OwnerID: owner, RootFolderID: folder, Holder: credID,
			Writable: grant == string(identity.ModeEdit) && credMode == string(identity.ModeEdit),
		}, userID, nil
	}
	return Scope{}, 0, identity.ErrUnauthorized
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
