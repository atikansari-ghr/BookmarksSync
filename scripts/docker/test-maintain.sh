#!/usr/bin/env bash
set -Eeuo pipefail
base=$(cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
mkdir "$tmp/bin" "$tmp/backups" "$tmp/restore"
cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s image=%s\n' "$*" "${BOOKMARKSYNC_IMAGE:-}" >> "$DOCKER_LOG"
case "$*" in
  *'ps -a -q bookmarksync') [[ "${FAKE_NO_SERVICE:-0}" = 1 ]] || echo abc;;
  *'ps --status running -q bookmarksync') echo abc;;
  *'ps -q bookmarksync') echo abc;;
  *'inspect --format {{.Image}}'*) cat "$DOCKER_STATE";;
  *'image inspect --format {{.Id}}'*) cat "$DOCKER_STATE";;
  *'image inspect --format {{json .Config.Healthcheck.Test}}'*)
    if [[ "${FAIL_BASELINE:-0}" = 1 ]]; then echo null; else printf '["CMD","/usr/local/bin/bookmarksync","healthcheck"]\n'; fi
    ;;
  *'image inspect --format {{index .RepoDigests 0}}'*) printf 'ghcr.io/atikansari-ghr/bookmarksync@sha256:%064d\n' 1;;
  *'backup create'*)
    [[ "${FAIL_BACKUP:-0}" != 1 ]] || exit 1
    mount=''; output=''
    while (($#)); do
      case "$1" in --volume) mount=${2%:/backup}; shift;; --output) output=${2#/backup/}; shift;; esac
      shift
    done
    printf archive > "$mount/$output"
    ;;
  *'up -d --no-deps --no-build bookmarksync'*)
    selected=${BOOKMARKSYNC_IMAGE:?}
    if [[ "$selected" = ghcr.io/* ]]; then selected="sha256:$(printf '%064d' 1)"; fi
    printf '%s\n' "$selected" > "$DOCKER_STATE"
    ;;
  *'up -d --no-deps --no-build --force-recreate bookmarksync'*)
    if [[ -n "${FAIL_REPAIR_ONCE:-}" && ! -e "$FAIL_REPAIR_ONCE" ]]; then touch "$FAIL_REPAIR_ONCE"; exit 1; fi
    ;;
  *'find /var/lib/bookmarksync -mindepth 1'*) [[ "${FAIL_NONEMPTY_DATA:-0}" != 1 ]];;
  *'exec -T bookmarksync /usr/local/bin/bookmarksync healthcheck'*)
    if [[ "${FAIL_NEW_HEALTH:-0}" = 1 && "$(cat "$DOCKER_STATE")" = "sha256:$(printf '%064d' 1)" ]]; then exit 1; fi
    ;;
esac
EOF
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
[ "${FAIL_PROVENANCE:-0}" != 1 ]
EOF
printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/sleep"
if [[ "$(uname -s)" = MINGW* || "$(uname -s)" = MSYS* ]]; then
  printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/flock"
fi
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" DOCKER_LOG="$tmp/log" DOCKER_STATE="$tmp/state" GH_LOG="$tmp/ghlog"
old="sha256:$(printf '%064d' 0)"
printf '%s\n' "$old" > "$DOCKER_STATE"
helper=$base/maintain.sh
for args in 'restore /tmp/a /' 'backup /' 'upgrade latest /tmp' 'rollback latest'; do
  if bash "$helper" $args > /dev/null 2>&1; then exit 1; fi
done
[[ ! -s "$tmp/log" ]]
if [[ "$(uname -s)" != MINGW* && "$(uname -s)" != MSYS* ]]; then
  ln -s "$tmp/backups" "$tmp/link"
  if bash "$helper" backup "$tmp/link"; then exit 1; fi
fi
bash "$helper" backup "$tmp/backups"
grep -q 'backup create --data-dir /var/lib/bookmarksync --output /backup/' "$tmp/log"
grep -q 'stop bookmarksync' "$tmp/log"
grep -q 'start bookmarksync' "$tmp/log"
bash "$helper" diagnostics
! grep -q 'logs\|config\|env\|--volumes' "$tmp/log"
if bash "$helper" install; then exit 1; fi
FAKE_NO_SERVICE=1 bash "$helper" install
if FAKE_NO_SERVICE=1 FAIL_NONEMPTY_DATA=1 bash "$helper" install; then exit 1; fi
bash "$helper" repair "$tmp/backups"
if FAIL_REPAIR_ONCE="$tmp/repair-failure" bash "$helper" repair "$tmp/backups"; then exit 1; fi
bash "$helper" uninstall
grep -q 'down --remove-orphans' "$tmp/log"
! grep -q 'down.*--volumes\|volume rm\|system prune' "$tmp/log"
image="ghcr.io/atikansari-ghr/bookmarksync:build-$(printf '%040d' 1)"
bash "$helper" upgrade "$image" "$tmp/backups"
grep -q 'up -d --no-deps --no-build bookmarksync' "$tmp/log"
grep -q 'attestation verify oci://ghcr.io/atikansari-ghr/bookmarksync@sha256:.* --repo atikansari-ghr/BookmarksSync --signer-workflow atikansari-ghr/BookmarksSync/.github/workflows/ci.yml --source-ref refs/heads/main' "$tmp/ghlog"
grep -q -- "--source-digest ${image##*:build-}" "$tmp/ghlog"
record=$(find "$tmp/backups" -maxdepth 1 -name '*.snapshot' -print | head -n 1)
bash "$helper" rollback "$record" "$tmp/backups"
[[ "$(cat "$DOCKER_STATE")" = "$old" ]]
grep -q '/run/bookmarksync-restore-volume.sh /backup/source.zip' "$tmp/log"
: > "$tmp/log"
{ head -n 3 "$record"; printf 'sha256=%064d\n' 0; } > "$tmp/backups/tampered.snapshot"
if bash "$helper" rollback "$tmp/backups/tampered.snapshot" "$tmp/backups"; then exit 1; fi
[[ ! -s "$tmp/log" ]]
if FAIL_BASELINE=1 bash "$helper" upgrade "$image" "$tmp/backups"; then exit 1; fi
! grep -q 'pull\|stop bookmarksync\|up -d' "$tmp/log"
: > "$tmp/log"
if FAIL_PROVENANCE=1 bash "$helper" upgrade "$image" "$tmp/backups"; then exit 1; fi
! grep -q 'stop bookmarksync\|backup create\|up -d' "$tmp/log"
: > "$tmp/log"
if FAIL_NEW_HEALTH=1 bash "$helper" upgrade "$image" "$tmp/backups"; then exit 1; fi
[[ "$(cat "$DOCKER_STATE")" = "$old" ]]
grep -q '/run/bookmarksync-restore-volume.sh /backup/source.zip' "$tmp/log"
: > "$tmp/log"
if FAIL_BACKUP=1 bash "$helper" backup "$tmp/backups"; then exit 1; fi
grep -q 'start bookmarksync' "$tmp/log"
printf 'PASS: safe arguments/targets, stopped verified backup, secret-safe diagnostics, attested digest upgrade, readiness failure rollback, exact snapshot rollback, failure restart and retained volumes\n'
