-- Link checks: persisted, resumable jobs and their per-bookmark results. Purely additive:
-- no existing table is altered, and migrations 0001-0004 are unchanged.
--
-- A job belongs to the user who submitted it (user_id) and to exactly one scope: the
-- owner's private collection (share_folder_id = 0) or one shared folder (share_folder_id =
-- the shared folder, owner_id = the share owner). Reads must match user, owner and scope,
-- so another user, or the same user in another scope, never sees a job or its results.
-- Results snapshot the URL at submission time and carry no title, tag or folder data.
--
-- Queue: status 'queued' -> 'running' -> 'completed'. A result starts 'pending' and is
-- written exactly once when its check finishes, so after a restart (running jobs are
-- re-queued) only pending results are checked again; completed work is never repeated.
-- Timestamps are unix milliseconds (UTC).
CREATE TABLE link_check_jobs (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id),
    owner_id        INTEGER NOT NULL REFERENCES users(id),
    share_folder_id INTEGER NOT NULL DEFAULT 0,
    status          TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'completed')),
    created_at      INTEGER NOT NULL,
    started_at      INTEGER,
    finished_at     INTEGER
) STRICT;
CREATE INDEX link_check_jobs_queue ON link_check_jobs(status, id);
CREATE INDEX link_check_jobs_user ON link_check_jobs(user_id, status);

-- bookmark_id deliberately has no foreign key: purging a bookmark must not be blocked by,
-- or rewrite, a finished check.
CREATE TABLE link_check_results (
    job_id      INTEGER NOT NULL REFERENCES link_check_jobs(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    bookmark_id INTEGER NOT NULL,
    url         TEXT    NOT NULL,
    status      TEXT    NOT NULL CHECK (status IN ('pending', 'ok', 'broken', 'skipped', 'excluded', 'blocked', 'error')),
    http_status INTEGER,
    detail      TEXT    NOT NULL DEFAULT '',
    redirects   INTEGER NOT NULL DEFAULT 0,
    checked_at  INTEGER,
    PRIMARY KEY (job_id, position)
) STRICT, WITHOUT ROWID;
