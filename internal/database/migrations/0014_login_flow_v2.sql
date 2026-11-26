-- Separate high-entropy browser and poll handles, both stored only as hashes.
-- No application password exists until an approved flow is consumed atomically.
CREATE TABLE login_flows_v2 (
    browser_hash TEXT PRIMARY KEY,
    poll_hash TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    session_id INTEGER REFERENCES web_sessions(id) ON DELETE CASCADE
) STRICT;
CREATE INDEX login_flows_v2_expiry ON login_flows_v2(expires_at);
