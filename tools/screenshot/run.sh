#!/bin/sh
#
# run.sh — take the README screenshot. `make screenshots` builds what this
# needs and runs it; it runs on the host, because it starts two containers.
#
# A server on empty storage, in the toolchain image, on a network of its own;
# seed.js fills it with an hour of traffic, and shoot.js photographs it from
# the screenshot image. Nothing is published on the host, and nothing is left
# behind: the storage lives in the server container's /tmp and goes with it.

set -eu

name=rtail-screenshot
network=rtail-screenshot
image=rtail-screenshot-tools

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cleanup
docker network create "$network" >/dev/null

docker run -d --name "$name" --network "$network" \
  -u "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$PWD:/work" -w /work rtail-tools \
  bin/rtail-server --web-root app --web-host 0.0.0.0 --data /tmp/data --insecure >/dev/null

run() {
  docker run --rm --network "$network" \
    -u "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$PWD:/work" -w /work "$image" "$@"
}

run sh -c "for _ in \$(seq 1 30); do curl -sf http://$name:8888/healthz >/dev/null && exit 0; sleep 1; done; exit 1" || {
  echo '!! the server did not come up; its last output:' >&2
  docker logs --tail 40 "$name" >&2
  exit 1
}

echo '==> seeding'
run node tools/screenshot/seed.js "http://$name:8888"

echo '==> shooting'
run node tools/screenshot/shoot.js "http://$name:8888"
