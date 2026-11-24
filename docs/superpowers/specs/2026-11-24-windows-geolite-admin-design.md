# Windows GeoLite Admin Design

## Intent

Let an administrator configure and operate GeoLite2 Country from the BookmarksSync Admin Settings GUI on native Windows. Updates must preserve the active database and fail-closed country policy whenever download, validation, or installation fails.

## User experience

- Add a GeoLite section to Admin Settings beside Country access.
- Admins can enter or replace the MaxMind Account ID and license-key pair, enable/disable scheduled updates, choose two to seven weekly run days and a local run time, inspect configured state, active database build time, last attempt/result, and run an update now.
- Saved credentials are never returned to the browser. Both blank secret fields keep the current pair; an explicit remove action clears credentials and disables the scheduled task. A partial pair change is rejected so an account ID cannot be combined with a mismatched key.
- Saving both credential fields blank preserves both stored values. Supplying only one credential is rejected; changing an account ID requires its matching license key. First-time configuration requires both.
- All controls require administrator authorization and recent authentication for changes or update runs. The UI explains that MaxMind credentials and GeoLite licensing are operator-provided.
- Default schedule is Monday and Thursday at 04:00 local time, matching Debian's twice-weekly timer. The installer remains responsible only for installing the Windows services and application; routine GeoLite setup requires no shell command.

## Architecture and security

- Extend the Windows-only Admin API and existing restricted `BookmarksSyncUpdater` named-pipe service. The web server remains the authorization boundary; the pipe accepts only a closed set of bounded GeoLite operations (configure, clear, run now, and status), never shell commands, executable paths, task names, or arbitrary file paths. One updater-service job manager serializes GeoLite work with application release updates; schedule-trigger processes only request a run through the pipe. Configure and clear return busy while a job is active.
- The admin API sends credential input only during configuration. Neither HTTP responses, job status, logs, nor error text contain the credential values. Validate limits and schedule values before forwarding them.
- The Windows application release packages `geoipupdate-windows-amd64.exe` from the pinned MaxMind v8.0.0 official archive after verifying its archive checksum; this keeps routine GUI configuration from requiring a separate binary download. Verify the installed executable against the BookmarksSync release manifest before every run, and publish the applicable upstream license notice with the binary.
- The LocalSystem updater service owns a protected credential/state directory and a separate staging directory under `%ProgramData%\BookmarksSync`; create directories with protected ACLs before writing secret files and reject reparse points in every managed path. Only Administrators and SYSTEM can read credentials; the app service SID cannot. Store the active database under `%ProgramData%\BookmarksSync\geolite\database` and grant `NT SERVICE\BookmarksSync` read-only access there.
- It creates or updates one fixed BookmarksSync GeoLite Scheduled Task running as SYSTEM with highest privileges, `StartWhenAvailable`, `IgnoreNew` multiple-instance policy, one-hour execution limit, and battery operation allowed. Schedule days and start time are local time (no UTC offset); the default is Monday/Thursday 04:00. Clearing credentials deletes the managed task before removing credentials, but retains the last valid database so the current country policy keeps working. Configuration does not make a live MaxMind request.
- The scheduled task runs fixed `geolite-run` mode in the installed updater executable. That LocalSystem process sends a bounded `run` request to the updater service pipe; only the service executes the update job. The worker runs manifest-verified `geoipupdate.exe` v8.0.0 with `-f` pointing to the protected config and `-d` pointing to fixed staging. It downloads only `GeoLite2-Country`, discards raw updater output, and uses an in-process strict GeoLite2-Country validator. Reject reparse files, databases above 64 MiB, unsupported editions, invalid build dates, and build-time downgrades.
- Install and upgrade set `BOOKMARKSYNC_GEOIP_DB` to that fixed active database path, including when the file is absent, preserving fail-closed behavior until a valid database exists. Never rewrite or truncate an active MMDB in place. On a changed valid file, keep a protected backup, atomically replace the path on the same volume, restart BookmarksSync, and wait for readiness. Delete the backup only after readiness succeeds. On activation failure, restore the backup and retry readiness; retain a recovery marker and backup if rollback cannot finish so the next updater attempt resumes recovery before another download.
- Store schedule and non-secret task status in an atomic updater-owned state file. The GUI receives only status, configured booleans, schedule, build time, and sanitized error categories. Task Scheduler instances use `IgnoreNew`; all actual run/configuration work is serialized by the updater service.
- No automatic firewall changes, no application-database or full-backup copy of the MaxMind key, no change to country rules/default `IN,SA`, and no Docker updater in this work.

## Acceptance

- API and UI tests cover admin/recent-auth requirements, invalid schedules, secret never returned, preserve-on-blank, clear behavior, status display, update-now feedback, and errors.
- Windows updater tests cover protocol bounds/operation whitelist, protected ACL creation and safe fixed-path management, scheduler XML settings and create/update/remove behavior, missed-run configuration, secret-safe output, successful changed/unchanged updates, download/invalid-file failures, exact edition/size/downgrade checks, atomic replacement while the prior reader remains mapped, restart ordering, rollback/recovery, and concurrent-run rejection across manual/scheduled/release jobs.
- Existing Go and web suites, Windows amd64 build/vet, PowerShell/static checks, and the GeoLite helper test suite pass. No real MaxMind key or production task is used in tests. The elevated live SCM test remains separately pending.

## Platform references

- [MaxMind updater options](https://maxmind.github.io/geoipupdate/doc/geoipupdate/)
- [MaxMind GeoIP.conf reference](https://maxmind.github.io/geoipupdate/doc/geoip.conf/)
- [MaxMind geoipupdate releases](https://github.com/maxmind/geoipupdate/releases)
