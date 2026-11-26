# Isolated Docker test on the LXC at 192.168.10.226

This guide runs a disposable BookmarksSync Compose deployment on the Debian 13
amd64 LXC at `192.168.10.226`. You reach it from a workstation through an SSH
tunnel. It runs beside any existing BookmarksSync service and leaves that
service alone. It does not stop the service, upgrade packages, reuse the
default image tag, or reuse the default Compose project and volumes.

| Item | Value |
| --- | --- |
| Source clone | `/opt/bookmarksync-lxc-test`, branch `build/bookmarksync` |
| Compose project | `bookmarksync-lxc-test` (always passed with `-p`) |
| Image tag | `bookmarksync:lxc-test` (never `bookmarksync:local`) |
| Host port | `127.0.0.1:18081` on the LXC only |
| Public URL | `http://localhost:18081` |
| Backups | `/srv/bookmarksync-lxc-test-backups` (outside the clone) |
| Test admin | `lxc-admin` |

## Why use a tunnel instead of `http://192.168.10.226`

The server accepts plain `http://` public URLs only for loopback hosts
(`internal/config/config.go`). Website session cookies use `__Host-` names with
`Secure`, and unsafe requests must match the configured origin exactly.
`http://192.168.10.226:18081` is therefore rejected as intended. **Do not
weaken or disable this HTTPS validation.**

The tunnel connects to the LXC by its IP over SSH. The browser still sees the
loopback origin `http://localhost:18081`, which matches the public URL. Use a
browser that treats `http://localhost` as a secure context, such as Chrome, Edge,
Brave or Firefox.

To browse by IP directly, use a real TLS reverse proxy with a certificate the
workstation trusts. Set the public URL to that exact `https://192.168.10.226:<port>`
origin and set `BOOKMARKSYNC_TRUSTED_PROXIES` to the proxy's peer address. See
[network access](network-access.md) and [authentication](authentication.md).
That setup is optional and outside this guide: it has not been tested here, and
this guide gives no proxy install commands.

## Prerequisites (operator)

- The Proxmox operator has already enabled Docker in the LXC (nesting and any
  keyctl settings the host needs). Docker Engine and the Compose v2 plugin
  already work there.
- You have a root shell on the LXC (`ssh root@192.168.10.226`). Every LXC command
  below runs as root.
- The LXC has outbound network access. The image build downloads pinned Go
  modules and rclone.
- The branch `build/bookmarksync` has been pushed to GitHub. The clone contains
  only pushed commits, not uncommitted work from a workstation.

Check the tools first. This changes nothing:

```bash
docker version --format '{{.Server.Version}}'
docker compose version
command -v git curl python3 flock timeout realpath
ss -H -ltn 'sport = :18081'
```

The port check should print nothing. If it shows a listener, choose another free
port and replace every `18081` below; do not stop the existing listener. Port
`18080` was already occupied during validation, which is why this guide uses
`18081`.

Install missing `git` or `curl` packages as needed. Do **not** run
`apt-get upgrade` or `full-upgrade` on this LXC:

```bash
apt-get update
apt-get install --no-install-recommends git curl
```

Check that your root shell does not export variables that would override the
`.env` values below. This command should print nothing:

```bash
python3 -c 'import os; print("\n".join(k for k in os.environ if k.startswith(("COMPOSE_", "BOOKMARKSYNC_"))))'
```

## 1. Clone the source separately

```bash
git clone --branch build/bookmarksync https://github.com/atikansari-ghr/BookmarksSync.git /opt/bookmarksync-lxc-test
cd /opt/bookmarksync-lxc-test
git log -1 --format='%H %s'
```

If the repository is private, clone it with credentials you already use for
GitHub. Never put a token in the URL or in `.env`.

## 2. Write the private deployment `.env`

This deployment uses a single `.env` file beside `compose.yaml`, owned by root
with mode `0600`. Git ignores it (`.gitignore`), and the Docker build context
excludes it.

```bash
cd /opt/bookmarksync-lxc-test
install -o root -g root -m 0600 /dev/null .env
cat > .env <<'EOF'
COMPOSE_PROJECT_NAME=bookmarksync-lxc-test
BOOKMARKSYNC_IMAGE=bookmarksync:lxc-test
BOOKMARKSYNC_HTTP_BIND=127.0.0.1
BOOKMARKSYNC_HTTP_PORT=18081
BOOKMARKSYNC_PUBLIC_URL=http://localhost:18081
BOOKMARKSYNC_MANAGEMENT_CIDRS=127.0.0.1/32,::1/128
EOF
stat -c '%U:%G %a %n' .env      # expect: root:root 600 .env
install -d -o root -g root -m 0755 geoip
```

`geoip/` stays empty, and GeoLite and the update worker stay disabled. There is
no first published release and no MaxMind license for this test. Without a
country database, the network gate denies every client outside
`BOOKMARKSYNC_MANAGEMENT_CIDRS`, including loopback (see
[network access](network-access.md)). The container healthcheck calls `/readyz`
on `127.0.0.1` from inside the container, so `127.0.0.1/32` is required.

## 3. Build and create the container without starting it, then find the Docker gateway

Requests from the LXC loopback to the published port, including tunnel traffic,
go through Docker's port publishing. Inside the container they appear to come
from the project network's gateway address, not from `127.0.0.1`. Add **only
that exact gateway `/32`**. Do not add the whole LAN, `172.16.0.0/12`, or every
Docker network.

`docker compose create` builds the image and creates the project network and
the container. It does not start the container. The build arguments are
optional and only fill in version metadata:

```bash
cd /opt/bookmarksync-lxc-test
commit="$(git rev-parse HEAD)"
BOOKMARKSYNC_COMMIT="$commit" \
BOOKMARKSYNC_VERSION="0.0.0-${commit:0:12}" \
BOOKMARKSYNC_DATE="$(git show -s --format=%cI "$commit")" \
  docker compose -p bookmarksync-lxc-test create --build bookmarksync

docker image inspect --format '{{.Id}}' bookmarksync:lxc-test
gw="$(docker network inspect --format '{{(index .IPAM.Config 0).Gateway}}' bookmarksync-lxc-test_default)"
printf 'gateway=%s\n' "$gw"
[[ "$gw" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]] || { echo 'gateway is not a single IPv4 address; stop and inspect the network' >&2; false; }
```

If the check fails, for example because the gateway is empty or the first IPAM
entry is IPv6, stop. Run
`docker network inspect bookmarksync-lxc-test_default` and choose the IPv4
gateway yourself. Do not guess a broad range.

Add the gateway to the `.env` file. GNU `sed -i` keeps the file's owner and
mode. Check them again afterwards:

```bash
sed -i "s|^BOOKMARKSYNC_MANAGEMENT_CIDRS=.*|BOOKMARKSYNC_MANAGEMENT_CIDRS=127.0.0.1/32,::1/128,${gw}/32|" .env
grep '^BOOKMARKSYNC_MANAGEMENT_CIDRS=' .env
stat -c '%U:%G %a %n' .env      # expect: root:root 600 .env
```

## 4. Start the deployment

The container created in step 3 still has the old CIDR list. Compose should
notice the changed environment and recreate the container on `up`. The
inspection below confirms the running container has the new value, so you do
not have to trust that behavior:

```bash
cd /opt/bookmarksync-lxc-test
docker compose -p bookmarksync-lxc-test up -d --no-build bookmarksync
docker compose -p bookmarksync-lxc-test ps
docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' \
  "$(docker compose -p bookmarksync-lxc-test ps -q bookmarksync)" | grep -E '^BOOKMARKSYNC_(MANAGEMENT_CIDRS|PUBLIC_URL)='
```

If `BOOKMARKSYNC_MANAGEMENT_CIDRS` lacks the gateway, run
`docker compose -p bookmarksync-lxc-test up -d --no-build --force-recreate bookmarksync`
and check again. Wait until `ps` shows the service as `healthy`.

## 5. Check health on the LXC

```bash
curl --fail --silent --show-error http://127.0.0.1:18081/healthz; echo
curl --fail --silent --show-error http://127.0.0.1:18081/readyz; echo
curl --fail --silent --show-error http://127.0.0.1:18081/api/v1/version; echo
curl --silent --output /dev/null --write-out '%{http_code}\n' http://192.168.10.226:18081/healthz || true
```

The first three commands should succeed. The last one should fail to connect,
because the port is bound only to `127.0.0.1`. A `403` from the first three
usually means the gateway CIDR is missing. Check with
`docker compose -p bookmarksync-lxc-test logs --tail 50 bookmarksync`.

## 6. Create the test administrator

The password is read with a hidden prompt and piped to standard input. It never
appears on the command line, in an environment variable or in `.env`. It must
be at least 12 bytes long.

```bash
cd /opt/bookmarksync-lxc-test
read -r -s -p "lxc-admin password: " ADMIN_PASSWORD; printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | docker compose -p bookmarksync-lxc-test exec -T bookmarksync \
  /usr/local/bin/bookmarksync user create --username lxc-admin --password-stdin --admin --data-dir /var/lib/bookmarksync
unset ADMIN_PASSWORD
```

The Compose service is named `bookmarksync`, not `app`.

## 7. Open the tunnel and test from the workstation

On the workstation, keep this command running in its own terminal:

```bash
ssh -N -L 18081:127.0.0.1:18081 root@192.168.10.226
```

From a second workstation terminal:

```bash
curl --fail http://localhost:18081/healthz
curl --fail http://localhost:18081/readyz
curl --fail http://localhost:18081/api/v1/version
```

Then use the browser at `http://localhost:18081`:

1. Sign in as `lxc-admin`.
2. Create a folder and a bookmark inside it.
3. Reload the page and confirm that both are still there.
4. Optional: restart the service and confirm they persist. On the LXC, run
   `docker compose -p bookmarksync-lxc-test restart bookmarksync`.

## 8. Floccus check (manual)

Use a **disposable browser profile**. Syncing replaces bookmarks, so never test
with your everyday profile.

1. In floccus, add a **Nextcloud Bookmarks** account with server URL
   `http://localhost:18081`, the same origin as the public URL.
2. Use the login action. Sign in to BookmarksSync in the tab it opens, confirm
   the account, and select **Approve connection** (see
   [Login Flow v2](login-flow-v2.md)).
3. When prompted for the local folder, choose the browser's absolute root, then
   run a sync.
4. Check that the website bookmarks appear in the browser. Add a bookmark in the
   browser, sync, and reload the website to confirm it arrived.

Accept floccus's plain-HTTP or permission prompts only for this loopback tunnel.
If the wizard does not finish, use the CLI credential and profile-import path in
[floccus compatibility](floccus-compatibility.md#setting-up-a-floccus-profile-initial-isolated-gate)
with `"url": "http://localhost:18081/"`. The CLI prints the secret once, so treat
your terminal scrollback as sensitive:

```bash
docker compose -p bookmarksync-lxc-test exec -T bookmarksync /usr/local/bin/bookmarksync \
  credential create --username lxc-admin --label lxc-test --mode edit --data-dir /var/lib/bookmarksync
```

Revoke test connections afterwards under **Account security → Floccus
connections**.

## 9. Back up and verify

Use the repository's maintenance helper. It briefly stops the app so the
database and files are consistent. It then runs `backup create` and
`backup verify` with the installed image and restarts the app only if it was
running. The helper fixes the project directory to this clone, so Compose loads
this `.env` automatically. The project name is also passed explicitly.

```bash
install -d -o 65532 -g 65532 -m 0700 /srv/bookmarksync-lxc-test-backups
cd /opt/bookmarksync-lxc-test
COMPOSE_PROJECT_NAME=bookmarksync-lxc-test bash scripts/docker/maintain.sh backup /srv/bookmarksync-lxc-test-backups
ls -l /srv/bookmarksync-lxc-test-backups
docker compose -p bookmarksync-lxc-test ps
```

Archives contain sensitive data, including `instance.key`. Keep that directory
private.

## 10. Update the test checkout

Make a backup first (step 9), then:

```bash
cd /opt/bookmarksync-lxc-test
git pull --ff-only
commit="$(git rev-parse HEAD)"
BOOKMARKSYNC_COMMIT="$commit" \
BOOKMARKSYNC_VERSION="0.0.0-${commit:0:12}" \
BOOKMARKSYNC_DATE="$(git show -s --format=%cI "$commit")" \
  docker compose -p bookmarksync-lxc-test build bookmarksync
docker compose -p bookmarksync-lxc-test up -d --no-build bookmarksync
curl --fail http://127.0.0.1:18081/readyz; echo
```

The named volumes `bookmarksync-lxc-test_bookmarksync-data` and
`bookmarksync-lxc-test_bookmarksync-rclone-config` are kept. The gateway CIDR
normally stays the same while the project network exists. After `down`, the
network may come back with a different gateway, so repeat the check from step 3
before `up`.

## 11. Stop and clean up while keeping data

```bash
cd /opt/bookmarksync-lxc-test
docker compose -p bookmarksync-lxc-test down
docker volume ls --filter name=bookmarksync-lxc-test
```

Never add `-v`. `down` removes only this project's container and network. The
volumes, the `bookmarksync:lxc-test` image, the clone and the backups are kept.
Remove any of them later only by explicit, individually named commands, after
you have decided you no longer need the test data. Do not use recursive
deletion or prune.

## Scope of this guide

- On 2026-11-25, an isolated equivalent on this LXC passed image build,
  create → exact gateway CIDR → start, health/version checks, admin creation,
  password login and persistence of the admin/session after container recreation.
  Only fixture resources were removed. The minimal host lacked `curl`, so the
  verification used the existing toolchain image's curl in the host network
  namespace; this guide lists curl as a host prerequisite. The workstation
  browser/tunnel and manual Floccus steps still require operator testing. These
  are local acceptance results, not GitHub-hosted CI results.
- Compose behavior can differ between plugin versions. In particular, step 4
  checks whether the container was recreated instead of assuming it.
- Not covered: GeoLite, the host update worker, published-image upgrades,
  reverse-proxy or TLS access by IP, and OIDC.
