# BookmarksSync approved design

Approved 2026-11-16. Owner: Atik Ansari. Source: repository planning conversation.

Milestone designs refine this specification without replacing it:
- [Identity and API](design/identity-api.md), approved 2026-11-17. Its operator and user guide is [docs/authentication.md](authentication.md), and its API contract is [api/openapi.yaml](../api/openapi.yaml).
- [Floccus compatibility status](floccus-compatibility.md) records the compatibility gate evidence.

## Architecture and delivery
- Standalone Go server embeds a React/TypeScript UI; local SQLite; approximately 20 users and 10,000 bookmarks/user.
- Linux amd64 server deployments: Debian systemd in Proxmox LXC and Linux Docker. Native Windows server support is removed by the [2026-11-24 Linux-only decision](superpowers/specs/2026-11-24-linux-only-design.md). Windows browser clients remain supported. Test LXC: 192.168.10.226; public route and provider credentials remain operator inputs.
- Public MIT repository https://github.com/atikansari-ghr/BookmarksSync. Sole attribution Atik Ansari; git author atikansari <atikansari@ansariz.biz>. Retain third-party notices. No assistant credits.
- Use scoped implementation tasks with independent supervision: review actual diffs, run independent checks, fix and retest.

## Sync and organization
- Existing unmodified floccus in desktop Chrome, Edge, Firefox, Brave. Implement required Nextcloud Bookmarks API compatibility without Nextcloud dependency.
- Native bookmarks bar and other bookmarks map to separate canonical roots. One private collection per account. Extra floccus profiles are mappings, not extra user collections.
- Default initial merge with bootstrap backup; admin default per account, user override wins. Website provides exact extension instructions, cannot remotely enforce client settings. Disable browser-vendor bookmark sync.
- Stock floccus conflict behavior accepted; last accepted write wins, not original offline-edit chronology. Floccus 5.13.0 resolves a same-folder same-URL move with a DELETE-only request. If that removes the source's last placement and exactly one same-URL record remains visible in the caller's scope, copy the source title/tags to that survivor before trashing the source. An intentional delete of the last placement can therefore update that one visible duplicate; hidden records and ambiguous multiple survivors are never merged. Automatic background sync within minutes while browser is active; manual sync in extension. Server knows last contact, not complete remote browser state.
- Private plus shared folders. Every user can share with existing users. Owner alone controls membership. View/edit permissions. Separate floccus connections for native shared folders; verify download-only behavior for read-only shares.
- Folder and bookmark CRUD, nesting/order, drag/drop, multi-selection, list default/grid optional, search, tags, favorites, HTML/XBEL import/export. Favorites belong to each user and follow a bookmark across its folder placements; favoriting a shared bookmark never changes the owner's or another recipient's favorite state. A recipient of a view-only share may favorite bookmarks it can see; that changes neither the bookmark nor the grant. Favorites are web/API metadata only and are not synced to Floccus or included in Floccus hashes.
- Duplicate review, no automatic deletion. Preserve repeated URL placements in folders. Broken-link checking PUBLIC ONLY with SSRF defenses across DNS/redirects/IPv6. User domain/address/CIDR exclusions only narrow access. No page archiving.
- Trash 30 days. Full restore/rollback automatically resumes sync (explicitly accepted risk that old state can propagate); retain pre-restore snapshot. Individual recovery adds/restores items without rolling back others' shared content.

## Accounts, privacy, API
- Admin-created accounts only. Passkeys or password, optional TOTP/recovery codes. Google and Authentik OIDC linked by signed-in user after reauthentication, no automatic signup or email-only identity matching.
- Per-connection revocable app credentials independent of interactive login. Scoped personal API tokens. Versioned REST /api/v1 and OpenAPI, full user operations plus role-protected account/settings/backup/connection management; no webhooks required.
- Admin sees overall AND per-user bookmark counts/storage but cannot browse private bookmarks via admin UI/API. Server filesystem and backups can expose stored data; do not claim end-to-end privacy from server operator.
- Suspend revokes sessions/tokens; transfer owned shares before account deletion. User restore cannot resurrect sessions or other users' data.

## Dashboards and branding
- Reference images: mocscreenshots/ (user dashboard, admin dashboard, split login). Correct image-generation text errors, do not copy mistakes.
- User dashboard: configurable top Total Bookmarks, Recent Changes counts/drilldown, last-contact Sync Status, Duplicate Count/review. Folder-tree bookmark workspace below. Widgets show/hide/reorder/resize/reset, persisted per user.
- Admin widgets: users, per-user counts, storage, health, backups, updates, alerts, operational activity with no private bookmark content. Dedicated Server Health: CPU/RAM/disk/uptime/DB/sync errors/diagnostics.
- Classic, Ocean Glass, Midnight Glass, Warm Glass; light/dark, reduced transparency. Ocean Glass default. Branding controls: logo/background/colors/copy. Split-screen default login; centered-card and plain alternatives.
- Defaults: app BookmarksSync; heading Personal Bookmarks Server; tagline Your bookmarks. Your browsers. Your control.; supporting line A self-hosted home for your bookmarks. Always visible actual version/build and © <release year> Atik Ansari.

## Operations
- Weekly/monthly compressed backups; retain 8 weekly and 12 monthly per destination. Encryption optional per destination, default off; export recovery key separately when enabled.
- Backup ALL application data, accounts, credentials/secrets, system/user settings, themes/branding, permissions, manifest/schema/build and checksums. Not an OS/proxy backup.
- Multiple destinations: NAS via NFS/SMB, OneDrive, Google Drive. Pinned rclone for cloud/SMB; host-mounted NFS. Consistent local staging mandatory. Live database NEVER on network share. Independent destination retries, prune only verified successful uploads.
- Defaults proposed by implementer: timezone Asia/Riyadh, weekly Sunday 03:00, monthly first day 04:00; admin editable. Pre-update/restore local snapshots separate (last 3).
- Full restore fresh machine matching backup build then upgrade; preserve origin for passkeys/OIDC. Individual user bookmark/settings restore without replacing auth/session identity.
- Optional SMTP and Telegram admin alerts and user opt-in security/sharing alerts; verified bindings, no recovery secrets over Telegram.
- Private/public HTTPS via NPM or Pangolin/Cloudflare Tunnel. Explicit trusted proxies; resist forged forwarding headers. Country allow/block modes, default allow SA and IN, unknown deny for ALL public UI/sync/API. Local GeoIP provisioned on setup; missing DB public closed. Explicit private management/console recovery, not implicit socket-IP bypass behind proxy.
- One-command install/upgrade/repair/backup/restore/diagnostics/uninstall for each supported platform. Restricted host updater, never unrestricted web root/Docker socket.
- Every successful CI main build becomes immutable SHA-identified available update. Verify artifact authenticity/provenance; admin NOTIFIED and must click; no automatic updates. Preupdate snapshot, writes paused for migration, health check and binary+DB rollback on failure.
- Complete README/guides/screenshots/interactive API examples, step-by-step floccus per browser and shared mode, settings explanations/accessibility-friendly tooltips, proxy docs, troubleshooting, recovery drills.

## Compatibility gate
Before substantial dashboard development prove stock floccus roots/order/locking/deletion/offline/shared permissions on real clients. If stock extension cannot meet requirements STOP for design revision; never silently omit required features or claim protocol tests prove browser compatibility.
