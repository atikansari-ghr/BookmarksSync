#!/usr/bin/env bash
set -Eeuo pipefail

[[ "$(id -u)" -eq 0 ]] || { printf 'run as root inside a disposable Debian test environment\n' >&2; exit 1; }
[[ -x "${BOOKMARKSYNC_TEST_BINARY:-}" ]] || { printf 'set BOOKMARKSYNC_TEST_BINARY to a built BookmarksSync executable\n' >&2; exit 1; }

repo="$(cd -- "$(dirname -- "$0")/../.." && pwd)"
helper="${repo}/scripts/debian/bookmarksync-update-geoip"
fixtures="${repo}/internal/network/testdata"
tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT

mkdir -p "$tmp/bin" "$tmp/db"
printf 'AccountID 12345\nLicenseKey never-print-this-secret\n' > "$tmp/GeoIP.conf"
chmod 0600 "$tmp/GeoIP.conf"

cat > "$tmp/bin/geoipupdate" <<'STUB'
#!/usr/bin/env bash
set -eu
[[ "${UPDATE_MODE:-ok}" == fail ]] && exit 23
out=""
while (($#)); do
  case "$1" in
    -f) shift 2 ;;
    -d) out="$2"; shift 2 ;;
    *) exit 24 ;;
  esac
done
[[ -n "$out" && -s "${UPDATE_SOURCE:?}" ]]
cp -- "$UPDATE_SOURCE" "$out/GeoLite2-Country.mmdb"
STUB
cat > "$tmp/bin/systemctl" <<'STUB'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
[[ "${RESTART_MODE:-ok}" != fail ]]
STUB
chmod 0755 "$tmp/bin/geoipupdate" "$tmp/bin/systemctl"

export BOOKMARKSYNC_GEOIP_CONFIG="$tmp/GeoIP.conf"
export BOOKMARKSYNC_GEOIP_DIR="$tmp/db"
export BOOKMARKSYNC_GEOIP_FILE="$tmp/db/GeoLite2-Country.mmdb"
export BOOKMARKSYNC_GEOIP_STAGE="$tmp/db/.staging"
export BOOKMARKSYNC_GEOIP_LOADED_HASH="$tmp/db/.loaded-sha256"
export BOOKMARKSYNC_GEOIP_UPDATER="$tmp/bin/geoipupdate"
export BOOKMARKSYNC_BINARY="$BOOKMARKSYNC_TEST_BINARY"
export BOOKMARKSYNC_SERVICE=bookmarksync-test.service
export BOOKMARKSYNC_SERVICE_USER=bookmarksync
export BOOKMARKSYNC_SERVICE_GROUP=bookmarksync
export PATH="$tmp/bin:$PATH"
export SYSTEMCTL_LOG="$tmp/systemctl.log"
export UPDATE_SOURCE="$fixtures/GeoLite2-Country-Test.mmdb"

"$helper"
[[ -s "$BOOKMARKSYNC_GEOIP_FILE" && -s "$BOOKMARKSYNC_GEOIP_LOADED_HASH" ]]
[[ "$(stat -c '%a' "$BOOKMARKSYNC_GEOIP_FILE")" == 644 ]]
[[ "$(stat -c '%U:%G' "$BOOKMARKSYNC_GEOIP_DIR")" == root:root ]]
[[ "$(cat "$SYSTEMCTL_LOG")" == 'try-restart bookmarksync-test.service' ]]

"$helper" > "$tmp/unchanged.out"
grep -q 'database is current' "$tmp/unchanged.out"
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 1 ]]

export UPDATE_SOURCE="$fixtures/GeoIP2-Country-Test.mmdb"
"$helper"
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 2 ]]

known_good="$(sha256sum "$BOOKMARKSYNC_GEOIP_FILE" | cut -d ' ' -f 1)"
export UPDATE_SOURCE="$fixtures/GeoLite2-Country-Test.mmdb"
export BOOKMARKSYNC_GEOIP_SKIP_RESTART=1
"$helper" > "$tmp/deferred-restart.out"
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 2 ]]
[[ "$(cat "$BOOKMARKSYNC_GEOIP_LOADED_HASH")" != "$(sha256sum "$BOOKMARKSYNC_GEOIP_FILE" | cut -d ' ' -f 1)" ]]
unset BOOKMARKSYNC_GEOIP_SKIP_RESTART
export RESTART_MODE=fail
if "$helper" > "$tmp/restart-failure.out" 2>&1; then
  printf 'service restart failure unexpectedly succeeded\n' >&2
  exit 1
fi
unset RESTART_MODE
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 3 ]]
"$helper" > "$tmp/restart-retry.out"
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 4 ]]
[[ "$(cat "$BOOKMARKSYNC_GEOIP_LOADED_HASH")" == "$(sha256sum "$BOOKMARKSYNC_GEOIP_FILE" | cut -d ' ' -f 1)" ]]
known_good="$(sha256sum "$BOOKMARKSYNC_GEOIP_FILE" | cut -d ' ' -f 1)"

export UPDATE_MODE=fail
if "$helper" > "$tmp/failure.out" 2>&1; then
  printf 'updater failure unexpectedly succeeded\n' >&2
  exit 1
fi
[[ "$(sha256sum "$BOOKMARKSYNC_GEOIP_FILE" | cut -d ' ' -f 1)" == "$known_good" ]]
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 4 ]]
! grep -q 'never-print-this-secret' "$tmp/failure.out"

unset UPDATE_MODE
printf 'not an MMDB' > "$tmp/invalid.mmdb"
export UPDATE_SOURCE="$tmp/invalid.mmdb"
if "$helper" > "$tmp/invalid.out" 2>&1; then
  printf 'invalid database unexpectedly succeeded\n' >&2
  exit 1
fi
[[ "$(sha256sum "$BOOKMARKSYNC_GEOIP_FILE" | cut -d ' ' -f 1)" == "$known_good" ]]
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 4 ]]
! grep -q 'never-print-this-secret' "$tmp/invalid.out"

chmod 0644 "$BOOKMARKSYNC_GEOIP_CONFIG"
if "$helper" > "$tmp/insecure-config.out" 2>&1; then
  printf 'insecure MaxMind config unexpectedly succeeded\n' >&2
  exit 1
fi
chmod 0600 "$BOOKMARKSYNC_GEOIP_CONFIG"
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 4 ]]
! grep -q 'never-print-this-secret' "$tmp/insecure-config.out"

chmod 0777 "$tmp"
if "$helper" > "$tmp/insecure-parent.out" 2>&1; then
  printf 'insecure MaxMind config directory unexpectedly succeeded\n' >&2
  exit 1
fi
chmod 0700 "$tmp"
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 4 ]]
! grep -q 'never-print-this-secret' "$tmp/insecure-parent.out"

rm -rf -- "$BOOKMARKSYNC_GEOIP_STAGE"
mkdir "$tmp/escape-stage"
ln -s "$tmp/escape-stage" "$BOOKMARKSYNC_GEOIP_STAGE"
if "$helper" > "$tmp/stage-symlink.out" 2>&1; then
  printf 'symlinked staging directory unexpectedly succeeded\n' >&2
  exit 1
fi
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 4 ]]
rm -- "$BOOKMARKSYNC_GEOIP_STAGE"
mkdir "$BOOKMARKSYNC_GEOIP_STAGE"

mv -- "$BOOKMARKSYNC_GEOIP_FILE" "$tmp/real.mmdb"
ln -s "$tmp/real.mmdb" "$BOOKMARKSYNC_GEOIP_FILE"
if "$helper" > "$tmp/live-symlink.out" 2>&1; then
  printf 'symlinked live database unexpectedly succeeded\n' >&2
  exit 1
fi
[[ "$(wc -l < "$SYSTEMCTL_LOG")" -eq 4 ]]
rm -- "$BOOKMARKSYNC_GEOIP_FILE"
mv -- "$tmp/real.mmdb" "$BOOKMARKSYNC_GEOIP_FILE"
[[ "$(sha256sum "$BOOKMARKSYNC_GEOIP_FILE" | cut -d ' ' -f 1)" == "$known_good" ]]

printf 'GeoIP updater integration cases passed (changed, unchanged, deferred/retried restart, updater failure, invalid, config/directory permissions, symlink rejection); unsafe paths preserved.\n'
