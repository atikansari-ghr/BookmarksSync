# Debian maintenance

Use `sudo bash scripts/debian/bookmarksync COMMAND` on Debian amd64 with systemd,
or the installed `/usr/local/sbin/bookmarksync-installer COMMAND`. Maintenance
commands require the standard managed installation paths and account. They refuse
custom paths, unmanaged/mismatched units, unrecognized overrides and symlinked
targets. The same platform and root checks apply as for installation.
The executable must be root-owned mode 0755 and the environment root-owned mode
0600 before root invokes application commands. New installations and upgrades
retain root ownership of the executable; the service account owns only its data.
For legacy installations with an account-owned executable, verify that installed
binary against its GitHub build attestation before changing ownership to root;
the helper refuses to bless and execute an unverified legacy file automatically.

## Diagnostics

```sh
sudo /usr/local/sbin/bookmarksync-installer diagnostics
```

This prints the managed service and queue-watcher states, application version and
readiness at the configured listen address. IPv4 and IPv6 wildcards map to their
matching loopback address; other hosts are preserved. Values are parsed literally,
without evaluating the environment file, and the probe bypasses HTTP proxies.
For a non-loopback bind, the host's own source IP must be allowed by the
management/country policy. Diagnostics do not print environment files,
credentials, response bodies, process command lines or journal entries.
A ready process does not establish public HTTPS, OIDC,
MaxMind or browser extension acceptance.

## Independent backups and offline recovery

Choose a protected directory outside application data and updater snapshots:

```sh
sudo install -d -o root -g root -m 0700 /srv/bookmarksync-backups
sudo /usr/local/sbin/bookmarksync-installer backup --output /srv/bookmarksync-backups/manual-20261124.zip
sudo install -d -o root -g root -m 0700 /srv/bookmarksync-restores
sudo /usr/local/sbin/bookmarksync-installer restore --file /srv/bookmarksync-backups/manual-20261124.zip --target-dir /srv/bookmarksync-restores/recovered
```

Backup requires a new absolute `.zip` path in an existing canonical directory.
It stops the app if running, invokes the installed binary's full backup and
verification commands, then restarts it only if it was previously running. A
failed backup or verification also attempts that restart. Archives contain
sensitive application keys and configuration; retain them in protected independent
storage and test recovery.

Restore verifies an existing regular archive, then restores into a new, absent
directory in an existing canonical parent. It rejects live application and update
storage, symlinks and system targets. It does not replace live data or change the
running service. Inspect the restored data, stop the service and select it through
a separately reviewed recovery operation. Restored files are created by root;
set appropriate `bookmarksync:bookmarksync` ownership before selecting them for
the service. For encrypted archives, use `bookmarksync backup verify/restore`
directly with the protected `--key-file` option documented in the backup guide.
These wrappers deliberately support ordinary ZIP archives only.

## Safe uninstall

```sh
sudo /usr/local/sbin/bookmarksync-installer uninstall
```

Uninstall completes its artifact checks before stopping anything. It disables
and stops the app and the managed GeoLite timer/service, removes the managed
binary, installer/update worker, marker-bearing legacy sudo helpers, unit files and recognized managed
GeoLite overrides, then reloads systemd. It refuses unmanaged units or helper
files instead of guessing ownership. Old installer copies are recognized by
their exact repository declaration. No recursive data deletion is performed.

It retains the service account, `/var/lib/bookmarksync`,
`/var/lib/bookmarksync-updates` (including snapshots and failed data),
`/var/lib/bookmarksync-update-queue` (including retained requests),
`/etc/bookmarksync` (including MaxMind credentials), GeoLite databases, independent
backups and restored directories. The separately installed `geoipupdate` and
GitHub CLI dependencies are retained. There is no `--purge` option. Remove
retained sensitive material separately only after recovery and retention needs
have been resolved.

## Validation

Browser approvals use a fixed service-owned mode-0700 inbox under the root-owned
mode-0755 `/var/lib/bookmarksync-update-queue`. The root worker claims markers into
private storage, checks a full lowercase SHA tag, empty regular file, mode 0600,
single link and service UID, then calls only the fixed installer. It processes at
most 64 entries per run, keeps invalid/failed entries in root-private `rejected/`,
and uses a separate nonblocking worker lock. The app keeps NoNewPrivileges and
does not need sudo. Repair removes only marker-bearing legacy helper/sudoers files.
Uninstall disables the watcher before removing its managed runtime artifacts.

`sudo bash scripts/debian/test-bookmarksync.sh` and
`sudo bash scripts/debian/test-update-queue.sh` cover installer ownership, queue
validation, lock contention, readiness addresses and failed repair activation with
mock services/releases. On an authorized disposable systemd host,
`sudo bash scripts/debian/test-update-queue-runtime.sh` uses unique runtime units
and a private `/run` directory. It verifies a real unprivileged NoNewPrivileges
sender, path watcher and production-checking worker calling a fixed root fixture
installer, then checks cleanup. It never invokes a real upgrade. Actual release
provenance and binary activation remain separate external gates.

`bash scripts/debian/test-maintenance.sh` uses fake systemctl/application/curl
commands and a disposable isolated tree. It covers sanitized output, backup and
offline restore, restart after backup failure, unsafe/unmanaged target rejection,
managed GeoLite removal and preservation of application data, snapshots, secrets
and independent backups. Symlink assertions require actual Linux filesystem
semantics. Existing installer and GeoLite regression suites remain separate.
