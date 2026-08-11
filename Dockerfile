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

# ---- build the webapp ------------------------------------------------------
FROM node:22.18.0-bookworm-slim AS build

WORKDIR /src

# Copy manifests first so the dependency layer is cached independently of source.
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .
RUN npm run dist

# ---- runtime ---------------------------------------------------------------
FROM node:22.18.0-bookworm-slim AS runtime

# update-notifier phones npm on startup and prints a banner nobody can act on
# from inside a container image.
ENV NODE_ENV=production \
    NO_UPDATE_NOTIFIER=1 \
    NPM_CONFIG_UPDATE_NOTIFIER=false

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund \
 && npm cache clean --force

COPY --from=build /src/cli ./cli
COPY --from=build /src/dist ./dist

# A container's loopback is its own; binding 127.0.0.1 would make the published
# ports unreachable. Set as env rather than in CMD so that `docker run rtail
# --web-port 9000` appends a flag instead of dropping the bind address.
ENV RTAIL_WEB_HOST=0.0.0.0 \
    RTAIL_UDP_HOST=0.0.0.0 \
    RTAIL_WEB_PORT=8888 \
    RTAIL_UDP_PORT=9999

EXPOSE 8888/tcp
EXPOSE 9999/udp

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD node -e "fetch('http://127.0.0.1:'+(process.env.RTAIL_WEB_PORT||8888)+'/').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"

USER node

ENTRYPOINT ["node", "cli/rtail-server.ts"]
