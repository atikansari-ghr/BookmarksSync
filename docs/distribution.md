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

Before starting, create a private `.env` file beside `compose.yaml`. Compose loads it automatically, and the repository ignores it. Configure either an exact management client CIDR or provide a valid GeoLite database; without one, every HTTP route stays blocked. For example, replace the documentation-only addresses below with your actual client and proxy addresses:

```dotenv
BOOKMARKSYNC_MANAGEMENT_CIDRS=192.0.2.25/32
BOOKMARKSYNC_GEOIP_DIR=./geoip
BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com
BOOKMARKSYNC_TRUSTED_PROXIES=192.0.2.10/32
```

Put `GeoLite2-Country.mmdb` in `./geoip/` if using GeoIP. Build and start the local image from the repository root. Set build identity from the checked-out commit so the UI, CLI and backup manifest can identify the source:

```sh
commit="$(git rev-parse HEAD)"
export BOOKMARKSYNC_COMMIT="$commit"
export BOOKMARKSYNC_VERSION="0.0.0-${commit:0:12}"
export BOOKMARKSYNC_DATE="$(git show -s --format=%cI "$commit")"
docker compose build --pull
docker compose up -d
```

The image builds the Go service and includes checksum-pinned rclone v1.76.0 for the application's remote backup destinations. It runs as UID/GID 65532, drops Linux capabilities, uses a read-only root filesystem, and stores SQLite data and rclone's OAuth configuration in separate named volumes. The country gate remains active: a valid `GeoLite2-Country.mmdb` under `./geoip` or an exact `BOOKMARKSYNC_MANAGEMENT_CIDRS` setting must allow your management client. If neither is configured and available, requests are denied. Do not use a broad management range as a shortcut around country policy.

Set `BOOKMARKSYNC_PUBLIC_URL` to the external HTTPS origin when using browser sign-in or OIDC. Set `BOOKMARKSYNC_TRUSTED_PROXIES` only to the actual reverse-proxy address ranges. The port binds to `127.0.0.1:8080` by default; set `BOOKMARKSYNC_HTTP_BIND` to a specific host interface reachable by a containerized reverse proxy. On Linux, Docker-published ports can bypass UFW input rules; use a private proxy network or Docker-aware forwarding firewall rules such as the `DOCKER-USER` chain. See [Docker's published-port firewall guidance](https://docs.docker.com/engine/network/packet-filtering-firewalls/#docker-and-ufw). The first deployment should be reached only from the configured management range until GeoIP and the admin account are ready.

Google and Authentik client settings can be provided through the matching `BOOKMARKSYNC_OIDC_*` variables in the host environment or a private Compose `.env` file. OIDC callback URLs must match the configured public URL. Full-system backup and verification use `/tmp` for private staging; the Compose tmpfs defaults to 1 GiB. Increase `BOOKMARKSYNC_TMPFS_SIZE` above the uncompressed database plus archive working space when the data volume is larger.

Google and Authentik client settings can be provided through the matching `BOOKMARKSYNC_OIDC_*` variables in the host environment or a private Compose `.env` file. OIDC callback URLs must match the configured public URL. Full-system backup and verification use `/tmp` for private staging; the Compose tmpfs defaults to 1 GiB. Increase `BOOKMARKSYNC_TMPFS_SIZE` above the uncompressed database plus archive working space when the data volume is larger.

Create the first administrator using a masked prompt; the password is piped to the CLI and is not placed in an argument or environment variable:

```sh
read -r -s -p "Admin password: " ADMIN_PASSWORD
printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | docker compose exec -T bookmarksync /usr/local/bin/bookmarksync user create --username admin --password-stdin --admin --data-dir /var/lib/bookmarksync
unset ADMIN_PASSWORD
```

Configure remote backup providers in the persistent rclone volume with `docker compose exec -it bookmarksync /usr/local/bin/rclone --config /run/bookmarksync/rclone/rclone.conf config`. The mounted volume is writable so rclone can persist OAuth token refreshes. Back up this credential volume separately and protect it as a secret; it is not part of the application backup archive. For local/NFS backup destinations, mount the host's already-mounted backup directory into the container through a Compose override and use that container path in Admin. Never place the live database on a network share.

Upgrade by rebuilding with the new source commit's identity args and recreating the container after reviewing a new repository revision. `docker compose down` preserves both named volumes; do not add `-v`. Compose updates are operator-run; the admin-approved host updater currently supports the Debian installer only. Docker images are built locally from source; GHCR publishing, automatic GeoIP scheduling, and browser-triggered Docker updates are not included yet. Before replacing an image, use the application's verified backup workflow and retain an independent copy of both named volumes.
