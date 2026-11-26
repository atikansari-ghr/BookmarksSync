# Build Releases

Each successful push to `main` publishes a prerelease named `build-<full commit SHA>`. Pull-request builds never publish. Releases contain:

- `bookmarksync-linux-amd64`;
- `BUILD_INFO.txt` with the source commit, commit timestamp, and embedded version;
- `SHA256SUMS` covering the binary and the build information file.

The binary release job runs only after every CI gate, the Linux amd64 build, and image publication with registry digest attestation succeed. It creates a draft, attaches all assets, generates Sigstore-backed GitHub artifact attestations, then publishes the draft. This ordering keeps a build out of the Administration release catalog until its corresponding verified Docker image is available. Immutable releases are enabled for this repository, so GitHub locks release assets and the associated SHA tag when published.

## Verify a downloaded binary

Install the GitHub CLI and verify the checksum from the directory containing the downloaded assets:

```sh
sha256sum --check SHA256SUMS
gh attestation verify bookmarksync-linux-amd64 -R atikansari-ghr/BookmarksSync
```

Verify the attestations before installing an update. The checksum detects file changes; the attestation establishes that GitHub Actions built the files for this repository and commit.

## Debian and Proxmox LXC

The Debian amd64 control script supports fresh install, explicit-version upgrade, and repair. It requires root, systemd, `curl`, Python 3, and the GitHub CLI (the script installs `gh` from its official signed Debian repository if needed). It downloads only published SHA-addressed prereleases and checks the checksum and GitHub attestations before changing the installed binary. The Admin Settings page lists eligible release metadata and, on a Debian host where the installer configured its restricted updater, allows a recently reauthenticated administrator to explicitly queue the selected release. No update runs automatically.

See [Debian maintenance](debian-maintenance.md) for diagnostics, independent backup, restore into a new directory and uninstall commands that retain application data, configuration and backups.

```sh
sudo scripts/debian/bookmarksync install
sudo scripts/debian/bookmarksync upgrade --tag build-<full-40-character-commit-sha>
sudo scripts/debian/bookmarksync repair --tag build-<full-40-character-commit-sha>
```

First install prompts for the public HTTPS origin, management CIDRs for GeoIP outage access, and initial administrator credentials. The service listens on `0.0.0.0:8080` by default; configure the reverse proxy and firewall before exposing it publicly. Application data is in `/var/lib/bookmarksync`, the root-only environment file is `/etc/bookmarksync/bookmarksync.env`, and the service is `bookmarksync.service`. Configure GeoLite separately using [docs/network-access.md](network-access.md).

Before upgrade or repair, the script stops the service, creates and verifies a compressed full-system backup, and saves the previous binary and matching service configuration. It checks local readiness after replacement and restores the previous binary and data on failure. The three newest rollback snapshots are retained under `/var/lib/bookmarksync-updates/snapshots`. Keep an independent backup destination as described in [docs/backup-restore.md](backup-restore.md); local rollback snapshots do not protect against host or disk loss. A failed rollback restore leaves the service stopped and preserves both the failed data directory and snapshot for manual recovery.

Browser approvals create an empty full-SHA marker in the service-owned
`/var/lib/bookmarksync-update-queue/inbox`. A root-owned systemd path watcher
invokes a fixed worker, which claims and validates the marker before calling the
root-owned installer copy. The installer repeats release and provenance checks
before replacing the binary. The app retains `NoNewPrivileges=true` and neither
reads sudoers nor invokes sudo. Review jobs with
`journalctl -u bookmarksync-update-queue.service`. See
[Debian maintenance](debian-maintenance.md) for ownership, migration and tests.

Validate the script and isolated service lifecycle with `sudo bash scripts/debian/test-bookmarksync.sh` and `sudo bash scripts/debian/test-update-queue.sh`. These use mock release assets. A real systemd watcher handoff to a fixed fixture installer also passed on the authorized Debian LXC, including an unprivileged sender with NoNewPrivileges enabled. A first published `main` release and real provenance-verified installation remain pending.

## Docker image provenance

The image publication job uses a job-scoped GitHub token with package-write and attestation permissions. It embeds the same full commit, short-SHA version and commit timestamp as the binary, labels the OCI image with source/revision/date, emits BuildKit provenance, and attests the pushed registry digest. Pull-request jobs cannot publish. No mutable latest tag is produced. A SHA tag is an identifier, not registry immutability: record and deploy the verified digest.

After the first successful publication, resolve the SHA tag to its digest, then verify and retain that exact reference:

```sh
gh attestation verify oci://ghcr.io/atikansari-ghr/bookmarksync@sha256:<digest> -R atikansari-ghr/BookmarksSync
docker pull ghcr.io/atikansari-ghr/bookmarksync@sha256:<digest>
```

See the official [GitHub image attestation guide](https://github.com/actions/attest#container-image) and [Docker build provenance options](https://docs.docker.com/reference/cli/docker/buildx/build/#provenance).

## Docker Compose

Use [Docker operations](docker-operations.md) for opt-in GeoLite scheduling, protected credential setup and lifecycle helper commands. Set `BOOKMARKSYNC_IMAGE` to a verified `ghcr.io/atikansari-ghr/bookmarksync@sha256:<digest>` when deploying a published image; use `docker compose pull` followed by `docker compose up -d --no-build` after making an independent backup. Leave it unset for local builds.

Before starting, create a private `.env` file beside `compose.yaml`. Compose loads it automatically, and the repository ignores it. Configure either an exact management client CIDR or provide a valid GeoLite database; without one, every HTTP route stays blocked. For example, replace the documentation-only addresses below with your actual client and proxy addresses:

```dotenv
BOOKMARKSYNC_MANAGEMENT_CIDRS=127.0.0.1/32,192.0.2.25/32
BOOKMARKSYNC_GEOIP_DIR=./geoip
BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com
BOOKMARKSYNC_TRUSTED_PROXIES=192.0.2.10/32
```

Put `GeoLite2-Country.mmdb` in `./geoip/` if using GeoIP. Build and start the local image from the repository root. Set build identity from the checked-out commit so the UI, CLI and backup manifest can identify the source:

```bash
commit="$(git rev-parse HEAD)"
export BOOKMARKSYNC_COMMIT="$commit"
export BOOKMARKSYNC_VERSION="0.0.0-${commit:0:12}"
export BOOKMARKSYNC_DATE="$(git show -s --format=%cI "$commit")"
docker compose build --pull
docker compose up -d
```

The image builds the Go service and includes checksum-pinned rclone v1.76.0 for the application's remote backup destinations. It runs as UID/GID 65532, drops Linux capabilities, uses a read-only root filesystem, and stores SQLite data and rclone's OAuth configuration in separate named volumes. The country gate remains active: a valid `GeoLite2-Country.mmdb` under `./geoip` or an exact `BOOKMARKSYNC_MANAGEMENT_CIDRS` setting must allow your management client. If neither is configured and available, requests are denied. Do not use a broad management range as a shortcut around country policy.

The container readiness probe calls `/readyz` through `127.0.0.1`. Include `127.0.0.1/32` in the explicit management CIDRs for that probe, alongside the exact operator ranges. Loopback has no automatic country-policy exemption. A working external route alone does not establish a healthy container.

Set `BOOKMARKSYNC_PUBLIC_URL` to the external HTTPS origin when using browser sign-in or OIDC. Set `BOOKMARKSYNC_TRUSTED_PROXIES` only to the actual reverse-proxy address ranges. The port binds to `127.0.0.1:8080` by default; set `BOOKMARKSYNC_HTTP_BIND` to a specific host interface reachable by a containerized reverse proxy. On Linux, Docker-published ports can bypass UFW input rules; use a private proxy network or Docker-aware forwarding firewall rules such as the `DOCKER-USER` chain. See [Docker's published-port firewall guidance](https://docs.docker.com/engine/network/packet-filtering-firewalls/#docker-and-ufw). The first deployment should be reached only from the configured management range until GeoIP and the admin account are ready.

Google and Authentik client settings can be provided through the matching `BOOKMARKSYNC_OIDC_*` variables in the host environment or a private Compose `.env` file. OIDC callback URLs must match the configured public URL. Full-system backup and verification use `/tmp` for private staging; the Compose tmpfs defaults to 1 GiB. Increase `BOOKMARKSYNC_TMPFS_SIZE` above the uncompressed database plus archive working space when the data volume is larger.

Create the first administrator using a masked prompt; the password is piped to the CLI and is not placed in an argument or environment variable:

```bash
read -r -s -p "Admin password: " ADMIN_PASSWORD
printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | docker compose exec -T bookmarksync /usr/local/bin/bookmarksync user create --username admin --password-stdin --admin --data-dir /var/lib/bookmarksync
unset ADMIN_PASSWORD
```

Configure remote backup providers in the persistent rclone volume with `docker compose exec -it bookmarksync /usr/local/bin/rclone --config /run/bookmarksync/rclone/rclone.conf config`. The mounted volume is writable so rclone can persist OAuth token refreshes. Back up this credential volume separately and protect it as a secret; it is not part of the application backup archive. For local/NFS backup destinations, mount the host's already-mounted backup directory into the container through a Compose override and use that container path in Admin. Never place the live database on a network share.

For source deployments, rebuild with the new commit's identity args and recreate the container after review. For published images, the host maintenance helper verifies the selected digest's provenance and source commit, snapshots the current image/data, checks readiness, and restores the matching snapshot on activation failure. `docker compose down` preserves both named volumes; do not add `-v`. The optional restricted host worker enables explicit approval from Administration through a SHA-only inbox, without giving the app Docker access. See [Docker operations](docker-operations.md) for setup, upgrade, repair, rollback and independent recovery.

The workflow is prepared to publish Linux amd64 images to `ghcr.io/atikansari-ghr/bookmarksync:build-<full-commit-sha>` only after successful main CI and the Linux binary build. Image publication and its registry digest attestation must finish before the binary prerelease becomes visible to the Administration catalog. The first live publication, registry access/visibility, provenance verification and pull-based deployment remain pending. Local builds remain available. Before replacing an image, use the verified backup workflow and retain independent copies of both named volumes and separately exported destination recovery keys.
