# tools/Dockerfile — pinned toolchain for rtail.
#
# Node.js is deliberately absent from the host; this image is the project's
# toolchain. Build it once:
#
#   docker build -t rtail-tools tools/
#
# Then run any tool against the checkout:
#
#   docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/work" -w /work rtail-tools \
#     npm run build
#
# v2 needs both toolchains: Node builds the webapp, Go builds the server that
# embeds it. Keeping them in one image means `make dev` is a single container
# and the UDP demo producers can reach the server over loopback instead of
# through Docker's NAT.
#
# Everything else (esbuild, sass, typescript) is a devDependency pinned in
# package-lock.json, so this image only pins the two runtimes.
FROM golang:1.26.3-bookworm AS go

FROM node:22.12.0-bookworm-slim

# g++ and libstdc++ are for cgo: P2 embeds DuckDB, which is a C++ library.
# duckdb itself is here so the Parquet interop tests can run — they assert that
# a completely unrelated implementation reads our files, which is the whole
# anti-lock-in claim and is worth checking rather than asserting.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates git make g++ libstdc++-12-dev unzip curl \
 && rm -rf /var/lib/apt/lists/*

# The architecture comes from dpkg rather than from BuildKit's TARGETARCH.
# Declaring `ARG TARGETARCH=amd64` looks equivalent and is not: the default
# wins, so an arm64 build silently downloads an x86 binary and fails at the
# first run with a dynamic-loader error. Debian's names (amd64, arm64) happen
# to match DuckDB's release assets exactly.
RUN arch="$(dpkg --print-architecture)" \
 && curl -fsSL -o /tmp/duckdb.zip \
      "https://github.com/duckdb/duckdb/releases/download/v1.4.1/duckdb_cli-linux-${arch}.zip" \
 && unzip -q /tmp/duckdb.zip -d /usr/local/bin \
 && rm /tmp/duckdb.zip \
 && duckdb -c "SELECT 'duckdb ' || version()"

COPY --from=go /usr/local/go /usr/local/go

# GOCACHE and GOMODCACHE live in the mounted workspace so they survive between
# runs and stay owned by the invoking user, rather than needing a root-owned
# volume. -buildvcs=false because the checkout's .git is owned by someone else
# as far as the container is concerned.
ENV PATH=/usr/local/go/bin:$PATH \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false \
    GOCACHE=/work/.gocache \
    GOMODCACHE=/work/.gomodcache \
    GOFLAGS=-buildvcs=false \
    CGO_ENABLED=1

WORKDIR /work
