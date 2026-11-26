-- Verified notification channels and per-user opt-in preferences.
CREATE TABLE notification_channels (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('email', 'telegram')),
    destination TEXT NOT NULL,
    verified_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, kind)
) STRICT;

CREATE TABLE notification_challenges (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('email', 'telegram')),
    destination TEXT NOT NULL,
    salt BLOB NOT NULL CHECK (length(salt) = 16),
    code_hash BLOB NOT NULL CHECK (length(code_hash) = 32),
    expires_at INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, kind)
) STRICT;

CREATE TABLE notification_preferences (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    security_alerts INTEGER NOT NULL DEFAULT 0 CHECK (security_alerts IN (0,1)),
    sharing_alerts INTEGER NOT NULL DEFAULT 0 CHECK (sharing_alerts IN (0,1))
) STRICT;
