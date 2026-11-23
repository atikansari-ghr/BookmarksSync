# Build Releases

Each successful push to `main` publishes a prerelease named `build-<full commit SHA>`. Pull-request builds never publish. Releases contain:

- `bookmarksync-linux-amd64` and `bookmarksync-windows-amd64.exe`;
- `BUILD_INFO.txt` with the source commit, commit timestamp, and embedded version;
- `SHA256SUMS` covering both binaries and the build information file.

The publish job runs only after every CI gate and both build targets succeed. It creates a draft, attaches all assets, generates Sigstore-backed GitHub artifact attestations, then publishes the draft. Immutable releases are enabled for this repository, so GitHub locks release assets and the associated SHA tag when published.

## Verify a downloaded binary

Install the GitHub CLI and verify the checksum from the directory containing the downloaded assets:

```sh
sha256sum --check SHA256SUMS
gh attestation verify bookmarksync-linux-amd64 -R atikansari-ghr/BookmarksSync
```

For Windows, substitute `bookmarksync-windows-amd64.exe` in the attestation command. Verify the attestation before installing an update. The checksum detects file changes; the attestation establishes that GitHub Actions built the file for this repository and commit.

## Debian and Proxmox LXC

The Debian amd64 control script supports fresh install, explicit-version upgrade, and repair. It requires root, systemd, `curl`, Python 3, and the GitHub CLI (the script installs `gh` from its official signed Debian repository if needed). It downloads only published SHA-addressed prereleases and checks the checksum and GitHub attestations before changing the installed binary. The Admin Settings page lists eligible release metadata and, on a Debian host where the installer configured its restricted updater, allows a recently reauthenticated administrator to explicitly queue the selected release. No update runs automatically.

```sh
sudo scripts/debian/bookmarksync install
sudo scripts/debian/bookmarksync upgrade --tag build-<full-40-character-commit-sha>
sudo scripts/debian/bookmarksync repair --tag build-<full-40-character-commit-sha>
```

First install prompts for the public HTTPS origin, management CIDRs for GeoIP outage access, and initial administrator credentials. The service listens on `0.0.0.0:8080` by default; configure the reverse proxy and firewall before exposing it publicly. Application data is in `/var/lib/bookmarksync`, the root-only environment file is `/etc/bookmarksync/bookmarksync.env`, and the service is `bookmarksync.service`. Configure GeoLite separately using [docs/network-access.md](network-access.md).

Before upgrade or repair, the script stops the service, creates and verifies a compressed full-system backup, and saves the previous binary and matching service configuration. It checks local readiness after replacement and restores the previous binary and data on failure. The three newest rollback snapshots are retained under `/var/lib/bookmarksync-updates/snapshots`. Keep an independent backup destination as described in [docs/backup-restore.md](backup-restore.md); local rollback snapshots do not protect against host or disk loss. A failed rollback restore leaves the service stopped and preserves both the failed data directory and snapshot for manual recovery.

Browser approvals use `sudo` only for the root-owned `/usr/local/sbin/bookmarksync-update` helper, which accepts one full SHA tag and schedules a dedicated systemd unit. Its sudoers rule does not allow a general shell or arbitrary command. The helper calls the root-owned installer copy, which repeats the release check and verifies GitHub provenance before any binary replacement. Review update job output with `journalctl -u bookmarksync-update-<full-sha>`.

Validate the script and its isolated service lifecycle with `bash scripts/debian/test-bookmarksync.sh`. The automated flow has been exercised on a Debian 13 amd64 Proxmox LXC using mock release assets, including the restricted updater helper. A first published `main` release and a real provenance-verified installation remain pending.

## Docker Compose

Build and start the local image from the repository root:

```sh
docker compose build
docker compose up -d
```

The image builds the Go service and includes checksum-pinned rclone v1.76.0 for the application's remote backup destinations. It runs as UID/GID 65532, drops Linux capabilities, uses a read-only root filesystem, and stores the SQLite database in the named `bookmarksync-data` volume. The country gate remains active: a valid `GeoLite2-Country.mmdb` under `./geoip` or an exact `BOOKMARKSYNC_MANAGEMENT_CIDRS` setting must allow your management client. If neither is configured and available, requests are denied. Do not use a broad management range as a shortcut around country policy.

Set `BOOKMARKSYNC_PUBLIC_URL` to the external HTTPS origin when using browser sign-in or OIDC. Set `BOOKMARKSYNC_TRUSTED_PROXIES` only to the actual reverse-proxy address ranges. The port binds to `127.0.0.1:8080` by default; set `BOOKMARKSYNC_HTTP_BIND` to a specific host interface reachable by a containerized reverse proxy. Restrict any non-loopback binding with the host firewall. The first deployment should be reached only from the configured management range until GeoIP and the admin account are ready.

Create the first administrator without putting the password in an argument or environment variable:

```sh
docker compose exec -T bookmarksync user create --username admin --password-stdin --admin --data-dir /var/lib/bookmarksync
```

The command reads the password from standard input. To keep rclone credentials out of the image and database volume, place a protected `rclone.conf` in `./rclone-config/`; that directory is mounted read-only. The service user (UID 65532) must be able to read it. For local/NFS backup destinations, mount the host's already-mounted backup directory into the container through a Compose override and use that container path in Admin. Never place the live database on a network share.

Upgrade by rebuilding and recreating the container after reviewing a new repository revision: `docker compose build --pull && docker compose up -d`. Compose updates are operator-run; the admin-approved host updater currently supports the Debian installer only. Docker images are built locally from source; GHCR publishing, automatic GeoIP scheduling, and browser-triggered Docker updates are not included yet. Before replacing an image, use the application's verified backup workflow and retain an independent copy of the named volume data.
