-- Extend administrator activity for explicit software update approvals.
ALTER TABLE admin_activity RENAME TO admin_activity_old;
CREATE TABLE admin_activity (
    id          INTEGER PRIMARY KEY,
    actor_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action      TEXT NOT NULL CHECK (action IN ('user_created', 'user_updated', 'user_settings_updated', 'system_settings_updated', 'branding_asset_uploaded', 'connection_revoked', 'password_reset', 'factor_reset', 'backup_schedule_updated', 'backup_run_started', 'backup_destination_created', 'backup_destination_updated', 'backup_destination_deleted', 'software_update_approved')),
    target_type TEXT NOT NULL CHECK (target_type IN ('user', 'system', 'asset', 'backup', 'backup_destination')),
    target_id   INTEGER,
    created_at  INTEGER NOT NULL,
    CHECK (
        (action IN ('user_created', 'user_updated', 'user_settings_updated', 'connection_revoked', 'password_reset', 'factor_reset') AND target_type='user' AND target_id > 0) OR
        (action IN ('system_settings_updated', 'software_update_approved') AND target_type='system' AND target_id IS NULL) OR
        (action='branding_asset_uploaded' AND target_type='system' AND target_id IS NULL) OR
        (action='branding_asset_uploaded' AND target_type='asset' AND target_id > 0) OR
        (action IN ('backup_schedule_updated', 'backup_run_started') AND target_type='backup' AND target_id IS NULL) OR
        (action IN ('backup_destination_created', 'backup_destination_updated', 'backup_destination_deleted') AND target_type='backup_destination' AND target_id IS NULL)
    )
) STRICT;
INSERT INTO admin_activity(id, actor_id, action, target_type, target_id, created_at)
SELECT id, actor_id, action, target_type, target_id, created_at FROM admin_activity_old;
DROP TABLE admin_activity_old;
CREATE INDEX admin_activity_created ON admin_activity(created_at DESC, id DESC);
