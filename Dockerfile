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
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

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

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --create-home rtail

COPY --from=build /rtail-server /usr/local/bin/rtail-server

# A container's loopback is its own; binding 127.0.0.1 would make the published
# ports unreachable. Set as env rather than in CMD so that `docker run rtail
# --web-port 9000` appends a flag instead of dropping the bind address.
ENV RTAIL_WEB_HOST=0.0.0.0 \
    RTAIL_UDP_HOST=0.0.0.0 \
    RTAIL_WEB_PORT=8888 \
    RTAIL_UDP_PORT=9999

# Where --data points by default in the image. Mount a volume here to keep logs
# across container restarts; without it the server stays memory-only.
ENV RTAIL_DATA=""

EXPOSE 8888/tcp
EXPOSE 9999/udp

USER rtail

# No HEALTHCHECK: there is no curl in this image and adding one to run a health
# probe is a poor trade. Point your orchestrator at GET /healthz instead — it
# reports the version, the stream count and the UDP ingest counters.

ENTRYPOINT ["/usr/local/bin/rtail-server"]
