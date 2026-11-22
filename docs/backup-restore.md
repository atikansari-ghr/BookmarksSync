# Backup and Restore

The current Operations slice provides a manual full-system ZIP backup and clean-directory restore. Scheduled weekly/monthly runs, retention, NAS/cloud destinations, optional encryption, user-only recovery, and notifications are planned separately and are not implemented yet.

## What the archive contains

Each Deflate-compressed archive contains a format-versioned manifest, a consistent SQLite online snapshot, the private runtime configuration (`runtime.json`, including OIDC client secrets), the instance key when present, and branding images currently referenced by system settings. Manifest entries include expanded sizes and SHA-256 checksums. The manifest records application/build and schema versions, creation time, public origin, and provider IDs. Treat the archive as a credential-bearing secret; store it in an access-controlled location.

`runtime.json` is created on server startup and is loaded as the default configuration on the next startup. Environment variables and explicit command-line flags override saved values. OIDC environment overrides must provide a complete provider configuration; values are not mixed with saved client secrets. Use `--disable-oidc-google`, `--disable-oidc-authentik`, or `--disable-public-url` to remove saved settings on the next start. Keep the same public origin when restoring if passkeys or external identity providers are in use. The restore includes authentication records and sessions, so restrict access to the recovered host and rotate credentials if the archive was exposed.

## Create and verify

Stop the service before running the standalone backup command so the database, runtime file, and branding assets are captured together. The database itself is copied through SQLite's online backup API; never copy `bookmarksync.db`, `-wal`, or `-shm` files manually while the service is active.

```sh
bookmarksync backup create --data-dir /var/lib/bookmarksync --output /backup/bookmarksync-2026-11-22.zip
bookmarksync backup verify --file /backup/bookmarksync-2026-11-22.zip
```

The output archive is created as a new private temporary file beside its final path and atomically published without replacing an existing archive. Use a local filesystem that supports atomic no-replace publication for the output; copy the verified archive to NAS/cloud storage afterward. Keep backups outside the server's live data directory. Do not pass secrets as command-line arguments.

## Restore

Restore verifies the complete archive, hashes, SQLite integrity, schema compatibility, and runtime configuration before publishing any files. The destination must not exist; restore never merges into an existing data directory.

```sh
bookmarksync backup restore --file /backup/bookmarksync-2026-11-22.zip --target-dir /var/lib/bookmarksync-restored
bookmarksync serve --data-dir /var/lib/bookmarksync-restored --listen 127.0.0.1:8080
```

The restored database may be migrated forward by a newer server build at startup. Archives from a newer database schema are rejected. A restore is published by renaming a private staging directory beside the destination; use a local filesystem for the destination. Keep your reverse-proxy TLS, DNS, and external identity-provider redirect URI configuration operational outside this server restore.
