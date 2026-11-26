-- Hash destinations so the cross-account verification throttle does not store
-- an additional copy of a user's email address or Telegram chat ID.
CREATE TABLE notification_rate_limits (
    kind TEXT NOT NULL CHECK (kind IN ('email', 'telegram')),
    destination_hash BLOB NOT NULL CHECK (length(destination_hash) = 32),
    request_token BLOB NOT NULL CHECK (length(request_token) = 16),
    last_sent_at INTEGER NOT NULL,
    PRIMARY KEY (kind, destination_hash)
) STRICT;
