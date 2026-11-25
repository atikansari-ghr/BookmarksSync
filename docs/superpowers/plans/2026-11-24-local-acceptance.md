# Remaining local acceptance

Continues the completed Linux-only implementation plan and its verification
record. Scope remains Debian/Proxmox LXC and Linux Docker amd64. Existing
uncommitted implementation changes are the baseline; do not redo them.

## Tasks

- [x] Drive the unmodified Floccus connection wizard's account type and server URL
  forms, website sign-in/consent, and completed connection in a disposable browser.
  Keep the ordinary connection smoke independent of stock-extension assets.
- [x] Exercise real stock-Floccus offline edits, reconnect/conflict and failsafe
  behavior, and continuation after verified full-server restore while retaining
  the browser's cache. Record actual outcomes and bounded coverage, preserving the
  approved last-accepted-write policy and accepted restore propagation risk.
- [x] Test a real isolated systemd.path → host worker → fixed fixture maintainer
  cycle on the authorized Linux LXC. Use unique runtime units and a private test
  root. Do not install production worker units, publish images, or change the
  existing service. Keep provenance/image activation acceptance separate.
- [x] Independently review the new acceptance harnesses, run their unit and actual
  browser/Linux checks, reconcile the main milestone/coverage documents and
  record any remaining external prerequisites.

## Validation boundaries

All browser profiles, credentials, databases, archives, unit names and Linux
runtime directories must be test-owned. A failed cleanup fails the check.
Distinguish PASS, FAIL, BLOCKED and PENDING. Never replace stock extension code or
infer real behavior from protocol tests. Do not infer that a fixture host worker
establishes a live published-image upgrade.

The first published release/GHCR installation, real MaxMind downloads, public
HTTPS/proxy/country behavior, live Google/Authentik tenants and physical passkey
devices still need external configuration. This plan does not silently claim
those gates or publish/merge/deploy to production.

Completed local acceptance and exact limitations:
[verification-2026-11-25.md](../../verification-2026-11-25.md).
Wizard screenshots remain optional diagnostics because capture can stall on the
stock extension's sync-options screen; form and upload assertions are mandatory.
