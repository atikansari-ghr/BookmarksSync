#!/usr/bin/env bash
# Host operator tool. The web application never invokes Docker.
set -Eeuo pipefail
umask 077
base=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
compose=(docker compose --project-directory "$base" -f "$base/compose.yaml")
if [[ "${BOOKMARKSYNC_DOCKER_ADMIN_UPDATES:-0}" = 1 ]]; then compose+=(-f "$base/compose.admin-updates.yaml"); fi
fail() { printf 'BookmarksSync Docker: %s\n' "$1" >&2; exit 1; }
usage() {
  printf 'Usage: maintain.sh install | repair ABS_BACKUP_DIRECTORY | backup ABS_DIRECTORY | restore ABS_ARCHIVE ABS_NEW_DIRECTORY | diagnostics | upgrade IMMUTABLE_IMAGE ABS_BACKUP_DIRECTORY | rollback ABS_SNAPSHOT_RECORD ABS_BACKUP_DIRECTORY | uninstall\n'
}
directory() {
  local path=$1 resolved
  [[ "$path" = /* && "$path" != / && "$path" != *$'\n'* && "$path" != *:* && -d "$path" && ! -L "$path" ]] || fail 'target must be an existing absolute directory'
  resolved=$(realpath -e -- "$path")
  [[ "$resolved" = "$path" ]] || fail 'target must be canonical and contain no symlinks or dot segments'
  case "$resolved" in /etc|/etc/*|/usr|/usr/*|/bin|/sbin|/lib|/lib/*|/lib64|/proc|/proc/*|/sys|/sys/*|/dev|/dev/*|/run|/run/*|/var/lib/docker|/var/lib/docker/*) fail 'system directory is not a maintenance target';; esac
}
image() {
  [[ "$1" =~ ^ghcr\.io/atikansari-ghr/bookmarksync(@sha256:[a-f0-9]{64}|:build-[a-f0-9]{40})$ ]] || fail 'image must be a BookmarksSync SHA tag or registry digest'
}
lock_operations() {
  command -v flock >/dev/null 2>&1 || fail 'flock (util-linux) is required for host maintenance'
  [[ ! -L "$base/.tools" ]] || fail 'unsafe maintenance lock directory'
  mkdir -p -- "$base/.tools"
  [[ ! -L "$base/.tools/docker-maintenance.lock" ]] || fail 'unsafe maintenance lock file'
  if [[ "${BOOKMARKSYNC_DOCKER_ADMIN_UPDATES:-0}" = 1 ]]; then
    local mode
    mode=$(stat -c '%a' "$base/.tools")
    [[ "$(stat -c '%u' "$base/.tools")" = 0 ]] && (( (8#$mode & 0022) == 0 )) || fail 'browser update state must be root owned and protected'
    if [[ -e "$base/.tools/docker-maintenance.lock" ]]; then
      [[ -f "$base/.tools/docker-maintenance.lock" && "$(stat -c '%u:%a:%h' "$base/.tools/docker-maintenance.lock")" = 0:600:1 ]] || fail 'unsafe browser update lock'
    fi
  fi
  exec 9> "$base/.tools/docker-maintenance.lock"
  flock -n 9 || fail 'another maintenance command is running for this deployment'
}
resume=false
recover() {
  if [[ "$resume" = true ]]; then "${compose[@]}" start bookmarksync >/dev/null || printf 'BookmarksSync Docker: restart failed; inspect service status\n' >&2; fi
}
trap recover EXIT
stop_app() {
  if [[ -n "$("${compose[@]}" ps --status running -q bookmarksync)" ]]; then
    "${compose[@]}" stop bookmarksync
    resume=true
  fi
}
backup() {
  local target=$1 filename container installed_image
  directory "$target"
  filename="bookmarksync-$(date -u +%Y%m%dT%H%M%SZ)-$$.zip"
  [[ ! -e "$target/$filename" ]] || fail 'backup file already exists'
  container=$("${compose[@]}" ps -q bookmarksync)
  [[ -n "$container" ]] || fail 'service must exist for a backup'
  installed_image=$(docker inspect --format '{{.Image}}' "$container")
  [[ "$installed_image" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'cannot determine backup image'
  stop_app
  BOOKMARKSYNC_IMAGE="$installed_image" "${compose[@]}" run --rm --no-deps --no-build -T --volume "$target:/backup" bookmarksync backup create --data-dir /var/lib/bookmarksync --output "/backup/$filename"
  BOOKMARKSYNC_IMAGE="$installed_image" "${compose[@]}" run --rm --no-deps --no-build -T --volume "$target:/backup:ro" bookmarksync backup verify --file "/backup/$filename"
  last_backup="$target/$filename"
  printf 'Verified independent backup: %s/%s\n' "$target" "$filename"
}
archive_file() {
  local path=$1
  [[ "$path" = /* && "$path" != *:* && "$path" != *$'\n'* && -f "$path" && ! -L "$path" && "$(realpath -e -- "$path")" = "$path" ]] || fail 'archive/record must be a canonical regular absolute file'
}
baseline_image() {
  local candidate=$1 health
  [[ "$candidate" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'cannot determine previous local image'
  health=$(docker image inspect --format '{{json .Config.Healthcheck.Test}}' "$candidate")
  [[ "$health" = '["CMD","/usr/local/bin/bookmarksync","healthcheck"]' ]] || fail 'baseline image lacks supported healthcheck; use documented offline migration before managed upgrades'
}
wait_ready() {
  local attempt
  for attempt in {1..12}; do
    if timeout 8 "${compose[@]}" exec -T bookmarksync /usr/local/bin/bookmarksync healthcheck >/dev/null 2>&1; then return 0; fi
    sleep 5
  done
  return 1
}
write_record() {
  local image_id=$1 archive=$2 hash
  hash=$(sha256sum -- "$archive"); hash=${hash%% *}
  record="${archive%.zip}.snapshot"
  [[ ! -e "$record" && ! -L "$record" ]] || fail 'snapshot record already exists'
  printf 'bookmarksync-docker-snapshot-v1\nimage=%s\narchive=%s\nsha256=%s\n' "$image_id" "$(basename -- "$archive")" "$hash" > "$record"
  chmod 0600 "$record"
  printf 'Rollback record: %s\n' "$record"
}
read_record() {
  local path=$1 lines hash
  archive_file "$path"
  directory "$(dirname -- "$path")"
  mapfile -t lines < "$path"
  [[ ${#lines[@]} = 4 && "${lines[0]}" = bookmarksync-docker-snapshot-v1 && "${lines[1]}" =~ ^image=sha256:[a-f0-9]{64}$ && "${lines[2]}" =~ ^archive=[A-Za-z0-9][A-Za-z0-9._-]*\.zip$ && "${lines[3]}" =~ ^sha256=[a-f0-9]{64}$ ]] || fail 'invalid snapshot record'
  snapshot_image=${lines[1]#image=}
  snapshot_archive="$(dirname -- "$path")/${lines[2]#archive=}"
  archive_file "$snapshot_archive"
  hash=$(sha256sum -- "$snapshot_archive"); hash=${hash%% *}
  [[ "$hash" = "${lines[3]#sha256=}" ]] || fail 'snapshot archive hash differs from record'
  baseline_image "$snapshot_image"
}
restore_snapshot() {
  local selected_image=$1 archive=$2 nonce
  archive_file "$archive"
  nonce="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  BOOKMARKSYNC_IMAGE="$selected_image" "${compose[@]}" run --rm --no-deps --no-build -T --volume "$archive:/backup/source.zip:ro" --volume "$base/scripts/docker/restore-volume.sh:/run/bookmarksync-restore-volume.sh:ro" --entrypoint /bin/sh bookmarksync /run/bookmarksync-restore-volume.sh /backup/source.zip "$nonce"
}
start_image() {
  BOOKMARKSYNC_IMAGE="$1" "${compose[@]}" up -d --no-deps --no-build bookmarksync && wait_ready
}
persist_selected_image() {
  [[ "${BOOKMARKSYNC_DOCKER_ADMIN_UPDATES:-0}" = 1 ]] || return 0
  # A browser-triggered update must survive the next ordinary Compose restart.
  # Write only a validated image selection; preserve all other private settings.
  python3 - "$base" "$1" <<'PY'
import os, re, stat, sys, tempfile
base, image = sys.argv[1:]
if not re.fullmatch(r'(?:ghcr\.io/atikansari-ghr/bookmarksync@)?sha256:[a-f0-9]{64}', image):
    raise SystemExit('Invalid image selection')
path = os.path.join(base, '.env')
info = os.lstat(path)
if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o600:
    raise SystemExit('Deployment environment must be root-owned mode0600')
with open(path, encoding='utf-8') as source:
    lines = source.readlines()
lines = [line for line in lines if not re.match(r'^\s*(?:export\s+)?BOOKMARKSYNC_IMAGE\s*=', line)]
if lines and not lines[-1].endswith('\n'):
    lines[-1] += '\n'
lines.append('BOOKMARKSYNC_IMAGE=' + image + '\n')
fd, temporary = tempfile.mkstemp(prefix='image-env.', dir=os.path.join(base, '.tools'))
try:
    with os.fdopen(fd, 'w', encoding='utf-8') as target:
        target.writelines(lines)
        target.flush()
        os.fsync(target.fileno())
    os.replace(temporary, path)
    parent = os.open(base, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(parent)
    finally:
        os.close(parent)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
}
recover_snapshot() {
  local selected_image=$1 archive=$2
  "${compose[@]}" stop bookmarksync || return 1
  restore_snapshot "$selected_image" "$archive" || return 1
  start_image "$selected_image" && persist_selected_image "$selected_image"
}
verify_registry_image() {
  local requested=$1
  local -a source_digest=()
  command -v gh >/dev/null 2>&1 || fail 'GitHub CLI required for image provenance verification'
  docker pull "$requested"
  verified_image=$(docker image inspect --format '{{index .RepoDigests 0}}' "$requested")
  [[ "$verified_image" =~ ^ghcr\.io/atikansari-ghr/bookmarksync@sha256:[a-f0-9]{64}$ ]] || fail 'cannot resolve image to the expected registry digest'
  if [[ "$requested" = *@* ]]; then [[ "$requested" = "$verified_image" ]] || fail 'resolved image digest differs from requested digest'; fi
  if [[ "$requested" = *:build-* ]]; then source_digest=(--source-digest "${requested##*:build-}"); fi
  gh attestation verify "oci://$verified_image" --repo atikansari-ghr/BookmarksSync --signer-workflow atikansari-ghr/BookmarksSync/.github/workflows/ci.yml --source-ref refs/heads/main "${source_digest[@]}" >/dev/null || fail 'container provenance verification failed; live service unchanged'
}
[[ $# -ge 1 ]] || { usage; exit 2; }
action=$1
shift
case "$action" in
  install)
    [[ $# = 0 ]] || fail 'install accepts no arguments; select BOOKMARKSYNC_IMAGE in the protected deployment environment'
    lock_operations
    [[ -z "$("${compose[@]}" ps -a -q bookmarksync)" ]] || fail 'service already exists; use repair or upgrade'
    selected=${BOOKMARKSYNC_IMAGE:-bookmarksync:local}
    if [[ "$selected" = bookmarksync:local ]]; then
      BOOKMARKSYNC_IMAGE=bookmarksync:local "${compose[@]}" build bookmarksync
      selected=$(docker image inspect --format '{{.Id}}' bookmarksync:local)
      baseline_image "$selected"
    else
      image "$selected"
      verify_registry_image "$selected"
      selected=$verified_image
    fi
    # A retained nonempty volume needs a reviewed recovery/migration, never a
    # fresh install that silently migrates data without a snapshot.
    BOOKMARKSYNC_IMAGE="$selected" "${compose[@]}" run --rm --no-deps --no-build -T --entrypoint /bin/sh bookmarksync -c '[ -z "$(find /var/lib/bookmarksync -mindepth 1 -maxdepth 1 -print -quit)" ]' || fail 'data volume is not empty; retained data requires explicit recovery, repair or migration'
    if start_image "$selected" && persist_selected_image "$selected"; then
      printf 'Installed and ready. Persist BOOKMARKSYNC_IMAGE=%s in the protected deployment environment.\n' "$selected"
    else
      "${compose[@]}" stop bookmarksync || true
      fail 'installation failed readiness; service stopped and all volumes/configuration retained'
    fi
    ;;
  repair)
    [[ $# = 1 ]] || fail 'repair needs an independent backup directory'
    directory "$1"
    lock_operations
    container=$("${compose[@]}" ps -q bookmarksync)
    [[ -n "$container" ]] || fail 'service must exist for repair'
    current=$(docker inspect --format '{{.Image}}' "$container")
    baseline_image "$current"
    backup "$1"
    write_record "$current" "$last_backup"
    resume=false
    if BOOKMARKSYNC_IMAGE="$current" "${compose[@]}" up -d --no-deps --no-build --force-recreate bookmarksync && wait_ready && persist_selected_image "$current"; then
      printf 'Recreated exact current image and readiness passed. Recovery record: %s\n' "$record"
    else
      recover_snapshot "$current" "$last_backup" || fail "repair recovery requires operator inspection; snapshot/failed data retained: $record"
      fail "repair failed; original snapshot/image restored and ready: $record"
    fi
    ;;
  backup)
    [[ $# = 1 ]] || fail 'backup needs one directory'
    directory "$1"
    lock_operations
    backup "$1"
    ;;
  restore)
    [[ $# = 2 ]] || fail 'restore needs an archive and an absent target directory'
    archive=$1; target=$2
    [[ "$archive" = /* && "$archive" != *:* && "$archive" != *$'\n'* && -f "$archive" && ! -L "$archive" && "$(realpath -e -- "$archive")" = "$archive" ]] || fail 'archive must be a canonical regular absolute file'
    directory "$(dirname -- "$target")"
    [[ "$target" = /* && ! -e "$target" && ! -L "$target" && "$(basename -- "$target")" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || fail 'restore target must be a new directory with a simple name'
    lock_operations
    "${compose[@]}" run --rm --no-deps -T --volume "$archive:/backup/source.zip:ro" --volume "$(dirname -- "$target"):/restore" bookmarksync backup verify --file /backup/source.zip
    "${compose[@]}" run --rm --no-deps -T --volume "$archive:/backup/source.zip:ro" --volume "$(dirname -- "$target"):/restore" bookmarksync backup restore --file /backup/source.zip --target-dir "/restore/$(basename -- "$target")"
    printf 'Restored offline data to %s; inspect before selecting it for a new deployment.\n' "$target"
    ;;
  diagnostics)
    [[ $# = 0 ]] || fail 'diagnostics accepts no arguments'
    "${compose[@]}" ps
    "${compose[@]}" images
    "${compose[@]}" exec -T bookmarksync version
    ;;
  upgrade)
    [[ $# = 2 ]] || fail 'upgrade needs immutable image and independent backup directory'
    image "$1"
    directory "$2"
    lock_operations
    replacement=$1
    container=$("${compose[@]}" ps -q bookmarksync)
    [[ -n "$container" ]] || fail 'service must exist; use compose up for initial installation'
    previous=$(docker inspect --format '{{.Image}}' "$container")
    baseline_image "$previous"
    verify_registry_image "$replacement"
    # Pin backup execution to the currently installed image, even if the
    # operator's environment points at a proposed replacement.
    BOOKMARKSYNC_IMAGE="$previous" backup "$2"
    write_record "$previous" "$last_backup"
    resume=false
    if start_image "$verified_image" && persist_selected_image "$verified_image"; then
      printf 'Ready on %s. Persist BOOKMARKSYNC_IMAGE in the protected deployment environment. Snapshot record: %s\n' "$verified_image" "$record"
    else
      printf 'Replacement failed readiness; restoring pre-upgrade snapshot before restarting previous image.\n' >&2
      recover_snapshot "$previous" "$last_backup" || fail "automatic recovery failed; service requires operator inspection. Snapshot: $record; failed data retained in volume"
      fail "upgrade failed; previous image and verified snapshot restored and ready. Persist BOOKMARKSYNC_IMAGE=$previous; record: $record"
    fi
    ;;
  rollback)
    [[ $# = 2 ]] || fail 'rollback needs an exact snapshot record and independent backup directory'
    directory "$2"
    read_record "$1"
    lock_operations
    container=$("${compose[@]}" ps -q bookmarksync)
    [[ -n "$container" ]] || fail 'service must exist for managed rollback'
    current=$(docker inspect --format '{{.Image}}' "$container")
    baseline_image "$current"
    BOOKMARKSYNC_IMAGE="$current" backup "$2"
    write_record "$current" "$last_backup"
    resume=false
    if recover_snapshot "$snapshot_image" "$snapshot_archive"; then
      printf 'Restored selected snapshot and image; service is ready. Persist BOOKMARKSYNC_IMAGE=%s. Pre-rollback recovery record: %s\n' "$snapshot_image" "$record"
    else
      fail "rollback requires operator inspection; failed data and verified pre-rollback snapshot retained. Recovery record: $record"
    fi
    ;;
  uninstall)
    [[ $# = 0 ]] || fail 'uninstall accepts no arguments'
    lock_operations
    "${compose[@]}" --profile geoip down --remove-orphans
    printf 'Containers removed. Data/config volumes, GeoLite files, images and independent backups retained.\n'
    ;;
  *) usage; exit 2;;
esac
