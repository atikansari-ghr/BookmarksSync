-- Accounts, per-device app credentials, bookmark tree, shares and scope locks.
-- All timestamps are unix milliseconds (UTC). Ids are AUTOINCREMENT so an id is
-- never reused, even after rows are purged.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT    NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
    active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at    INTEGER NOT NULL
) STRICT;

-- parent_id NULL means "direct child of the owner's virtual root (-1)".
-- kind 'bar'/'other' are the canonical fixed roots: one each per owner.
CREATE TABLE folders (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id   INTEGER NOT NULL REFERENCES users(id),
    parent_id  INTEGER REFERENCES folders(id),
    kind       TEXT    NOT NULL DEFAULT 'folder' CHECK (kind IN ('bar', 'other', 'folder')),
    title      TEXT    NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    deleted_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (id IS NOT parent_id),
    CHECK (kind = 'folder' OR parent_id IS NULL)
) STRICT;
CREATE UNIQUE INDEX folders_canonical ON folders(owner_id, kind) WHERE kind <> 'folder';
CREATE INDEX folders_parent ON folders(parent_id, deleted_at);
CREATE INDEX folders_owner ON folders(owner_id);

-- Exact-URL dedupe is per *visible* scope (enforced by the service), not globally per
-- owner: a record hidden from a share scope must never be reused by that scope.
CREATE TABLE bookmarks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id   INTEGER NOT NULL REFERENCES users(id),
    url        TEXT    NOT NULL,
    title      TEXT    NOT NULL,
    deleted_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX bookmarks_owner_url ON bookmarks(owner_id, url) WHERE deleted_at IS NULL;

CREATE TABLE bookmark_tags (
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    tag         TEXT    NOT NULL,
    PRIMARY KEY (bookmark_id, tag)
) STRICT;

-- A placement puts a bookmark into a folder. position shares one ordering space
-- with sibling folders, so mixed folder/bookmark order is representable.
CREATE TABLE placements (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    folder_id   INTEGER NOT NULL REFERENCES folders(id),
    position    INTEGER NOT NULL DEFAULT 0,
    deleted_at  INTEGER
) STRICT;
CREATE UNIQUE INDEX placements_live ON placements(bookmark_id, folder_id) WHERE deleted_at IS NULL;
CREATE INDEX placements_folder ON placements(folder_id, deleted_at);

-- Owner grants a folder subtree to an existing user.
CREATE TABLE shares (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id     INTEGER NOT NULL REFERENCES users(id),
    folder_id    INTEGER NOT NULL REFERENCES folders(id),
    recipient_id INTEGER NOT NULL REFERENCES users(id),
    mode         TEXT    NOT NULL CHECK (mode IN ('view', 'edit')),
    created_at   INTEGER NOT NULL,
    UNIQUE (folder_id, recipient_id),
    CHECK (owner_id <> recipient_id)
) STRICT;

-- Per-device app credentials. Only the SHA-256 of the high-entropy secret is stored.
-- A share credential disappears with its grant (ON DELETE CASCADE).
CREATE TABLE credentials (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    label        TEXT    NOT NULL DEFAULT '',
    secret_hash  TEXT    NOT NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('private', 'share')),
    share_id     INTEGER REFERENCES shares(id) ON DELETE CASCADE,
    mode         TEXT    NOT NULL CHECK (mode IN ('view', 'edit')),
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER,
    CHECK ((kind = 'share') = (share_id IS NOT NULL))
) STRICT;
CREATE INDEX credentials_user ON credentials(user_id);

-- Physical-scope locks: root_folder_id 0 = the owner's whole collection.
CREATE TABLE locks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id  INTEGER NOT NULL UNIQUE REFERENCES credentials(id) ON DELETE CASCADE,
    owner_id       INTEGER NOT NULL REFERENCES users(id),
    root_folder_id INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL
) STRICT;
CREATE INDEX locks_owner ON locks(owner_id);
