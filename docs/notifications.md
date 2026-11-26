# Notifications

Email and Telegram delivery is optional. Configure providers under **Administration → Notifications**. Saving credentials and sending provider tests require recent administrator authentication. Passwords and bot tokens are stored in the private `runtime.json`; blank secret fields preserve saved values, and the clear buttons remove them. Full-system backups include this file, so protect backup archives as credentials. User-only recovery does not copy notification channels or preferences.

## Email (SMTP)

Enter the SMTP host, port, sender address, and TLS mode. Choose implicit TLS when the server expects TLS at connection start, or STARTTLS when it upgrades the SMTP connection. TLS certificate verification is always enabled. Username and password are optional only when the SMTP server allows unauthenticated mail. The administrator alert recipient is required to test and receive administrator notices; user email verification can use the same SMTP server without an admin recipient.

## Telegram

Create a bot with Telegram's BotFather and copy its token into the Bot token field. To verify a personal chat, start the bot in Telegram, find that chat's numeric ID using a trusted method, then enter the ID in Account security. The server sends an eight-digit code through the HTTPS Bot API; enter it in the account page within ten minutes. BookmarksSync does not poll Telegram updates and does not expose a webhook.

## User channels and preferences

In **Account security → Security and sharing alerts**, add an email address or Telegram chat ID and confirm the code delivered there. Verification codes expire after ten minutes, allow at most five confirmation attempts, and can be requested once per account/channel and once per destination each minute. Destination limits are stored as hashes. Channels are private to the signed-in account. Security and sharing alert preferences default off and require at least one verified channel to deliver anything. Removing a channel cancels its pending verification.

Messages summarize only that an account-security or sharing setting changed. Security notices follow password, passkey, authenticator/recovery-code, session, linked sign-in identity, API token, Floccus-connection, notification-channel, and administrator reset changes. Sharing notices follow share creation, permission updates, and removal. They never include passwords, recovery codes, passkey challenges, API credentials, bookmark titles, or URLs. Delivery runs outside the request that caused an alert; provider outages do not reverse the account action. A bounded queue drops excess notices during sustained outages. Admin shows aggregate delivery failures since the current process start without recipient or message details.

## Administrator alerts and troubleshooting

Select backup success, backup failure, and account-security changes separately. Backup alerts are generated after each scheduled or manual run. Admin activity alerts cover account administration actions. The test buttons send neutral test messages. A failed test returns a generic message; check the mail server or Telegram bot configuration on the host without copying secrets into logs or support requests.
