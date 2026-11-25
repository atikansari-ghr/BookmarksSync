# Docker Packaging Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** Execute with `superpowers:executing-plans`; complete each checkbox before moving on.

**Goal:** Let an operator build and run BookmarksSync in Docker Compose with persistent, protected local data.

**Architecture:** A multi-stage Dockerfile compiles the existing Go executable and serves it from a small Debian runtime image as an unprivileged user. Compose binds the HTTP port, stores application data and OAuth-capable rclone configuration in separate named volumes, and accepts explicit deployment access-policy settings; it does not mount the Docker socket or install a host updater.

**Tech Stack:** Go 1.27.4, Docker BuildKit, Docker Compose.

**Spec:** `docs/design.md` (Architecture and delivery; Operations); `docs/network-access.md`; `docs/distribution.md`.

## Global Constraints
- Keep SQLite on the container's local named volume, never NFS/SMB.
- Run the service as a non-root UID and expose only port 8080.
- Do not bypass the country gate for loopback or Docker health probes; without a valid GeoIP database or matching management CIDR, all requests must remain denied.
- Do not mount `/var/run/docker.sock` or give the web service host privileges.
- Build from committed embedded assets and pin the Go builder image to the repository's Go toolchain version.

## Review Focus
1. Missing GeoIP and management CIDRs must fail closed, with a clear setup path.
2. A named volume must be writable by the non-root runtime UID and survive container replacement.
3. Public URL, OIDC and reverse-proxy settings must be passed as environment values without baking secrets into the image.
4. Image build context must exclude local data, credentials, browser profiles, and tool caches.
5. Compose shutdown/recreation must preserve the SQLite volume and allow CLI administration through `docker compose exec`.

## Files
- Create `Dockerfile` for reproducible multi-stage image build, non-root runtime and declared data mount.
- Create `.dockerignore` as a positive source allowlist for Go sources, migrations, exact UI asset types, and licenses; exclude local state, credentials, generated tools, tests, and browser artifacts from the build context.
- Create `compose.yaml` with persistent volumes, fail-closed access-policy settings, optional read-only GeoIP mount, configurable public URL/OIDC settings, and port mapping.
- Update `docs/distribution.md`, `README.md`, and `docs/implementation-plan.md` with build, first-admin, proxy, data, backup and update limitations.

## Tasks

### Task 1: Build a restricted Docker image

**Files:** Create `Dockerfile`, `.dockerignore`, and `third_party/rclone-COPYING`.

- [x] Pin the Go builder image to `golang:1.27.4-bookworm`; build with `CGO_ENABLED=0` from committed embedded assets.
- [x] Include rclone v1.76.0 only for supported Docker architectures (amd64, arm64), pinned to official SHA-256 checksums; reject other architectures.
- [x] Include the rclone MIT license in the final image and retain the upstream notice in the repository.
- [x] Include the project `LICENSE` and `THIRD_PARTY_NOTICES.md` in the runtime image, and pass version/commit/date build args to the binary.
- [x] Run the final image as UID/GID 65532, use read-only root filesystem-compatible paths, expose 8080, and declare `/var/lib/bookmarksync` as the data volume.
- [x] Exclude local data, keys, MMDB, `.env`, credential JSON, `.tools`, and browser artifacts from the build context.
- [x] Verify pinned rclone checksums against downloaded official amd64 and arm64 archives and check expected executable paths.

### Task 2: Add Compose deployment configuration

**Files:** Create `compose.yaml`.

- [x] Bind a configurable host port to 8080, defaulting to loopback-only, and pass public URL, management CIDRs, trusted proxies and GeoIP path through environment values.
- [x] Persist application data and writable OAuth rclone config in separate named local volumes; mount GeoIP read-only.
- [x] Enable `read_only`, drop all Linux capabilities, set `no-new-privileges`, and provide a configurable `/tmp` tmpfs sized for backup staging; do not add an unrestricted health-check bypass.
- [x] Forward all Google and Authentik OIDC variables without baking credentials into the image.
- [x] Validate rendered Compose config with explicit and empty policy variables; both must render, and runtime access policy must remain closed when no database/CIDR works.

### Task 3: Exercise the container lifecycle

**Files:** No additional files required.

- [x] On the authorized Debian 13 LXC, build the complete image, start an isolated Compose project, create test admins via `docker compose exec`, verify fail-closed/open readiness and persistence after forced recreation, then remove only the isolated project and volumes.
- [x] Confirm runtime hardening (UID 65532, read-only root, dropped capabilities, no-new-privileges, loopback-only host port) and version endpoint.
- [x] Inject credential/tool sentinels into the build context, rebuild without cache, and verify the sentinels are absent while embedded assets and migrations remain.

### Task 4: Document operations and limitations

**Files:** Modify `docs/distribution.md`, `README.md`, and `docs/implementation-plan.md`.

- [x] Document build identity, first admin with masked password input, reverse proxy firewalling, country policy, OAuth config persistence, backups, and Compose upgrades.
- [x] State that Docker images are built locally and admin-triggered updates and GeoIP scheduling are not included.
- [x] Run the Go suite, vet, Linux/Windows builds, Compose config validation, and `git diff --check`; frontend verification is unnecessary unless embedded assets change.

## Verification
- `docker compose config` with and without optional variables; missing GeoIP/CIDR access returned 403 and configured test policy returned 200 on `/readyz`.
- Debian 13 LXC Docker image build, isolated startup, CLI admin provisioning, volume persistence across forced recreation, build-context sentinel checks, and runtime hardening inspection all passed. Debian Docker used its legacy builder because BuildKit/buildx was not installed.
- `go test ./... -count=1`, `go vet ./...`, Linux/Windows builds, and frontend check only if the existing embedded bundle changes.
