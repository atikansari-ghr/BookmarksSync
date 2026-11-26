#!/bin/sh
# Invoked only by the host operator, in a stopped application's one-off container.
set -eu
umask 077
[ "$#" = 2 ] || exit 2
archive=$1
nonce=$2
case "$nonce" in ''|*[!A-Za-z0-9-]*) echo 'invalid recovery identifier' >&2; exit 2;; esac
data=/var/lib/bookmarksync
if [ "${BOOKMARKSYNC_RECOVERY_TESTING:-0}" = 1 ]; then
  data=${BOOKMARKSYNC_RECOVERY_TEST_DIR:?}
  case "$data" in /tmp/*) ;; *) exit 2;; esac
fi
[ -d "$data" ] && [ ! -L "$data" ] && [ "$(realpath -e -- "$data")" = "$data" ] || exit 2
[ -f "$archive" ] && [ ! -L "$archive" ] || exit 2
stage=$data/.bookmarksync-restore-$nonce
failed=$data/.bookmarksync-failed-$nonce
lock=$data/.bookmarksync-recovery-lock
[ ! -e "$stage" ] && [ ! -L "$stage" ] && [ ! -e "$failed" ] && [ ! -L "$failed" ] || exit 2
# Destination recovery keys deliberately live outside full backup archives. Keep
# their fixed live directory across database rollback, and never follow a link.
keys=$data/backup-keys
[ ! -L "$keys" ] && { [ ! -e "$keys" ] || [ -d "$keys" ]; } || exit 2
if [ -d "$keys" ]; then
  [ "$(realpath -e -- "$keys")" = "$keys" ] || exit 2
  [ "$(stat -c '%u:%a' "$keys")" = "$(id -u):700" ] || exit 2
  # Keys themselves must be ordinary private files, never links or directories.
  for key in "$keys"/* "$keys"/.[!.]* "$keys"/..?*; do
    [ -e "$key" ] || [ -L "$key" ] || continue
    [ -f "$key" ] && [ ! -L "$key" ] || exit 2
    [ "$(stat -c '%u:%a' "$key")" = "$(id -u):600" ] || exit 2
  done
fi
mkdir "$lock" || exit 1
trap 'rmdir "$lock"' EXIT
# Verify and restore completely before moving a single live file. On any error
# retain all recovery artifacts for inspection, with the service still stopped.
bookmarksync backup verify --file "$archive"
bookmarksync backup restore --file "$archive" --target-dir "$stage"
[ -d "$stage" ] && [ ! -L "$stage" ] || exit 1
[ ! -e "$stage/backup-keys" ] && [ ! -L "$stage/backup-keys" ] || exit 1
mkdir "$failed"
for entry in "$data"/* "$data"/.[!.]* "$data"/..?*; do
  [ -e "$entry" ] || [ -L "$entry" ] || continue
  case "$entry" in "$keys"|"$stage"|"$failed"|"$lock"|"$data"/.bookmarksync-failed-*|"$data"/.bookmarksync-restore-*) continue;; esac
  mv -- "$entry" "$failed/"
done
for entry in "$stage"/* "$stage"/.[!.]* "$stage"/..?*; do
  [ -e "$entry" ] || [ -L "$entry" ] || continue
  [ ! -e "$data/$(basename -- "$entry")" ] && [ ! -L "$data/$(basename -- "$entry")" ] || exit 1
  mv -- "$entry" "$data/"
done
rmdir "$stage"
printf 'Restored verified snapshot; failed data retained in %s\n' "$failed"
