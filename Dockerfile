# syntax=docker/dockerfile:1
#
# Runtime image for rtail-server — the default way to run rtail.
#
#   docker build -t rtail .
#   docker run --rm -p 8888:8888 -p 9999:9999/udp rtail
#
# This is the *server*. The rtail client is a pipe on your host and is not
# meant to run in here; install it with npm, or pipe into the container's UDP
# port from anywhere on the network.
#
# Not to be confused with tools/Dockerfile, which is the development toolchain
# and mounts your checkout.
#
# ** This image is no longer static. **
#
# P2 embeds DuckDB, which is cgo. That ends CGO_ENABLED=0, ends the distroless
# static base, and means the binary is linked against the glibc of the image it
# was built in — so the build and runtime stages have to agree on their base.
# The cost is roughly 45MB of DuckDB and a per-platform build; the alternative
# was no SQL, which was the whole feature.

ARG VERSION=2.0.0-dev

# ---- build the webapp ------------------------------------------------------
FROM node:22.12.0-bookworm-slim AS webapp

WORKDIR /src

# Copy manifests first so the dependency layer is cached independently of source.
# --ignore-scripts because nothing in the lockfile needs an install script to
# work: esbuild finds its platform binary without its postinstall.
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund

COPY app ./app
COPY tools ./tools
RUN npm run dist

# ---- build the server ------------------------------------------------------
FROM golang:1.26.3-bookworm AS build

ARG VERSION

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY web ./web

# The webapp is embedded into the binary, so it has to land before the build.
COPY --from=webapp /src/web/dist ./web/dist

# CGO_ENABLED=1 and no -extldflags '-static': go-duckdb ships a prebuilt static
# DuckDB per platform, but the Go side still links against the system libc.
RUN CGO_ENABLED=1 go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /rtail-server ./cmd/rtail-server

# ---- runtime ---------------------------------------------------------------
#
# Debian slim rather than distroless static, because the binary needs glibc and
# libstdc++. Matching the build stage's Debian release is not optional: a
# binary linked against bookworm's glibc will not start on an older one.
FROM debian:bookworm-slim AS runtime

# /data is created here, owned by the user the server runs as, so --data has
# somewhere to point that the server can actually write. A named volume
# mounted over an existing directory starts out with that directory's owner,
# which is what makes `-v rtail-data:/data` work with no chown of its own.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system --gid 10001 rtail \
 && useradd --system --uid 10001 --gid 10001 --create-home rtail \
 && mkdir /data \
 && chown 10001:10001 /data

COPY --from=build /rtail-server /usr/local/bin/rtail-server

# Apache-2.0 §4(d): whoever redistributes this image passes NOTICE on, so the
# image has to carry it.
COPY LICENSE NOTICE /usr/share/doc/rtail/

# A container's loopback is its own; binding 127.0.0.1 would make the published
# ports unreachable. Set as env rather than in CMD so that `docker run rtail
# --web-port 9000` appends a flag instead of dropping the bind address.
ENV RTAIL_WEB_HOST=0.0.0.0 \
    RTAIL_UDP_HOST=0.0.0.0 \
    RTAIL_WEB_PORT=8888 \
    RTAIL_UDP_PORT=9999

# ** This image is open unless you give it a token. **
#
# The server refuses to bind a non-loopback address without --auth-token,
# which is the right rule on a host and the wrong question inside a container:
# 0.0.0.0 here does not mean "public", it means "reachable through whatever
# you published with -p". So the image answers it once, and the server warns
# on every start instead.
#
# Set RTAIL_AUTH_TOKEN and this goes away:
#
#   docker run -e RTAIL_AUTH_TOKEN="$(openssl rand -hex 32)" -p 8888:8888 rtail
ENV RTAIL_INSECURE=true

# Memory-only by default, as on a host: nothing touches the disk until --data
# says where. To keep logs across restarts, point it at /data and put a volume
# there:
#
#   docker run -e RTAIL_DATA=/data -v rtail-data:/data -p 8888:8888 rtail
#
# Deliberately no VOLUME instruction. It would create an anonymous volume on
# every run, including every run that never writes a byte to it.
ENV RTAIL_DATA=""

EXPOSE 8888/tcp
EXPOSE 9999/udp

# Numeric, so Kubernetes' runAsNonRoot can tell it is not root without a
# passwd file to look the name up in.
USER 10001:10001

# No HEALTHCHECK: there is no curl in this image and adding one to run a health
# probe is a poor trade. Point your orchestrator at GET /healthz instead — it
# reports the version, the stream count and the UDP ingest counters.

ENTRYPOINT ["/usr/local/bin/rtail-server"]
