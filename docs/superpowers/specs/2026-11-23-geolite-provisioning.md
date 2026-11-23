# GeoLite Provisioning Design

## Intent

Automate GeoLite2 Country installation and updates for Debian and the dedicated Proxmox LXC, so the server's default Saudi Arabia/India access policy works without manually downloading MMDB files. Failed or invalid updates must not replace a working database or weaken the country gate.

## Approaches

1. **App-managed downloads:** store MaxMind credentials in runtime settings and download from the server process. This centralizes control but expands sensitive settings, backups, API/UI, and runtime networking.
2. **Host-managed official updater (selected):** install a pinned MaxMind `geoipupdate` binary, store credentials in a root-only config, and run it with a systemd timer. Stage, validate, and atomically install changed databases; restart BookmarksSync only after a verified change. This follows MaxMind's updater and keeps its credentials out of application data.
3. **Manual file replacement:** simplest, but leaves the known setup and update burden unchanged.

## Scope and behavior

- Debian/LXC only in this slice; Docker and Windows need their own scheduling and volume integration later.
- Provisioning requires the operator's MaxMind AccountID and license key, entered without echo and stored in `/etc/bookmarksync/GeoIP.conf` with mode `0600`.
- Pin the official updater release and verify its published SHA-256 before installation. Configure only `GeoLite2-Country`.
- A twice-weekly systemd timer runs an updater helper. The helper writes to a staging directory, asks BookmarksSync to validate the MMDB type/build time, and keeps the live database untouched on every failure. For changed valid data, install through a same-filesystem temporary file and atomic rename, then restart the configured service.
- Missing credentials, network errors, invalid databases, and unchanged builds leave the running service/database intact. The existing fail-closed behavior remains for a new deployment with no valid database.
- The CLI gains `network validate-geo --file PATH` for safe validation and build-time reporting without printing credentials.

## Non-goals

No MaxMind secret in app runtime settings, Admin API, or full app backups; no automatic updater for Docker/Windows yet; no public-network country claim without an end-to-end path through the operator's proxy.

## Acceptance

Unit/CLI tests cover valid and invalid MMDB validation. Linux tests exercise the updater helper with a stub updater and fixture databases, including unchanged, changed, updater-failure, and invalid-download cases; failures preserve the known-good live file. Validate systemd unit syntax and run a LXC smoke with the stub updater. Real MaxMind download remains dependent on an operator-supplied credential and is not claimed unless configured.
