-- Identity and API milestone schema, created complete and up front. Purely additive:
-- no existing table is altered. Later milestone tasks consume these tables and must
-- not edit this migration. All timestamps are unix milliseconds (UTC). Secrets that
-- the server only needs to compare (session secrets, API tokens, recovery codes,
-- auth-flow handles) are stored as SHA-256 hex digests, never in plaintext.

-- Opaque server-side browser sessions. Idle expiry is last_seen_at + 12h; absolute
-- expiry is expires_at (created_at + 30d) and is never extended by rotation.
-- authenticated_at records the last (re)authentication for recent-auth checks.
CREATE TABLE web_sessions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users(id),
    secret_hash      TEXT    NOT NULL UNIQUE,
    label            TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    last_seen_at     INTEGER NOT NULL,
    authenticated_at INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    revoked_at       INTEGER
) STRICT;
CREATE INDEX web_sessions_user ON web_sessions(user_id, revoked_at);

-- Personal API tokens. scopes is a space-separated canonical list validated by the
-- service; expires_at NULL means the token does not expire.
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    label        TEXT    NOT NULL DEFAULT '',
    secret_hash  TEXT    NOT NULL UNIQUE,
    scopes       TEXT    NOT NULL CHECK (scopes <> ''),
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    last_used_at INTEGER,
    revoked_at   INTEGER
) STRICT;
CREATE INDEX api_tokens_user ON api_tokens(user_id, revoked_at);

-- Random per-user WebAuthn user handle (never the numeric user id), so discoverable
-- passkey login can map the returned userHandle back to an account.
CREATE TABLE webauthn_users (
    user_id     INTEGER PRIMARY KEY REFERENCES users(id),
    user_handle BLOB    NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL
) STRICT;

-- Registered passkeys. Columns mirror the verified credential record a WebAuthn
-- relying party must keep (public key, signature counter, flags, AAGUID).
CREATE TABLE webauthn_credentials (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users(id),
    credential_id    BLOB    NOT NULL UNIQUE,
    public_key       BLOB    NOT NULL,
    attestation_type TEXT    NOT NULL DEFAULT '',
    transports       TEXT    NOT NULL DEFAULT '',
    aaguid           BLOB,
    sign_count       INTEGER NOT NULL DEFAULT 0,
    clone_warning    INTEGER NOT NULL DEFAULT 0 CHECK (clone_warning IN (0, 1)),
    user_verified    INTEGER NOT NULL DEFAULT 0 CHECK (user_verified IN (0, 1)),
    backup_eligible  INTEGER NOT NULL DEFAULT 0 CHECK (backup_eligible IN (0, 1)),
    backup_state     INTEGER NOT NULL DEFAULT 0 CHECK (backup_state IN (0, 1)),
    attachment       TEXT    NOT NULL DEFAULT '',
    attestation      BLOB,
    name             TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    last_used_at     INTEGER
) STRICT;
CREATE INDEX webauthn_credentials_user ON webauthn_credentials(user_id);

-- One TOTP seed per user, encrypted with the instance key (AEAD; nonce carried in the
-- ciphertext). confirmed_at NULL means enrollment is pending. last_used_step blocks
-- replay of an accepted time step.
CREATE TABLE totp_secrets (
    user_id         INTEGER PRIMARY KEY REFERENCES users(id),
    seed_ciphertext BLOB    NOT NULL,
    key_version     INTEGER NOT NULL DEFAULT 1,
    confirmed_at    INTEGER,
    last_used_step  INTEGER,
    created_at      INTEGER NOT NULL
) STRICT;

-- Single-use recovery codes, stored hashed; used_at marks consumption.
CREATE TABLE recovery_codes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    code_hash  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    used_at    INTEGER,
    UNIQUE (user_id, code_hash)
) STRICT;

-- Linked external identities, keyed only by verified issuer + subject. Display
-- fields are informational and are never used for matching or linking.
CREATE TABLE oidc_identities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id),
    provider      TEXT    NOT NULL,
    issuer        TEXT    NOT NULL,
    subject       TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    UNIQUE (issuer, subject)
) STRICT;
CREATE INDEX oidc_identities_user ON oidc_identities(user_id);

-- Short-lived, single-use server-side authentication flows (WebAuthn ceremonies,
-- pending second-factor challenges, OIDC state/nonce/PKCE). flow_hash is the hash of
-- the opaque handle given to the browser; data is service-defined JSON.
CREATE TABLE auth_flows (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    flow_hash   TEXT    NOT NULL UNIQUE,
    kind        TEXT    NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
    user_id     INTEGER REFERENCES users(id),
    session_id  INTEGER REFERENCES web_sessions(id) ON DELETE CASCADE,
    data        TEXT    NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    consumed_at INTEGER
) STRICT;
CREATE INDEX auth_flows_expires ON auth_flows(expires_at);

-- Per-user settings. Keys are whitelisted by the service; values are bounded JSON.
CREATE TABLE user_settings (
    user_id    INTEGER NOT NULL REFERENCES users(id),
    key        TEXT    NOT NULL CHECK (length(key) BETWEEN 1 AND 64),
    value      TEXT    NOT NULL CHECK (length(value) <= 8192),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, key)
) STRICT;
