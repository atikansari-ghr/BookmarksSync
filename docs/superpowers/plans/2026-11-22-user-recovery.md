# User Recovery Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** Use the TDD workflow task-by-task. Steps use checkbox syntax.

**Goal:** Restore one user's private bookmarks and saved settings from a verified full-system archive without replacing their current account or affecting other users.

**Architecture:** A host-admin CLI command verifies and stages the full archive, opens its database through the normal migration layer, selects the matching username, and exports only that user's canonical bookmark roots, favorites, and explicit settings overrides. The live database imports both roots transactionally with a per-user archive-hash marker; setting restoration is independently repeatable so a retry repairs a partial run. It never restores authentication, sessions, shares, another user's favorites, system settings, or user defaults.

**Tech Stack:** Go, SQLite, existing backup ZIP verification, bookmark domain service, identity settings validation.

**Spec:** `docs/design.md` Operations and `docs/backup-restore.md`.

## Global Constraints

- Full-system restore remains separate and requires an empty target directory.
- User recovery is additive and scoped to one existing username; no account credentials or session records are copied.
- Archive contents must pass existing manifest, hash, SQLite integrity, and schema checks before import.
- Never log bookmark contents or credentials.
- Commits use Atik Ansari as sole author.

## Review Focus

- Wrong/missing username or malformed archive changes no live data.
- Repeating the same archive does not duplicate folders or bookmarks.
- Share-only/private and other-user content remains isolated.
- A settings failure after bookmark import can be repaired by safely rerunning the command.
- Cancellation and temporary-directory cleanup do not leave extracted archive data behind.

---

### Task 1: Idempotent private-tree merge

**Files:** migration `internal/database/migrations/0009_user_restore_imports.sql`; bookmark domain implementation and tests.

**Interfaces:** Add `MergeUserRestore(ctx, scope, archiveSHA256, bar, other) (imported bool, err error)`; require a writable private scope, canonical target roots, a valid SHA-256, and transactionally record the marker with both imports.

- [x] Test marker repeat, per-user isolation, and rollback-safe transactional import.
- [x] Run focused test and observe compile-time RED before adding the interface.
- [x] Implement the migration and scoped merge; preserve a user's favorites during merge.
- [x] Run focused tests and package tests.

### Task 2: CLI recovery command

**Files:** `cmd/bookmarksync/backup.go`, tests, usage text, `docs/backup-restore.md`.

**Interfaces:** Add `bookmarksync backup restore-user --file ARCHIVE --username NAME --data-dir DIR`.

- [x] Test CLI rejects absent archive/user and reports missing archived account.
- [x] Implement staged archive extraction, source-tree/settings selection, idempotent import, cleanup, and settings validation/application; invalid settings are rejected before live changes.
- [x] Test full recovery, repeat recovery, per-user isolation, favorites/tags/settings, and invalid archived settings.

### Task 3: Verification and documentation

- [x] Run `gofmt`, `go test -count=1 ./...`, `go vet ./...`, and CGO-free Linux/Windows builds.
- [x] Review changed files for credential leakage and scope violations.
- [x] Update the backup guide and implementation plan with implemented status and limits.
