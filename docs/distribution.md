# Build Releases

Each successful push to `main` publishes a prerelease named `build-<full commit SHA>`. Pull-request builds never publish. Releases contain:

- `bookmarksync-linux-amd64` and `bookmarksync-windows-amd64.exe`;
- `BUILD_INFO.txt` with the source commit, commit timestamp, and embedded version;
- `SHA256SUMS` covering both binaries and the build information file.

The publish job runs only after every CI gate and both build targets succeed. It creates a draft, attaches all assets, generates Sigstore-backed GitHub artifact attestations, then publishes the draft. Immutable releases are enabled for this repository, so GitHub locks release assets and the associated SHA tag when published.

## Verify a downloaded binary

Install the GitHub CLI and verify the checksum from the directory containing the downloaded assets:

```sh
sha256sum --check SHA256SUMS
gh attestation verify bookmarksync-linux-amd64 -R atikansari-ghr/BookmarksSync
```

For Windows, substitute `bookmarksync-windows-amd64.exe` in the attestation command. Verify the attestation before installing an update. The checksum detects file changes; the attestation establishes that GitHub Actions built the file for this repository and commit.

These releases are the build source for upcoming platform installers. The admin update catalog, click-to-approve UI, pre-update snapshot, write pause, health check, and automatic rollback are separate Milestone 6 work and are not implemented by this release workflow.
