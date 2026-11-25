#!/usr/bin/env bash
# Tests root-owned deployment state without touching the real checkout or Docker.
set -Eeuo pipefail
[[ $(id -u) = 0 ]] || { echo 'SKIP: image environment ownership test requires root'; exit 0; }
base=$(cd -- "$(dirname -- "$0")" && pwd -P)
tmp=$(mktemp -d /tmp/bookmarksync-image-env.XXXXXXXX)
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/deployment/scripts/docker" "$tmp/bin" "$tmp/backups"
cp "$base/maintain.sh" "$tmp/deployment/scripts/docker/maintain.sh"
printf 'services: {}\n' > "$tmp/deployment/compose.yaml"
cp "$tmp/deployment/compose.yaml" "$tmp/deployment/compose.admin-updates.yaml"
printf '#!/bin/sh\nexit 0\n' > "$tmp/deployment/scripts/docker/restore-volume.sh"
old="sha256:$(printf '%064d' 0)"
new="ghcr.io/atikansari-ghr/bookmarksync@sha256:$(printf '%064d' 1)"
cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$IMAGE_TEST_LOG"
case "$*" in
  *'ps -q bookmarksync'|*'ps --status running -q bookmarksync') echo container;;
  *'inspect --format {{.Image}}'*) cat "$IMAGE_TEST_STATE";;
  *'image inspect --format {{json .Config.Healthcheck.Test}}'*) echo '["CMD","/usr/local/bin/bookmarksync","healthcheck"]';;
  *'image inspect --format {{index .RepoDigests 0}}'*) printf 'ghcr.io/atikansari-ghr/bookmarksync@sha256:%064d\n' 1;;
  *'backup create'*)
    mount=''; output=''
    while (($#)); do case "$1" in --volume) mount=${2%:/backup}; shift;; --output) output=${2#/backup/}; shift;; esac; shift; done
    printf archive > "$mount/$output";;
  *'up -d --no-deps --no-build bookmarksync'*) printf '%s\n' "$BOOKMARKSYNC_IMAGE" > "$IMAGE_TEST_STATE";;
  *'exec -T bookmarksync /usr/local/bin/bookmarksync healthcheck'*)
    if [[ "${IMAGE_TEST_FAIL:-0}" = 1 && "$(cat "$IMAGE_TEST_STATE")" = ghcr.io/* ]]; then exit 1; fi;;
esac
EOF
printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/gh"
printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/sleep"
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" IMAGE_TEST_LOG="$tmp/log" IMAGE_TEST_STATE="$tmp/state" BOOKMARKSYNC_DOCKER_ADMIN_UPDATES=1
printf '%s\n' "$old" > "$tmp/state"
printf 'BOOKMARKSYNC_IMAGE=%s\nPRIVATE_SENTINEL=preserved\n' "$old" > "$tmp/deployment/.env"
chmod 600 "$tmp/deployment/.env"
helper=$tmp/deployment/scripts/docker/maintain.sh
bash "$helper" upgrade ghcr.io/atikansari-ghr/bookmarksync:build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$tmp/backups"
grep -Fxq "BOOKMARKSYNC_IMAGE=$new" "$tmp/deployment/.env"
grep -Fxq 'PRIVATE_SENTINEL=preserved' "$tmp/deployment/.env"
[[ $(stat -c '%u:%a' "$tmp/deployment/.env") = 0:600 ]]
# Failed replacement restores the matching old image, snapshot and persisted
# selection, so the next ordinary Compose restart cannot revive a wrong binary.
printf '%s\n' "$old" > "$tmp/state"
printf 'BOOKMARKSYNC_IMAGE=%s\nPRIVATE_SENTINEL=preserved\n' "$old" > "$tmp/deployment/.env"
if IMAGE_TEST_FAIL=1 bash "$helper" upgrade ghcr.io/atikansari-ghr/bookmarksync:build-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb "$tmp/backups"; then exit 1; fi
grep -Fxq "BOOKMARKSYNC_IMAGE=$old" "$tmp/deployment/.env"
grep -Fxq 'PRIVATE_SENTINEL=preserved' "$tmp/deployment/.env"
grep -q '/run/bookmarksync-restore-volume.sh /backup/source.zip' "$tmp/log"
chmod 777 "$tmp/deployment/.tools"
if bash "$helper" upgrade ghcr.io/atikansari-ghr/bookmarksync:build-cccccccccccccccccccccccccccccccccccccccc "$tmp/backups"; then
  echo 'unsafe browser update state accepted' >&2; exit 1
fi
grep -Fxq "BOOKMARKSYNC_IMAGE=$old" "$tmp/deployment/.env"
echo 'PASS: browser-approved image selection persists atomically with private settings and restores the original selection on failed readiness'
