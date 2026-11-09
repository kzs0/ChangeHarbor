# syntax=docker/dockerfile:1
# ChangeHarbor (reviewd) container image. Multi-arch builds cross-compile on
# the build platform (CGO is needed for SQLite), so arm64 never compiles
# under emulation. The builder's Debian release matches the runtime's glibc.

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
ARG TARGETARCH
RUN set -eu; \
    native="$(dpkg --print-architecture)"; \
    if [ "$TARGETARCH" = "$native" ]; then \
      echo gcc > /tmp/cc; \
    else \
      case "$TARGETARCH" in \
        amd64) triple=x86_64-linux-gnu; pkg=gcc-x86-64-linux-gnu ;; \
        arm64) triple=aarch64-linux-gnu; pkg=gcc-aarch64-linux-gnu ;; \
        *) echo "unsupported TARGETARCH $TARGETARCH" >&2; exit 1 ;; \
      esac; \
      apt-get update; \
      apt-get install -y --no-install-recommends "$pkg" "libc6-dev-$TARGETARCH-cross"; \
      rm -rf /var/lib/apt/lists/*; \
      echo "$triple-gcc" > /tmp/cc; \
    fi
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH="$TARGETARCH" CC="$(cat /tmp/cc)" \
    go build -trimpath -o /out/reviewd ./cmd/reviewd

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 reviewd \
    && useradd --uid 10001 --gid 10001 --home-dir /data --no-create-home --shell /usr/sbin/nologin reviewd \
    && mkdir -p /data /mirrors \
    && chown reviewd:reviewd /data /mirrors
COPY --from=build /out/reviewd /usr/local/bin/reviewd
USER reviewd
VOLUME /data
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/reviewd"]
