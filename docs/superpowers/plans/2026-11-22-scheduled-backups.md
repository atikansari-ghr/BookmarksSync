# Scheduled Backups Implementation Plan

> **For agentic workers:** Use `superpowers:executing-plans` task by task. Keep implementation and review commits authored only by Atik Ansari.

**Goal:** Run verified weekly and monthly full-system backups to operator-prepared filesystem and rclone destinations, with admin controls and safe retention.

**Architecture:** Add an operations backup store and runner to the existing Go service. A single serialized manager creates one local archive using the existing backup package, verifies and copies it independently to configured destinations, and prunes only after successful delivery. Admin-only APIs and a section in the existing admin page configure schedules/destinations and show safe run status.

**Tech Stack:** Go, SQLite migrations, `modernc.org/sqlite`, React/TypeScript, standard-library process execution, rclone v1.76.0 (host installed/configured).

**Spec:** `docs/superpowers/specs/2026-11-22-scheduled-backups-design.md`; approved Operations requirements in `docs/design.md`.

## Global Constraints

- Defaults: `Asia/Riyadh`, Sunday at 03:00 weekly, day 1 at 04:00 monthly; retain 8 weekly and 12 monthly archives per destination.
- Missed schedule occurrences are skipped; startup does not run a catch-up backup.
- Build each archive on local storage and verify it before remote transfer. Never write SQLite database/WAL files directly to a network destination.
- Store only destination references in BookmarksSync; rclone credentials/configuration stay operator-managed on the host. NFS is a separate destination kind and must be an actual mounted NFS path.
- Launch rclone as a fixed executable with argument arrays, never through a shell. Pin and verify version 1.76.0. Do not return raw process output or credentials through logs/API.
- Do not prune when destination listing, backup verification, or transfer state is uncertain. Existing manual backup CLI remains usable.
- All configuration mutation routes are admin-only and require recent reauthentication/CSRF. New files/archives retain platform-private permissions.
- Full Go, web, vet, and CGO-free Linux/Windows checks. LXC deployment is allowed only to its dedicated `bookmarksync-test` service after preserving its current binary/data; never change a production service without separate authorization.

## Review Focus

- Restart at or across a schedule boundary must not run a missed occurrence or duplicate a future run.
- DST transitions and Asia/Riyadh time must select one valid local occurrence.
- A failed destination cannot stop other destinations or cause retention deletion.
- rclone output, configured remote paths, and secrets must not leak into user-visible errors or logs.
- Concurrent branding edits must not make a backup combine settings from one snapshot with missing/replaced asset files.

---

### Task 1: Persist backup schedules, destinations, and run records

**Files:** Create `internal/database/migrations/0007_scheduled_backups.sql`, `internal/backup/store.go`, `internal/backup/store_test.go`; modify migration fixtures/version assertions.

**Interfaces:** Add `Store` methods to load/save the singleton `Schedule`, add/update/remove `Destination` records, and create/finish/list sanitized `Run` records. Schedule values include timezone, weekly day/time, monthly day/time, enable flags, and retention counts. Destinations contain ID, label, kind (`local`, `nfs`, or `rclone`), reference, enabled state; never credentials.

- [x] Write tests for defaults, validation, persistence/reopen, destination constraints, run transitions, bounded history reads, and ensuring run errors cannot store arbitrary secret/process output.
- [x] Run focused tests and confirm expected RED before implementing migration and repository (`NewStore`, `DefaultSchedule`, and model symbols missing).
- [x] Implement migration `0007_scheduled_backups.sql` and the typed store with validated writes and fixed error codes.
- [x] Run `go test -p=1 -count=1 ./internal/database ./internal/backup` (PASS).

### Task 2: Compute schedule occurrences and run safely

**Files:** Create `internal/backup/schedule.go`, `internal/backup/schedule_test.go`, `internal/backup/scheduler.go`, `internal/backup/scheduler_test.go`.

**Interfaces:** `NextOccurrence(Schedule, time.Time) (time.Time, bool, error)` returns the next future occurrence only. `Scheduler.Run(ctx)` loads current settings, waits for an occurrence or a reconfiguration signal, invokes a `RunFunc(context.Context, Trigger) error` callback, and exits on cancellation.

- [x] Write tests for configured weekly/monthly times, strict-future behavior, missed-slot skip, DST gap/fold behavior, and disabled schedules.
- [x] Run tests to confirm RED (`NextOccurrence` missing).
- [x] Implement timezone-aware schedule calculation; skip nonexistent wall times and run the earlier instant once for repeated DST wall times.
- [x] Add deterministic scheduler-loop tests for reconfiguration, invocation, cancellation, schedule reload failure, and no startup catch-up; implement the injectable timer loop.
- [x] Run focused scheduler tests.

### Task 3: Add filesystem and rclone destinations with verified retention, then orchestrate runs

**Files:** Create `internal/backup/destination.go`, `internal/backup/destination_test.go`, `internal/backup/rclone.go`, `internal/backup/rclone_test.go`, `internal/backup/manager.go`, `internal/backup/manager_test.go`; modify `internal/backup/archive.go`, `internal/api/api.go`, `internal/api/branding_assets.go`, `cmd/bookmarksync/main.go`, and `internal/securefile/` as required.

**Interfaces:** A `DestinationDriver` supports `Test`, `Publish`, and `Delete`; retention uses only successful archive names tracked by the store. Local and NFS references are absolute host paths; NFS checks the actual mount type before any write. Rclone references use a validated remote name plus relative destination path. `Manager` consumes the store and destination driver, creates one verified local archive per batch, records each destination independently, and serializes manual/scheduled invocations. The admin API and manager share a snapshot lock with branding mutations; hold it only while staging a consistent set of files.

- [x] Test path/remote validation and filesystem no-replace publication, including collisions and partial failures.
- [x] Use an injected rclone runner to test exact-version checks, argument boundaries, transfer/check/cleanup ordering, timeouts, cancellation, and output/secret redaction.
- [x] Implement rclone transfer using a random temporary remote name, byte verification (`rclone check --download`), then final publication. For local targets, copy to a private temporary file and publish without replacement.
- [x] Implement retention separately by backup class and destination; prune only after a new archive is verified and delivered, never on a partial/unknown listing.
- [x] Test manager success/failure isolation, manual/scheduled serialization, cancellation, snapshot-lock scope, and sanitized run history before adding production code.
- [x] Implement manager orchestration and run the existing plus new focused package tests.

### Task 4: Expose admin configuration and run controls

**Files:** Modify `internal/api/api.go`, `internal/api/api_account_handlers.go`, `internal/api/openapi.yaml`, their tests, `cmd/bookmarksync/main.go`; modify or add API manager wiring in `internal/backup/`.

**Interfaces:** Add admin-only `GET /api/v1/admin/backups`, full-replacement `PUT /api/v1/admin/backups/schedule`, destination create/replace/delete/test routes, and `POST /api/v1/admin/backups/run` returning 202 with a batch ID. GET returns schedule, destination labels/kinds/references, and bounded sanitized history; no rclone config or raw process output.

- [x] Add route-policy, role, recent-reauthentication, CSRF, validation, and secret-free response tests before handlers.
- [x] Implement routes through the manager/store and update the checked-in OpenAPI contract.
- [x] Run focused API tests and `go test ./internal/api`.

### Task 5: Add admin backup controls and operator documentation

**Files:** Create `web/src/components/AdminBackups.tsx` and tests; modify `web/src/pages/AdminPage.tsx`, `README.md`, `docs/backup-restore.md`, and `docs/implementation-plan.md`.

- [x] Test loading/error/empty states, editing defaults, adding/testing/removing destinations, triggering a run, and rendering sanitized status.
- [x] Build the admin UI with accessible labels, confirmation before destination removal, and clear disclosure that server-managed rclone credentials are configured on the host.
- [x] Document Debian/systemd and Windows environment setup, rclone v1.76.0 installation/config path, mounted NFS paths, Google/OneDrive/SMB remote setup, schedule defaults, retention, and troubleshooting.
- [x] Run web tests, typecheck, build, full Go tests, vet, and CGO-free Linux/Windows builds.
- [x] Run an isolated Proxmox LXC backup/rclone/restore smoke, then update only the dedicated test service after preserving its existing binary/data.

### Task 6: Independent review and commit

- [x] Request read-only Claude Code reviews through Herd; Codex reviewed and fixed findings; final review found no actionable issues.
- [x] Run `git diff --check`, confirm only intended files are staged, verify Atik-only commit identity, and commit the feature. Do not push.
