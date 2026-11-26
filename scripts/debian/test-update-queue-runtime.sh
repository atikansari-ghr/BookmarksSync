#!/usr/bin/env bash
# Isolated systemd handoff; never installs or upgrades a real application.
set -Eeuo pipefail
[[ $(id -u) == 0 ]] || { printf 'BLOCKED: root required\n' >&2; exit 1; }
[[ -d /run/systemd/system ]] || { printf 'BLOCKED: systemd required\n' >&2; exit 1; }
source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
fixture=$(mktemp -d /run/bookmarksync-debian-queue-test.XXXXXXXX)
chmod 0755 "$fixture"
suffix=${fixture##*.}
base=bookmarksync-debian-acceptance-$suffix
watcher=$base.path
worker=$base.service
sender=$base-sender.service
marker='# BookmarksSync isolated Debian queue acceptance'
cleanup() {
  local status=$? unit path
  trap - EXIT
  for unit in "$watcher" "$sender" "$worker"; do
    path=/run/systemd/system/$unit
    if [[ -f "$path" ]] && grep -Fxq "$marker" "$path"; then
      timeout 30 systemctl stop "$unit" || status=1
      rm -f -- "$path" || status=1
    fi
  done
  systemctl daemon-reload || status=1
  for unit in "$watcher" "$sender" "$worker"; do
    [[ $(systemctl show -p LoadState --value "$unit") == not-found ]] || status=1
  done
  if [[ "$fixture" == /run/bookmarksync-debian-queue-test.* && -d "$fixture" && ! -L "$fixture" ]]; then
    rm -rf -- "$fixture" || status=1
  else status=1; fi
  [[ ! -e "$fixture" ]] || status=1
  if [[ "$status" == 0 ]]; then printf 'PASS: fixture units unloaded and private runtime directory removed\n'; fi
  exit "$status"
}
trap cleanup EXIT
service_user=nobody
uid=$(id -u "$service_user")
[[ "$uid" != 0 ]]
queue=$fixture/queue
install -d -m 0755 "$queue"
install -d -m 0700 "$queue/processing" "$queue/rejected"
install -d -m 0700 -o "$uid" -g "$uid" "$queue/inbox" "$fixture/proof"
printf 'BookmarksSync Debian update queue\n' > "$queue/OWNER"
managed_marker='# Managed by BookmarksSync installer'
# Load only the current worker generator, without executing the installer CLI.
source <(sed -n '/^write_update_queue_worker()/,/^write_update_queue()/p' "$source_dir/bookmarksync" | sed '$d')
tag=build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
cat > "$fixture/installer" <<EOF
#!/bin/bash
$managed_marker
set -eu
[[ \$# == 3 && \$1 == upgrade && \$2 == --tag && \$3 == $tag ]]
[[ \$(id -u) == 0 ]]
printf 'root upgrade %s\n' "\$3" >> '$fixture/calls'
EOF
chmod 0755 "$fixture/installer"
write_update_queue_worker "$fixture/worker" 0 "$uid" "$queue" "$fixture/installer"
chmod 0755 "$fixture/worker"
cat > "/run/systemd/system/$worker" <<EOF
$marker
[Service]
Type=oneshot
ExecStart=$fixture/worker
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
TimeoutStartSec=30
EOF
cat > "/run/systemd/system/$watcher" <<EOF
$marker
[Path]
DirectoryNotEmpty=$queue/inbox
Unit=$worker
EOF
cat > "/run/systemd/system/$sender" <<EOF
$marker
[Service]
Type=oneshot
User=$uid
Group=$uid
NoNewPrivileges=true
UMask=0077
ExecStart=/bin/sh -c 'id -u > "$fixture/proof/sender"; grep "^NoNewPrivs:" /proc/self/status >> "$fixture/proof/sender"; touch "$queue/inbox/$tag"'
TimeoutStartSec=30
EOF
systemd-analyze verify "/run/systemd/system/$worker" "/run/systemd/system/$watcher" "/run/systemd/system/$sender"
systemctl daemon-reload
timeout 30 systemctl start "$watcher"
timeout 30 systemctl start "$sender"
for ((attempt=0; attempt<100; attempt++)); do
  if [[ -f "$fixture/calls" && ! -e "$queue/inbox/$tag" && -z $(find "$queue/processing" -mindepth 1 -maxdepth 1 -print -quit) ]]; then break; fi
  sleep .1
done
grep -Fxq "$uid" "$fixture/proof/sender"
grep -Eq '^NoNewPrivs:[[:space:]]+1$' "$fixture/proof/sender"
[[ $(cat "$fixture/calls") == "root upgrade $tag" ]]
[[ ! -e "$queue/inbox/$tag" && -z $(find "$queue/rejected" -mindepth 1 -maxdepth 1 -print -quit) ]]
systemctl is-active --quiet "$watcher"
[[ $(systemctl show -p Result --value "$worker") == success ]]
printf 'PASS: unprivileged NoNewPrivileges sender -> real systemd.path -> production-checking Debian worker -> fixed root fixture installer\n'
