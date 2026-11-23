# Windows Service and Approved Updates Design

## Intent

Provide a supported native Windows install for BookmarksSync and let an administrator approve signed SHA-addressed upgrades from the existing Admin page. The server process must not gain system-wide privileges to replace its own executable.

## Goals

- Offer PowerShell `install`, `upgrade --tag`, and `repair --tag` commands for Windows amd64.
- Run the application under Windows Service Control Manager as its dedicated virtual account (`NT SERVICE\BookmarksSync`), with protected configuration and data under ProgramData.
- Reuse the existing Admin approval flow, recent reauthentication, GitHub release catalog, SHA verification, and workflow-scoped artifact attestations.
- Run updates in a separate LocalSystem Windows service. The application submits only a full release tag over a local named pipe with a restrictive DACL; remote clients are rejected, and no shell, path, or executable arguments are accepted.
- Before replacing files, stop the app service, create and verify a full-system archive, and retain the prior executable and config. Restart and check local readiness; restore the old binary and data if activation fails.
- Include the Windows rclone binary for supported backup destinations, pinned by official checksum and version.

## Non-goals

- Windows automatic GeoLite database scheduling, Docker updates, public image publishing, or update approval without recent admin authentication.
- Windows ARM64 or x86 support in this slice; the current release matrix ships Windows amd64 only.
- Running the web server as LocalSystem or allowing the updater to accept arbitrary commands or paths.

## Approaches

1. **PowerShell-only maintenance:** install, upgrade, and repair run locally as an elevated administrator. This is smaller but leaves the existing Admin-page update control unavailable on Windows.
2. **Selected: native services plus a restricted updater service:** run the app as its dedicated virtual account (`NT SERVICE\BookmarksSync`) and a separate updater as LocalSystem. A DACL-protected named pipe carries one validated release tag. This matches the Debian approval workflow while keeping system privileges out of the web process.
3. **Third-party service wrapper:** simpler service setup, but adds another executable and update chain and does not provide the restricted Admin-page updater boundary by itself.

## Design calls

- Use the already-pinned `golang.org/x/sys/windows/svc` for native service dispatch. Keep console `serve` and test execution available.
- Give the server a unique virtual service account and service SID instead of the shared Network Service identity. Use an explicit pipe DACL for that SID, LocalSystem, and Administrators; reject remote clients, bound request size and time, accept only one full SHA tag, and serialize update jobs.
- The Admin API remains the approval authority and requires recent reauthentication. The updater accepts requests only from the server service identity and independently verifies that the tag is an eligible published Windows build with valid checksums, build metadata, and GitHub attestations. As with Debian, a compromised server process could request any eligible release; the helper cannot execute arbitrary commands or paths.
- Ship a distinct `bookmarksync-updater-windows-amd64.exe` beside the existing server executable in each SHA release. The installed updater service is not replaced while it is running: it validates and stages the new updater, then launches a temporary worker copy to stop services and replace both installed executables. Local PowerShell upgrade/repair updates the server and helper together.
- Snapshot the application data archive, protected service configuration, rclone config, and both executables with a verification manifest; retain three verified snapshots. On failed activation, restore data, config, and executables, and leave failed state recoverable if restoration itself fails.
- Keep the current API contract and approval checks. The release catalog must require the server, updater, metadata, and checksum assets before listing a Windows release as eligible.
- Protect Program Files, service environment, data, rclone config, updater logs, and snapshots with explicit Windows ACLs. Do not add inbound firewall rules automatically; document proxy and firewall setup.
- Keep GeoLite installation separate; Windows administrators may supply a read-only country database and management CIDRs until a dedicated Windows updater exists.

## Acceptance

- Unit tests cover service control, named-pipe authorization/protocol limits, tag validation, release asset selection, rollback, and secret-safe output.
- Windows amd64 tests, vet, and build pass. A disposable local Windows service lifecycle test installs, starts, updates, forces a readiness failure, proves rollback, and removes only its own services, paths, and test data.
- Documentation explains installation, first admin setup, service accounts, paths, firewall/reverse proxy, upgrades, repair, backups, and recovery.

## Builder review resolutions

- Update authorization follows the existing Admin API's recent-authentication gate; the restricted pipe only carries the selected tag. The LocalSystem service independently checks eligible catalog membership and provenance, but does not duplicate web session verification.
- Use a per-service virtual account/SID to avoid granting the pipe to every process running as Network Service.
- Rollback covers all application data and the configuration/secrets that determine service behavior, plus rclone configuration and both executables. Keep three verified snapshots under administrator-only ACLs.
- Each release adds the updater executable as a separately checksummed and attested asset. The persistent service launches a staged worker copy so it can replace the installed helper without replacing a running image.
- Existing releases already publish the Windows server executable, `BUILD_INFO.txt`, and `SHA256SUMS`; the plan extends the asset contract with the updater binary and later pins the official Windows rclone archive.

## Platform references

- [Go Windows service package](https://pkg.go.dev/golang.org/x/sys/windows/svc)
- [Microsoft named-pipe security and access rights](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights)
- [Microsoft service SID documentation](https://learn.microsoft.com/en-us/windows/desktop/api/winsvc/ns-winsvc-service_sid_info)
- [Microsoft CreateNamedPipe remote-client flag](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-createnamedpipew)
