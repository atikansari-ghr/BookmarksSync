# Scheduled Backups Design

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

## Intent

Add reliable, hands-off weekly and monthly full-system backups so an operator can recover the service after host or storage failure. This builds on the manual archive and restore commands in `docs/backup-restore.md`.

## Goals

- Let an administrator configure weekly and monthly schedules, destinations, enablement, and view recent run status in the admin interface.
- Use the approved defaults: Asia/Riyadh, Sundays at 03:00 weekly, the first day of each month at 04:00 monthly; retain 8 verified weekly and 12 verified monthly archives at each destination.
- Make each backup locally first, verify its manifest and SQLite integrity, then deliver it to each configured destination independently.
- Support local paths, explicitly selected host-mounted NFS paths, and host-configured rclone remotes for cloud and SMB targets. Never write a live SQLite database directly to a network destination.
- Show sanitized progress and failure information. A failure at one destination must not block another destination.
- Coordinate snapshots with live branding changes, and allow the server to shut down without orphaning backup or rclone processes.

## Non-goals

- BookmarksSync does not perform OAuth sign-in to storage providers or collect their passwords. The administrator prepares remote definitions and credentials on the host.
- Optional archive encryption, user-only restore, pre-update snapshots, notifications, installers, and geo access controls remain separate work.
- No backup is enabled until at least one destination and schedule are configured. No arbitrary shell commands or user-provided rclone flags are accepted.

## Approaches

1. **Host-managed rclone and mounted paths (selected):** BookmarksSync stores a display label and a destination reference only. The operator owns the rclone config and credentials. This follows the approved design and keeps provider secrets outside the application.
2. **Provider accounts managed in the admin interface:** more convenient, but requires OAuth/token lifecycle and secret storage for every backend; it duplicates rclone's provider support.
3. **Mounted paths only:** simpler, but leaves Google Drive, OneDrive, and SMB remote setup to external mounts and does not meet the approved rclone design.

## Behavior and boundaries

- The scheduler runs in the server process using a timezone database available on Linux and Windows. It computes the next scheduled occurrence, records completed runs, avoids duplicate concurrent runs, and skips missed slots after downtime (no startup catch-up run).
- A scheduled run takes a consistent local snapshot. The existing branding mutation lock protects the database/settings/assets snapshot; release it after staging so compression and destination transfers do not block UI edits.
- For local/NFS targets, publish the verified archive as a new file without replacing an existing archive. NFS targets must resolve to a real NFS mount; never let an unmounted mountpoint silently fall back to local disk. For rclone targets, use a fixed binary and config path controlled by the operator, pass arguments without a shell, upload under a temporary name, verify the remote copy, then publish its final name.
- Retention runs only after a successful, verified upload. Keep the newest 8 weekly and 12 monthly archives per destination. Never prune when listing, verification, or upload state is uncertain; report deletion failures.
- Admin settings and recent run records are persisted in SQLite. Credentials, full remote config, and archive contents are never returned through the API or written to logs. Restrict destination types and paths to explicit configuration; validate remote names and destination references before saving.
- Shutdown cancels in-progress work, waits for child processes to exit, and keeps the existing manual CLI available.

## Builder call

- Missed schedules are skipped; the next future weekly/monthly occurrence runs normally. A startup catch-up could create an unexpected large upload or multiple overlapping runs after a long outage.
- NFS is represented as its own destination kind and validated against the host's mount table — an unmounted mountpoint must fail closed instead of writing to the underlying local directory. This is a small explicit distinction that avoids false-success backups on the server disk.

## Acceptance

Tests cover time-zone boundaries and daylight-saving-safe scheduling, restart/no-duplicate behavior, concurrent/manual run exclusion, live branding mutation during snapshots, local and rclone transfer verification, path/remote validation, independent destinations, retention thresholds, partial failures, shutdown cleanup, admin authorization/CSRF, and secret-free logs/responses. Linux and Windows tests/builds must pass. A Proxmox LXC smoke uses a temporary data directory and a local test destination; it must leave the existing service data unchanged.
