-- Prevent repeated per-user recovery from duplicating imported folders.
CREATE TABLE user_restore_imports (
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    archive_sha256 TEXT    NOT NULL CHECK (length(archive_sha256) = 64),
    imported_at   INTEGER NOT NULL,
    PRIMARY KEY (user_id, archive_sha256)
) STRICT;
