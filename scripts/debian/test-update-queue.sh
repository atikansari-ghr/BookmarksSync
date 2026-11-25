#!/usr/bin/env bash
# Exercises the Debian browser-update queue worker inside an isolated testing
# root. It never touches production paths, units or an installed service.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
installer="${script_dir}/bookmarksync"
if [[ "$(uname -s)" == MINGW* || "$(uname -s)" == MSYS* ]]; then
  printf 'SKIP: update queue worker test requires Linux\n'
  exit 0
fi
command -v flock >/dev/null 2>&1 || { printf 'BLOCKED: flock (util-linux) is required\n' >&2; exit 1; }
test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT
mkdir -p "${test_root}/bin" "${test_root}/fixtures" "${test_root}/root"

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
elif [[ "$url" == http://127.0.0.1:* ]]; then
  exit 0
elif [[ "$url" == http://* ]]; then
  exit 7
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
printf '#!/bin/sh\nexit 0\n' >"${test_root}/bin/sleep"
printf '#!/bin/sh\nexit 0\n' >"${test_root}/bin/gh"
for forbidden in sudo visudo systemd-run; do
  printf '#!/bin/sh\nprintf "%%s\\n" %s >> "%s/forbidden.log"\nexit 1\n' "$forbidden" "$test_root" >"${test_root}/bin/${forbidden}"
done
chmod +x "${test_root}/bin/"*

tag=build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
other_tag=build-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
fixtures="${test_root}/fixtures"
cat >"${fixtures}/bookmarksync-linux-amd64" <<'EOF'
#!/usr/bin/env bash
set -e
if [[ "$1" == backup && "$2" == create ]]; then
  while (($#)); do
    if [[ "$1" == --output ]]; then printf 'snapshot\n' > "$2"; exit 0; fi
    shift
  done
  exit 2
elif [[ "$1" == backup ]]; then
  exit 0
fi
printf 'test binary\n'
EOF
chmod +x "${fixtures}/bookmarksync-linux-amd64"
printf 'commit=%s\nbuild_date=2026-11-23T00:00:00Z\nversion=0.0.0-aaaaaaaaaaaa\n' "${tag#build-}" > "${fixtures}/BUILD_INFO.txt"
(cd "$fixtures" && sha256sum bookmarksync-linux-amd64 BUILD_INFO.txt > SHA256SUMS)
# The second release is catalog-valid, but its build metadata will not match,
# so a queued upgrade to it fails provenance before the service is touched.
cat >"${test_root}/release.json" <<EOF
[{"tag_name":"${tag}","draft":false,"prerelease":true,"published_at":"2026-11-23T00:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]},
 {"tag_name":"${other_tag}","draft":false,"prerelease":true,"published_at":"2026-11-22T00:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]}]
EOF

root="${test_root}/root"
run_env() {
  env BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_TEST_OS=debian BOOKMARKSYNC_INSTALL_ROOT="$root" \
    BOOKMARKSYNC_TEST_RELEASE_JSON="${test_root}/release.json" BOOKMARKSYNC_TEST_FIXTURES="$fixtures" \
    BOOKMARKSYNC_TEST_SYSTEMCTL_LOG="${test_root}/systemctl.log" PATH="${test_root}/bin:${PATH}" "$@"
}
run_env BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com BOOKMARKSYNC_LISTEN=127.0.0.1:18081 bash "$installer" install >/dev/null

worker="${root}/usr/local/libexec/bookmarksync-update-queue-worker"
queue="${root}/var/lib/bookmarksync-update-queue"
inbox="${queue}/inbox"
snapshots="${root}/var/lib/bookmarksync-updates/snapshots"
[[ -x "$worker" && -d "$inbox" ]]
grep -Fxq "readonly installer=${root}/usr/local/sbin/bookmarksync-installer" "$worker"
grep -Fxq "readonly queue=${queue}" "$worker"
grep -Fxq 'readonly testing=1' "$worker"
grep -Fxq "readonly service_uid=$(id -u)" "$worker"

mark() { (umask 077 && : > "${inbox}/$1"); }
run_worker() { run_env bash "$worker" 2>&1; }
count() { find "$1" -mindepth 1 -maxdepth 1 | wc -l; }
reset_log() { : >"${test_root}/systemctl.log"; }
service_stopped() { grep -Fxq 'stop bookmarksync.service' "${test_root}/systemctl.log"; }

# 1. A valid empty marker runs the fixed installer upgrade and is consumed.
reset_log
before="$(find "$snapshots" -maxdepth 1 -name 'preupdate-*.zip' | wc -l)"
mark "$tag"
output="$(run_worker)"
grep -Fq "Upgraded BookmarksSync to ${tag}" <<<"$output"
grep -Fq "Approved update ${tag} completed" <<<"$output"
[[ "$(count "$inbox")" == 0 && "$(count "$queue/processing")" == 0 && "$(count "$queue/rejected")" == 0 ]]
[[ "$(find "$snapshots" -maxdepth 1 -name 'preupdate-*.zip' | wc -l)" -gt "$before" ]]
printf 'PASS: valid SHA marker invokes the fixed installer upgrade and is consumed\n'

# 2. A failed upgrade retains the claimed marker in rejected/.
reset_log
mark "$other_tag"
output="$(run_worker)"
grep -Fq 'build metadata does not match the release tag' <<<"$output"
grep -Fq "Approved update ${other_tag} failed" <<<"$output"
[[ "$(count "$inbox")" == 0 && "$(count "$queue/processing")" == 0 ]]
[[ -n "$(find "$queue/rejected" -maxdepth 1 -name "${other_tag}.failed-*" -print -quit)" ]]
! service_stopped
printf 'PASS: failed upgrade retains its marker in root-private rejected storage\n'

# 3. Unsafe markers are rejected without invoking the installer.
rejected_before="$(count "$queue/rejected")"
reset_log
mark latest
mark build-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
mark "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
printf 'not empty' > "${inbox}/${tag}"; chmod 0600 "${inbox}/${tag}"
output="$(run_worker)"
[[ "$(count "$inbox")" == 0 && "$(count "$queue/rejected")" == $((rejected_before + 4)) ]]
! service_stopped
grep -Fq 'Rejected unsafe update request' <<<"$output"
(umask 077 && : > "${inbox}/${tag}"); chmod 0644 "${inbox}/${tag}"
run_worker >/dev/null
: > "${test_root}/outside"; chmod 0600 "${test_root}/outside"
ln "${test_root}/outside" "${inbox}/${tag}"
run_worker >/dev/null
[[ -f "${test_root}/outside" && "$(stat -c '%h' "${test_root}/outside")" == 2 ]]
rm -f -- "$(find "$queue/rejected" -samefile "${test_root}/outside" -print -quit)"
ln -s "${test_root}/outside" "${inbox}/${tag}"
run_worker >/dev/null
[[ -f "${test_root}/outside" && "$(stat -c '%h' "${test_root}/outside")" == 1 ]]
mkdir "${inbox}/${tag}"
run_worker >/dev/null
if [[ "$(id -u)" == 0 ]]; then
  mark "$tag"; chown 65534 "${inbox}/${tag}"
  run_worker >/dev/null
  wrong_owner_checked=1
fi
[[ "$(count "$inbox")" == 0 && "$(count "$queue/processing")" == 0 ]]
! service_stopped
[[ "$(find "$queue/rejected" -mindepth 1 -maxdepth 1 -name '*.invalid' | wc -l)" -ge 7 ]]
printf 'PASS: invalid names, non-empty, wrong-mode, hard-linked, symlinked and directory markers are rejected\n'
if [[ "${wrong_owner_checked:-0}" == 1 ]]; then
  printf 'PASS: markers not owned by the configured service UID are rejected\n'
else
  printf 'PENDING: wrong-owner marker rejection requires running this test as root\n'
fi

# 4. One run processes at most 64 entries.
for index in $(seq 1 70); do mark "invalid-${index}"; done
run_worker >/dev/null
[[ "$(count "$inbox")" == 6 ]]
run_worker >/dev/null
[[ "$(count "$inbox")" == 0 ]]
printf 'PASS: each worker run is bounded to 64 queue entries\n'

# 5. The worker lock is non-blocking and distinct from the maintenance lock.
mark "$tag"
reset_log
output="$(run_env flock -n "${queue}/worker.lock" bash "$worker" 2>&1)"
grep -Fq 'Another update queue worker is running' <<<"$output"
[[ -e "${inbox}/${tag}" ]]
! service_stopped
maintenance_lock="${root}/run/bookmarksync-maintenance/maintenance.lock"
[[ -f "$maintenance_lock" ]]
output="$(run_env flock -n "$maintenance_lock" bash "$worker" 2>&1)"
grep -Fq 'another BookmarksSync maintenance command is running' <<<"$output"
grep -Fq "Approved update ${tag} failed" <<<"$output"
[[ "$(count "$inbox")" == 0 ]]
! service_stopped
printf 'PASS: worker lock is non-blocking and separate from the installer maintenance lock\n'

# 6. Unsafe queue layout fails closed without consuming markers.
mark "$tag"
chmod 0755 "$inbox"
if run_worker >/dev/null; then printf 'worker accepted a group/other-readable inbox\n' >&2; exit 1; fi
[[ -e "${inbox}/${tag}" ]]
chmod 0700 "$inbox"
mv "$inbox" "${queue}/inbox-real"
ln -s inbox-real "$inbox"
if run_worker >/dev/null; then printf 'worker followed a symlinked inbox\n' >&2; exit 1; fi
[[ -e "${queue}/inbox-real/${tag}" ]]
rm -f -- "$inbox"
mv "${queue}/inbox-real" "$inbox"
mv "${queue}/OWNER" "${queue}/OWNER.saved"
if run_worker >/dev/null; then printf 'worker accepted an unmanaged queue root\n' >&2; exit 1; fi
mv "${queue}/OWNER.saved" "${queue}/OWNER"
if run_env bash "$worker" extra >/dev/null 2>&1; then printf 'worker accepted arguments\n' >&2; exit 1; fi
rm -f -- "${inbox}/${tag}"
printf 'PASS: unsafe inbox mode, symlinked inbox, unmanaged queue and arguments fail closed\n'

[[ ! -s "${test_root}/forbidden.log" ]]
printf 'PASS: queue worker never invokes sudo, visudo or systemd-run\n'
