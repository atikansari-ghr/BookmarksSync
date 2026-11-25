#!/usr/bin/env bash
set -Eeuo pipefail
base=$(cd -- "$(dirname -- "$0")" && pwd -P)
tmp=$(mktemp -d /tmp/bookmarksync-queue-test.XXXXXXXX)
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/root/opt/deployment/scripts/docker" "$tmp/root/etc" "$tmp/root/backups" "$tmp/bin"
deployment=$tmp/root/opt/deployment
cp "$base/update-worker.sh" "$deployment/scripts/docker/update-worker.sh"
cp "$base/../../compose.admin-updates.yaml" "$deployment/compose.admin-updates.yaml"
printf 'services: {}\n' > "$deployment/compose.yaml"
printf 'BOOKMARKSYNC_IMAGE=fixture\n' > "$deployment/.env"
chmod 600 "$deployment/.env"
printf '#!/bin/sh\nexit 0\n' > "$deployment/scripts/docker/restore-volume.sh"
cat > "$deployment/scripts/docker/maintain.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$QUEUE_LOG"
[[ "$1" = upgrade && "$2" =~ ^ghcr\.io/atikansari-ghr/bookmarksync:build-[0-9a-f]{40}$ && "$3" = "$QUEUE_BACKUPS" ]]
EOF
cat > "$tmp/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$QUEUE_SYSTEMCTL_LOG"
EOF
chmod +x "$tmp/bin/systemctl"
export PATH="$tmp/bin:$PATH" BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_INSTALL_ROOT="$tmp/root" QUEUE_LOG="$tmp/requests" QUEUE_SYSTEMCTL_LOG="$tmp/systemctl" QUEUE_BACKUPS="$tmp/root/backups"
bash "$base/setup-admin-updates.sh" --deployment "$deployment" --backups "$QUEUE_BACKUPS"
queue=$tmp/root/var/lib/bookmarksync-docker-updates
test -d "$queue/inbox"
grep -q 'enable --now bookmarksync-docker-updates.path' "$QUEUE_SYSTEMCTL_LOG"
[[ ! -e "$QUEUE_LOG" ]]
tag=build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
: > "$queue/inbox/$tag"
chmod 600 "$queue/inbox/$tag"
printf 'ignored command content\n' > "$queue/inbox/build-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
mkdir "$queue/inbox/invalid-directory"
ln -s "$deployment/.env" "$queue/inbox/build-cccccccccccccccccccccccccccccccccccccccc"
bash "$base/update-worker.sh"
[[ $(wc -l < "$QUEUE_LOG") = 1 ]]
grep -Fxq "upgrade ghcr.io/atikansari-ghr/bookmarksync:$tag $QUEUE_BACKUPS" "$QUEUE_LOG"
[[ -z $(find "$queue/inbox" -mindepth 1 -maxdepth 1 -print -quit) ]]
[[ $(find "$queue/rejected" -mindepth 1 -maxdepth 1 | wc -l) = 3 ]]
grep -Fxq 'BOOKMARKSYNC_IMAGE=fixture' "$deployment/.env"
mkdir "$deployment/.tools"
chmod 777 "$deployment/.tools"
events=$(wc -l < "$QUEUE_SYSTEMCTL_LOG")
if bash "$base/setup-admin-updates.sh" --deployment "$deployment" --backups "$QUEUE_BACKUPS"; then
  echo 'unsafe ignored maintenance directory accepted' >&2; exit 1
fi
[[ $(wc -l < "$QUEUE_SYSTEMCTL_LOG") = "$events" ]]
chmod 700 "$deployment/.tools"
printf 'INVALID=unsafe\n' >> "$tmp/root/etc/bookmarksync-docker-updates.env"
if bash "$base/update-worker.sh"; then echo 'unrecognized config accepted' >&2; exit 1; fi
if BOOKMARKSYNC_INSTALL_ROOT=/ bash "$base/setup-admin-updates.sh" --deployment "$deployment" --backups "$QUEUE_BACKUPS"; then exit 1; fi
echo 'PASS: isolated setup, SHA-only approval, private claiming, content/directory/symlink rejection, config validation and no setup-triggered upgrade'
