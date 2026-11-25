# Docker operations on Linux amd64

Use Docker Engine with the Compose plugin on a Linux amd64 host. The application
runs as UID/GID 65532, with a read-only root filesystem, all capabilities dropped,
and no Docker socket. The optional updater uses the same restrictions. Docker
control belongs to the host operator; the web UI cannot upgrade containers.

## Opt-in GeoLite2 Country updates

The default application mount reads `./geoip/GeoLite2-Country.mmdb`. You can supply
that database yourself or enable the `geoip` profile. Configure the public URL,
management CIDRs, trusted proxies and country policy as described in
[network access](network-access.md), before enabling country restrictions.
Include `127.0.0.1/32` explicitly in management CIDRs for the container's local
readiness probe; private/loopback addresses are never exempt automatically.

Create the shared directory and protected secret file outside the repository:

```sh
sudo install -d -o 65532 -g 65532 -m 0755 /srv/bookmarksync-geoip
sudo install -d -o root -g root -m 0700 /etc/bookmarksync-docker
sudo install -o 65532 -g 65532 -m 0600 /dev/null /etc/bookmarksync-docker/GeoIP.conf
sudoedit /etc/bookmarksync-docker/GeoIP.conf
```

Enter these directives in that protected file, replacing the two placeholders:

```text
AccountID YOUR_MAXMIND_ACCOUNT_ID
LicenseKey YOUR_MAXMIND_LICENSE_KEY
EditionIDs GeoLite2-Country
```

Keep the file owned by 65532 with mode 0400 or 0600 after editing. Compose local
file secrets are bind mounts: its `uid`/`gid`/`mode` settings do not remap an
incorrectly owned source file. The updater rejects readable-by-others files.
Never put credentials in `.env`, shell arguments, a Dockerfile, or build args.
The build context is an allowlist that excludes configuration and MMDB files.

Set these non-secret deployment variables in your operator environment:

```sh
export BOOKMARKSYNC_GEOIP_DIR=/srv/bookmarksync-geoip
export BOOKMARKSYNC_MAXMIND_CONFIG=/etc/bookmarksync-docker/GeoIP.conf
export BOOKMARKSYNC_GEOIP_INTERVAL=86400
docker compose --profile geoip build
docker compose --profile geoip run --rm --no-deps geoip-updater once
docker compose --profile geoip up -d --no-build
```

Intervals are 3600–604800 seconds. The updater runs at startup and retries after
each interval, including after failure. It uses the official MaxMind
[geoipupdate 8.0.0 release](https://github.com/maxmind/geoipupdate/releases/tag/v8.0.0),
whose amd64 archive SHA-256 is pinned in the Dockerfile. This separate local
`geoip-updater` target includes the updater binary and upstream licenses; the
ordinary application image does not. The app's published GHCR image does not
replace this locally built maintenance image.

Each update downloads into a private, unique directory on the shared filesystem,
validates with `bookmarksync network validate-geo`, then renames a complete valid
file into place. Unchanged files are left alone. Download, missing file and
validation failures preserve the current database. Only fixed status messages
are logged; downloader output is suppressed. There is no Docker socket or
service restart. The app checks for atomic replacements on lookup at most once
every 30 seconds and retains its previous reader if a replacement is invalid.
Mount the directory, rather than the individual file, to make replacements
visible. A killed updater may leave `.update-lock` and `.staging.*` directories;
stop the updater, confirm no updater process remains, and remove those specific
stale directories before starting it again.

Check without printing credentials:

```sh
docker compose --profile geoip logs --tail 20 geoip-updater
docker compose exec -T bookmarksync network validate-geo --file /geo/GeoLite2-Country.mmdb
curl --fail http://127.0.0.1:8080/readyz
```

These are operator acceptance checks. Fake-updater regression tests do not prove
a live MaxMind account, download, country lookup or production policy works.

## Host maintenance commands

Run `bash scripts/docker/maintain.sh` from the checked-out deployment repository
as root, or as an operator with Docker access and permission to write backup
metadata in the independent backup directory. Use the same environment used for
Compose (select it again when switching to a root shell). The helper fixes its Compose file
and project directory to that checkout. Retain that checkout path and normal
Compose project name across upgrades so the same named volumes are selected.
It accepts canonical absolute paths without symlinks or dot segments, rejects
system directories, and never removes volumes or host data.
Host maintenance requires Bash, coreutils (including `timeout`), util-linux
`flock`, Docker Compose and, for upgrades, GitHub CLI `gh`. Mutating commands share
a deployment lock under the checkout's ignored `.tools/` directory; always use
this deployment checkout for maintenance.

```sh
# Independent backups: the container user needs write permission here.
sudo install -d -o 65532 -g 65532 -m 0700 /srv/bookmarksync-backups
bash scripts/docker/maintain.sh backup /srv/bookmarksync-backups
bash scripts/docker/maintain.sh diagnostics

# Restore to a NEW directory in a pre-created writable parent, for inspection.
sudo install -d -o 65532 -g 65532 -m 0700 /srv/bookmarksync-restores
bash scripts/docker/maintain.sh restore /srv/bookmarksync-backups/ARCHIVE.zip /srv/bookmarksync-restores/recovered
```

Backup stops the app to keep database and asset files consistent, creates and
verifies a full archive, then restarts it only if it was running. Backup failure
also attempts that restart. Backups include sensitive runtime configuration and
the sign-in encryption key (`instance.key`) when present: protect the directory,
copy archives to independent storage, and test recovery. Destination backup
encryption recovery keys are excluded from every application archive. For each
encrypted destination, use **Administration → Backups → Download recovery key**
after recent authentication, save the key in separate protected storage, and
verify that you can recover an encrypted archive with that exported key. See
[backup encryption and recovery](backup-restore.md#optional-destination-encryption).
Keep independent protected copies of the rclone configuration volume, the
MaxMind secret file and deployment environment as well; application archives do
not contain these host-managed files. This helper handles ordinary ZIP archives; use the documented
application backup CLI for encrypted archive recovery with a protected key file.
Restore writes a fresh offline directory; it never overwrites the live volume.
To select restored data, stop the deployment and explicitly configure a new
deployment mount after reviewing the restored directory and file ownership.

## Upgrade, rollback and removal

Choose a reviewed published image digest, or a SHA-addressed build tag from a
successful `main` build. Publication is prepared in CI; no first image is
assumed to exist. The example uses a placeholder that must be replaced:

```sh
bash scripts/docker/maintain.sh upgrade ghcr.io/atikansari-ghr/bookmarksync@sha256:REPLACE_WITH_64_HEX_DIGEST /srv/bookmarksync-backups
```

Upgrade pulls the selected image, resolves its registry digest, and verifies that
digest's GitHub attestation against this repository's `main` CI workflow before
stopping the service. A failure leaves the live deployment untouched. The
currently installed baseline image must contain this project's standard Docker
healthcheck; older development images without it need an offline, reviewed
migration to this baseline before managed upgrades.

The helper stops the app, creates and verifies an independent archive using its
currently installed image, and writes a protected `.snapshot` record pairing
that archive's SHA-256 with the exact previous local image ID. It starts the
verified replacement digest with `--no-build`, then makes up to 12 readiness
probes, each bounded by an 8-second host timeout and followed by a 5-second wait.
The application probe requests `/readyz` at loopback without printing its body.
Configure `127.0.0.1/32` as an explicit management CIDR: the country gate still
applies, so a missing exception intentionally makes the healthcheck fail.

On failed startup/readiness, the helper stops the replacement. It runs the
previous image's backup CLI offline to verify and restore the original archive
into a fresh directory inside the data volume. Only after successful restoration
does it move the failed live files (including hidden keys) into a retained
`.bookmarksync-failed-TIMESTAMP-ID` directory, move restored files into the live
root, and start the previous image. It checks readiness again and returns failure
to report the rejected upgrade even when recovery succeeds. A failed restore
leaves the app stopped and retains all recovery artifacts for operator inspection.
The previous binary is never started against migrated live data.
The fixed `backup-keys/` directory stays at its original live path, since full
archives deliberately exclude destination recovery keys; symlinks/unsafe key
types or an archive attempting to introduce that directory cause refusal.

For an explicit rollback, select the exact record printed by the earlier upgrade:

```sh
bash scripts/docker/maintain.sh rollback /srv/bookmarksync-backups/BOOKMARKSYNC.snapshot /srv/bookmarksync-backups
```

Rollback verifies the selected record/archive hash, checks its retained local
image, first creates a verified backup and record of the current state, then
restores the selected matching archive before starting that image. It accepts
no arbitrary image-only downgrade. Keep old images, records and archives together;
do not prune a recorded image. A missing image or altered archive causes refusal.
Recovery mounts this checkout's `restore-volume.sh` read-only, so older supported
images need not include the helper. It never deletes failed-data directories.

Persist the selected `BOOKMARKSYNC_IMAGE` printed by the helper in your protected
deployment environment before subsequent Compose commands. Check `/healthz`,
`/readyz`, `/api/v1/version`, login, and bookmark sync before restoring external
traffic. These host operations do not alter the GeoLite sidecar.

```sh
bash scripts/docker/maintain.sh uninstall
```

Uninstall stops/removes the application and optional updater containers/network.
It retains application and rclone volumes, GeoLite files, images, secrets,
restored directories and independent backups. It never uses `down --volumes`,
prune, or volume deletion. Remove retained sensitive material separately only
after you have verified recovery and retention requirements.

## Optional updates approved from Administration

The ordinary Compose deployment uses operator-run updates. To enable the same
explicit approval button as the LXC installation, install the restricted host
worker below. The application receives only an inbox mount; it never receives
the Docker socket, host credentials or a command interpreter on the host.

Use a trusted root-owned deployment checkout under `/opt` or `/srv`. Its parent
directories, `compose.yaml`, `compose.admin-updates.yaml`, maintenance scripts
and any existing `.tools` directory must be root-owned and not writable by other
users. The deployment `.env` must be root-owned mode `0600`. Install Docker
Engine/Compose, GitHub CLI, Python 3 and util-linux (`flock`) on the host. Create
an independent backup directory for the container's UID, then run:

```sh
sudo install -d -o 65532 -g 65532 -m 0700 /srv/bookmarksync-backups
sudo bash /opt/bookmarksync/scripts/docker/setup-admin-updates.sh \
  --deployment /opt/bookmarksync --backups /srv/bookmarksync-backups
sudo docker compose --project-directory /opt/bookmarksync \
  -f /opt/bookmarksync/compose.yaml -f /opt/bookmarksync/compose.admin-updates.yaml \
  up -d --no-build bookmarksync
```

Prepare the `.env`, image and existing healthy application before setup. Setup
does not request an update. In Administration, a recently reauthenticated admin
can select an eligible full-SHA release and explicitly approve it. The app writes
one empty SHA-named marker into the host inbox, bounded to 64 pending entries.
The root-owned systemd worker claims each marker into a private directory,
rejects invalid names, content, links and ownership, verifies the exact selected
build's registry digest and GitHub provenance, and invokes the host maintenance
workflow. Backups, readiness checks and snapshot rollback use the same safeguards
as manual updates. The worker atomically persists the selected image in the
protected `.env`; an update whose readiness or configuration persistence fails
restores the prior snapshot and image selection before reporting failure.

Inspect host status with `systemctl status bookmarksync-docker-updates.path` and
`journalctl -u bookmarksync-docker-updates.service`. Failed/rejected markers stay
under `/var/lib/bookmarksync-docker-updates/rejected`; do not resubmit a failed
update until its journal and retained snapshots have been inspected. Only the
inbox is mounted in the app. Protect and back up host configuration separately.
The worker is optional and does not bypass recent-authentication or published
release checks in the application's approval API.

Before removing an opted-in deployment, disable its host watcher with
`sudo systemctl disable --now bookmarksync-docker-updates.path`, then use the
normal uninstall helper. Preserve the queue, configuration and independent
backups until recovery and retention needs are resolved.

## Regression checks

On Linux, run `sh scripts/docker/test-geoip-update.sh` and
`bash scripts/docker/test-maintain.sh`. These use disposable directories and
fake executables; they do not contact MaxMind or a Docker daemon. A real image
build and isolated Docker smoke run through `sudo bash scripts/docker/test-runtime.sh`.
That test requires Linux amd64, Docker Engine/Compose, Python 3, network access
for pinned build dependencies and permission to assign its fixture files to UID
65532. It uses a fresh Compose project, random loopback port and disposable
volumes/images, then removes only those resources. It covers health, persistence,
runtime restrictions and simulated country policy with a bundled test MMDB,
including atomic publication without restart and invalid replacement retention.
It does not contact MaxMind or establish real public proxy/country acceptance.
`sh scripts/docker/test-restore-volume.sh` exercises offline file replacement and
retention of the failed database and hidden keys in disposable directories.
`bash scripts/docker/test-admin-updates.sh` covers isolated host-worker setup,
strict request validation and unsafe deployment state rejection.
`sudo bash scripts/docker/test-image-env.sh` verifies atomic image selection
persistence and restoration after failed readiness, using fake Docker commands
and a disposable deployment directory.

The [isolated watcher acceptance](host-worker-acceptance.md) separately exercises
real systemd `.path` orchestration, the current worker and a fixed fixture
maintainer. It passed on the authorized LXC on 2026-11-25, including failure
retention, unsafe-marker rejection and removal of all fixture units and files.
