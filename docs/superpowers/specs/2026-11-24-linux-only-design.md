# Linux LXC and Docker scope

User-directed scope change, 2026-11-24: proceed with remaining work, leave Windows work, remove Windows support; complete scoped tasks in parallel.

## Target

Supported server deployments are Debian amd64 on a Linux host or Proxmox LXC, and Docker Compose on Linux amd64. Browser clients remain independent of the server operating system. Existing data formats, backup compatibility, ownership and Floccus behavior remain intact.

## Approach

Remove the native Windows server service, updater, installer, IPC implementation, release assets and Windows-only GeoLite controls. Keep small portable filesystem adapters where useful to run development tests on the current Windows workstation; they do not constitute a supported server deployment. Keep historical design records, clearly superseded by this decision, rather than rewriting past work as if it never happened.

Continue the existing Debian restricted updater and verified release pipeline. Add a Docker GeoLite update mechanism that stages and validates the database, preserves the previous valid database on failures, and refreshes the application's reader without granting the application Docker control. Supply safe maintenance commands and release automation for supported platforms. Finish remaining server features such as Login Flow v2 where feasible in this run.

Docker browser approvals use an opt-in host-owned queue parent with an inbox writable by container UID 65532. The app can create only validated `build-<full SHA>` requests; a root systemd path/oneshot worker consumes basenames, never command content, and calls the fixed provenance-verifying host maintenance workflow. The app receives no Docker socket, root credentials or general host command interface. Setup requires a protected root-owned deployment checkout and backup configuration. Unconfigured Docker remains manually maintained.

## Acceptance

- No native Windows services, updater package, installer, Windows CI matrix or release artifacts.
- Current docs and platform catalogs describe Linux LXC/Docker only; superseded plans do not create open Windows tasks.
- Existing Go, web, harness and Floccus behavior remains covered; fresh Linux build and relevant Linux runtime tests.
- GeoLite updater uses protected configuration, validation and atomic publication; Docker socket remains outside the application.
- Distinguish implementation from live acceptance requiring a public URL, MaxMind/OIDC credentials or a first published build.
- No production deployment or GitHub push/merge/publication without the separately required authorization. Local changes and isolated test LXC work are authorized.

## Review focus

Protect migrations and backup format from accidental changes during removal. Retain client operating-system labels. Prevent credential leakage in updater logs. Test failure paths for GeoLite refresh and updates. Verify platform-specific release filtering and ensure Docker runtime receives a usable staged database.
