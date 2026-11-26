#!/usr/bin/env bash
# Real systemd watcher acceptance with private fixtures and a fake maintainer.
# Run only as root on an authorized disposable Linux systemd host. Installs one
# uniquely named runtime .service/.path pair under /run/systemd/system, a private
# /tmp tree, and removes both. It never touches production queue, config, units,
# Docker, or registries. BOOKMARKSYNC_TESTING=1 bypasses production ownership
# checks; this proves watcher orchestration only, not a real image upgrade.
set -Eeuo pipefail
umask 077
base=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
fail() { printf 'Host worker acceptance: %s\n' "$1" >&2; exit 1; }
step() { printf 'ok: %s\n' "$1"; }
[[ $(uname -s) = Linux && $(id -u) = 0 ]] || fail 'requires root on an authorized disposable Linux systemd host'
[[ $(ps -p 1 -o comm=) = systemd ]] || fail 'PID 1 must be systemd'
for tool in systemctl systemd-analyze journalctl setpriv flock stat realpath timeout find; do command -v "$tool" >/dev/null || fail "missing prerequisite: $tool"; done
unit_root=/run/systemd/system
[[ -d "$unit_root" && ! -L "$unit_root" && $(realpath -e -- "$unit_root") = "$unit_root" ]] || fail 'unsafe runtime unit directory'
[[ -f "$base/update-worker.sh" && ! -L "$base/update-worker.sh" ]] || fail 'worker source missing or unsafe'
tmp=$(mktemp -d /tmp/bookmarksync-host-worker.XXXXXXXX)
[[ "$tmp" =~ ^/tmp/bookmarksync-host-worker\.[A-Za-z0-9]{8}$ && $(realpath -e -- "$tmp") = "$tmp" && $(stat -c '%u' "$tmp") = 0 ]] || fail 'unsafe fixture root'
suffix=${tmp##*.}
suffix=${suffix,,}
name="bookmarksync-acceptance-$suffix"
[[ "$name" =~ ^bookmarksync-acceptance-[a-z0-9]{8}$ ]] || fail 'unsafe fixture unit name'
service_file=$unit_root/$name.service
path_file=$unit_root/$name.path
marker="# BookmarksSync isolated acceptance fixture $name"
installed=0
unit_loaded() { [[ $(systemctl show --property=LoadState --value "$1" 2>/dev/null || :) != not-found ]]; }
finish() {
  local status=$? cleanup_ok=1 file state unit
  trap - EXIT HUP INT TERM
  if ((installed)); then
    if ((status)); then timeout 10 journalctl --no-pager -u "$name.service" -n 40 >&2 || :; fi
    for unit in "$name.path" "$name.service"; do
      if unit_loaded "$unit"; then timeout 30 systemctl stop "$unit" >/dev/null 2>&1 || cleanup_ok=0; fi
      state=$(systemctl show --property=ActiveState --value "$unit" 2>/dev/null || :)
      case "$state" in active|activating|deactivating|reloading) cleanup_ok=0;; esac
    done
    if ((cleanup_ok)); then
      for file in "$path_file" "$service_file"; do
        [[ -e "$file" || -L "$file" ]] || continue
        if [[ -f "$file" && ! -L "$file" && $(realpath -e -- "$file") = "$file" ]] && grep -Fxq "$marker" "$file"; then
          rm -f -- "$file"
        else
          cleanup_ok=0
        fi
      done
      timeout 60 systemctl daemon-reload || cleanup_ok=0
      systemctl reset-failed "$name.path" "$name.service" >/dev/null 2>&1 || :
      for unit in "$name.path" "$name.service"; do
        if unit_loaded "$unit"; then cleanup_ok=0; fi
      done
      [[ ! -e "$path_file" && ! -L "$path_file" && ! -e "$service_file" && ! -L "$service_file" ]] || cleanup_ok=0
    fi
  fi
  if ((cleanup_ok)); then
    [[ "$tmp" =~ ^/tmp/bookmarksync-host-worker\.[A-Za-z0-9]{8}$ && ! -L "$tmp" && $(realpath -e -- "$tmp") = "$tmp" ]] || cleanup_ok=0
    if ((cleanup_ok)); then rm -rf -- "$tmp"; [[ ! -e "$tmp" ]] || cleanup_ok=0; fi
  fi
  if ((!cleanup_ok)); then
    printf 'Host worker acceptance: cleanup incomplete; retain fixture for inspection: %s (%s)\n' "$tmp" "$name" >&2
    status=1
  elif ((installed)); then
    printf 'ok: cleanup stopped and removed %s.path/.service, daemon-reloaded (LoadState=not-found), removed %s\n' "$name" "$tmp"
  fi
  if ((status == 0)); then printf 'PASS: real systemd.path -> current update-worker.sh -> fixed fake maintainer; UID65532 0600 markers; success, failure retention, unsafe content/name/symlink/hardlink rejection; test-owned cleanup\n'; fi
  exit "$status"
}
trap finish EXIT
trap 'exit 1' HUP INT TERM
[[ ! -e "$service_file" && ! -L "$service_file" && ! -e "$path_file" && ! -L "$path_file" ]] || fail 'fixture unit already exists'
! unit_loaded "$name.service" && ! unit_loaded "$name.path" || fail 'fixture unit name already loaded'
prefix=$tmp/root
deployment=$prefix/opt/deployment
backups=$prefix/srv/backups
queue=$prefix/var/lib/bookmarksync-docker-updates
inbox=$queue/inbox
publish_dir=$tmp/publish
install -d -m 755 "$prefix" "$prefix/var" "$prefix/var/lib" "$queue"
install -d -m 700 "$prefix/etc" "$prefix/usr" "$prefix/usr/local" "$prefix/usr/local/libexec" "$prefix/opt" "$deployment" "$deployment/scripts" "$deployment/scripts/docker" "$prefix/srv" "$backups" "$inbox" "$queue/processing" "$queue/rejected" "$publish_dir"
# UID65532 can traverse only to its publishing directory and the inbox.
chmod 711 "$tmp"
chown 65532:65532 "$inbox" "$publish_dir" "$backups"
install -m 755 "$base/update-worker.sh" "$prefix/usr/local/libexec/fixture-worker"
cmp -s "$base/update-worker.sh" "$prefix/usr/local/libexec/fixture-worker" || fail 'worker copy differs from current source'
printf 'BOOKMARKSYNC_DOCKER_DEPLOYMENT_DIR=%s\nBOOKMARKSYNC_DOCKER_BACKUP_DIR=%s\n' "$deployment" "$backups" > "$prefix/etc/bookmarksync-docker-updates.env"
printf 'BOOKMARKSYNC_IMAGE=fixture-only\n' > "$deployment/.env"
chmod 600 "$prefix/etc/bookmarksync-docker-updates.env" "$deployment/.env"
printf 'services: {}\n' > "$deployment/compose.yaml"
cp "$deployment/compose.yaml" "$deployment/compose.admin-updates.yaml"
printf '#!/bin/sh\nexit 97\n' > "$deployment/scripts/docker/restore-volume.sh"
printf 'link target must stay unchanged\n' > "$tmp/link-target"
success_tag=build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
failed_tag=build-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
content_tag=build-cccccccccccccccccccccccccccccccccccccccc
name_tag=build-DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD
symlink_tag=build-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
hardlink_tag=build-ffffffffffffffffffffffffffffffffffffffff
# Fixed fake maintainer: accepts only the exact worker contract, records it, and
# fails for the designated failure tag. No Docker, registry, or download.
cat > "$deployment/scripts/docker/maintain.sh" <<'SH'
#!/usr/bin/env bash
set -eu
[[ $# = 3 && $1 = upgrade && $2 =~ ^ghcr\.io/atikansari-ghr/bookmarksync:build-[a-f0-9]{40}$ && $3 = "$BOOKMARKSYNC_INSTALL_ROOT/srv/backups" && ${BOOKMARKSYNC_DOCKER_ADMIN_UPDATES:-0} = 1 ]] || exit 97
printf '%s %s %s %s\n' "$(id -u)" "$1" "$2" "$3" >> "$BOOKMARKSYNC_FIXTURE_ROOT/invocations"
[[ "$2" != ghcr.io/atikansari-ghr/bookmarksync:build-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb ]]
SH
chmod 755 "$deployment/scripts/docker/maintain.sh" "$deployment/scripts/docker/restore-volume.sh"
cat > "$tmp/$name.service" <<EOF
$marker
[Unit]
Description=Isolated BookmarksSync host worker acceptance
[Service]
Type=oneshot
ExecStart=/bin/bash $prefix/usr/local/libexec/fixture-worker
Environment=BOOKMARKSYNC_TESTING=1
Environment=BOOKMARKSYNC_INSTALL_ROOT=$prefix
Environment=BOOKMARKSYNC_FIXTURE_ROOT=$tmp
UMask=0077
PrivateTmp=true
BindPaths=$tmp:$tmp
ProtectHome=true
NoNewPrivileges=true
TimeoutStartSec=30s
EOF
cat > "$tmp/$name.path" <<EOF
$marker
[Unit]
Description=Isolated BookmarksSync approval inbox acceptance
[Path]
DirectoryNotEmpty=$inbox
Unit=$name.service
EOF
timeout 60 systemd-analyze verify "$tmp/$name.service" "$tmp/$name.path"
step "systemd-analyze verify $name.service $name.path"
installed=1
install -m 644 "$tmp/$name.service" "$service_file"
install -m 644 "$tmp/$name.path" "$path_file"
[[ $(realpath -e -- "$service_file") = "$service_file" && $(realpath -e -- "$path_file") = "$path_file" ]] || fail 'runtime unit escaped expected directory'
timeout 60 systemctl daemon-reload
timeout 30 systemctl start "$name.path"
systemctl is-active --quiet "$name.path" || fail 'fixture watcher did not start'
[[ $(systemctl show --property=FragmentPath --value "$name.path") = "$path_file" ]] || fail 'loaded watcher is not the runtime fixture unit'
step "runtime watcher $path_file active on $inbox"
[[ ! -e "$tmp/invocations" ]] || fail 'watcher invoked maintenance before an approval'
as_app() { setpriv --reuid=65532 --regid=65532 --clear-groups /bin/sh -c "$@"; }
# Regular marker created by UID/GID65532 with mode0600 in its private staging dir, then renamed into the inbox.
publish() {
  local tag=$1 contents=$2 staging=$publish_dir/request
  as_app 'umask 077; printf %s "$4" > "$1"; stat -c "%u:%g:%a:%h" "$1" > "$2"; mv -- "$1" "$3"' sh "$staging" "$publish_dir/owner" "$inbox/$tag" "$contents"
  [[ $(cat "$publish_dir/owner") = 65532:65532:600:1 ]] || fail "marker $tag was not privately published by UID/GID65532 mode0600"
}
publish_symlink() {
  as_app 'ln -s -- "$1" "$2"' sh "$tmp/link-target" "$inbox/$1"
}
publish_hardlink() {
  as_app 'umask 077; : > "$1"; ln -- "$1" "$2"' sh "$publish_dir/hardlink" "$inbox/$1"
}
wait_for() {
  local expected_calls=$1 expected_rejected=$2 attempt calls rejected
  for ((attempt=0; attempt<200; attempt++)); do
    calls=0
    [[ ! -f "$tmp/invocations" ]] || calls=$(wc -l < "$tmp/invocations")
    rejected=$(find "$queue/rejected" -mindepth 1 -maxdepth 1 | wc -l)
    if [[ "$calls" = "$expected_calls" && "$rejected" = "$expected_rejected" && -z $(find "$inbox" "$queue/processing" -mindepth 1 -maxdepth 1 -print -quit) && $(systemctl show --property=ActiveState --value "$name.service") = inactive ]]; then return 0; fi
    sleep 0.1
  done
  fail "watcher/worker did not reach calls=$expected_calls rejected=$expected_rejected within 20 seconds (calls=$calls rejected=$rejected)"
}
publish "$success_tag" ''
wait_for 1 0
grep -Fxq "0 upgrade ghcr.io/atikansari-ghr/bookmarksync:$success_tag $backups" "$tmp/invocations" || fail 'success request did not reach the exact fixed maintainer as root'
step 'success marker consumed; maintainer ran as UID0 with exact full-SHA image and backup path'
publish "$failed_tag" ''
wait_for 2 1
grep -Fxq "0 upgrade ghcr.io/atikansari-ghr/bookmarksync:$failed_tag $backups" "$tmp/invocations" || fail 'failed maintainer invocation was not recorded'
[[ -n $(find "$queue/rejected" -mindepth 1 -maxdepth 1 -type f -empty -print -quit) ]] || fail 'failed request was not retained'
step 'failed maintainer request retained in rejected/'
publish "$content_tag" 'nonempty content must never execute'
publish "$name_tag" ''
publish_symlink "$symlink_tag"
publish_hardlink "$hardlink_tag"
wait_for 2 5
[[ $(wc -l < "$tmp/invocations") = 2 ]] || fail 'unsafe request reached maintenance'
[[ $(find "$queue/rejected" -mindepth 1 -maxdepth 1 -type l | wc -l) = 1 ]] || fail 'symlink request was not retained as a link'
[[ $(find "$queue/rejected" -mindepth 1 -maxdepth 1 -type f -size +0 | wc -l) = 1 ]] || fail 'nonempty request was not retained'
[[ $(stat -c '%h' "$publish_dir/hardlink") = 2 ]] || fail 'hardlink request was not retained'
[[ $(cat "$tmp/link-target") = 'link target must stay unchanged' ]] || fail 'symlink target was modified'
step 'nonempty, invalid-name, symlink and hardlink markers rejected without maintenance'
[[ $(stat -c '%u:%a' "$deployment/.env") = 0:600 && $(stat -c '%u:%g:%a' "$inbox") = 65532:65532:700 ]] || fail 'fixture protections changed'
systemctl is-active --quiet "$name.path" || fail 'watcher stopped after processing a failed request'
[[ $(systemctl show --property=Result --value "$name.service") = success ]] || fail 'worker service did not exit successfully'
step 'watcher still active; worker service Result=success'
