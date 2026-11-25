#!/bin/sh
set -eu
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
mkdir "$tmp/bin" "$tmp/data"
mkdir "$tmp/data/backup-keys"
printf original-recovery-key > "$tmp/data/backup-keys/destination.key"
chmod 700 "$tmp/data/backup-keys"
chmod 600 "$tmp/data/backup-keys/destination.key"
printf snapshot > "$tmp/backup.zip"
printf migrated > "$tmp/data/bookmarksync.db"
printf hidden > "$tmp/data/.secret"
cat > "$tmp/bin/bookmarksync" <<'EOF'
#!/bin/sh
[ "${FAIL_RESTORE:-0}" != 1 ] || exit 1
if [ "$2" = restore ]; then
  mkdir "$6"
  printf original > "$6/bookmarksync.db"
  printf original-key > "$6/.secret"
  if [ "${INJECT_KEYS:-0}" = 1 ]; then mkdir "$6/backup-keys"; fi
fi
EOF
chmod +x "$tmp/bin/bookmarksync"
export PATH="$tmp/bin:$PATH" BOOKMARKSYNC_RECOVERY_TESTING=1 BOOKMARKSYNC_RECOVERY_TEST_DIR="$tmp/data"
if sh "$base/restore-volume.sh" "$tmp/backup.zip" ../bad; then exit 1; fi
if FAIL_RESTORE=1 sh "$base/restore-volume.sh" "$tmp/backup.zip" failure; then exit 1; fi
[ "$(cat "$tmp/data/bookmarksync.db")" = migrated ]
[ "$(cat "$tmp/data/backup-keys/destination.key")" = original-recovery-key ]
if INJECT_KEYS=1 sh "$base/restore-volume.sh" "$tmp/backup.zip" archive-keys; then exit 1; fi
[ "$(cat "$tmp/data/backup-keys/destination.key")" = original-recovery-key ]
sh "$base/restore-volume.sh" "$tmp/backup.zip" recovery
[ "$(cat "$tmp/data/bookmarksync.db")" = original ]
[ "$(cat "$tmp/data/.secret")" = original-key ]
[ "$(cat "$tmp/data/backup-keys/destination.key")" = original-recovery-key ]
[ "$(cat "$tmp/data/.bookmarksync-failed-recovery/bookmarksync.db")" = migrated ]
[ "$(cat "$tmp/data/.bookmarksync-failed-recovery/.secret")" = hidden ]
if sh "$base/restore-volume.sh" "$tmp/backup.zip" recovery; then exit 1; fi
sh "$base/restore-volume.sh" "$tmp/backup.zip" recovery2
[ "$(cat "$tmp/data/.bookmarksync-failed-recovery/bookmarksync.db")" = migrated ]
[ "$(cat "$tmp/data/.bookmarksync-failed-recovery2/bookmarksync.db")" = original ]
case "$(uname -s)" in MINGW*|MSYS*) ;; *)
  mv "$tmp/data/backup-keys" "$tmp/keys"
  ln -s "$tmp/keys" "$tmp/data/backup-keys"
  if sh "$base/restore-volume.sh" "$tmp/backup.zip" unsafe-keys; then exit 1; fi
  rm "$tmp/data/backup-keys"
  mv "$tmp/keys" "$tmp/data/backup-keys"
esac
printf 'PASS: snapshot restored before swap; failed database/hidden keys preserved; unsafe identifier, restore failure and existing targets rejected\n'
