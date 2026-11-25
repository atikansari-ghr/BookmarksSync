# Local acceptance verification — 2026-11-25

Server scope remains Linux amd64 on Debian/Proxmox LXC and Linux Docker.
Windows browser clients remain supported. This supplements the
[Linux implementation record](verification-2026-11-24.md).

The acceptance harness changes were reviewed and the checks below independently run.
No release, registry publication or production deployment was performed.

| Check | Result | Evidence and limits |
| --- | --- | --- |
| Browser harness unit suite | PASS | Final `npm test` in `tests/browser`: 93 tests, zero failures, including offline evidence/cleanup/settings and CI regression checks. |
| Four-browser offline conflicts and failsafe | PASS | Chrome, Edge, Brave and Firefox, two disposable profiles per run, eight initial bookmarks, actual server requests and native trees. Last accepted edit won; delete won both tested reconnect orders; mass deletion raised E029 without accepted DELETEs. |
| Full restore with retained browser caches | PASS | CLI archive create/verify and restore into an absent directory. Both real extension alarms completed sync 148 s and 156 s after restart, with zero harness sync RPCs after restart. Older restored state propagated to clients; untouched bookmarks survived and no duplicates appeared. |
| Isolated real systemd host watcher | PASS | Actual `.path` → worker → fixed fixture maintainer on authorized LXC. Success, failed-maintenance retention, nonempty/invalid-name/symlink/hardlink rejection. UID 65532 markers and root maintainer verified. Fixture units and directories removed. |
| Stock Chrome connection wizard | PASS | Independent run reproduced a duplicate-Continue transition race; settled-step scoping fixed it. Two consecutive full functional reruns passed through saved profile and native bookmark upload. |
| Whitespace check | PASS | `git diff --check`, exit 0. |

Offline browser report:
`.tools/browser-results/offline-sync-2026-11-25T05-37-44-116Z.json`.
Host fixture log: `.tools/scratch/claude-host-worker.log`. These ignored local
artifacts contain runtime evidence; reproducible procedures and limitations are
in [offline acceptance](offline-acceptance.md) and
[host worker acceptance](host-worker-acceptance.md).

Wizard functional report:
`.tools/browser-results/loginflow-smoke-2026-11-25T08-21-45-090Z.json`.
Repeat report:
`.tools/browser-results/loginflow-smoke-2026-11-25T08-22-33-488Z.json`.
The default extension-independent consent smoke also passed. Diagnostic wizard
screenshots are opt-in: full-page and viewport captures stalled at the stock
sync-options screen. Capture is not part of the functional PASS; all form,
consent, saved-credential and upload assertions remain mandatory.

The offline runs used Chrome, Edge, Brave and Firefox on the development host
against the real server code, with stopped-origin loss and sequential reconnects.
They do not establish Linux deployment behavior, browser-restart
continuation, concurrent reconnects or real network-loss timing. Linux runtime
packaging was checked separately in the preceding record. The host fixture uses
testing mode and a fake maintainer; it does not prove a published-image upgrade.

Remaining external gates: first published binary/GHCR installation and attested
upgrade, live MaxMind downloads, public HTTPS/trusted-proxy/country acceptance,
real Google/Authentik tenants and physical passkeys. Broader browser scenarios
remain explicitly outside this bounded acceptance. Operations and Distribution
milestones stay open for their external gates.

## Release-path review and corrections

The follow-up review found that Debian browser approvals could not work through
the old sudo path: the app could not read root-only sudoers, and NoNewPrivileges
prevented elevation. Discovery and the installer now use a protected SHA-marker
inbox and root systemd watcher. The obsolete sudo runner was removed.

- A Linux regression first failed as UID65532 in a private filesystem. It then
  passed with NoNewPrivileges enabled, including rejection of writable parents,
  foreign owners, exposed inboxes, symlinks and ambiguous platform configuration.
- The full Linux `internal/updates` tests, vet and race checks passed after the fix.
  The Linux amd64 server build passed.
- Debian installer, maintenance and worker regressions passed on the authorized
  LXC: fixed SHA-only execution, failure retention, unsafe markers, 64-entry bound,
  distinct nonblocking locks, legacy migration, unmanaged installer preservation,
  safe uninstall, configured
  readiness addresses, literal invalid-value rejection and repair activation
  failure recovery.
- A real unprivileged NoNewPrivileges sender → systemd path → production-checking
  Debian worker → fixed root fixture installer passed. Temporary units were
  unloaded and the private runtime directory removed. This does not establish a
  real release upgrade.
- Main publication is no longer cancelled by a newer push. PR supersession is
  preserved. Regression tests failed before the workflow correction and passed
  afterward. Debian ownership/queue fixtures now run as root in Linux CI.

Reproduce the isolated checks with the commands in
[Debian maintenance](debian-maintenance.md). The latest local fixture log is
`.tools/scratch/debian-fix-check.log`. Additional browser report paths and timings
are listed in [offline acceptance](offline-acceptance.md).

At the pre-push review, read-only GitHub inspection found no releases and only
older branch CI runs; these changes had not yet run on GitHub-hosted CI. See
[release prerequisites](release-readiness.md) for the remaining external gates.

## Pre-push verification and LXC instructions

Final full Linux `go test ./...`, `go vet ./...` and `go test -race ./...` passed
in the disposable toolchain container. Web typecheck, 80 UI tests and the
embedded production build passed; browser harness suites passed 93 and 27 tests.

The [LXC IP guide](lxc-ip-testing.md) uses a separate checkout, image, Compose
project and volumes, with port 18081 and SSH to `192.168.10.226`. An isolated
equivalent passed build/create/start, exact gateway access, health/version,
admin provisioning, password login and admin/session persistence after container
recreation. Fixture resources were removed. The host lacked curl; verification
used containerized curl in the host network namespace, and the guide includes
the host dependency. Port 18080 was occupied and left untouched. Workstation
browser/tunnel and manual Floccus checks remain instructions for the operator.
Local log: `.tools/scratch/lxc-ip-guide-check.log`.
