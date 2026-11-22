# Backup and Restore

The Operations slice provides scheduled weekly and monthly full-system backups, manual runs from the admin interface, per-destination retention, archive verification, and clean-directory restore. Backups use Deflate compression. Optional encryption, user-only recovery, and backup notifications remain planned.

## What the archive contains

Each Deflate-compressed archive contains a format-versioned manifest, a consistent SQLite online snapshot, the private runtime configuration (`runtime.json`, including OIDC client secrets), the instance key when present, and branding images currently referenced by system settings. Manifest entries include expanded sizes and SHA-256 checksums. The manifest records application/build and schema versions, creation time, public origin, and provider IDs. Treat the archive as a credential-bearing secret; store it in an access-controlled location.

`runtime.json` is created on server startup and is loaded as the default configuration on the next startup. Environment variables and explicit command-line flags override saved values. OIDC environment overrides must provide a complete provider configuration; values are not mixed with saved client secrets. Use `--disable-oidc-google`, `--disable-oidc-authentik`, or `--disable-public-url` to remove saved settings on the next start. Keep the same public origin when restoring if passkeys or external identity providers are in use. The restore includes authentication records and sessions, so restrict access to the recovered host and rotate credentials if the archive was exposed.

## Create and verify

The server's scheduled and admin-triggered jobs take a consistent SQLite online snapshot and serialize branding asset reads with admin asset changes. The standalone CLI command does not share that asset lock; stop the service before using it so the database, runtime file, and branding assets are captured together. Never copy `bookmarksync.db`, `-wal`, or `-shm` files manually while the service is active.

## Scheduled backups and destinations

Open **Administration → System backups** to set the timezone, weekly/monthly times, retention counts, and destinations. Schedules are disabled until enabled. Default times are Sunday at 03:00 and day 1 at 04:00 in `Asia/Riyadh`; missed times are skipped after downtime, with no startup catch-up. Retention is counted separately for weekly and monthly runs and applies only after a verified successful run. Manual runs do not trigger scheduled retention.

Destinations are host-managed references; the web interface never stores provider credentials:

- **Local folder:** an existing absolute directory writable by the service account.
- **NFS mount:** an existing directory on an active NFS mount. The Linux server checks the mount type before writing, preventing writes into an unmounted host directory. Windows deployments should use a local or rclone destination.
- **rclone remote:** install rclone **v1.76.0** and configure a remote in a host-owned config file. Set `BOOKMARKSYNC_RCLONE_BINARY` to the executable path if it is not on `PATH`, and `BOOKMARKSYNC_RCLONE_CONFIG` to the config file path. Then enter a remote reference such as `nas:bookmarks` in the admin interface. The server rejects unsupported rclone versions and verifies transfers by downloading and comparing bytes before accepting the final archive.

On Debian, configure the host-owned remote as the same OS account that runs BookmarksSync. For example:

```sh
sudo install -d -m 0700 -o <service-user> -g <service-group> /etc/bookmarksync
sudo -u <service-user> rclone config --config /etc/bookmarksync/rclone.conf
sudo chmod 0600 /etc/bookmarksync/rclone.conf
```

Add these variables to the BookmarksSync service environment (for systemd, a service drop-in), then reload and restart the unit:

```ini
BOOKMARKSYNC_RCLONE_BINARY=/usr/bin/rclone
BOOKMARKSYNC_RCLONE_CONFIG=/etc/bookmarksync/rclone.conf
```

For Windows, set the same variables in the service or process environment and restart the service. Create remotes with `rclone config`; provider-specific setup is documented for [Google Drive](https://rclone.org/drive/), [OneDrive](https://rclone.org/onedrive/), and [SMB](https://rclone.org/smb/). A mounted NAS can instead use a local folder or the NFS destination type.

Use **Test destination** before enabling a target. It creates a random temporary probe, verifies it, and removes it. Each successful archive has a generated name beginning `bookmarksync-`; retention deletes only archive names recorded by this server. Removing a destination stops future runs but leaves its existing files untouched. Backups contain authentication settings and must be protected as secrets.

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

The restored database may be migrated forward by a newer server build at startup. Archives from a newer database schema are rejected. For safety, restore disables weekly/monthly schedules and every external backup destination; review and re-enable the intended targets in Admin after confirming the new host's mount paths and rclone configuration. Any run recorded as still in progress is marked failed. A restore is published by renaming a private staging directory beside the destination; use a local filesystem for the destination. Keep your reverse-proxy TLS, DNS, and external identity-provider redirect URI configuration operational outside this server restore.
