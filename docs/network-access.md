# Network Access Controls

Every HTTP route passes through the network gate, including Floccus synchronization, browser sign-in, the web interface, and health endpoints. The default country policy allows Saudi Arabia (`SA`) and India (`IN`). Unknown locations are denied. If the database is missing or invalid, **every address outside the explicit management CIDRs is denied**, including loopback. Private and local addresses are not exempt automatically. Set up either the database or management CIDRs before starting a new deployment or upgrading.

## Provision the country database

The database is not bundled with BookmarksSync and the server makes no network lookups. Obtain your own MaxMind GeoLite account and accept its [GeoLite EULA](https://www.maxmind.com/en/geolite/eula). On Debian and Proxmox LXC, the repository includes a setup command that installs a checksum-pinned [MaxMind GeoIP Update](https://github.com/maxmind/geoipupdate/releases) binary and schedules verified Country database updates twice weekly:

```sh
sudo scripts/debian/bookmarksync-geoip-setup
```

Setup prompts for the MaxMind Account ID and license key without echoing the key. It stores them in `/etc/bookmarksync/GeoIP.conf`, owned by root with mode `0600`; credentials stay outside BookmarksSync runtime settings and backups. The updater first writes to a staging directory. BookmarksSync validates the MMDB edition and build time, then the helper atomically replaces the live file and restarts the configured service only when the database changed. Failed downloads and invalid files leave the current database in place.

MaxMind requires DNS and HTTPS access to its update service and the redirected database host; see [MaxMind's updater requirements](https://dev.maxmind.com/geoip/updating-databases/). Twice-weekly updates help keep the local copy current. After setup, inspect the schedule and run an update manually with:

```sh
systemctl list-timers bookmarksync-geoip-update.timer
sudo systemctl start bookmarksync-geoip-update.service
journalctl -u bookmarksync-geoip-update.service --since today
```

Validate a local database explicitly with `bookmarksync network validate-geo --file /path/to/GeoLite2-Country.mmdb`. The setup script supports Debian amd64 and arm64.

For Docker, use a validated MMDB through `BOOKMARKSYNC_GEOIP_DB`; the application mounts its database directory read-only. See [Docker operations](docker-operations.md) for the opt-in updater profile, protected MaxMind secret, scheduling and manual refresh commands. MaxMind credentials are never mounted in the application container.

This product includes GeoLite Data created by MaxMind, available from https://www.maxmind.com. The Country code is used only to apply the configured access policy; BookmarksSync does not expose visitor locations in its UI or API.

The server validates the database type and logs its build date. With `BOOKMARKSYNC_GEOIP_DB` configured, request-time lookups check for file changes at most once every 30 seconds. A missing or invalid database at startup keeps country lookups unavailable until a valid file appears; management CIDRs remain the recovery path. A valid atomic replacement refreshes the reader without a restart. A missing or invalid replacement preserves the last valid reader. The Debian updater also restarts its configured service after publishing a changed database. When managing a database manually, stage and validate it first, then atomically replace the file in the mounted directory.

If the database is missing or invalid, health checks and all other traffic are denied outside configured management CIDRs. Keep a local management path or configured management network available.

## Reverse proxies

Set `BOOKMARKSYNC_TRUSTED_PROXIES` to comma-separated CIDR ranges for the actual proxy peers (for example `127.0.0.1/32` or `192.168.10.0/24`). Only these peers may supply the visitor address, and only through `X-Forwarded-For`. The proxy chain must append its connecting peer at each hop. Configure every proxy hop that can connect to BookmarksSync. Missing or malformed chains from a trusted proxy are denied.

`Forwarded`, `X-Real-IP`, and `CF-Connecting-IP` are ignored. Ensure the final proxy sets or appends a valid XFF chain and prevent direct access around it. Do not trust whole LAN ranges unless every host in that range is a controlled proxy.

The same rules apply behind Nginx Proxy Manager, Pangolin, and Cloudflare Tunnel: identify the TCP peer that actually connects to BookmarksSync and verify that its XFF chain includes each proxy hop. A Cloudflare `CF-Connecting-IP` header alone is not used.

## Management networks and country rules

`BOOKMARKSYNC_MANAGEMENT_CIDRS` is an optional comma-separated list of explicit CIDR ranges that bypass country lookup. It is empty by default. This is for carefully controlled private administration paths; the server does not automatically exempt private, loopback, or link-local addresses. Behind a proxy, the CIDR must match the resolved client address, not the proxy's socket address.

Administrators can change allow/block country rules under **Administration → Country access**. Changes apply to the next request. The page shows whether the database is available, its build date, and the number of configured trusted proxies; it never exposes local paths or proxy CIDRs. Changing rules can block the current session. Recover locally with:

```sh
bookmarksync network reset-geo --data-dir /var/lib/bookmarksync
```

The reset restores `allow` with `IN,SA` and records a local system settings activity event. The process reads policy from SQLite on each request, so the reset takes effect while the server is running.

Example service environment:

```ini
BOOKMARKSYNC_GEOIP_DB=/var/lib/bookmarksync-geoip/GeoLite2-Country.mmdb
BOOKMARKSYNC_TRUSTED_PROXIES=127.0.0.1/32,192.168.10.20/32
BOOKMARKSYNC_MANAGEMENT_CIDRS=192.168.10.0/24
```

Debian/LXC provisioning and scheduled updates are automated by the setup script. Final live MaxMind and public proxy/country acceptance require operator credentials and a public route.
