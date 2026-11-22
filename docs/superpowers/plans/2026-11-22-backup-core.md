# Operations Backup Core Plan

## Goal

Deliver a portable, compressed full-system backup archive and a safe restore path into a new data directory. This is the first Operations slice; scheduling, network/cloud destinations, optional encryption, user-only recovery, geo restrictions, notifications, and installers follow in separate tasks.

## Approved behavior

- Preserve all current application state: SQLite database, instance key when present, branding assets, public origin and configured OIDC provider settings/secrets, plus a format/schema/build manifest and checksums.
- Use ZIP/Deflate from the Go standard library. Compression is on; encryption remains off by default. Treat every archive as sensitive and create files with owner-only permissions where supported.
- Obtain the database snapshot through the current `modernc.org/sqlite` online backup API, reached via `database/sql.Conn.Raw`; never copy a live WAL database file directly.
- Coordinate the snapshot of database and runtime files with the branding asset lifecycle. Do not hold the coordination lock during later destination uploads.
- Validate the entire archive before extraction. Restore only into a new, absent target data directory; never overwrite a running instance. Refuse unknown paths, links, duplicate entries, hash mismatches, unsupported formats, and databases newer than this binary.

## Files and interfaces

- `internal/database/backup.go`: add `(*DB).BackupTo(ctx context.Context, dest string) error` using `sqlite.Conn.NewBackup`, bounded page steps, context checks, busy/locked retry, cleanup, and private output permissions.
- `internal/config/runtime_file.go` and `internal/config/config.go`: persist the effective listen address, public origin, and OIDC provider configuration in a private data-directory runtime file. Load it as the default layer, then apply environment overrides and CLI flags; atomically rewrite the effective file on server startup so backups retain external provider secrets.
- `internal/backup/manifest.go`: define the versioned manifest and per-entry size/SHA-256 records; list the exact allowed archive names.
- `internal/backup/archive.go`: add `Create(ctx, Options) (Result, error)` and `Inspect(ctx, path) (Manifest, error)`; stage locally, verify checksums, write a temporary archive, sync, and atomically publish it.
- `internal/backup/restore.go`: add `Restore(ctx, archivePath, targetDir string) error`; verify before extraction, stage beside the absent target, restore safe permissions, then rename into place.
- `internal/maintenance/filegate.go`: coordinate concurrent system-file mutations with a snapshot-exclusive operation while preserving serialization between branding mutations.
- `internal/api/branding_assets.go` and `internal/api/api_account_handlers.go`: replace the private asset mutex with the shared file gate for settings/assets mutation.
- `cmd/bookmarksync/backup.go` and `cmd/bookmarksync/main.go`: expose `backup create`, `backup verify`, and `backup restore` CLI commands without printing secret values.
- Tests live beside each Go package; CLI integration tests use temporary data directories and actual SQLite databases.

## Tasks

### 1. Consistent SQLite online snapshot

- Add tests first: `TestBackupToCapturesCommittedWALData`, `TestBackupToHandlesSpecialDestinationPaths`, `TestBackupToHonorsCancellationAndRemovesPartialOutput`, and `TestBackupToRetriesBusySteps`.
- Verify the test failure is caused by the missing backup operation.
- Implement `DB.BackupTo` using a pinned `*sql.Conn`, structural interface assertion for `NewBackup(string)`, page-bounded `Step`, retry only SQLite BUSY/LOCKED codes with a short context-aware delay, and `Finish` on every path.
- Reopen the destination with SQLite and compare committed rows, including a write committed while the online copy is in progress.

### 2. Versioned full-system archive

- Add tests first for manifest fields, required/optional files, per-entry checksums, ZIP compression, invalid or missing source files, and private archive permissions.
- Define fixed entries: `manifest.json`, `database/bookmarksync.db`, optional `instance.key`, `branding/<generated-id>.<png|jpg|webp>`, and `config/runtime.json` for listen address, public origin, and OIDC provider settings/secrets. Never place secrets in manifest metadata.
- Under the file gate, snapshot the database and copy runtime files into a private local staging directory; release the gate before compression or destination I/O.
- Include format version, app version/commit/build time, applied/latest schema, UTC creation time, public origin, OIDC provider IDs, entry sizes and SHA-256. Keep OIDC secrets out of logs and error strings.
- Write the ZIP to a same-filesystem temporary path; flush, sync, close, then rename to the requested output. Remove only temporary output created by the failed operation.

### 3. Verify and restore to a new host directory

- Add tests first for correct verify/restore, corrupt ZIP, bad hashes, missing database, future schema, traversal/absolute names, symlink entries, duplicate names, oversized entries, and existing target rejection.
- Inspect and hash every entry before writing any target files; cap manifest, entry count, and expanded total size.
- Restore `config/runtime.json` with private permissions; on next start, explicit environment variables or CLI flags override its saved values.
- Restore into a private temporary sibling directory; require the destination not to exist; restore the instance key using the repository's platform-safe private-file rules; validate SQLite and migration version; atomically rename the completed directory to the destination.
- Add CLI integration tests proving `backup verify` works on good/corrupt archives and `backup restore` never replaces an existing directory.

### 4. Documentation and independent review

- Document archive contents, sensitive-data handling, creation/verification/restore commands, version compatibility, and external reverse-proxy/OIDC configuration requirements.
- Run focused RED/GREEN cycles, `go test -count=1 ./...`, `go vet ./...`, gofmt, Linux/Windows CGO-free builds, and the LXC restore smoke using a fresh test data directory.
- Request a read-only Claude review through Herd; Codex owns implementation, triage, and acceptance.

## Review focus

1. No WAL/SHM copy race or state loss under active writes.
2. Database, instance key, and referenced branding assets describe one snapshot.
3. No extraction path can escape the staging directory or follow links.
4. A failed archive/restore leaves the existing service data and target untouched.
5. Archive/config secrets never appear in logs, HTTP errors, or command arguments.
6. Windows paths, file permissions, and atomic rename behavior are covered.

## Delivery status (2026-11-22)

- Implemented manual full-system `backup create`, `backup verify`, and clean-directory `backup restore`, with archive and CLI regression coverage plus operator documentation.
- Independent read-only review found no remaining Important issues after fixes to archive publication, Windows ACL compatibility, and restore safety. Follow-up review verified the Windows legacy instance-key ownership case.
- Full Go suite, vet, and CGO-free Linux/Windows builds passed after the final archive-safety tests.
- Isolated Proxmox LXC smoke passed using a fresh temporary data directory: bootstrap, archive, verify, restore, restart, and readiness. The pre-existing `bookmarksync-test.service` and its data were left untouched.
- This completes only the manual backup/restore core. Scheduling, retention, external destinations, encryption, user-only restore, geo controls, notifications, and admin UI remain pending.
