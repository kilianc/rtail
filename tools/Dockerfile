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

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git make \
 && rm -rf /var/lib/apt/lists/*

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
    GOFLAGS=-buildvcs=false

WORKDIR /work
