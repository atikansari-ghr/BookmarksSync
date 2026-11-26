# Offline, conflict, failsafe and restore-continuation acceptance

This page covers the stock floccus 5.13.0 offline/reconnect, conflict, failsafe and
full-restore continuation behaviour, run by `tests/browser/offline-sync.mjs` against
a real BookmarksSync server. It records what was observed in one bounded slice. It
does not describe every possible floccus outcome.

Independent verification on 2026-11-25 passed with both disposable Chrome
profiles, including automatic continuation 148 s and 156 s after restart and
successful cleanup. Local report:
`.tools/browser-results/offline-sync-2026-11-25T05-37-44-116Z.json`.

The same scenario also passed on 2026-11-25 in Edge 158.0.4370.33,
Brave 158.0.8127.92 and Firefox 161.0, with stock floccus 5.13.0. Both profiles
in every run completed automatic post-restore sync and all cleanup checks passed.

| Browser | Automatic sync after restart, A / B | Local report under `.tools/browser-results/` |
| --- | --- | --- |
| Edge | 148 s / 157 s | `offline-sync-2026-11-25T08-33-31-882Z.json` |
| Brave | 145 s / 154 s | `offline-sync-2026-11-25T08-33-28-499Z.json` |
| Firefox | 136 s / 149 s | `offline-sync-2026-11-25T08-33-58-573Z.json` |

## What the harness does

```sh
node tests/browser/offline-sync.mjs --browser=chrome [--headed] [--auto-wait-ms=240000]
node --test tests/browser/test/offline-evidence.test.mjs
```

- Builds the server (`CGO_ENABLED=0`, `.tools/go`), creates a test-only user and
  three app credentials with the real CLI. The password goes only to child stdin,
  and app secrets stay in process memory. Everything is redacted before it is
  stored or printed.
- Opens two disposable profiles (A, B) of the unmodified extension and imports one
  account into each. Both accounts use the same user, separate credentials, the
  absolute native root and the `default` strategy, with failsafe on. Change-triggered
  and interval sync stay off until the restore phase, so the harness controls
  reconnect order. Syncs use floccus's own `syncAccount` background message. Edits
  use the native bookmarks API.
- Takes the origin offline by stopping the server process and checking that
  `/readyz` refuses connections. It reconnects by restarting the server on the same
  loopback port and data directory.
- All paths are inside a marked `.tools/offline-sync/run-*` directory. That
  directory is removed only after both browsers are closed (profiles deleted) and
  every server process has exited. A cleanup failure fails the run. Exit codes:
  0 PASS, 1 FAIL, 2 NOT_RUN, 3 BLOCKED, 4 PENDING.

## Cases and observed outcomes (stock floccus 5.13.0)

Seed: A uploads 8 bookmarks to a sandbox folder in the bookmarks bar, and B downloads
an identical tree. Each profile then holds floccus's per-account `cache`, `mappings`
and `continuation` storage entries. These entries are kept for the whole run.

| Case | Result | Observation |
| --- | --- | --- |
| Sync attempt while the origin is down | PASS | `E017: Network error…`; local edits stay |
| Edit/edit, offline. A edits first, B second; B reconnects first, A second | PASS | The last accepted `PUT /bookmark/<id>` came from A, so A's **earlier** edit is final on the server and in both profiles. The original edit order was not used. |
| Edit vs delete: delete accepted first (B), then the edit reconnects (A) | PASS (converged) | A's edit was not uploaded. The bookmark was removed from A, and it is absent everywhere. |
| Edit vs delete: edit accepted first (A `PUT` 200), then the delete reconnects (B) | PASS (converged) | B's `DELETE` was accepted. The bookmark is absent everywhere. |
| Local deletion of 5 of 6 bookmarks | PASS | Real `E029` (84%). No `DELETE` was accepted, the server is unchanged, and B is unchanged after its own sync. |
| State of A after the refused run | observation | Floccus does not revert the local deletion by itself. A `slave` (download-only) sync restored A, and the next default sync uploaded nothing. |
| Backup `create` + `verify` with the server stopped | PASS | `format 1, schema 14, 2 files` |
| Restore into an absent directory, restart on the same origin and credentials | PASS | The restored server holds exactly the pre-backup tree |
| Automatic continuation after restore, with no RPC after restart | PASS | Each profile ran its own alarm-driven interval sync (A after 145 s, B after 156 s), with only `GET` and lock requests. |
| Rollback propagation | PASS (converged) | A bookmark added after the backup was removed from both profiles. A bookmark deleted after the backup came back in both profiles. The restored (older) server state spread to the clients, which is the [accepted restore risk](design.md). |
| No unexpected damage | PASS | Every bookmark left untouched since before the backup is intact on the server, A and B. |

Delete beat edit in both reconnect orders tested. This matches stock floccus behaviour
for this slice. Treat it as an observation, not as a server guarantee.

## How automatic continuation was arranged

Before the restore, both accounts were switched to `syncIntervalEnabled: true` with
`syncInterval: 5`, the smallest step the 5.13.0 options page offers. The harness
writes these fields to the same `accounts` storage entry the options page saves.
It does not click the options UI, and it rejects any other field or any interval
the options page does not offer. Floccus checks once a minute (`checkSync` alarm)
and syncs when `lastSync + interval` has passed. The harness restarts the restored
server about 3 minutes after the last sync, so the interval falls due about 2–3
minutes after the restart. The wait is capped at 3.5 minutes (5 minutes at most).
If no automatic sync is seen inside that window, the case is recorded as PENDING.
Any manual RPC continuation after that is reported as a separate MANUAL case and
never counts as automatic evidence.

## Limits

- This was run on the Windows development host with Chrome, Edge, Brave and Firefox, against a
  development build of the server. It checks stock-extension behaviour against the
  real server code. It is not a Linux LXC/Docker deployment test. Browser restart, sync-on-startup and
  real network loss (as opposed to a stopped server) were not exercised.
- The server is stopped by forcibly terminating its process before backup and
  restore. A graceful systemd/Docker stop was not exercised here.
- Backups are created with the CLI while the server is stopped. The admin-triggered
  online backup and encrypted archives are covered elsewhere, not by this slice.
- Change-triggered (7-second inactivity) sync was off throughout. The conflict cases
  use explicit sequential reconnects.
- Each outcome was observed on one dataset and one timing. Other floccus versions,
  strategies, folder moves, or concurrent rather than sequential reconnects may
  behave differently.
