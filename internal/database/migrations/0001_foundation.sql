-- Foundation schema: only generic instance metadata. Bookmark, account and
-- sharing tables are added by later migrations once their protocols are fixed.
CREATE TABLE app_metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;
