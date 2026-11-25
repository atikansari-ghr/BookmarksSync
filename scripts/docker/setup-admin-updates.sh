#!/usr/bin/env bash
# Opt-in host service. Run only from a trusted, root-owned deployment checkout.
set -Eeuo pipefail
umask 077
base=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
fail() { printf 'BookmarksSync Docker update setup: %s\n' "$1" >&2; exit 1; }
deployment=''; backups=''
while (($#)); do
  case "$1" in
    --deployment) [[ $# -ge 2 && -z "$deployment" ]] || fail 'invalid deployment option'; deployment=$2; shift 2;;
    --backups) [[ $# -ge 2 && -z "$backups" ]] || fail 'invalid backups option'; backups=$2; shift 2;;
    *) fail 'use --deployment ABS_DIRECTORY --backups ABS_DIRECTORY';;
  esac
done
[[ -n "$deployment" && -n "$backups" ]] || fail 'deployment and backups required'
prefix=''
if [[ "${BOOKMARKSYNC_TESTING:-0}" = 1 ]]; then
  prefix=${BOOKMARKSYNC_INSTALL_ROOT:-}
  [[ "$prefix" = /tmp/* && -d "$prefix" && ! -L "$prefix" && "$(realpath -e -- "$prefix")" = "$prefix" ]] || fail 'isolated test root required'
else
  [[ "$(id -u)" = 0 ]] || fail 'root required'
  case "$deployment" in /root|/root/*|/home|/home/*|/tmp|/tmp/*|/var/tmp|/var/tmp/*) fail 'deployment must remain visible to the protected host service (use /opt or /srv)';; esac
fi
worker=$base/scripts/docker/update-worker.sh
[[ -f "$worker" && ! -L "$worker" ]] || fail 'worker source missing'
work=$(mktemp -d /tmp/bookmarksync-queue-setup.XXXXXXXX)
trap 'rm -rf -- "$work"' EXIT
printf 'BOOKMARKSYNC_DOCKER_DEPLOYMENT_DIR=%s\nBOOKMARKSYNC_DOCKER_BACKUP_DIR=%s\n' "$deployment" "$backups" > "$work/config"
chmod 600 "$work/config"
# Validate the production ownership chain using a candidate config already under
# /etc; test mode instead confines every candidate to its disposable tree.
config=$prefix/etc/bookmarksync-docker-updates.env
mkdir -p -- "$prefix/etc"
[[ ! -L "$prefix/etc" && ! -L "$config" && ! -e "$config.next" && ! -L "$config.next" ]] || fail 'unsafe config path'
if [[ -e "$config" ]]; then
  [[ -f "$config" ]] && grep -q '^BOOKMARKSYNC_DOCKER_DEPLOYMENT_DIR=' "$config" || fail 'unmanaged existing config'
fi
install -m 600 "$work/config" "$config.next"
trap 'rm -f -- "$config.next"; rm -rf -- "$work"' EXIT
bash "$worker" --check-config "$config.next"
command -v python3 >/dev/null || fail 'Python3 required for atomic deployment image persistence'
[[ -f "$deployment/.env" ]] || fail 'prepare a protected deployment .env before enabling browser updates'
queue=$prefix/var/lib/bookmarksync-docker-updates
runtime=$prefix/usr/local/libexec/bookmarksync-docker-update-worker
units=$prefix/etc/systemd/system
[[ ! -L "$queue" && ! -L "$runtime" && ! -L "$units" ]] || fail 'unsafe managed target'
if [[ -e "$queue" ]]; then
  [[ -f "$queue/OWNER" && ! -L "$queue/OWNER" ]] && grep -Fxq 'BookmarksSync Docker update queue' "$queue/OWNER" || fail 'refusing unmanaged queue directory'
fi
for unit in bookmarksync-docker-updates.service bookmarksync-docker-updates.path; do
  if [[ -e "$units/$unit" || -L "$units/$unit" ]]; then
    [[ -f "$units/$unit" && ! -L "$units/$unit" ]] && grep -Fxq '# Managed by BookmarksSync Docker update setup' "$units/$unit" || fail 'refusing unmanaged unit'
  fi
done
if [[ -e "$runtime" ]]; then grep -Fxq '# Managed by BookmarksSync Docker update setup' "$runtime" || fail 'refusing unmanaged worker'; fi
for target in "$queue" "$queue/inbox" "$queue/processing" "$queue/rejected"; do
  [[ ! -L "$target" && ( ! -e "$target" || -d "$target" ) ]] || fail 'unsafe queue directory'
done
install -d -m 755 "$queue" "$prefix/usr/local/libexec" "$units"
install -d -m 700 "$queue/inbox" "$queue/processing" "$queue/rejected"
if [[ -z "$prefix" ]]; then
  chown root:root "$queue" "$queue/processing" "$queue/rejected"
  chown 65532:65532 "$queue/inbox"
fi
printf 'BookmarksSync Docker update queue\n' > "$queue/OWNER"
chmod 644 "$queue/OWNER"
install -m 755 "$worker" "$runtime"
mv -f -- "$config.next" "$config"
cat > "$work/bookmarksync-docker-updates.service" <<EOF
# Managed by BookmarksSync Docker update setup
[Unit]
Description=Apply an explicitly approved BookmarksSync Docker build
After=docker.service network-online.target
Requires=docker.service
[Service]
Type=oneshot
ExecStart=/usr/local/libexec/bookmarksync-docker-update-worker
UMask=0077
PrivateTmp=true
ProtectHome=true
NoNewPrivileges=true
TimeoutStartSec=30min
EOF
cat > "$work/bookmarksync-docker-updates.path" <<EOF
# Managed by BookmarksSync Docker update setup
[Unit]
Description=Watch explicit BookmarksSync Docker update approvals
[Path]
DirectoryNotEmpty=/var/lib/bookmarksync-docker-updates/inbox
Unit=bookmarksync-docker-updates.service
[Install]
WantedBy=multi-user.target
EOF
install -m 644 "$work/bookmarksync-docker-updates.service" "$units/bookmarksync-docker-updates.service"
install -m 644 "$work/bookmarksync-docker-updates.path" "$units/bookmarksync-docker-updates.path"
systemctl daemon-reload
systemctl enable --now bookmarksync-docker-updates.path
printf 'Host worker installed. Recreate only this app with compose.yaml and compose.admin-updates.yaml; no update is requested by setup.\n'
