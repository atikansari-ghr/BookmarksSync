# syntax=docker/dockerfile:1
FROM golang:1.27.4-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG BOOKMARKSYNC_VERSION=dev
ARG BOOKMARKSYNC_COMMIT=unknown
ARG BOOKMARKSYNC_DATE=unknown
RUN mkdir -p /out \
    && CGO_ENABLED=0 go build -trimpath \
       -ldflags="-s -w -X github.com/atikansari-ghr/BookmarksSync/internal/version.Version=${BOOKMARKSYNC_VERSION} -X github.com/atikansari-ghr/BookmarksSync/internal/version.Commit=${BOOKMARKSYNC_COMMIT} -X github.com/atikansari-ghr/BookmarksSync/internal/version.Date=${BOOKMARKSYNC_DATE}" \
       -o /out/bookmarksync ./cmd/bookmarksync

FROM debian:bookworm-slim AS rclone

ARG TARGETARCH
RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/* \
    && case "${TARGETARCH}" in \
         amd64) arch=amd64; checksum=b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7 ;; \
         *) echo "unsupported Docker architecture: ${TARGETARCH}" >&2; exit 1 ;; \
       esac \
    && curl --fail --location --silent --show-error "https://downloads.rclone.org/v1.76.0/rclone-v1.76.0-linux-${arch}.zip" --output /tmp/rclone.zip \
    && echo "${checksum}  /tmp/rclone.zip" | sha256sum --check --status \
    && mkdir -p /out \
    && unzip -p /tmp/rclone.zip "rclone-v1.76.0-linux-${arch}/rclone" > /out/rclone \
    && chmod 0755 /out/rclone \
    && rm -f /tmp/rclone.zip

FROM debian:bookworm-slim AS geoip-binary
RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates curl \
    && curl --fail --location --silent --show-error https://github.com/maxmind/geoipupdate/releases/download/v8.0.0/geoipupdate_8.0.0_linux_amd64.tar.gz --output /tmp/geoipupdate.tar.gz \
    && echo '941eb4dd8c1eafb6ee1d56ccd5f4c62ffbdaca5f65a9f9cadc4008c8d805f2a2  /tmp/geoipupdate.tar.gz' | sha256sum --check --status \
    && mkdir /out \
    && tar -xzf /tmp/geoipupdate.tar.gz -C /out --strip-components=1

FROM debian:bookworm-slim AS app-runtime

RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 bookmarksync \
    && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /var/lib/bookmarksync --shell /usr/sbin/nologin bookmarksync \
    && install -d --owner=0 --group=0 --mode=0755 /run/bookmarksync \
    && install -d --owner=65532 --group=65532 --mode=0700 /var/lib/bookmarksync /run/bookmarksync/rclone

COPY --from=build --chown=65532:65532 /out/bookmarksync /usr/local/bin/bookmarksync
COPY --from=rclone --chown=65532:65532 /out/rclone /usr/local/bin/rclone
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/bookmarksync/
COPY third_party/rclone-COPYING /usr/share/doc/rclone/COPYING

USER 65532:65532
WORKDIR /var/lib/bookmarksync
ENV HOME=/var/lib/bookmarksync
VOLUME ["/var/lib/bookmarksync"]
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=6s --start-period=15s --retries=3 CMD ["/usr/local/bin/bookmarksync", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/bookmarksync"]
CMD ["serve", "--listen", "0.0.0.0:8080", "--data-dir", "/var/lib/bookmarksync"]

# Only this opt-in maintenance target contains the official MaxMind updater.
FROM app-runtime AS geoip-updater
HEALTHCHECK NONE
COPY --from=geoip-binary /out/geoipupdate /usr/local/bin/geoipupdate
COPY --from=geoip-binary /out/LICENSE-APACHE /out/LICENSE-MIT /usr/share/doc/geoipupdate/
COPY --chmod=0555 scripts/docker/geoip-update.sh /usr/local/bin/geoip-update
ENTRYPOINT ["/bin/sh", "/usr/local/bin/geoip-update"]
CMD ["schedule"]

FROM app-runtime AS runtime
