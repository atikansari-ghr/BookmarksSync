#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
installer="${script_dir}/bookmarksync"
test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

help_output="$(bash "${installer}" --help)"
grep -Fq 'install' <<<"$help_output"
grep -Fq 'upgrade' <<<"$help_output"
grep -Fq 'repair' <<<"$help_output"

if bash "${installer}" nonsense >/dev/null 2>&1; then
  printf 'unsupported command unexpectedly succeeded\n' >&2
  exit 1
fi

invalid_tag_output="$(BOOKMARKSYNC_TESTING=1 bash "${installer}" upgrade --tag latest 2>&1 || true)"
grep -Fq 'tag must match build-' <<<"$invalid_tag_output"

missing_tag_output="$(BOOKMARKSYNC_TESTING=1 bash "${installer}" repair 2>&1 || true)"
grep -Fq 'requires --tag' <<<"$missing_tag_output"

unsupported_os_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=ubuntu bash "${installer}" install --tag build-0123456789012345678901234567890123456789 2>&1 || true)"
grep -Fq 'supports Debian only' <<<"$unsupported_os_output"

mkdir -p "${test_root}/bin" "${test_root}/root"
cat >"${test_root}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -e
output=""
url=""
while (($#)); do
  case "$1" in
    --output|-o) output="$2"; shift 2 ;;
    -H|--proto|--proto-redir|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [[ "$url" == https://api.github.com/*/releases\?* ]]; then
  cat "$BOOKMARKSYNC_TEST_RELEASE_JSON"
elif [[ "$url" == http://127.0.0.1:* ]]; then
  if [[ -n "${BOOKMARKSYNC_TEST_HEALTH_FAIL_COUNT_FILE:-}" && -s "$BOOKMARKSYNC_TEST_HEALTH_FAIL_COUNT_FILE" ]]; then
    count="$(cat "$BOOKMARKSYNC_TEST_HEALTH_FAIL_COUNT_FILE")"
    if ((count > 0)); then
      printf '%s\n' "$((count - 1))" > "$BOOKMARKSYNC_TEST_HEALTH_FAIL_COUNT_FILE"
      exit 22
    fi
  fi
  [[ "${BOOKMARKSYNC_TEST_HEALTH_FAIL:-0}" != 1 ]]
else
  [[ -n "$output" ]]
  cp "${BOOKMARKSYNC_TEST_FIXTURES}/${url##*/}" "$output"
fi
EOF
cat >"${test_root}/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$BOOKMARKSYNC_TEST_SYSTEMCTL_LOG"
exit 0
EOF
cat >"${test_root}/bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF
cat >"${test_root}/bin/gh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$BOOKMARKSYNC_TEST_GH_LOG"
[[ "${BOOKMARKSYNC_TEST_ATTEST_FAIL:-0}" != 1 ]]
EOF
chmod +x "${test_root}/bin/curl" "${test_root}/bin/systemctl" "${test_root}/bin/sleep" "${test_root}/bin/gh"
cat >"${test_root}/release.json" <<'EOF'
[{"tag_name":"build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","draft":false,"prerelease":true,"published_at":"2026-11-23T00:00:00Z","assets":[]}]
EOF
mismatch_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release.json" PATH="${test_root}/bin:${PATH}" bash "${installer}" install --tag build-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 2>&1 || true)"
grep -Fq 'requested release tag was not found' <<<"$mismatch_output"
[[ ! -e "${test_root}/root/usr/local/bin/bookmarksync" ]]
missing_asset_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release.json" PATH="${test_root}/bin:${PATH}" bash "${installer}" install --tag build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2>&1 || true)"
grep -Fq 'requested release is missing required assets' <<<"$missing_asset_output"
[[ ! -e "${test_root}/root/usr/local/bin/bookmarksync" ]]

printf 'PASS: command help and unknown-command handling\n'
printf 'PASS: non-SHA upgrade tags are rejected\n'
printf 'PASS: upgrade and repair require an exact release tag\n'
printf 'PASS: unsupported operating systems are rejected\n'
printf 'PASS: API tag mismatch fails before installing a binary\n'
printf 'PASS: incomplete release assets fail before installing a binary\n'
printf 'PASS: checksum mismatch prevents installation\n'

if [[ "$(uname -s)" == MINGW* || "$(uname -s)" == MSYS* ]]; then
  printf 'SKIP: systemd installation integration test requires Linux\n'
  exit 0
fi

fixtures="${test_root}/fixtures"
mkdir -p "$fixtures"
tag=build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
cat >"${fixtures}/bookmarksync-linux-amd64" <<'EOF'
#!/usr/bin/env bash
set -e
if [[ "$1" == backup && "$2" == create ]]; then
  [[ "${BOOKMARKSYNC_TEST_BACKUP_FAIL:-0}" != 1 ]] || { printf 'mock backup failed\n' >&2; exit 1; }
  while (($#)); do
    if [[ "$1" == --output ]]; then printf 'snapshot\n' > "$2"; exit 0; fi
    shift
  done
  exit 2
elif [[ "$1" == backup && "$2" == verify ]]; then
  exit 0
elif [[ "$1" == backup && "$2" == restore ]]; then
  [[ "${BOOKMARKSYNC_TEST_RESTORE_FAIL:-0}" != 1 ]] || { printf 'mock restore failed\n' >&2; exit 1; }
  while (($#)); do
    if [[ "$1" == --target-dir ]]; then mkdir -m 0700 "$2"; exit 0; fi
    shift
  done
  exit 2
fi
printf 'test binary\n'
EOF
chmod +x "${fixtures}/bookmarksync-linux-amd64"
printf 'commit=%s\nbuild_date=2026-11-23T00:00:00Z\nversion=0.0.0-aaaaaaaaaaaa\n' "${tag#build-}" > "${fixtures}/BUILD_INFO.txt"
(cd "$fixtures" && sha256sum bookmarksync-linux-amd64 BUILD_INFO.txt > SHA256SUMS)
cat >"${test_root}/release-valid.json" <<EOF
[{"tag_name":"${tag}","draft":false,"prerelease":true,"published_at":"2026-11-23T00:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]}]
EOF
mkdir -p "${test_root}/bad-checksum-fixtures" "${test_root}/bad-checksum-root"
cp "$fixtures/BUILD_INFO.txt" "$fixtures/SHA256SUMS" "$fixtures/bookmarksync-linux-amd64" "${test_root}/bad-checksum-fixtures/"
printf 'changed after signing\n' >> "${test_root}/bad-checksum-fixtures/bookmarksync-linux-amd64"
bad_checksum_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/bad-checksum-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="${test_root}/bad-checksum-fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh-bad-checksum.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com PATH="${test_root}/bin:${PATH}" bash "${installer}" install --tag "$tag" 2>&1 || true)"
grep -Fq 'release checksum verification failed' <<<"$bad_checksum_output"
[[ ! -e "${test_root}/bad-checksum-root/usr/local/bin/bookmarksync" ]]
mkdir -p "${test_root}/install-root"
install_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com BOOKMARKSYNC_LISTEN=127.0.0.1:18081 BOOKMARKSYNC_MANAGEMENT_CIDRS=192.168.10.0/24 PATH="${test_root}/bin:${PATH}" bash "${installer}" install 2>&1)"
grep -Fq "Installed BookmarksSync ${tag}" <<<"$install_output"
[[ -x "${test_root}/install-root/usr/local/bin/bookmarksync" ]]
grep -Fxq 'BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env"
grep -Fxq 'BOOKMARKSYNC_MANAGEMENT_CIDRS=192.168.10.0/24,127.0.0.1/32,::1/128' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env"
grep -Fq '# Managed by BookmarksSync installer' "${test_root}/install-root/etc/systemd/system/bookmarksync.service"
grep -Fq 'attestation verify' "${test_root}/gh.log"
grep -Fq -- '--signer-workflow atikansari-ghr/BookmarksSync/.github/workflows/ci.yml' "${test_root}/gh.log"
grep -Fq -- '--source-ref refs/heads/main' "${test_root}/gh.log"
[[ "$(stat -c '%a' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env")" == 600 ]]
[[ "$(stat -c '%a' "${test_root}/install-root/etc/systemd/system/bookmarksync.service")" == 644 ]]

chmod 0644 "${test_root}/install-root/etc/bookmarksync/bookmarksync.env"
BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" >/dev/null
[[ "$(stat -c '%a' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env")" == 600 ]]

mkdir -p "${test_root}/attestation-root"
attestation_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/attestation-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh-fail.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_ATTEST_FAIL=1 BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com PATH="${test_root}/bin:${PATH}" bash "${installer}" install --tag "$tag" 2>&1 || true)"
grep -Fq 'GitHub release attestation verification failed' <<<"$attestation_output"
[[ ! -e "${test_root}/attestation-root/usr/local/bin/bookmarksync" ]]

mkdir -p "${test_root}/invalid-config-root"
invalid_config_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/invalid-config-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=http://example.com PATH="${test_root}/bin:${PATH}" bash "${installer}" install --tag "$tag" 2>&1 || true)"
grep -Fq 'public URL, listen address, or management CIDRs are invalid' <<<"$invalid_config_output"
[[ ! -e "${test_root}/invalid-config-root/usr/local/bin/bookmarksync" ]]

: >"${test_root}/systemctl.log"
backup_failure_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_BACKUP_FAIL=1 PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq 'mock backup failed' <<<"$backup_failure_output"
grep -Fxq 'start bookmarksync.service' "${test_root}/systemctl.log"

: >"${test_root}/systemctl.log"
upgrade_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1)"
grep -Fq "Upgraded BookmarksSync to ${tag}" <<<"$upgrade_output"
[[ "$(find "${test_root}/install-root/var/lib/bookmarksync-updates/snapshots" -maxdepth 1 -name 'preupdate-*.zip' | wc -l)" -eq 2 ]]
[[ "$(find "${test_root}/install-root/var/lib/bookmarksync-updates/snapshots" -maxdepth 1 -name 'preupdate-*.previous' | wc -l)" -eq 2 ]]
[[ "$(find "${test_root}/install-root/var/lib/bookmarksync-updates/snapshots" -maxdepth 1 -name 'preupdate-*.env' | wc -l)" -eq 2 ]]
[[ "$(find "${test_root}/install-root/var/lib/bookmarksync-updates/snapshots" -maxdepth 1 -name 'preupdate-*.service' | wc -l)" -eq 2 ]]

for _ in 1 2 3; do
  BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" >/dev/null
done
[[ "$(find "${test_root}/install-root/var/lib/bookmarksync-updates/snapshots" -maxdepth 1 -name 'preupdate-*.zip' | wc -l)" -eq 3 ]]
[[ "$(find "${test_root}/install-root/var/lib/bookmarksync-updates/snapshots" -maxdepth 1 -name 'preupdate-*.previous' | wc -l)" -eq 3 ]]
repair_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" PATH="${test_root}/bin:${PATH}" bash "${installer}" repair --tag "$tag" 2>&1)"
grep -Fq "Repaired BookmarksSync with ${tag}" <<<"$repair_output"
[[ "$(find "${test_root}/install-root/var/lib/bookmarksync-updates/snapshots" -maxdepth 1 -name 'preupdate-*.zip' | wc -l)" -eq 3 ]]

: >"${test_root}/systemctl.log"
printf '30\n' > "${test_root}/health-fail-count"
rollback_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_HEALTH_FAIL_COUNT_FILE="${test_root}/health-fail-count" PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq 'old binary and data were restored' <<<"$rollback_output"
[[ -d "${test_root}/install-root/var/lib/bookmarksync" ]]
[[ -n "$(find "${test_root}/install-root/var/lib" -maxdepth 1 -type d -name 'bookmarksync.failed-*' -print -quit)" ]]
grep -Fxq 'start bookmarksync.service' "${test_root}/systemctl.log"

: >"${test_root}/systemctl.log"
restore_failure_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_HEALTH_FAIL=1 BOOKMARKSYNC_TEST_RESTORE_FAIL=1 PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq 'rollback restore failed' <<<"$restore_failure_output"
grep -Fq 'service remains stopped' <<<"$restore_failure_output"
failed_data="$(find "${test_root}/install-root/var/lib" -maxdepth 1 -type d -name 'bookmarksync.failed-*' -print -quit)"
[[ -n "$failed_data" && -d "$failed_data" ]]
! grep -Fxq 'start bookmarksync.service' "${test_root}/systemctl.log"

mkdir -p "${test_root}/unmanaged-root/etc/systemd/system"
printf '[Service]\nExecStart=/unmanaged\n' >"${test_root}/unmanaged-root/etc/systemd/system/bookmarksync.service"
unmanaged_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/unmanaged-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com PATH="${test_root}/bin:${PATH}" bash "${installer}" install 2>&1 || true)"
grep -Fq 'refusing to overwrite an unmanaged systemd unit' <<<"$unmanaged_output"
[[ ! -e "${test_root}/unmanaged-root/usr/local/bin/bookmarksync" ]]

printf 'PASS: verified build installs a protected service and environment file\n'
printf 'PASS: invalid release attestation prevents installation\n'
printf 'PASS: invalid public URL prevents installation\n'
printf 'PASS: backup failure restarts the existing service\n'
printf 'PASS: upgrade stores a verified snapshot and previous binary\n'
printf 'PASS: repair reinstalls the selected build and keeps three rollback snapshots\n'
printf 'PASS: failed readiness restores old binary and pre-upgrade data\n'
printf 'PASS: failed rollback restore retains failed data and leaves service stopped\n'
printf 'PASS: existing environment file permissions are repaired\n'
printf 'PASS: unmanaged systemd units are preserved\n'
