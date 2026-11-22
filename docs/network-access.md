# Network Access Controls

Every HTTP route passes through the network gate, including Floccus synchronization, browser sign-in, the web interface, and health endpoints. The default country policy allows Saudi Arabia (`SA`) and India (`IN`). Unknown locations are denied. If the database is missing or invalid, **every address outside the explicit management CIDRs is denied**, including loopback. Private and local addresses are not exempt automatically. Set up either the database or management CIDRs before starting a new deployment or upgrading.

## Provision the country database

Download a MaxMind GeoLite2 Country MMDB through your MaxMind account and place it in a protected local file. The database is not bundled with BookmarksSync and the server makes no network lookups. Set `BOOKMARKSYNC_GEOIP_DB=/path/to/GeoLite2-Country.mmdb` for the service. The server validates the database type at startup and logs its build date. Replace the file and restart to load updates.

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
BOOKMARKSYNC_GEOIP_DB=/var/lib/bookmarksync/GeoLite2-Country.mmdb
BOOKMARKSYNC_TRUSTED_PROXIES=127.0.0.1/32,192.168.10.20/32
BOOKMARKSYNC_MANAGEMENT_CIDRS=192.168.10.0/24
```

Automated database provisioning and scheduled database downloads belong to the installer/operations milestone; operators currently update the MMDB separately.
