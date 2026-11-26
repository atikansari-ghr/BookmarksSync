# Linux-only verification — 2026-11-24

Scope: Debian/Proxmox LXC and Docker on Linux amd64. Native Windows services,
updater/IPC, installer, GeoLite administration, CI jobs and release assets were
removed. Portable development filesystem adapters and Windows browser clients
remain independent of the supported server deployment scope.

## Changes covered

- Linux-only release filtering, CI and attested binary/image publication. Images
  are published and attested before the corresponding release enters the catalog.
- Docker GeoLite scheduling with protected configuration, validated atomic
  publication and request-time reader refresh, including initially missing data
  and preservation of the last valid reader on bad replacement data.
- Debian and Docker maintenance, verified snapshots, readiness rollback,
  recovery-key retention, protected ownership and shared maintenance locks.
- Optional Docker approvals through a bounded SHA-only host inbox, protected
  worker configuration and atomic image selection persistence; no Docker socket
  or MaxMind secret is exposed to the application container.
- Login Flow v2, explicit consent and one-time credential issuance. A separate
  regression preserves typed JWKS transport failures through the pinned OIDC
  verifier, so a provider timeout is reported as unavailable rather than an
  invalid login. Authentication still fails closed.

## Validation

On the isolated Debian 13 Linux test LXC:

- `go test ./... -count=1` and `go vet ./...` passed. The full race run passed
  every package except an existing OIDC timeout-classification test; after its
  reproducing regression and fix, the full `internal/auth` normal suite, vet and
  race suite passed separately. No data races were reported.
- Debian installer, GeoLite and maintenance shell suites passed, including failed
  activation/rollback, root-owned executable checks, shared locking and recovery
  key preservation.
- Docker updater, maintenance, offline volume recovery, host queue and protected
  image environment suites passed with disposable fake host operations.
- Actual application and GeoLite-updater images built. The committed runtime
  regression passed non-root/read-only/capability restrictions, secret/socket
  isolation, country-policy denial and management access, readiness, embedded
  build identity, persisted administrator data and atomic MMDB refresh without
  restarting the app. The protected update queue was detected in the actual
  application container and had the required ownership and permissions.

Local frontend typechecking, all 80 component tests, the production build,
browser-harness unit suites, workflow dependency checks, release-manifest
assembly/checksum checks, actionlint, Go formatting and diff checks passed.
A final CGO-free Linux amd64 cross-build passed after the OIDC fix. Current
documentation links were checked and resolved.

Disposable real browser checks passed:

- Website sign-in → explicit connection consent → one-time polling → an app
  credential usable against the actual Floccus folder API. Desktop/mobile
  layout and script-inaccessible secrets were checked.
- The unmodified stock floccus 5.13.0 login action in Chrome opened its own
  consent tab and received usable credentials through its actual polling code.
- All 72 stock-sync checks passed across Chrome 158.0.8127.37,
  Edge 158.0.4370.33, Brave 158.0.8127.42 and Firefox 160.0.1. These include
  ordering, upload/download, edits, locks, read-only permissions, shared/private
  isolation and the documented same-folder collision behavior.

Test-owned profiles, temporary servers, Linux source trees, containers, images
and volumes were cleaned up. No existing deployed service was replaced.

## Remaining acceptance

The code and publication workflow remain local changes. The first live
main-branch release/GHCR publication and provenance-verified installation/pull
have not run. The root host worker was tested with isolated fake systemd/host
operations and a real application inbox mount; a live published-image approval
cycle remains part of that first-release acceptance.

Real MaxMind downloads, public HTTPS/proxy/country behavior, Google/Authentik
tenants and physical passkey devices require operator configuration and were
not exercised. The connection wizard's preceding account-type/URL forms,
other-browser connection flows, and broader offline conflict/failsafe/restore
continuation scenarios remain outside the completed browser coverage. These
limits do not reopen removed Windows server milestones.

Follow-up on 2026-11-25: Chrome's visible connection wizard and bounded
offline/conflict/failsafe/automatic restore continuation checks have since passed,
along with real isolated systemd watcher orchestration. See the
[supplemental verification record](verification-2026-11-25.md) for evidence and
remaining limits; this dated record describes the earlier coverage.
