# Debian Installation Design

## Intent

Provide a host administrator one control command for installing and maintaining BookmarksSync on Debian or a Proxmox LXC. The installer consumes only the new SHA-addressed GitHub releases, verifies the downloaded Linux binary, and preserves the complete application data through upgrades.

## Goals and constraints

- Support Debian amd64 with systemd. Other OS/architectures remain separate work.
- First install may select the newest published `build-<40-character SHA>` release. Upgrades and repairs require an explicit release tag so they follow a human-selected version.
- Check the release SHA-256 manifest and verify the GitHub artifact attestation for the binary and expected repository/workflow before installing it.
- Run as a dedicated unprivileged service account; store configuration under `/etc/bookmarksync` and app state under `/var/lib/bookmarksync`.
- Before upgrade, stop the service and use the server's own full-system backup CLI. Retain three verified rollback snapshots and the previous binary.
- On failed readiness after upgrade, restore the prior binary and matching full-system snapshot, then verify readiness again. Never remove user data on uninstall by default.
- Keep secrets out of command arguments, logs, and world-readable files.

## Approaches considered

1. **Checksum-only downloads:** simple, but an attacker who can replace release assets can replace the checksum too.
2. **Pinned GitHub attestation verification (selected):** use the published checksum plus GitHub CLI verification scoped to this repo and workflow; this adds a verified CLI prerequisite but establishes build provenance.
3. **Manual binary installation:** avoids package setup but does not provide the requested one-command host maintenance.

## Design calls

- Expose `install`, `upgrade --tag`, and `repair --tag` through `scripts/debian/bookmarksync`.
- Fresh install asks for the public HTTPS origin, listen address, explicit management CIDRs, and first administrator credentials. GeoIP remains separately configured with `bookmarksync-geoip-setup`.
- Repair re-installs a verified selected binary and recreates the managed service files without replacing existing data.
- Use a test install root and fake system commands for isolated integration tests. The live LXC smoke must use a separate service name, port, and data directory.

## Out of scope for this slice

Docker/Windows installers, web-admin update approval/catalog, scheduled updates, database write-pause/migration maintenance mode, backup destination configuration, and uninstall/restore UI remain later tasks.
