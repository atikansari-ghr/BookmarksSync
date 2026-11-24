# Windows GeoLite Admin Design

## Intent

Let an administrator configure and operate GeoLite2 Country from the BookmarksSync Admin Settings GUI on native Windows. Updates must preserve the active database and fail-closed country policy whenever download, validation, or installation fails.

## User experience

- Add a GeoLite section to Admin Settings beside Country access.
- Admins can enter or replace MaxMind Account ID and license key, enable/disable scheduled updates, choose two to seven weekly run days and a local run time, inspect configured state, active database build time, last attempt/result, and run an update now.
- Saved credentials are never returned to the browser. Blank secret fields keep the current value; an explicit remove action clears credentials and disables the scheduled task.
- All controls require administrator authorization and recent authentication for changes or update runs. The UI explains that MaxMind credentials and GeoLite licensing are operator-provided.
- Default schedule is Monday and Thursday at 04:00 local time, matching Debian's twice-weekly timer. The installer remains responsible only for installing the Windows services and application; routine GeoLite setup requires no shell command.

## Architecture and security

- Extend the Windows-only Admin API and existing restricted `BookmarksSyncUpdater` named-pipe service. The web server remains the authorization boundary; the pipe continues to accept only a closed set of bounded GeoLite operations (configure, clear, run now, and status), never shell commands, executable paths, task names, or arbitrary file paths.
- The admin API sends credential input only during configuration. Neither HTTP responses, job status, logs, nor error text contain the credential values. Validate limits and schedule values before forwarding them.
- The Windows application release packages `geoipupdate-windows-amd64.exe` from the pinned MaxMind v8.0.0 official archive after verifying its archive checksum; this keeps routine GUI configuration from requiring a separate binary download. Publish the applicable upstream license notice with the packaged binary.
- The LocalSystem updater service owns `%ProgramData%\BookmarksSync\GeoIP.conf` and grants access only to Administrators and SYSTEM. It creates or updates one fixed BookmarksSync GeoLite Scheduled Task running as SYSTEM, with highest privileges, start-when-available behavior, and the configured weekly days/time. Clearing credentials disables and removes that managed task.
- The scheduled task invokes a fixed GeoLite worker mode in the installed updater executable. It uses a protected staging directory under `%ProgramData%\BookmarksSync`, downloads only `GeoLite2-Country` with MaxMind `geoipupdate` v8.0.0 (`geoipupdate_8.0.0_windows_amd64.zip`, SHA-256 `1aadec5283389670729b1332ca938e3cf12a1413853c30a2193cfe9d3fc8bbea`), matching the Debian updater pin, validates the candidate with `bookmarksync.exe network validate-geo --file PATH`, and never accepts paths from the API.
- Keep the active database under `%ProgramData%\BookmarksSync\geoip`; grant `NT SERVICE\BookmarksSync` read-only access to that database. Download, validation, unchanged content, and replacement failures leave the current file intact. A valid changed database is installed with a same-volume atomic replacement; restart `BookmarksSync` only after successful replacement.
- Install and upgrade set `BOOKMARKSYNC_GEOIP_DB` to that fixed active database path in the protected service environment, including when the file is not present yet. The application retains its existing fail-closed behavior until a valid database is installed.
- Store schedule and non-secret task status in a small protected host state file or equivalent updater-owned state. The GUI receives only status, configured booleans, schedule, build time, and sanitized error categories. Concurrent updates are serialized.
- No automatic firewall changes, no application-database or full-backup copy of the MaxMind key, no change to country rules/default `IN,SA`, and no Docker updater in this work.

## Acceptance

- API and UI tests cover admin/recent-auth requirements, invalid schedules, secret never returned, preserve-on-blank, clear behavior, status display, update-now feedback, and errors.
- Windows updater tests cover protocol bounds/operation whitelist, ACL and safe fixed-path management, scheduler create/update/remove, missed-run configuration, secret-safe output, successful changed/unchanged updates, download/invalid-file failures, atomic replacement, restart ordering, and concurrent-run rejection.
- Existing Go and web suites, Windows amd64 build/vet, PowerShell/static checks, and the GeoLite helper test suite pass. No real MaxMind key or production task is used in tests. The elevated live SCM test remains separately pending.

## Platform references

- [MaxMind updater options](https://maxmind.github.io/geoipupdate/doc/geoipupdate/)
- [MaxMind GeoIP.conf reference](https://maxmind.github.io/geoipupdate/doc/geoip.conf/)
- [MaxMind geoipupdate releases](https://github.com/maxmind/geoipupdate/releases)
