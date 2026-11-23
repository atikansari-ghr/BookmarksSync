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

The Debian amd64 control script supports fresh install, explicit-version upgrade, and repair. It requires root, systemd, `curl`, Python 3, and the GitHub CLI (the script installs `gh` from its official signed Debian repository if needed). It downloads only published SHA-addressed prereleases and checks the checksum and GitHub attestations before changing the installed binary.

```sh
sudo scripts/debian/bookmarksync install
sudo scripts/debian/bookmarksync upgrade --tag build-<full-40-character-commit-sha>
sudo scripts/debian/bookmarksync repair --tag build-<full-40-character-commit-sha>
```

First install prompts for the public HTTPS origin, management CIDRs for GeoIP outage access, and initial administrator credentials. The service listens on `0.0.0.0:8080` by default; configure the reverse proxy and firewall before exposing it publicly. Application data is in `/var/lib/bookmarksync`, the root-only environment file is `/etc/bookmarksync/bookmarksync.env`, and the service is `bookmarksync.service`. Configure GeoLite separately using [docs/network-access.md](network-access.md).

Before upgrade or repair, the script stops the service, creates and verifies a compressed full-system backup, and saves the previous binary and matching service configuration. It checks local readiness after replacement and restores the previous binary and data on failure. The three newest rollback snapshots are retained under `/var/lib/bookmarksync-updates/snapshots`. Keep an independent backup destination as described in [docs/backup-restore.md](backup-restore.md); local rollback snapshots do not protect against host or disk loss. A failed rollback restore leaves the service stopped and preserves both the failed data directory and snapshot for manual recovery.

Validate the script and its isolated service lifecycle with `bash scripts/debian/test-bookmarksync.sh`. The automated flow has been exercised on a Debian 13 amd64 Proxmox LXC using mock release assets. A first published `main` release and a real provenance-verified installation remain pending. Docker/Windows installers and admin-interface update approval/catalog are not included yet.
