#!/usr/bin/env bash
# Managed by BookmarksSync Docker update setup
set -Eeuo pipefail
umask 077
fail() { printf 'BookmarksSync Docker worker: %s\n' "$1" >&2; exit 1; }
prefix=''
if [[ "${BOOKMARKSYNC_TESTING:-0}" = 1 ]]; then
  prefix=${BOOKMARKSYNC_INSTALL_ROOT:-}
  [[ "$prefix" = /tmp/* && -d "$prefix" && ! -L "$prefix" && "$(realpath -e -- "$prefix")" = "$prefix" ]] || fail 'isolated test root required'
else
  [[ "$(id -u)" = 0 ]] || fail 'root required'
  export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
fi
safe_path() {
  local path=$1 part mode
  [[ "$path" =~ ^/[A-Za-z0-9_./+-]+$ && -e "$path" && ! -L "$path" && "$(realpath -e -- "$path")" = "$path" ]] || fail 'unsafe configured path'
  if [[ -n "$prefix" ]]; then [[ "$path" = "$prefix" || "$path" = "$prefix"/* ]] || fail 'test path escaped root'; return; fi
  part=$path
  while :; do
    mode=$(stat -c '%a' "$part")
    [[ "$(stat -c '%u' "$part")" = 0 ]] && (( (8#$mode & 0022) == 0 )) || fail 'configured paths and ancestors must be root owned and protected'
    [[ "$part" != / ]] || break
    part=$(dirname -- "$part")
  done
}
config=$prefix/etc/bookmarksync-docker-updates.env
if [[ "${1:-}" = --check-config && $# = 2 ]]; then config=$2; check_only=1; elif [[ $# = 0 ]]; then check_only=0; else fail 'invalid arguments'; fi
safe_path "$config"
[[ -f "$config" && "$(stat -c '%a' "$config")" = 600 ]] || fail 'config must be a regular mode 0600 file'
deployment=''; backups=''
while IFS='=' read -r key value || [[ -n "$key" ]]; do
  case "$key" in
    BOOKMARKSYNC_DOCKER_DEPLOYMENT_DIR) [[ -z "$deployment" ]] || fail 'duplicate deployment'; deployment=$value;;
    BOOKMARKSYNC_DOCKER_BACKUP_DIR) [[ -z "$backups" ]] || fail 'duplicate backup directory'; backups=$value;;
    '') [[ -z "$value" ]] || fail 'invalid config';;
    *) fail 'unknown config key';;
  esac
done < "$config"
[[ -n "$deployment" && -n "$backups" ]] || fail 'incomplete config'
safe_path "$deployment"
[[ -d "$deployment" ]] || fail 'deployment directory unavailable'
for artifact in scripts/docker/maintain.sh scripts/docker/restore-volume.sh compose.yaml compose.admin-updates.yaml; do safe_path "$deployment/$artifact"; [[ -f "$deployment/$artifact" ]] || fail 'deployment artifact is not regular'; done
if [[ -e "$deployment/.tools" || -L "$deployment/.tools" ]]; then
  safe_path "$deployment/.tools"
  [[ -d "$deployment/.tools" ]] || fail 'deployment maintenance state is not a directory'
  state_mode=$(stat -c '%a' "$deployment/.tools")
  (( (8#$state_mode & 0022) == 0 )) || fail 'deployment maintenance state is writable by other users'
fi
if [[ -e "$deployment/.tools/docker-maintenance.lock" || -L "$deployment/.tools/docker-maintenance.lock" ]]; then
  safe_path "$deployment/.tools/docker-maintenance.lock"
  [[ -f "$deployment/.tools/docker-maintenance.lock" && "$(stat -c '%a:%h' "$deployment/.tools/docker-maintenance.lock")" = 600:1 ]] || fail 'unsafe deployment maintenance lock'
fi
if [[ -e "$deployment/.env" || -L "$deployment/.env" ]]; then safe_path "$deployment/.env"; [[ "$(stat -c '%a' "$deployment/.env")" = 600 ]] || fail 'deployment environment must have mode 0600'; fi
# The independent backup directory belongs to container UID65532. Its parent
# chain must be root protected; the worker only writes root-owned records there.
safe_path "$(dirname -- "$backups")"
[[ "$backups" =~ ^/[A-Za-z0-9_./+-]+$ && -d "$backups" && ! -L "$backups" && "$(realpath -e -- "$backups")" = "$backups" ]] || fail 'unsafe backup directory'
if [[ -n "$prefix" ]]; then [[ "$backups" = "$prefix"/* ]] || fail 'test backup escaped root';
else [[ "$(stat -c '%u:%a' "$backups")" = 65532:700 ]] || fail 'backup directory must belong to UID65532 with mode0700'; fi
[[ "$check_only" = 0 ]] || exit 0
queue=$prefix/var/lib/bookmarksync-docker-updates
safe_path "$queue"
[[ -d "$queue" ]] || fail 'queue root unavailable'
inbox=$queue/inbox
[[ -d "$inbox" && ! -L "$inbox" && "$(realpath -e -- "$inbox")" = "$inbox" ]] || fail 'unsafe inbox'
if [[ -z "$prefix" ]]; then [[ "$(stat -c '%u:%a' "$inbox")" = 65532:700 ]] || fail 'unsafe inbox owner/mode'; fi
for dir in processing rejected; do safe_path "$queue/$dir"; [[ -d "$queue/$dir" && "$(stat -c '%a' "$queue/$dir")" = 700 ]] || fail 'unsafe private queue directory'; done
[[ ! -L "$queue/worker.lock" ]] || fail 'unsafe worker lock'
exec 9> "$queue/worker.lock"
flock -n 9 || exit 0
while IFS= read -r -d '' request; do
  [[ -e "$request" || -L "$request" ]] || continue
  # Rename into a root-private directory before checking inode properties.
  claimed=$(mktemp "$queue/processing/request.XXXXXXXX")
  rm -f -- "$claimed"
  if ! mv -T -- "$request" "$claimed"; then rm -f -- "$claimed"; continue; fi
  tag=$(basename -- "$request")
  valid=1
  [[ "$tag" =~ ^build-[a-f0-9]{40}$ && -f "$claimed" && ! -L "$claimed" && ! -s "$claimed" && "$(stat -c '%h:%a' "$claimed")" = 1:600 ]] || valid=0
  if [[ -z "$prefix" && "$(stat -c '%u' "$claimed")" != 65532 ]]; then valid=0; fi
  if [[ "$valid" = 0 ]]; then
    mv -T -- "$claimed" "$queue/rejected/$(basename -- "$claimed")"
    printf 'Rejected unsafe update request.\n' >&2
    continue
  fi
  if BOOKMARKSYNC_DOCKER_ADMIN_UPDATES=1 bash "$deployment/scripts/docker/maintain.sh" upgrade "ghcr.io/atikansari-ghr/bookmarksync:$tag" "$backups"; then
    rm -f -- "$claimed"
    printf 'Approved update completed.\n'
  else
    mv -T -- "$claimed" "$queue/rejected/$(basename -- "$claimed")"
    printf 'Approved update failed; inspect host journal and retained snapshots.\n' >&2
  fi
done < <(find "$inbox" -mindepth 1 -maxdepth 1 -print0 | head -z -n 64)
