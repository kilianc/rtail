#!/bin/sh
#
# ci.sh — everything CI checks, in one script, runnable on a laptop.
#
#   sh tools/ci.sh            server, webapp and npm — all but image
#   sh tools/ci.sh server     one job
#   sh tools/ci.sh webapp npm several
#   sh tools/ci.sh image      the fourth, which needs a Docker daemon
#
# ** Why this is a script and not a workflow. **
#
# The checks used to live as `run:` blocks inside .github/workflows/ci.yml,
# which made the only way to find out whether they pass a push — and a push
# costs runner minutes, bills macOS at ten times the rate, and takes three
# minutes to tell you about a typo. Worse, it meant the local Makefile and the
# workflow were two descriptions of the same thing, free to disagree, and the
# disagreement only ever showed up as a red build.
#
# So the workflow calls this, and so do you. `make ci` runs the first three
# jobs in the toolchain container and the fourth on the host, because that one
# needs a Docker daemon.
#
# The jobs are deliberately the same names the workflow uses, so a red job in
# GitHub's UI tells you exactly what to run here.
#
# Not covered locally, and honestly so:
#
#   - the macOS half of the `server` matrix. The point of that runner is a
#     different libc and a different DuckDB build; a Linux container cannot
#     stand in for it. `make ci` says so when it finishes.
#   - the Node 20 half of the `webapp` matrix. The image pins one Node, and
#     that is the one the lockfile was resolved against.

set -eu

ROOT="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT"

# Go and Node spell architectures differently, and the npm job needs both.
case "$(uname -m)" in
  aarch64 | arm64) NODE_ARCH=arm64 ;;
  *)               NODE_ARCH=x64   ;;
esac

# The OS too, so the npm job installs the package for the machine it is on —
# the toolchain container is Linux, a Mac running `sh tools/ci.sh npm` is not.
case "$(uname -s)" in
  Darwin) NODE_OS=darwin ;;
  *)      NODE_OS=linux  ;;
esac

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
fail() { printf '\n\033[31m!! %s\033[0m\n' "$1" >&2; exit 1; }

# ** server **

job_server() {
  step 'vet'
  go vet ./...

  step 'gofmt'
  unformatted="$(gofmt -l cmd internal web)"
  if [ -n "$unformatted" ]; then
    echo "$unformatted"
    fail 'gofmt needed on the files above'
  fi

  # The interop tests assert that a completely unrelated implementation reads
  # our Parquet. They skip themselves when duckdb is missing, which would turn
  # the product's central claim into an untested one without a word.
  step 'duckdb is present (the interop tests skip without it)'
  command -v duckdb >/dev/null || fail 'no duckdb on PATH'
  duckdb -c "SELECT 'duckdb ' || version()"

  step 'go test -race ./...'
  go test -race ./...

  # CGO_ENABLED=1 from P2 on: go-duckdb has no pure-Go fallback, so a
  # CGO_ENABLED=0 build fails at link time rather than quietly losing search.
  step 'build the server'
  CGO_ENABLED=1 go build -trimpath -o /tmp/rtail-server ./cmd/rtail-server

  # install.sh is the one piece of this project that runs on a stranger's
  # machine, straight off the internet, before they have read it. A syntax
  # error there is a broken install for everyone, so it is parsed here.
  #
  # The toolchain image carries shellcheck, as GitHub's Ubuntu runners do, and
  # when it runs, what it finds fails the job. A lint that can only ever pass
  # is not a check. It cannot be required outright — the macOS runners have
  # none — but a skip is said out loud rather than passing for a green run,
  # because here the usual reason for one is a toolchain image built before
  # it carried shellcheck, and `make` does not rebuild an image it already
  # has.
  step 'parse install.sh'
  sh -n install.sh
  if command -v shellcheck >/dev/null 2>&1; then
    step 'shellcheck install.sh'
    shellcheck -s sh install.sh
  else
    printf '\n\033[33m!! no shellcheck here: install.sh was parsed, not linted.\n' >&2
    printf '   In the toolchain container, rebuild the image: docker build -t rtail-tools tools/\033[0m\n' >&2
  fi
}

# ** webapp **

job_webapp() {
  # --ignore-scripts: nothing in the lockfile needs an install script to work
  # — esbuild finds its platform binary without one — and each would be a
  # stranger's code running with everything the job can reach.
  step 'npm ci'
  npm ci --ignore-scripts --no-audit --no-fund

  step 'typecheck'
  npm run typecheck

  step 'build the webapp'
  npm run build

  step 'build the embedded webapp'
  npm run dist
}

# ** npm **
#
# The one failure this layout can have is a launcher that cannot find its
# binary, and it only shows up after `npm publish` — so it is exercised here
# instead.

job_npm() {
  step 'lay out the packages'
  sh tools/npm-pack.sh 0.0.0-ci

  # Through tarballs, not the laid-out directories. `npm install <dir>`
  # symlinks rather than copies, and Node resolves modules through the
  # realpath, so the launcher would look for its binary next to dist/npm/ and
  # never see the node_modules it was installed into. Packing first makes this
  # the install a user gets.
  step 'install as a user would, and run it'
  rm -rf /tmp/rtail-ci && mkdir -p /tmp/rtail-ci/tgz /tmp/rtail-ci/launch

  ( cd "dist/npm/rtail-$NODE_OS-$NODE_ARCH" && npm pack --pack-destination /tmp/rtail-ci/tgz )
  ( cd dist/npm/rtail                       && npm pack --pack-destination /tmp/rtail-ci/tgz )

  cd /tmp/rtail-ci/launch
  npm init -y >/dev/null

  # The platform package goes in on its own because the one the launcher wants
  # is not published yet; --omit=optional then stops npm reaching for the
  # other four.
  npm install "/tmp/rtail-ci/tgz/rtail-$NODE_OS-$NODE_ARCH-0.0.0-ci.tgz" --no-audit --no-fund
  npm install /tmp/rtail-ci/tgz/rtail-0.0.0-ci.tgz --omit=optional --no-audit --no-fund

  [ "$(./node_modules/.bin/rtail --version)" = '0.0.0-ci' ] \
    || fail 'the launcher did not report the version it was stamped with'

  printf 'hello\n' | ./node_modules/.bin/rtail --port 9999 | grep -qx hello \
    || fail 'the launcher did not pass stdin through to stdout'

  ./node_modules/.bin/rtail --nope 2>&1 | grep -q 'unknown argument' \
    || fail 'an unknown flag was not rejected'

  cd "$ROOT"
}

# ** image **
#
# Needs a Docker daemon, so this one runs on the host rather than inside the
# toolchain container. `make ci` drives it; `sh tools/ci.sh image` works too
# wherever docker is reachable.
#
# The release runs it once per architecture, which is what the two knobs are
# for: RTAIL_CI_PLATFORM runs an image built for another one (linux/arm64,
# under QEMU), and RTAIL_CI_HEALTH_WAIT gives that emulated server longer than
# the native thirty seconds to answer.

job_image() {
  # CI builds the image with buildx and a layer cache that lives on GitHub,
  # which is worth real minutes, so it hands the built image over and sets
  # this rather than paying for a cold build twice.
  if [ -n "${RTAIL_CI_IMAGE_PREBUILT:-}" ]; then
    step 'image already built'
  else
    step 'build the image'
    docker build -t rtail:ci .
  fi

  step 'smoke-test the image'
  docker rm -f rtail-ci >/dev/null 2>&1 || true
  docker run -d ${RTAIL_CI_PLATFORM:+--platform "$RTAIL_CI_PLATFORM"} \
    --name rtail-ci -p 8899:8888 -p 9988:9999/udp rtail:ci >/dev/null

  ok=''
  for _ in $(seq 1 "${RTAIL_CI_HEALTH_WAIT:-30}"); do
    if curl -sf http://127.0.0.1:8899/healthz >/dev/null 2>&1; then ok=1; break; fi
    sleep 1
  done

  if [ -z "$ok" ]; then
    docker logs rtail-ci 2>&1 | tail -30
    docker rm -f rtail-ci >/dev/null
    fail 'the server never became healthy'
  fi

  curl -sf http://127.0.0.1:8899/healthz | tee /dev/stderr | grep -q '"status":"ok"'
  curl -sf http://127.0.0.1:8899/ | grep -qi '<title'

  docker rm -f rtail-ci >/dev/null
}

[ $# -gt 0 ] || set -- server webapp npm

for job in "$@"; do
  case "$job" in
    server | webapp | npm | image) "job_$job" ;;
    *) fail "no such job: $job (server, webapp, npm, image)" ;;
  esac
done

printf '\n\033[32mok\033[0m\n'
