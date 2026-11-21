-- Persistent administrator settings, per-user defaults, and safe activity metadata.
-- User values remain authoritative: defaults are consulted only when an override is absent.
CREATE TABLE system_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE user_setting_defaults (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, key)
) STRICT, WITHOUT ROWID;

CREATE TABLE admin_activity (
    id          INTEGER PRIMARY KEY,
    actor_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action      TEXT NOT NULL CHECK (action IN ('user_created', 'user_updated', 'user_settings_updated', 'system_settings_updated', 'branding_asset_uploaded', 'connection_revoked', 'password_reset', 'factor_reset')),
    target_type TEXT NOT NULL CHECK (target_type IN ('user', 'system', 'asset')),
    target_id   INTEGER,
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX admin_activity_created ON admin_activity(created_at DESC, id DESC);
