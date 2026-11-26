-- Operator-owned destination references and scheduled system backup history.
CREATE TABLE backup_schedule (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    weekly_enabled    INTEGER NOT NULL CHECK (weekly_enabled IN (0, 1)),
    monthly_enabled   INTEGER NOT NULL CHECK (monthly_enabled IN (0, 1)),
    timezone          TEXT NOT NULL,
    weekly_day        INTEGER NOT NULL CHECK (weekly_day BETWEEN 0 AND 6),
    weekly_hour       INTEGER NOT NULL CHECK (weekly_hour BETWEEN 0 AND 23),
    weekly_minute     INTEGER NOT NULL CHECK (weekly_minute BETWEEN 0 AND 59),
    monthly_day       INTEGER NOT NULL CHECK (monthly_day BETWEEN 1 AND 28),
    monthly_hour      INTEGER NOT NULL CHECK (monthly_hour BETWEEN 0 AND 23),
    monthly_minute    INTEGER NOT NULL CHECK (monthly_minute BETWEEN 0 AND 59),
    weekly_retention  INTEGER NOT NULL CHECK (weekly_retention BETWEEN 1 AND 100),
    monthly_retention INTEGER NOT NULL CHECK (monthly_retention BETWEEN 1 AND 100),
    updated_at        INTEGER NOT NULL
) STRICT;

INSERT INTO backup_schedule(id, weekly_enabled, monthly_enabled, timezone,
    weekly_day, weekly_hour, weekly_minute, monthly_day, monthly_hour,
    monthly_minute, weekly_retention, monthly_retention, updated_at)
VALUES (1, 0, 0, 'Asia/Riyadh', 0, 3, 0, 1, 4, 0, 8, 12, 0);

CREATE TABLE backup_destinations (
    id         TEXT PRIMARY KEY CHECK (length(id) BETWEEN 16 AND 64),
    label      TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    kind       TEXT NOT NULL CHECK (kind IN ('local', 'nfs', 'rclone')),
    reference  TEXT NOT NULL CHECK (length(reference) BETWEEN 1 AND 1024),
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (kind, reference)
) STRICT;
CREATE INDEX backup_destinations_enabled ON backup_destinations(enabled, label, id);

CREATE TABLE backup_runs (
    id               INTEGER PRIMARY KEY,
    batch_id         TEXT NOT NULL CHECK (length(batch_id) BETWEEN 16 AND 64),
    trigger_kind     TEXT NOT NULL CHECK (trigger_kind IN ('manual', 'weekly', 'monthly')),
    destination_id   TEXT NOT NULL,
    destination_label TEXT NOT NULL CHECK (length(destination_label) BETWEEN 1 AND 80),
    archive_name    TEXT NOT NULL CHECK (length(archive_name) BETWEEN 16 AND 128),
    state            TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed')),
    size_bytes       INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    error_code       TEXT NOT NULL DEFAULT '' CHECK (error_code IN ('', 'archive_failed', 'destination_unavailable', 'transfer_failed', 'verification_failed', 'retention_failed', 'cancelled')),
    archive_pruned   INTEGER NOT NULL DEFAULT 0 CHECK (archive_pruned IN (0, 1)),
    started_at       INTEGER NOT NULL,
    finished_at      INTEGER
) STRICT;
CREATE INDEX backup_runs_recent ON backup_runs(started_at DESC, id DESC);
CREATE INDEX backup_runs_batch ON backup_runs(batch_id, id);
