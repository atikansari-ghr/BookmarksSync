-- Per-user bookmark favorites. Purely additive: no existing table is altered, and
-- migrations 0001-0003 are unchanged. A favorite belongs to one user and one bookmark
-- record, so it follows the record across every folder placement and is private to
-- that user (a share recipient may favorite a bookmark it can view without changing
-- the owner's data). Favorites are API metadata only: they never enter the Floccus
-- wire format or folder hashes. Purging a bookmark removes its favorites; trashing it
-- keeps them so a restore brings them back. Timestamps are unix milliseconds (UTC).
CREATE TABLE bookmark_favorites (
    user_id     INTEGER NOT NULL REFERENCES users(id),
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, bookmark_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX bookmark_favorites_bookmark ON bookmark_favorites(bookmark_id);
