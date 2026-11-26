#!/usr/bin/env bash
# Disposable Linux Docker acceptance: no live credentials or existing services.
set -Eeuo pipefail
umask 077
base=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
[[ $(id -u) = 0 ]] || { echo 'Run with sudo on a disposable Linux Docker test host (fixture ownership uses UID 65532).' >&2; exit 1; }
command -v python3 >/dev/null
docker compose version >/dev/null
tmp=$(mktemp -d /tmp/bookmarksync-runtime.XXXXXXXX)
project="bookmarksync-runtime-${tmp##*.}"
project=${project,,}
export COMPOSE_PROJECT_NAME="$project"
export BOOKMARKSYNC_IMAGE="$project:app"
export BOOKMARKSYNC_HTTP_BIND=127.0.0.1 BOOKMARKSYNC_HTTP_PORT=0
export BOOKMARKSYNC_GEOIP_DIR="$tmp/geoip"
export BOOKMARKSYNC_MAXMIND_CONFIG="$tmp/GeoIP.conf"
export BOOKMARKSYNC_VERSION=0.0.0-runtime-test
export BOOKMARKSYNC_COMMIT=isolated-runtime-test
export BOOKMARKSYNC_DATE=2026-11-24T00:00:00Z
export BOOKMARKSYNC_PUBLIC_URL=http://127.0.0.1:8080
export BOOKMARKSYNC_MANAGEMENT_CIDRS='' BOOKMARKSYNC_TRUSTED_PROXIES=''
# Override inherited identity providers; this test uses a disposable local admin.
export BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_ID='' BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET=''
export BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER='' BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_ID='' BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_SECRET=''
mkdir "$tmp/geoip"
chown 65532:65532 "$tmp/geoip"
chmod 755 "$tmp/geoip"
printf 'AccountID 12345\nLicenseKey disposable-no-network-test\nEditionIDs GeoLite2-Country\n' > "$tmp/GeoIP.conf"
chown 65532:65532 "$tmp/GeoIP.conf"
chmod 400 "$tmp/GeoIP.conf"
cat > "$tmp/compose-test.yaml" <<EOF
services:
  bookmarksync:
    env_file: []
  geoip-updater:
    image: $project:geoip
EOF
compose=(docker compose --project-directory "$base" --env-file /dev/null -p "$project" -f "$base/compose.yaml" -f "$tmp/compose-test.yaml")
cleanup() {
  status=$?
  if ((status)); then
    tail -n 60 "$tmp/build.log" >&2 || :
    "${compose[@]}" logs --tail 40 bookmarksync >&2 || :
  fi
  "${compose[@]}" --profile geoip down --volumes --remove-orphans >/dev/null 2>&1 || :
  docker image rm "$project:app" "$project:geoip" >/dev/null 2>&1 || :
  [[ "$tmp" = /tmp/bookmarksync-runtime.* ]] && rm -rf -- "$tmp"
}
trap cleanup EXIT
status() {
  python3 - "$1" "${2:-127.0.0.1}" <<'PY'
import sys, urllib.request, urllib.error
request = urllib.request.Request(sys.argv[1], headers={"X-Forwarded-For": sys.argv[2]})
try:
    print(urllib.request.urlopen(request, timeout=3).status)
except urllib.error.HTTPError as error:
    print(error.code)
except OSError:
    print(0)
PY
}
wait_status() {
  local want=$1 client=${2:-127.0.0.1} seconds=${3:-30}
  for ((attempt=0; attempt<seconds; attempt++)); do
    [[ $(status "http://$address/readyz" "$client") = "$want" ]] && return 0
    sleep 1
  done
  echo "Expected HTTP $want for disposable client $client" >&2
  return 1
}
echo 'Building isolated application and optional GeoLite updater images.'
"${compose[@]}" --profile geoip build > "$tmp/build.log" 2>&1
[[ $(docker image inspect --format '{{json .Config.Healthcheck.Test}}' "$project:app") = '["CMD","/usr/local/bin/bookmarksync","healthcheck"]' ]]
[[ $(docker image inspect --format '{{json .Config.Healthcheck.Test}}' "$project:geoip") = '["NONE"]' ]]
"${compose[@]}" --profile geoip run --rm --no-deps --entrypoint /bin/sh geoip-updater -c 'test "$(id -u)" = 65532; test "$(stat -c %u:%a /run/secrets/maxmind_config)" = 65532:400; test -s /usr/share/doc/geoipupdate/LICENSE-MIT; geoipupdate --version'
"${compose[@]}" up -d --no-build bookmarksync
address=$("${compose[@]}" port bookmarksync 8080)
wait_status 403
container=$("${compose[@]}" ps -q bookmarksync)
gateway=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}' "$container")
[[ "$gateway" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
export BOOKMARKSYNC_MANAGEMENT_CIDRS=127.0.0.1/32
export BOOKMARKSYNC_TRUSTED_PROXIES="$gateway/32"
"${compose[@]}" up -d --force-recreate --no-build bookmarksync
address=$("${compose[@]}" port bookmarksync 8080)
wait_status 200
printf '%s\n' 'disposable-runtime-password-123456' | "${compose[@]}" exec -T bookmarksync /usr/local/bin/bookmarksync user create --username runtime-test --password-stdin --admin --data-dir /var/lib/bookmarksync
container=$("${compose[@]}" ps -q bookmarksync)
[[ $(docker inspect --format '{{.Config.User}}|{{.HostConfig.ReadonlyRootfs}}|{{json .HostConfig.CapDrop}}' "$container") = '65532:65532|true|["ALL"]' ]]
[[ $(docker inspect --format '{{json .HostConfig.SecurityOpt}}' "$container") = *no-new-privileges* ]]
! docker inspect --format '{{json .Mounts}}' "$container" | grep -q 'docker.sock\|GeoIP.conf\|maxmind_config'
"${compose[@]}" exec -T bookmarksync /usr/local/bin/bookmarksync healthcheck
python3 - "http://$address" <<'PY'
import http.cookies, json, sys, urllib.request
base = sys.argv[1]
cookies = http.cookies.SimpleCookie()
def request(path, body=None, method=None, csrf=None):
    headers = {"X-Forwarded-For": "127.0.0.1", "Origin": "http://127.0.0.1:8080"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    if csrf:
        headers["X-CSRF-Token"] = csrf
    if cookies:
        # The fixture transport is plain loopback HTTP. Carry the server's
        # Secure session cookie explicitly in memory; never relax app cookies.
        headers["Cookie"] = "; ".join(m.OutputString(attrs=[]) for m in cookies.values())
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=5) as response:
        for value in response.headers.get_all('Set-Cookie', []):
            cookies.load(value)
        return json.load(response)
assert request('/api/v1/version')['commit'] == 'isolated-runtime-test'
state = request('/api/v1/auth/login', {"username": "runtime-test", "password": "disposable-runtime-password-123456"})
request('/api/v1/admin/network-access', {"mode": "allow", "countries": ["GB"]}, "PATCH", state['csrfToken'])
PY
[[ $(status "http://$address/readyz" 81.2.69.160) = 403 ]]
# Publish atomically while the same application container stays running.
cp "$base/internal/network/testdata/GeoLite2-Country-Test.mmdb" "$tmp/geoip/candidate"
chmod 644 "$tmp/geoip/candidate"
mv "$tmp/geoip/candidate" "$tmp/geoip/GeoLite2-Country.mmdb"
wait_status 200 81.2.69.160 40
[[ $("${compose[@]}" ps -q bookmarksync) = "$container" ]]
printf 'invalid replacement' > "$tmp/geoip/candidate"
chmod 644 "$tmp/geoip/candidate"
mv "$tmp/geoip/candidate" "$tmp/geoip/GeoLite2-Country.mmdb"
sleep 31
[[ $(status "http://$address/readyz" 81.2.69.160) = 200 ]]
[[ $(docker inspect --format '{{.State.Health.Status}}' "$container") = healthy ]]
"${compose[@]}" up -d --force-recreate --no-build bookmarksync
address=$("${compose[@]}" port bookmarksync 8080)
wait_status 200
if printf '%s\n' 'disposable-runtime-password-123456' | "${compose[@]}" exec -T bookmarksync /usr/local/bin/bookmarksync user create --username runtime-test --password-stdin --admin --data-dir /var/lib/bookmarksync > "$tmp/duplicate.out" 2>&1; then
  echo 'Administrator did not persist across recreation' >&2
  exit 1
fi
grep -qi 'exists\|unique\|already' "$tmp/duplicate.out"
# Enable only a private inbox mount: no host worker, external registry or real
# /var/lib/bookmarksync-docker-updates path is involved in this availability check.
mkdir "$tmp/inbox"
chown 65532:65532 "$tmp/inbox"
chmod 700 "$tmp/inbox"
cat > "$tmp/compose-queue-test.yaml" <<EOF
services:
  bookmarksync:
    environment:
      BOOKMARKSYNC_DOCKER_UPDATE_QUEUE: /run/bookmarksync/update-queue
    volumes:
      - $tmp/inbox:/run/bookmarksync/update-queue
EOF
compose+=(-f "$tmp/compose-queue-test.yaml")
"${compose[@]}" up -d --force-recreate --no-build bookmarksync
address=$("${compose[@]}" port bookmarksync 8080)
wait_status 200
"${compose[@]}" logs bookmarksync > "$tmp/queue.log"
grep -q 'host update approvals enabled' "$tmp/queue.log"
"${compose[@]}" exec -T bookmarksync /bin/sh -c 'parent=$(stat -c %u:%a /run/bookmarksync); inbox=$(stat -c %u:%a /run/bookmarksync/update-queue); test "$parent" = 0:755 || { echo "Unexpected queue parent ownership/mode: $parent" >&2; exit 1; }; test "$inbox" = 65532:700 || { echo "Unexpected inbox ownership/mode: $inbox" >&2; exit 1; }; test -w /run/bookmarksync/update-queue || { echo "Inbox is not writable" >&2; exit 1; }'
container=$("${compose[@]}" ps -q bookmarksync)
! docker inspect --format '{{json .Mounts}}' "$container" | grep -q docker.sock
echo 'PASS: actual image builds, sidecar permissions/version, fail-closed country policy, management access, health, identity, hardening, persistence, hot GeoIP reader reload/failure retention and protected update-queue availability.'
