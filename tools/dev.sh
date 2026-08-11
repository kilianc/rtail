#!/bin/sh
#
# Dev loop, run inside the tools container by `make dev`.
#
# Builds the assets, then runs the real rtail-server together with a few demo
# log producers so the webapp has live streams to render. Stylesheets are
# recompiled on save; reload the browser to pick them up.
#
# This replaces the old `gulp app` task, which cannot run at all on modern
# Node (gulp 3.9 dies with "primordials is not defined").

set -e

WEB_PORT="${WEB_PORT:-8888}"

echo "==> building assets"
sh tools/build-assets.sh

echo "==> starting sass --watch"
sass --watch app/scss/main.scss:app/css/main.css --no-source-map &

echo "==> starting rtail-server on 0.0.0.0:${WEB_PORT}"
# 0.0.0.0 so the port is reachable from the host through Docker's NAT.
node cli/rtail-server.js \
  --web-version development \
  --web-host 0.0.0.0 \
  --web-port "${WEB_PORT}" &

# Give the UDP listener a moment before the producers start talking to it.
sleep 1

echo "==> starting demo streams"
for name in api-gateway worker-billing nginx-access; do
  node tools/demo-logs.js "$name" | node cli/rtail-client.js --id "$name" --mute &
done

echo ""
echo "  rTail is up:  http://localhost:${WEB_PORT}/app"
echo "  Ctrl-C to stop."
echo ""

# Take the whole process group down together on Ctrl-C, so no sass watcher or
# log producer is left running in the background.
trap 'kill 0' INT TERM
wait
