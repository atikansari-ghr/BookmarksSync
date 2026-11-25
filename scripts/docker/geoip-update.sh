#!/bin/sh
# Never enable shell tracing: configuration and updater output are private.
set -eu
umask 077
config=${BOOKMARKSYNC_GEOIP_CONFIG:-/run/secrets/maxmind_config}
directory=${BOOKMARKSYNC_GEOIP_DIR:-/geo}
interval=${BOOKMARKSYNC_GEOIP_INTERVAL:-86400}
fail() { printf 'GeoLite update: %s\n' "$1" >&2; return 1; }
case "$interval" in ''|*[!0-9]*) fail 'invalid interval'; exit 1;; esac
[ "$interval" -ge 3600 ] && [ "$interval" -le 604800 ] || { fail 'interval must be 3600..604800 seconds'; exit 1; }
case "${1:-schedule}" in once|schedule) mode=${1:-schedule};; *) fail 'expected once or schedule'; exit 1;; esac
[ "$#" -le 1 ] || { fail 'too many arguments'; exit 1; }
update() (
  [ -f "$config" ] && [ ! -L "$config" ] && [ -r "$config" ] || { fail 'protected config unavailable'; exit 1; }
  [ "$(stat -c '%u' "$config")" = "$(id -u)" ] || { fail 'config must belong to updater user'; exit 1; }
  case "$(stat -c '%a' "$config")" in 400|600) ;; *) fail 'config must have mode 0400 or 0600'; exit 1;; esac
  [ -d "$directory" ] && [ ! -L "$directory" ] || { fail 'database directory unavailable or unsafe'; exit 1; }
  live=$directory/GeoLite2-Country.mmdb
  [ ! -L "$live" ] && { [ ! -e "$live" ] || [ -f "$live" ]; } || { fail 'unsafe live database'; exit 1; }
  # mkdir is an interprocess lock; never delete a lock owned by another writer.
  lock=$directory/.update-lock
  mkdir "$lock" 2>/dev/null || { fail 'another update is running (or a stale lock needs operator review)'; exit 1; }
  stage=''
  cleanup() { [ -z "$stage" ] || rm -rf -- "$stage"; rmdir "$lock"; }
  trap cleanup EXIT
  trap 'exit 1' HUP INT TERM
  stage=$(mktemp -d "$directory/.staging.XXXXXXXX")
  # geoipupdate can include request details in errors: emit only fixed messages.
  geoipupdate -f "$config" -d "$stage" > /dev/null 2>&1 || { fail 'download failed; retained previous database'; exit 1; }
  candidate=$stage/GeoLite2-Country.mmdb
  [ -s "$candidate" ] && [ -f "$candidate" ] && [ ! -L "$candidate" ] || { fail 'download missing; retained previous database'; exit 1; }
  bookmarksync network validate-geo --file "$candidate" > /dev/null 2>&1 || { fail 'validation failed; retained previous database'; exit 1; }
  if [ -f "$live" ] && cmp -s "$candidate" "$live"; then
    printf 'GeoLite update: database is current\n'
    exit 0
  fi
  chmod 644 "$candidate"
  # Staging lives on the same filesystem; rename publishes one complete file.
  mv -f -- "$candidate" "$live"
  printf 'GeoLite update: published validated database\n'
)
if [ "$mode" = once ]; then update; exit; fi
trap 'exit 0' HUP INT TERM
while :; do
  update || :
  sleep "$interval" &
  wait "$!" || :
done
