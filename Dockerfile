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

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY web ./web

# The webapp is embedded into the binary, so it has to land before the build.
COPY --from=webapp /src/web/dist ./web/dist

# P0 has no cgo dependencies, so the result is a fully static binary and the
# runtime image can be distroless. That changes in P2: DuckDB is cgo, and this
# will need a glibc base and a per-platform build.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /rtail-server ./cmd/rtail-server

# ---- runtime ---------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=build /rtail-server /rtail-server

# A container's loopback is its own; binding 127.0.0.1 would make the published
# ports unreachable. Set as env rather than in CMD so that `docker run rtail
# --web-port 9000` appends a flag instead of dropping the bind address.
ENV RTAIL_WEB_HOST=0.0.0.0 \
    RTAIL_UDP_HOST=0.0.0.0 \
    RTAIL_WEB_PORT=8888 \
    RTAIL_UDP_PORT=9999

EXPOSE 8888/tcp
EXPOSE 9999/udp

# No HEALTHCHECK: a distroless image has no shell and no curl to run one with.
# Point your orchestrator at GET /healthz instead — it reports the version,
# the stream count and the UDP ingest counters.

ENTRYPOINT ["/rtail-server"]
