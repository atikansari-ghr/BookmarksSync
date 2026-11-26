#!/bin/sh
set -eu
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
mkdir "$tmp/bin" "$tmp/geo"
printf 'secret-do-not-print\n' > "$tmp/config"
chmod 600 "$tmp/config"
cat > "$tmp/bin/geoipupdate" <<'EOF'
#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in -d) shift; dir=$1;; esac
  shift
done
case "$CASE" in
  retry) if [ ! -f "$RETRY_MARKER" ]; then touch "$RETRY_MARKER"; exit 1; fi; printf valid > "$dir/GeoLite2-Country.mmdb";;
  fail) echo secret-do-not-print >&2; exit 1;;
  missing) exit 0;;
  invalid) printf invalid > "$dir/GeoLite2-Country.mmdb";;
  *) printf valid > "$dir/GeoLite2-Country.mmdb";;
esac
EOF
cat > "$tmp/bin/bookmarksync" <<'EOF'
#!/bin/sh
[ "$(cat "$4")" = valid ]
EOF
chmod +x "$tmp/bin/geoipupdate" "$tmp/bin/bookmarksync"
export PATH="$tmp/bin:$PATH" BOOKMARKSYNC_GEOIP_CONFIG="$tmp/config" BOOKMARKSYNC_GEOIP_DIR="$tmp/geo"
export CASE=valid
sh "$base/geoip-update.sh" once
[ "$(cat "$tmp/geo/GeoLite2-Country.mmdb")" = valid ]
before=$(stat -c '%i:%Y' "$tmp/geo/GeoLite2-Country.mmdb")
sh "$base/geoip-update.sh" once
[ "$before" = "$(stat -c '%i:%Y' "$tmp/geo/GeoLite2-Country.mmdb")" ]
for CASE in invalid missing fail; do
  export CASE
  if sh "$base/geoip-update.sh" once > "$tmp/log" 2>&1; then exit 1; fi
  ! grep -q secret-do-not-print "$tmp/log"
  [ "$(cat "$tmp/geo/GeoLite2-Country.mmdb")" = valid ]
done
export CASE=valid
sh "$base/geoip-update.sh" once
mkdir "$tmp/geo/.update-lock"
if sh "$base/geoip-update.sh" once > "$tmp/log" 2>&1; then exit 1; fi
grep -q 'another update is running' "$tmp/log"
rmdir "$tmp/geo/.update-lock"
cat > "$tmp/bin/sleep" <<'EOF'
#!/bin/sh
if [ -f "$BOOKMARKSYNC_GEOIP_DIR/GeoLite2-Country.mmdb" ]; then kill -TERM "$PPID"; fi
EOF
chmod +x "$tmp/bin/sleep"
rm "$tmp/geo/GeoLite2-Country.mmdb"
export RETRY_MARKER="$tmp/retry"
CASE=retry sh "$base/geoip-update.sh" schedule
[ "$(cat "$tmp/geo/GeoLite2-Country.mmdb")" = valid ]
chmod 644 "$tmp/config"
if sh "$base/geoip-update.sh" once > "$tmp/log" 2>&1; then exit 1; fi
grep -q 'config must have mode' "$tmp/log"
printf 'PASS: staged publication, unchanged data, invalid/missing/failure retention, credential suppression, lock, scheduled retry and config permissions\n'
