# Notifications Design

## Goal and scope

Add optional email and Telegram delivery for administrator alerts and user-opt-in security and sharing notices. Delivery is disabled until configured. No one-time passwords, recovery codes, passkey challenges, API credentials, bookmark titles, or bookmark URLs may be sent in notifications.

## Configuration

Administrators configure SMTP host, port, TLS mode, username, password, sender address, and Telegram bot token in Administration. Secrets are stored only in the owner-private `runtime.json`, alongside existing OIDC credentials, and are included in full-system backup archives. API reads return configured booleans and nonsecret connection details only. Blank secret fields preserve existing values; explicit clear actions remove them. Save/test actions require recent admin authentication. A test action sends a neutral test message and reports only a generic result.

## Verified channels and preferences

Users can add an email address and verify it with a short-lived, single-use code delivered to that address. For Telegram, a user enters a chat ID, receives a short-lived single-use code from the bot, and confirms it in the signed-in account. This proves control without polling Telegram updates or exposing an inbound webhook. Bindings are scoped to the signed-in user; verification codes are hashed at rest and rate-limited. A user must verify a channel and opt in before it receives notices. Users may remove channels and disable categories at any time.

## Events and content

- Administrators may enable backup-run success/failure alerts and account/security administration alerts.
- Users may separately enable account-security and sharing alerts.
- Messages contain the event type, time, and a generic action summary. They do not contain sensitive credentials or bookmark content.
- Email uses authenticated SMTP with TLS required for every server. Telegram sends through the HTTPS Bot API and does not require a public webhook or inbound polling.
- Send work is queued outside request handlers. Delivery failures are recorded without recipient addresses, message bodies, or secrets and do not roll back the originating action.

## Operations and recovery

Secrets are part of the full-system runtime configuration backup. User channel bindings and preferences live in SQLite and follow normal full backup/restore rules. User-only recovery does not restore notification identities or preferences. If notification configuration is absent or invalid, the service remains available and delivery reports a safe unavailable state. There is no automatic account creation or identity linking based on email equality.

## Acceptance

Tests prove secret redaction and backup inclusion, verification expiry/replay/rate limits, cross-user isolation, opt-in behavior, nonblocking delivery errors, TLS enforcement, Telegram binding without webhook exposure, and that prohibited secret/bookmark content never enters messages or logs. Admin and user interfaces explain channel verification and opt-in status.
