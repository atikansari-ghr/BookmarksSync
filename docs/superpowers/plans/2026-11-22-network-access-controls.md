# Network Access Controls Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** This plan follows the approved Operations milestone in `docs/design.md`. Keep all work on the selected `build/bookmarksync` branch; do not push or change production services.

**Goal:** Restrict public BookmarksSync traffic by country while safely resolving client addresses behind explicitly trusted reverse proxies.

**Architecture:** Add an offline GeoIP country reader and a request middleware that resolves client IPs only through configured trusted proxy CIDRs. Store country allow/block rules in existing system settings, manage them in Admin, and provide a local CLI reset path if an administrator locks themselves out.

**Tech Stack:** Go `net/netip`, MaxMind GeoLite2 Country MMDB reader (pinned v2.5.0), existing SQLite system settings, React/TypeScript admin UI.

**Spec:** `docs/design.md` (Operations and proxy-chain review focus).

## Global Constraints
- Default public policy allows only `SA` and `IN`; unknown public locations are denied.
- Missing/unreadable country data denies public IPs. No private address is exempt unless its resolved client IP matches the explicit `BOOKMARKSYNC_MANAGEMENT_CIDRS` setting; the default list is empty.
- Forwarding headers are ignored unless the TCP peer matches a configured trusted-proxy CIDR.
- For a trusted peer, use only `X-Forwarded-For`; concatenate duplicate header lines in wire order, walk right-to-left, and deny missing/malformed chains. Ignore `Forwarded`, `X-Real-IP`, and `CF-Connecting-IP`.
- Never treat a trusted proxy's private socket address as the visitor address. The proxy must append the connecting peer; configure every hop in its chain as trusted.
- Read policy settings from SQLite on each request, so Admin updates and the local CLI reset apply without a restart. Reload the MMDB only after a restart.
- A local CLI reset is the recovery path; there is no network-level country bypass.
- Use the repository's TDD, sole-author attribution, no-push, and test-LXC constraints.

## Review Focus
1. Private and loopback peers must be denied unless explicitly listed as management CIDRs; a proxy's private socket address must never grant a visitor an exemption.
2. Forged `X-Forwarded-For` from an untrusted peer must not affect country checks or login rate limits.
3. Trusted proxy chains must resolve right-to-left and reject malformed or missing chains; ignored headers never override XFF.
4. Missing, unreadable, corrupt, wrong-kind, or no-result MMDB lookups must fail closed for public addresses; a replacement file is not active until restart.
5. Invalid country modes/codes and empty allow lists must not silently broaden access; empty block list allows all known countries but never unknown ones. CLI reset must restore `allow` + `IN,SA` and be effective while the server runs.
6. Every route, including login, Floccus sync, unregistered paths, health, and static UI, must share the same network policy. Denials are generic and set no cookies.

---

### Task 1: Parse trusted proxies and evaluate country policy

**Files:** Create `internal/network/clientip.go`, `geo.go`, and focused tests; modify `internal/config/config.go` and its tests; add a pinned `geoip2-golang/v2` v2.5.0 dependency and required third-party notices.

- [x] Test trusted and management CIDR parsing for IPv4/IPv6, whitespace, empty input, invalid entries, and host IPs expressed as `/32` or `/128`.
- [x] Test `ResolveClientIP` for direct peers, untrusted spoofed headers, trusted multi-hop XFF, duplicate XFF lines, IPv4-mapped IPv6, malformed/absent/overlong chains, forwarded ports, zones, `unknown`, and ignored `Forwarded`/`X-Real-IP`/`CF-Connecting-IP` headers.
- [x] Test policy defaults, allow/block modes, ISO country-code validation, empty lists, explicit management CIDRs, missing/corrupt/wrong-kind databases, MMDB build metadata, and unknown GeoIP results.
- [x] Add config fields/envs `BOOKMARKSYNC_TRUSTED_PROXIES`, `BOOKMARKSYNC_MANAGEMENT_CIDRS`, and `BOOKMARKSYNC_GEOIP_DB`; parse CIDRs at startup and expose only sanitized status (not the filesystem path).
- [x] Implement a reusable CountryLookup interface and an MMDB adapter; use the database read-only and never perform network lookups.
- [x] Run focused `go test ./internal/network ./internal/config`.

### Task 2: Enforce policy before every handler

**Files:** Modify `internal/server/server.go` and tests; wire configuration and policy loading in `cmd/bookmarksync/main.go`; update auth tests if rate-limit behavior changes.

- [x] Add failing HTTP tests proving allowed-country, blocked-country, private-client, unknown-country, missing-database, and policy-read-error outcomes across `/`, `/api/v1/auth/login`, Floccus paths, health, and API routes.
- [x] Add forged-header, ignored-header, trusted-proxy chain, explicit private-CIDR, and malformed-XFF integration tests; verify rate-limit keys use the resolved visitor IP after `RemoteAddr` normalization.
- [x] Implement outer middleware shared by all registered and built-in handlers; respond with a small generic 403 for denied clients and 503 for unavailable policy storage.
- [x] Ensure absent GeoIP data denies all clients outside explicit management CIDRs; startup warnings name this behavior without logging visitor IPs.
- [x] Verify the policy still gates Floccus when `--public-url` is absent; a live server reset test covers SQLite WAL updates.
- [x] Run focused server/auth/CLI tests and full Go tests.

### Task 3: Admin policy controls and local recovery

**Files:** Modify `internal/identity/admin.go`, `internal/api/api.go`, `api/openapi.yaml`, API tests, `cmd/bookmarksync/admin.go` and `main.go`; create `web/src/components/AdminNetworkAccess.tsx`; update `web/src/pages/AdminPage.tsx` and web tests.

- [x] Add tests for admin/recent-auth route policy, GET defaults and database build date/status, PATCH allow/block rules (including empty block list), invalid codes/mode, audit events, denied non-admin requests, and backup archive policy round-trip.
- [x] Add `GET/PATCH /api/v1/admin/network-access`; PATCH validates and stores mode and country codes in the existing system settings transaction.
- [x] Show database availability and country controls in Admin, with a clear warning that changing countries can end the current session's access.
- [x] Add `bookmarksync network reset-geo --data-dir DIR` to restore the default `allow` policy for `IN,SA`, record a system-actor audit event, and verify reset takes effect immediately on a running server while SQLite WAL is open.
- [x] Update OpenAPI and run API and web focused tests, typecheck, and build.

### Task 4: Operator guide and acceptance

**Files:** Update `README.md`, `docs/authentication.md`, `docs/implementation-plan.md`, and add a focused network-access guide.

- [x] Document separate GeoLite2 provisioning (never bundled), environment variables, exact XFF requirements and ignored headers for NPM/Pangolin/Cloudflare Tunnel, management CIDRs, restart requirements, and CLI recovery. Debian/LXC scheduled provisioning is now delivered in `docs/superpowers/plans/2026-11-23-geolite-provisioning.md`; Docker/Windows remain pending.
- [x] Run `go test -count=1 ./...`, `go vet ./...`, `npm test`, `npm run typecheck`, `npm run build`, and CGO-free Linux/Windows builds.
- [x] Validate the built server in the isolated LXC. Preserved its data snapshot and previous binary; the existing public-network path remains operator-dependent because this workstation has no HTTP route to the LXC.
- [x] Record tested and operator-dependent cases; update the Operations milestone only for the delivered slice.

## Review Fix Round (Claude Code)

- [x] Preserve explicit management access when policy storage is unavailable.
- [x] Normalize or reject IPv4-mapped IPv6 CIDR prefixes.
- [x] Validate exactly one two-letter country code per API list item.
- [x] Keep the rest of Admin usable when network-status loading fails.
- [x] Document that a missing MMDB and empty management list denies every client, including loopback.
