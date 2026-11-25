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
    -H|--proto|--proto-redir|--max-time|--noproxy) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [[ "$url" == https://api.github.com/*/releases\?* ]]; then
  cat "$BOOKMARKSYNC_TEST_RELEASE_JSON"
elif [[ "$url" == http://* ]]; then
  [[ -z "${BOOKMARKSYNC_TEST_CURL_LOG:-}" ]] || printf '%s\n' "$url" >> "$BOOKMARKSYNC_TEST_CURL_LOG"
  if [[ -n "${BOOKMARKSYNC_TEST_READY_URL:-}" ]]; then
    [[ "$url" == "$BOOKMARKSYNC_TEST_READY_URL" ]] || exit 7
  elif [[ "$url" != http://127.0.0.1:* ]]; then
    exit 7
  fi
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
if [ "$1" = is-active ] && [ "$3" = bookmarksync-update-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service ]; then exit 1; fi
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
# Browser updates must not depend on sudo, sudoers or transient root units.
for forbidden in sudo visudo systemd-run; do
  printf '#!/bin/sh\nprintf "%%s %%s\\n" %s "$*" >> "${BOOKMARKSYNC_TEST_FORBIDDEN_LOG:-/dev/null}"\nexit 1\n' "$forbidden" >"${test_root}/bin/${forbidden}"
done
chmod +x "${test_root}/bin/curl" "${test_root}/bin/systemctl" "${test_root}/bin/sleep" "${test_root}/bin/gh" "${test_root}/bin/sudo" "${test_root}/bin/visudo" "${test_root}/bin/systemd-run"
export BOOKMARKSYNC_TEST_FORBIDDEN_LOG="${test_root}/forbidden.log"
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
install_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_CURL_LOG="${test_root}/curl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com BOOKMARKSYNC_LISTEN=127.0.0.1:18081 BOOKMARKSYNC_MANAGEMENT_CIDRS=192.168.10.0/24 PATH="${test_root}/bin:${PATH}" bash "${installer}" install 2>&1)"
grep -Fq "Installed BookmarksSync ${tag}" <<<"$install_output"
[[ -x "${test_root}/install-root/usr/local/bin/bookmarksync" ]]
grep -Fxq 'BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env"
grep -Fxq 'BOOKMARKSYNC_MANAGEMENT_CIDRS=192.168.10.0/24,127.0.0.1/32,::1/128' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env"
grep -Fq '# Managed by BookmarksSync installer' "${test_root}/install-root/etc/systemd/system/bookmarksync.service"
grep -Fq 'attestation verify' "${test_root}/gh.log"
grep -Fq -- '--signer-workflow atikansari-ghr/BookmarksSync/.github/workflows/ci.yml' "${test_root}/gh.log"
grep -Fq -- '--source-ref refs/heads/main' "${test_root}/gh.log"
grep -Fq -- '--source-digest aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "${test_root}/gh.log"
[[ "$(stat -c '%a' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env")" == 600 ]]
[[ "$(stat -c '%a' "${test_root}/install-root/etc/systemd/system/bookmarksync.service")" == 644 ]]
[[ -x "${test_root}/install-root/usr/local/sbin/bookmarksync-installer" ]]
# Debian browser updates use an empty SHA marker queue watched by a root .path
# unit. No sudo helper, sudoers rule or setuid handoff is installed.
[[ ! -e "${test_root}/install-root/usr/local/sbin/bookmarksync-update" && ! -e "${test_root}/install-root/etc/sudoers.d/bookmarksync-update" && ! -e "${test_root}/install-root/usr/local/libexec/bookmarksync-update-worker" ]]
[[ ! -s "${test_root}/forbidden.log" ]]
queue_root="${test_root}/install-root/var/lib/bookmarksync-update-queue"
[[ -x "${test_root}/install-root/usr/local/libexec/bookmarksync-update-queue-worker" ]]
grep -Fxq '# Managed by BookmarksSync installer' "${test_root}/install-root/usr/local/libexec/bookmarksync-update-queue-worker"
[[ "$(stat -c '%a' "$queue_root")" == 755 && "$(stat -c '%a' "$queue_root/inbox")" == 700 && "$(stat -c '%a' "$queue_root/processing")" == 700 && "$(stat -c '%a' "$queue_root/rejected")" == 700 ]]
grep -Fxq 'BookmarksSync Debian update queue' "$queue_root/OWNER"
app_unit="${test_root}/install-root/etc/systemd/system/bookmarksync.service"
grep -Fxq 'Environment=BOOKMARKSYNC_DEBIAN_UPDATE_QUEUE=/var/lib/bookmarksync-update-queue/inbox' "$app_unit"
grep -Fxq 'NoNewPrivileges=true' "$app_unit"
grep -Fxq 'ReadWritePaths=/var/lib/bookmarksync -/var/lib/bookmarksync-update-queue/inbox' "$app_unit"
! grep -Fq 'bookmarksync-updates' "$app_unit"
! grep -Fqi 'sudo' "$app_unit"
queue_service="${test_root}/install-root/etc/systemd/system/bookmarksync-update-queue.service"
queue_path_unit="${test_root}/install-root/etc/systemd/system/bookmarksync-update-queue.path"
grep -Fxq '# Managed by BookmarksSync installer' "$queue_service"
grep -Fxq '# Managed by BookmarksSync installer' "$queue_path_unit"
grep -Fxq 'ExecStart=/usr/local/libexec/bookmarksync-update-queue-worker' "$queue_service"
[[ "$(grep -Ec '^Exec[A-Za-z]+=' "$queue_service")" == 1 ]]
grep -Fxq 'Type=oneshot' "$queue_service"
grep -Fxq 'NoNewPrivileges=true' "$queue_service"
grep -Fxq 'DirectoryNotEmpty=/var/lib/bookmarksync-update-queue/inbox' "$queue_path_unit"
grep -Fxq 'Unit=bookmarksync-update-queue.service' "$queue_path_unit"
[[ "$(stat -c '%a' "$queue_service")" == 644 && "$(stat -c '%a' "$queue_path_unit")" == 644 ]]
grep -Fxq 'enable --now bookmarksync-update-queue.path' "${test_root}/systemctl.log"
grep -Fxq 'http://127.0.0.1:18081/readyz' "${test_root}/curl.log"

# Upgrades of sudo-era installs remove only marker-bearing legacy helpers and
# leave unmanaged files untouched.
mkdir -p "${test_root}/install-root/usr/local/sbin" "${test_root}/install-root/usr/local/libexec" "${test_root}/install-root/etc/sudoers.d"
printf '#!/usr/bin/env bash\n# Managed by BookmarksSync installer\nexit 0\n' > "${test_root}/install-root/usr/local/sbin/bookmarksync-update"
printf '#!/usr/bin/env bash\n# Managed by BookmarksSync installer\nexit 0\n' > "${test_root}/install-root/usr/local/libexec/bookmarksync-update-worker"
printf '# operator-owned rule\n' > "${test_root}/install-root/etc/sudoers.d/bookmarksync-update"
chmod 0644 "${test_root}/install-root/etc/bookmarksync/bookmarksync.env"
legacy_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1)"
grep -Fq "Upgraded BookmarksSync to ${tag}" <<<"$legacy_output"
[[ "$(stat -c '%a' "${test_root}/install-root/etc/bookmarksync/bookmarksync.env")" == 600 ]]
[[ ! -e "${test_root}/install-root/usr/local/sbin/bookmarksync-update" && ! -e "${test_root}/install-root/usr/local/libexec/bookmarksync-update-worker" ]]
grep -Fxq '# operator-owned rule' "${test_root}/install-root/etc/sudoers.d/bookmarksync-update"
grep -Fq 'left unmanaged legacy file in place' <<<"$legacy_output"
rm -f -- "${test_root}/install-root/etc/sudoers.d/bookmarksync-update"
[[ ! -s "${test_root}/forbidden.log" ]]

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

# A5 regression: a failed repair activation restarts the old service exactly as
# the upgrade path does instead of exiting with the service stopped.
mkdir -p "${test_root}/mvfail-bin"
cat >"${test_root}/mvfail-bin/mv" <<'EOF'
#!/usr/bin/env bash
if (($# >= 2)) && [[ "${!#}" == "$BOOKMARKSYNC_TEST_MV_FAIL_TARGET" && "${@: -2:1}" == "${BOOKMARKSYNC_TEST_MV_FAIL_TARGET}.next" ]]; then
  printf 'mock activation failure\n' >&2
  exit 1
fi
exec /bin/mv "$@"
EOF
chmod +x "${test_root}/mvfail-bin/mv"
: >"${test_root}/systemctl.log"
repair_mv_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_MV_FAIL_TARGET="${test_root}/install-root/usr/local/bin/bookmarksync" PATH="${test_root}/mvfail-bin:${test_root}/bin:${PATH}" bash "${installer}" repair --tag "$tag" 2>&1 || true)"
grep -Fq 'could not activate repair binary; the old service was restarted' <<<"$repair_mv_output"
grep -Fxq 'stop bookmarksync.service' "${test_root}/systemctl.log"
[[ "$(tail -n 1 "${test_root}/systemctl.log")" == 'start bookmarksync.service' ]]
[[ -x "${test_root}/install-root/usr/local/bin/bookmarksync" && ! -e "${test_root}/install-root/usr/local/bin/bookmarksync.next" ]]

: >"${test_root}/systemctl.log"
mkdir -m 0700 "${test_root}/install-root/var/lib/bookmarksync/backup-keys"
printf 'recovery-key-retained\n' > "${test_root}/install-root/var/lib/bookmarksync/backup-keys/destination.key"
chmod 0600 "${test_root}/install-root/var/lib/bookmarksync/backup-keys/destination.key"
printf '30\n' > "${test_root}/health-fail-count"
rollback_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_HEALTH_FAIL_COUNT_FILE="${test_root}/health-fail-count" PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq 'old binary and data were restored' <<<"$rollback_output"
[[ -d "${test_root}/install-root/var/lib/bookmarksync" ]]
[[ -n "$(find "${test_root}/install-root/var/lib" -maxdepth 1 -type d -name 'bookmarksync.failed-*' -print -quit)" ]]
grep -Fxq 'start bookmarksync.service' "${test_root}/systemctl.log"
grep -Fxq recovery-key-retained "${test_root}/install-root/var/lib/bookmarksync/backup-keys/destination.key"
[[ "$(stat -c '%a' "${test_root}/install-root/var/lib/bookmarksync/backup-keys/destination.key")" = 600 ]]

: >"${test_root}/systemctl.log"
restore_failure_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/install-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_HEALTH_FAIL=1 BOOKMARKSYNC_TEST_RESTORE_FAIL=1 PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq 'rollback restore failed' <<<"$restore_failure_output"
grep -Fq 'service remains stopped' <<<"$restore_failure_output"
failed_data="$(find "${test_root}/install-root/var/lib" -maxdepth 1 -type d -name 'bookmarksync.failed-*' -print -quit)"
[[ -n "$failed_data" && -d "$failed_data" ]]
[[ -n "$(find "${test_root}/install-root/var/lib" -path '*/backup-keys/destination.key' -type f -print -quit)" ]]
! grep -Fxq 'start bookmarksync.service' "${test_root}/systemctl.log"

mkdir -p "${test_root}/unmanaged-root/etc/systemd/system"
printf '[Service]\nExecStart=/unmanaged\n' >"${test_root}/unmanaged-root/etc/systemd/system/bookmarksync.service"
unmanaged_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/unmanaged-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com PATH="${test_root}/bin:${PATH}" bash "${installer}" install 2>&1 || true)"
grep -Fq 'refusing to overwrite an unmanaged systemd unit' <<<"$unmanaged_output"
[[ ! -e "${test_root}/unmanaged-root/usr/local/bin/bookmarksync" ]]

# A3 regression: readiness probes the configured listen host (wildcards map to
# the matching loopback) instead of assuming 127.0.0.1.
listen_case=0
for listen_pair in '10.0.0.5:18082 http://10.0.0.5:18082/readyz' '[::1]:18083 http://[::1]:18083/readyz' '0.0.0.0:18084 http://127.0.0.1:18084/readyz' '[::]:18085 http://[::1]:18085/readyz' 'localhost:18086 http://localhost:18086/readyz'; do
  listen_case=$((listen_case + 1))
  listen_value=${listen_pair%% *}
  expected_url=${listen_pair#* }
  mkdir -p "${test_root}/listen-root-${listen_case}"
  : >"${test_root}/listen-curl.log"
  listen_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/listen-root-${listen_case}" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_CURL_LOG="${test_root}/listen-curl.log" BOOKMARKSYNC_TEST_READY_URL="$expected_url" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com BOOKMARKSYNC_LISTEN="$listen_value" PATH="${test_root}/bin:${PATH}" bash "${installer}" install 2>&1 || true)"
  grep -Fq "Installed BookmarksSync ${tag}" <<<"$listen_output" || { printf 'readiness failed for listen %s: %s\n' "$listen_value" "$listen_output" >&2; exit 1; }
  grep -Fxq "$expected_url" "${test_root}/listen-curl.log"
done
# Upgrade readiness uses the same configured host and does not roll back.
: >"${test_root}/systemctl.log"
listen_upgrade_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/listen-root-1" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_READY_URL=http://10.0.0.5:18082/readyz PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq "Upgraded BookmarksSync to ${tag}" <<<"$listen_upgrade_output"
[[ -z "$(find "${test_root}/listen-root-1/var/lib" -maxdepth 1 -name 'bookmarksync.failed-*' -print -quit)" ]]
# A tampered listen value is parsed as data only, never sourced or evaluated.
printf 'BOOKMARKSYNC_LISTEN=$(touch %s/pwned):1\n' "$test_root" > "${test_root}/listen-root-1/etc/bookmarksync/bookmarksync.env.tmp"
grep -v '^BOOKMARKSYNC_LISTEN=' "${test_root}/listen-root-1/etc/bookmarksync/bookmarksync.env" >> "${test_root}/listen-root-1/etc/bookmarksync/bookmarksync.env.tmp"
mv -f "${test_root}/listen-root-1/etc/bookmarksync/bookmarksync.env.tmp" "${test_root}/listen-root-1/etc/bookmarksync/bookmarksync.env"
: >"${test_root}/systemctl.log"
tampered_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/listen-root-1" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq 'BOOKMARKSYNC_LISTEN is not a recognized listen address' <<<"$tampered_output"
[[ ! -e "${test_root}/pwned" ]]
! grep -Fxq 'stop bookmarksync.service' "${test_root}/systemctl.log"

# Unmanaged queue artifacts are refused before any binary or service change.
mkdir -p "${test_root}/unmanaged-queue-root/etc/systemd/system"
printf '[Path]\nDirectoryNotEmpty=/tmp\n' >"${test_root}/unmanaged-queue-root/etc/systemd/system/bookmarksync-update-queue.path"
unmanaged_queue_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/unmanaged-queue-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com PATH="${test_root}/bin:${PATH}" bash "${installer}" install 2>&1 || true)"
grep -Fq 'refusing to overwrite unmanaged update queue artifact' <<<"$unmanaged_queue_output"
[[ ! -e "${test_root}/unmanaged-queue-root/usr/local/bin/bookmarksync" ]]
grep -Fxq 'DirectoryNotEmpty=/tmp' "${test_root}/unmanaged-queue-root/etc/systemd/system/bookmarksync-update-queue.path"
mkdir -p "${test_root}/unmanaged-queue-dir-root/var/lib/bookmarksync-update-queue"
unmanaged_queue_dir_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/unmanaged-queue-dir-root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com PATH="${test_root}/bin:${PATH}" bash "${installer}" install 2>&1 || true)"
grep -Fq 'refusing unmanaged update queue directory' <<<"$unmanaged_queue_dir_output"
[[ ! -e "${test_root}/unmanaged-queue-dir-root/usr/local/bin/bookmarksync" ]]
for kind in file directory; do
  fixture_root="${test_root}/unmanaged-installer-${kind}"
  mkdir -p "$fixture_root/usr/local/sbin"
  if [[ "$kind" == file ]]; then printf '# operator-owned\n' > "$fixture_root/usr/local/sbin/bookmarksync-installer";
  else mkdir "$fixture_root/usr/local/sbin/bookmarksync-installer"; fi
  output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_INSTALL_ROOT="$fixture_root" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com PATH="${test_root}/bin:${PATH}" bash "$installer" install 2>&1 || true)"
  grep -Fq 'refusing unmanaged installer copy' <<< "$output"
  [[ ! -e "$fixture_root/usr/local/bin/bookmarksync" ]]
done
# Upgrade preflight refuses an unmanaged queue unit before stopping the service.
cp "${test_root}/unmanaged-queue-root/etc/systemd/system/bookmarksync-update-queue.path" "${test_root}/listen-root-2/etc/systemd/system/bookmarksync-update-queue.path"
: >"${test_root}/systemctl.log"
unmanaged_upgrade_output="$(BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="${test_root}/listen-root-2" BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release-valid.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" BOOKMARKSYNC_TEST_GH_LOG="${test_root}/gh.log" BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" BOOKMARKSYNC_TEST_READY_URL='http://[::1]:18083/readyz' PATH="${test_root}/bin:${PATH}" bash "${installer}" upgrade --tag "$tag" 2>&1 || true)"
grep -Fq 'refusing to overwrite unmanaged update queue artifact' <<<"$unmanaged_upgrade_output"
! grep -Fxq 'stop bookmarksync.service' "${test_root}/systemctl.log"
[[ ! -s "${test_root}/forbidden.log" ]]

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
printf 'PASS: browser updates install a root .path queue worker without sudo, sudoers or setuid handoff\n'
printf 'PASS: app unit keeps NoNewPrivileges, sets the fixed queue and drops snapshot write access\n'
printf 'PASS: upgrades remove only marker-bearing legacy sudo helpers and keep unmanaged files\n'
printf 'PASS: unmanaged queue units and directories are refused before changes\n'
printf 'PASS: readiness probes the configured listen host, wildcard loopback and IPv6 literals\n'
printf 'PASS: tampered listen values are rejected before maintenance without evaluation\n'
printf 'PASS: failed repair activation restarts the old service\n'
