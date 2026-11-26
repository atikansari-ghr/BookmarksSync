# Remaining release acceptance

Server deployment scope is Linux amd64 on Debian/Proxmox LXC and Linux Docker.
The current local verification is recorded in
[verification-2026-11-25.md](verification-2026-11-25.md). Read-only GitHub
inspection on that date found no published releases; the latest remote CI run
covered the older `b0eab9239c3c73d2d29d1804ee7b12249e2b78fd` branch commit.
Local tests do not establish hosted CI or published-artifact acceptance.

## First published build

The publication workflow runs on successful pushes to `main`, after all required
checks. It publishes and attests the Linux image before exposing the binary
prerelease in the update catalog. Main runs are allowed to finish; newer PR runs
can cancel superseded PR runs. No publication was performed during local checks.

For the first real publication, verify the intended registry visibility/access,
the exact SHA tag and resolved digest, immutable release metadata, checksums,
source commit and signer workflow. Then exercise an independent Debian install
and Docker digest install, an approved upgrade and readiness-failure rollback on
disposable hosts, retaining verified backups and recovery keys. The generated
fixtures establish request validation and orchestration, not release provenance.

## Host credentials and dependencies

Release verification uses GitHub CLI. Configure and test its authentication as
the root worker identity before testing browser-approved upgrades. Do not put
GitHub or registry credentials in the application environment, data volume,
approval markers or repository. The Debian worker starts with a clean environment
and `HOME=/root`; a login belonging only to the interactive operator is not
inherited. Its root CLI configuration must be available to that identity.

The Docker worker uses `ProtectHome=true`, so credentials only under `/root` are
hidden. If authentication is required, use a protected directory outside home
directories and a root-controlled systemd override for the worker. For example,
configure `GH_CONFIG_DIR=/etc/bookmarksync/github` in the worker's `[Service]`
environment and authenticate the CLI using that same directory as root. Make the
credential directory root-owned mode 0700, keep credential files private, and
reload systemd after adding the override. Registry credentials, if required,
likewise need a protected configuration visible to the worker. Confirm both
registry pull and provenance verification from the worker context. These live
credential checks remain pending; no credentials were provisioned during tests.

The two GitHub REST headers used by the installer and catalog are supported as
of this review; their differing dates are not a release blocker. See
[GitHub's supported API versions](https://docs.github.com/en/rest/about-the-rest-api/api-versions).
The [GitHub CLI verification manual](https://cli.github.com/manual/gh_attestation_verify)
describes API, local-bundle and registry-bundle verification. Existing verification
policy remains unchanged.

## Other external acceptance

Live MaxMind downloads require an operator account and license key. Public
HTTPS/trusted-proxy/country behavior requires the configured public route and
external clients. Google/Authentik sign-in requires real tenants, and physical
passkey acceptance requires actual devices. No inputs for these checks were
provided. Operations and Distribution remain open until their external gates
pass; removed Windows server milestones do not return to scope.
