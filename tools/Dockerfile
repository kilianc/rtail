# tools/Dockerfile — pinned toolchain for rtail.
#
# Node.js is deliberately absent from the host; every JS tool this project
# needs lives in this image. Build it once:
#
#   docker build -t rtail-tools tools/
#
# Then run any tool against the checkout:
#
#   docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/work" -w /work rtail-tools \
#     sass app/scss/main.scss app/css/main.css
#
FROM node:22.12.0-bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/*

# Dart Sass replaces the abandoned node-sass@3 (libsass) the project used in
# 2015. esbuild produces the one browser bundle the app still needs (highlight.js
# ships no UMD build since v11). Pin both: this Dockerfile is the toolchain
# lockfile.
RUN npm install -g --ignore-scripts \
      sass@1.83.0 \
      esbuild@0.24.2

ENV NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false

WORKDIR /work
