# Email and Telegram Notifications Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> Follow the repository TDD and verification workflows. Keep SMTP and Telegram optional and never log secrets or message bodies.

**Goal:** Implement the approved notification design in `docs/superpowers/specs/2026-11-23-notifications.md`.
**Configuration decision:** Admin UI; secret values live in private `runtime.json` and are included in full backups.
**Scope:** SMTP and Telegram channels, verified user bindings and opt-in categories, admin alert preferences, safe delivery, tests and documentation.

## Task 1: Secret-safe provider configuration

**Files:** `internal/config/runtime_file.go`, `internal/config/config.go`, configuration tests, startup wiring, `internal/backup/archive.go` if explicit staging needs extension.

- Add SMTP/Telegram settings to runtime configuration with strict validation and secret-safe formatting/errors.
- Add a serialized, atomic runtime settings update path that preserves unrelated runtime fields and survives restart.
- Prove full backup includes configured provider secrets and that ordinary API responses/logs never reveal them.
- Add admin read/update/test endpoints with recent-auth policy; reads expose only safe configured flags and fields.

## Task 2: Notification delivery and administrator alerts

**Files:** new `internal/notifications` provider/dispatcher files and tests; `cmd/bookmarksync/main.go`; backup run completion hooks; API event hooks.

- Implement SMTP delivery with required TLS and bounded timeouts; implement Telegram Bot API send and signed-in code confirmation without polling/webhooks.
- Keep sending asynchronous from request/backup operations; queue is bounded, failures are safe and observable, shutdown drains within a bound.
- Add admin-selectable backup success/failure and account/security alert categories.
- Test provider errors, timeout, TLS refusal, secret/message redaction, and that delivery failure does not undo the triggering operation.

## Task 3: Verified user channels and preferences

**Files:** migrations `0011_notifications.sql` and `0012_notification_rate_limits.sql`; `internal/notifications` user service/store; API auth/account routes, OpenAPI and tests; account-security UI and tests.

- Add salted-hash, expiring, single-use email codes and Telegram confirmation codes; enforce per-user/channel and destination-hash rate limits.
- Ensure signed-in confirmation cannot bind another user’s channel and remove only the caller’s binding.
- Add user opt-in toggles for security and sharing notices; default all off.
- Emit generic account-security and share lifecycle events without secrets/bookmark details.
- Test expiry, replay, rate limits, isolation, opt-out, no content leaks, and transactional behavior.

## Task 4: Admin UI, documentation, verification

**Files:** `web/src/components/AdminNotifications.tsx`, admin/user tests, `README.md`, `docs/notifications.md`, `docs/implementation-plan.md`, generated `internal/webui/static`.

- Add provider settings, redacted configured state, test actions, alert categories, help text, and user channel verification/preferences.
- Document SMTP TLS, Telegram bot setup, backup inclusion, channel binding, privacy limits, and troubleshooting.
- Run Go tests/vet, web tests/typecheck/build, OpenAPI parity, Linux/Windows builds, and isolated LXC smoke.
- Update the operations milestone honestly and commit with Atik Ansari as sole author; do not push.

## Validation record

- Go: `go test ./... -count=1` and `go vet ./...` pass. Coverage includes SMTP implicit TLS and generic Telegram errors, secret redaction/full-backup inclusion, verification expiry/replay/five-attempt lockout, per-account and per-destination throttles, cross-account isolation, opt-in delivery, queue drain, and OpenAPI/auth parity.
- Web: `npm test` passes (75 tests); typecheck and production build pass.
- Portability: CGO-free Linux/amd64 and Windows/amd64 builds pass.
- LXC: the isolated `bookmarksync-test.service` was upgraded after preserving prior binaries and data. `/readyz` returned ready, the user notification API rejected anonymous access with 401, and the embedded notification UI asset was served. SMTP and Telegram delivery were validated with local test servers; no real provider credentials were configured on the LXC.
