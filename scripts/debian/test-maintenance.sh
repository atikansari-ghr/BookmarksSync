#!/usr/bin/env bash
set -Eeuo pipefail
base=$(cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/root" "$tmp/bin"
export BOOKMARKSYNC_TESTING=1 BOOKMARKSYNC_INSTALL_ROOT="$tmp/root" MAINTENANCE_LOG="$tmp/log"
export PATH="$tmp/bin:$PATH"
cat > "$tmp/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$MAINTENANCE_LOG"
case "$1" in show) printf 'active\n';; esac
EOF
cat > "$tmp/bin/curl" <<'EOF'
#!/bin/sh
for arg do last=$arg; done
printf 'curl %s\n' "$last" >> "$MAINTENANCE_LOG"
echo 'SECRET response must not be printed'
EOF
chmod +x "$tmp/bin/systemctl" "$tmp/bin/curl"
if [[ "$(uname -s)" = MINGW* || "$(uname -s)" = MSYS* ]]; then
  printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/flock"
  chmod +x "$tmp/bin/flock"
fi
fixture() {
  mkdir -p "$tmp/root/etc/systemd/system" "$tmp/root/etc/bookmarksync" "$tmp/root/usr/local/bin" "$tmp/root/var/lib/bookmarksync" "$tmp/root/var/lib/bookmarksync-updates/snapshots" "$tmp/root/var/lib/bookmarksync-geoip" "$tmp/root/backups"
  cat > "$tmp/root/etc/systemd/system/bookmarksync.service" <<'EOF'
# Managed by BookmarksSync installer
[Service]
User=bookmarksync
Group=bookmarksync
EnvironmentFile=/etc/bookmarksync/bookmarksync.env
Environment=BOOKMARKSYNC_UPDATE_SERVICE_USER=bookmarksync
Environment=BOOKMARKSYNC_DEBIAN_UPDATE_QUEUE=/var/lib/bookmarksync-update-queue/inbox
ExecStart=/usr/local/bin/bookmarksync serve
NoNewPrivileges=true
ReadWritePaths=/var/lib/bookmarksync -/var/lib/bookmarksync-update-queue/inbox
EOF
  printf 'BOOKMARKSYNC_LISTEN=127.0.0.1:8080\nBOOKMARKSYNC_DATA_DIR=/var/lib/bookmarksync\nSECRET=do-not-print\n' > "$tmp/root/etc/bookmarksync/bookmarksync.env"
  # Managed browser-update queue artifacts written by the installer.
  mkdir -p "$tmp/root/usr/local/libexec" "$tmp/root/usr/local/sbin" "$tmp/root/var/lib/bookmarksync-update-queue/inbox" "$tmp/root/var/lib/bookmarksync-update-queue/processing" "$tmp/root/var/lib/bookmarksync-update-queue/rejected"
  printf 'BookmarksSync Debian update queue\n' > "$tmp/root/var/lib/bookmarksync-update-queue/OWNER"
  printf 'kept\n' > "$tmp/root/var/lib/bookmarksync-update-queue/rejected/request.kept.invalid"
  printf '#!/bin/bash\n# Managed by BookmarksSync installer\nexit 0\n' > "$tmp/root/usr/local/libexec/bookmarksync-update-queue-worker"
  printf '# Managed by BookmarksSync installer\n[Service]\nType=oneshot\nExecStart=/usr/local/libexec/bookmarksync-update-queue-worker\n' > "$tmp/root/etc/systemd/system/bookmarksync-update-queue.service"
  printf '# Managed by BookmarksSync installer\n[Path]\nDirectoryNotEmpty=/var/lib/bookmarksync-update-queue/inbox\nUnit=bookmarksync-update-queue.service\n' > "$tmp/root/etc/systemd/system/bookmarksync-update-queue.path"
  # A marker-bearing helper left by a sudo-era install is also removed.
  printf '#!/usr/bin/env bash\n# Managed by BookmarksSync installer\nexit 0\n' > "$tmp/root/usr/local/sbin/bookmarksync-update"
  printf data > "$tmp/root/var/lib/bookmarksync/user-data"
  printf snapshot > "$tmp/root/var/lib/bookmarksync-updates/snapshots/keep.zip"
  printf geo > "$tmp/root/var/lib/bookmarksync-geoip/GeoLite2-Country.mmdb"
  cat > "$tmp/root/usr/local/bin/bookmarksync" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MAINTENANCE_LOG"
if [[ "$1" = --version ]]; then echo 'bookmarksync 1.0.0'; exit; fi
[[ "${FAIL_BACKUP:-0}" != 1 ]] || exit 1
case "$2" in
 create) printf archive > "$6";;
 restore) mkdir "$6"; printf restored > "$6/data";;
esac
EOF
  chmod +x "$tmp/root/usr/local/bin/bookmarksync"
}
fixture
helper=$base/bookmarksync
if BOOKMARKSYNC_INSTALL_ROOT=/ bash "$helper" diagnostics; then exit 1; fi
if bash "$helper" uninstall --purge; then exit 1; fi
if BOOKMARKSYNC_DATA_PATH=/tmp/custom bash "$helper" uninstall; then exit 1; fi
[[ ! -e "$tmp/log" ]]
bash "$helper" diagnostics > "$tmp/diagnostics"
! grep -q 'SECRET\|do-not-print' "$tmp/diagnostics"
grep -q 'Readiness: ready' "$tmp/diagnostics"
grep -Fxq 'curl http://127.0.0.1:8080/readyz' "$tmp/log"
grep -q 'Update queue watcher: active' "$tmp/diagnostics"
# A3: diagnostics probe the configured listen host, never a hard-coded loopback.
sed -i 's/^BOOKMARKSYNC_LISTEN=.*/BOOKMARKSYNC_LISTEN=192.168.10.5:8080/' "$tmp/root/etc/bookmarksync/bookmarksync.env"
bash "$helper" diagnostics > "$tmp/diagnostics"
grep -Fxq 'curl http://192.168.10.5:8080/readyz' "$tmp/log"
sed -i 's/^BOOKMARKSYNC_LISTEN=.*/BOOKMARKSYNC_LISTEN=[::]:8080/' "$tmp/root/etc/bookmarksync/bookmarksync.env"
bash "$helper" diagnostics > "$tmp/diagnostics"
grep -Fxq 'curl http://[::1]:8080/readyz' "$tmp/log"
printf 'BOOKMARKSYNC_LISTEN=$(touch %s/pwned):1\nBOOKMARKSYNC_DATA_DIR=/var/lib/bookmarksync\n' "$tmp" > "$tmp/root/etc/bookmarksync/bookmarksync.env"
bash "$helper" diagnostics > "$tmp/diagnostics"
grep -q 'Readiness: unavailable (listen address not recognized)' "$tmp/diagnostics"
[[ ! -e "$tmp/pwned" ]]
fixture
# Units written by sudo-era installers remain recognized for maintenance.
sed -i -e 's#^ReadWritePaths=.*#ReadWritePaths=/var/lib/bookmarksync /var/lib/bookmarksync-updates#' -e '/^Environment=BOOKMARKSYNC_DEBIAN_UPDATE_QUEUE=/d' "$tmp/root/etc/systemd/system/bookmarksync.service"
bash "$helper" diagnostics > "$tmp/diagnostics"
grep -q 'Readiness: ready' "$tmp/diagnostics"
fixture
sed -i 's#^Environment=BOOKMARKSYNC_DEBIAN_UPDATE_QUEUE=.*#Environment=BOOKMARKSYNC_DEBIAN_UPDATE_QUEUE=/tmp/elsewhere#' "$tmp/root/etc/systemd/system/bookmarksync.service"
if bash "$helper" diagnostics >/dev/null 2>&1; then exit 1; fi
fixture
bash "$helper" backup --output "$tmp/root/backups/test.zip"
[[ -f "$tmp/root/backups/test.zip" ]]
grep -q 'stop bookmarksync.service' "$tmp/log"
grep -q 'start bookmarksync.service' "$tmp/log"
bash "$helper" restore --file "$tmp/root/backups/test.zip" --target-dir "$tmp/root/restored"
[[ -f "$tmp/root/restored/data" ]]
if bash "$helper" restore --file "$tmp/root/backups/test.zip" --target-dir "$tmp/root/var/lib/bookmarksync"; then exit 1; fi
printf '# unmanaged\n' > "$tmp/root/etc/systemd/system/bookmarksync.service"
if bash "$helper" uninstall; then exit 1; fi
[[ -e "$tmp/root/usr/local/bin/bookmarksync" ]]
fixture
printf 'User=root\n' >> "$tmp/root/etc/systemd/system/bookmarksync.service"
if bash "$helper" uninstall; then exit 1; fi
fixture
printf 'BOOKMARKSYNC_DATA_DIR=/tmp/custom\n' >> "$tmp/root/etc/bookmarksync/bookmarksync.env"
if bash "$helper" backup --output "$tmp/root/backups/mismatch.zip"; then exit 1; fi
fixture
mkdir -p "$tmp/root/usr/local/libexec" "$tmp/root/etc/systemd/system/bookmarksync.service.d" "$tmp/root/etc/systemd/system/bookmarksync-geoip-update.service.d"
cp "$base/bookmarksync-geoip-update.service" "$base/bookmarksync-geoip-update.timer" "$tmp/root/etc/systemd/system/"
cp "$base/bookmarksync-update-geoip" "$tmp/root/usr/local/libexec/"
printf '# Managed by BookmarksSync GeoLite setup.\n[Service]\nEnvironment=BOOKMARKSYNC_GEOIP_DB=/var/lib/bookmarksync-geoip/GeoLite2-Country.mmdb\n' > "$tmp/root/etc/systemd/system/bookmarksync.service.d/90-bookmarksync-geoip.conf"
printf '# Managed by BookmarksSync GeoLite setup.\n[Service]\nReadWritePaths=/var/lib/bookmarksync-geoip\n' > "$tmp/root/etc/systemd/system/bookmarksync-geoip-update.service.d/90-bookmarksync-geoip-paths.conf"
printf secret > "$tmp/root/etc/bookmarksync/GeoIP.conf"
if FAIL_BACKUP=1 bash "$helper" backup --output "$tmp/root/backups/fail.zip"; then exit 1; fi
tail -n 1 "$tmp/log" | grep -q 'start bookmarksync.service'
if [[ "$(uname -s)" != MINGW* && "$(uname -s)" != MSYS* ]]; then
  mv "$tmp/root/usr/local/bin" "$tmp/root/usr/local/bin-real"
  ln -s bin-real "$tmp/root/usr/local/bin"
  if bash "$helper" uninstall; then exit 1; fi
  rm "$tmp/root/usr/local/bin"
  mv "$tmp/root/usr/local/bin-real" "$tmp/root/usr/local/bin"
fi
cp "$tmp/root/etc/systemd/system/bookmarksync-update-queue.path" "$tmp/queue-path.saved"
printf '[Path]\nDirectoryNotEmpty=/tmp\n' > "$tmp/root/etc/systemd/system/bookmarksync-update-queue.path"
: > "$tmp/log"
if bash "$helper" uninstall; then exit 1; fi
! grep -q 'disable\|stop' "$tmp/log"
[[ -e "$tmp/root/usr/local/libexec/bookmarksync-update-queue-worker" && -e "$tmp/root/usr/local/bin/bookmarksync" ]]
cp "$tmp/queue-path.saved" "$tmp/root/etc/systemd/system/bookmarksync-update-queue.path"
: > "$tmp/log"
bash "$helper" uninstall
grep -q 'disable --now bookmarksync-geoip-update.timer' "$tmp/log"
queue_line=$(grep -n -m1 -Fx 'disable --now bookmarksync-update-queue.path' "$tmp/log" | cut -d: -f1)
app_line=$(grep -n -m1 -Fx 'disable --now bookmarksync.service' "$tmp/log" | cut -d: -f1)
[[ -n "$queue_line" && -n "$app_line" && "$queue_line" -lt "$app_line" ]]
grep -Fxq 'stop bookmarksync-update-queue.service' "$tmp/log"
[[ ! -e "$tmp/root/etc/systemd/system/bookmarksync-update-queue.path" && ! -e "$tmp/root/etc/systemd/system/bookmarksync-update-queue.service" && ! -e "$tmp/root/usr/local/libexec/bookmarksync-update-queue-worker" && ! -e "$tmp/root/usr/local/sbin/bookmarksync-update" ]]
[[ -f "$tmp/root/var/lib/bookmarksync-update-queue/OWNER" && -f "$tmp/root/var/lib/bookmarksync-update-queue/rejected/request.kept.invalid" ]]
[[ -f "$tmp/root/etc/bookmarksync/GeoIP.conf" && ! -e "$tmp/root/etc/systemd/system/bookmarksync-geoip-update.timer" && ! -e "$tmp/root/usr/local/libexec/bookmarksync-update-geoip" ]]
[[ ! -e "$tmp/root/usr/local/bin/bookmarksync" && ! -e "$tmp/root/etc/systemd/system/bookmarksync.service" ]]
[[ -f "$tmp/root/var/lib/bookmarksync/user-data" && -f "$tmp/root/var/lib/bookmarksync-updates/snapshots/keep.zip" && -f "$tmp/root/var/lib/bookmarksync-geoip/GeoLite2-Country.mmdb" && -f "$tmp/root/etc/bookmarksync/bookmarksync.env" && -f "$tmp/root/backups/test.zip" ]]
printf 'PASS: sanitized diagnostics, backup/restore, failure restart, safe uninstall and retained data/config/backups\n'
printf 'PASS: diagnostics probe the configured listen host and reject tampered listen values\n'
printf 'PASS: legacy and queue-era managed units are recognized; conflicting queue paths are refused\n'
printf 'PASS: uninstall stops the queue watcher first, removes managed queue artifacts and retains queue records\n'
