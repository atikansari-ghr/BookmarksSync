# Host worker acceptance (isolated systemd fixture)

Independently run on the authorized Debian LXC on 2026-11-25: **PASS** for all
six cases below. The final check found no fixture runtime units, loaded units or
temporary directories. Local evidence: `.tools/scratch/claude-host-worker.log`.

`scripts/docker/test-host-worker-runtime.sh` exercises the real chain
**systemd `.path` unit → current `scripts/docker/update-worker.sh` → fixed fake
maintainer** on a disposable Linux systemd host (Debian/Proxmox LXC). Run it as
root only on a host authorized for disposable tests:

```sh
bash scripts/docker/test-host-worker-runtime.sh
```

## What it creates (and removes)

- One private root `/tmp/bookmarksync-host-worker.<random>` (root owned, mode
  0711) holding a fake install tree: config, deployment, backups and queue.
- One unique unit pair `bookmarksync-acceptance-<random>.service` and `.path`,
  only under `/run/systemd/system` (runtime, never enabled). Each file carries a
  fixture marker line; cleanup deletes only files with that marker.
- The service runs the copied current worker as root with
  `BOOKMARKSYNC_TESTING=1` and `BOOKMARKSYNC_INSTALL_ROOT=<private root>`, and the
  same hardening as the production unit (`PrivateTmp`, `ProtectHome`,
  `NoNewPrivileges`, `UMask=0077`) plus a bind of the fixture root.

It does not touch the production queue, config, worker, or
`bookmarksync-docker-updates.*` units, restart any existing service, start
Docker, or contact GHCR. The fake `maintain.sh` accepts only `upgrade
ghcr.io/atikansari-ghr/bookmarksync:build-<40 hex> <fixture backups>` with
`BOOKMARKSYNC_DOCKER_ADMIN_UPDATES=1`, records the call, and fails for one
designated tag.

## Cases

| Case | Marker (published as UID/GID 65532) | Expected |
| --- | --- | --- |
| Success | empty, mode 0600, valid full-SHA tag | maintainer called once as UID 0 with exact image and backup path; marker removed |
| Failure retention | valid tag, maintainer exits non-zero | marker kept in `rejected/`; watcher stays active |
| Unsafe content | non-empty | rejected, no maintainer call |
| Unsafe name | `build-` + uppercase hex | rejected, no maintainer call |
| Symlink | symlink to a fixture file | rejected as a link; target unchanged |
| Hardlink | second link to a staged file | rejected (link count 2) |

Waits are bounded (20 s per stage, 30–60 s for systemctl operations). Markers
are created by `setpriv --reuid=65532 --regid=65532` in a staging directory and
renamed into the inbox; the script checks owner, group, mode 0600 and link count.

## Cleanup

The EXIT trap stops the fixture `.path`/`.service`, deletes only marker-bearing
runtime unit files, runs `systemctl daemon-reload`, confirms both units report
`LoadState=not-found`, and removes the fixture root. Any cleanup failure makes
the run fail and prints the retained paths.

## Limits

`BOOKMARKSYNC_TESTING=1` bypasses the production root-ownership chain and the
UID 65532 inbox/marker owner checks inside the worker. Those checks, and inbox
availability to the application container, are covered by separate tests. A
pass here proves only the watcher orchestration and request validation; it does
not establish a real attested or published image upgrade.
