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
# Everything else (esbuild, sass, typescript) is a devDependency pinned in
# package-lock.json, so this image only needs to pin the runtime.
FROM node:22.12.0-bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

ENV NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false

WORKDIR /work
