#!/bin/sh
#
# npm-pack.sh — build the client for every published platform and lay out the
# npm packages that carry them.
#
#   tools/npm-pack.sh <version> [outdir]
#
# Produces, under outdir (default dist/npm):
#
#   rtail/                    the package people install; a launcher and a
#                             list of optional dependencies, no binary
#   rtail-<os>-<arch>/        one per platform, each a few MB, each declaring
#                             the os and cpu it is for
#
# Nothing here runs at install time. npm resolves `os`/`cpu` against the
# machine doing the installing, downloads the one package that matches and
# skips the others — which is why there is no postinstall script to be
# disabled by --ignore-scripts, no download to fail behind a proxy, and
# nothing executing with the installing user's privileges.
#
# Written in sh rather than Node on purpose: this is the step that takes the
# Node toolchain out of the product, and it would be a poor joke to need Node
# to run it. Go is the only tool it calls.

set -eu

VERSION="${1:?usage: npm-pack.sh <version> [outdir]}"
ROOT="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
OUT_REL="${2:-dist/npm}"

rm -rf "${ROOT:?}/${OUT_REL:?}"
mkdir -p "$ROOT/$OUT_REL"
OUT="$(CDPATH='' cd -- "$ROOT/$OUT_REL" && pwd)"

# The platforms published to npm, as "<goos> <goarch> <node-platform> <node-arch>".
#
# Node and Go disagree about architecture names — amd64 against x64 — so both
# spellings are written out rather than derived twice and got wrong once.
# Windows is on the list because npm reaches Windows and the release tarballs
# do not; the client is pure Go with no cgo, so this whole matrix
# cross-compiles from one runner. The server cannot, because it embeds DuckDB,
# which is why that build has four runners of its own.
PLATFORMS='linux:amd64:linux:x64
linux:arm64:linux:arm64
darwin:amd64:darwin:x64
darwin:arm64:darwin:arm64
windows:amd64:win32:x64'

# ** one package per platform **

for row in $PLATFORMS; do
  goos="${row%%:*}"; rest="${row#*:}"
  goarch="${rest%%:*}"; rest="${rest#*:}"
  nodeos="${rest%%:*}"
  nodearch="${rest##*:}"

  dir="$OUT/rtail-${nodeos}-${nodearch}"
  exe='rtail'
  [ 'windows' = "$goos" ] && exe='rtail.exe'

  mkdir -p "$dir/bin"
  echo "building ${goos}/${goarch}"

  ( cd "$ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o "$dir/bin/$exe" \
        ./cmd/rtail )

  cat > "$dir/package.json" <<JSON
{
  "name": "@rtail/${nodeos}-${nodearch}",
  "version": "${VERSION}",
  "description": "The rtail client binary for ${nodeos} ${nodearch}.",
  "os": ["${nodeos}"],
  "cpu": ["${nodearch}"],
  "files": ["bin", "NOTICE"],
  "repository": {
    "type": "git",
    "url": "git+https://github.com/kilianc/rtail.git"
  },
  "license": "Apache-2.0"
}
JSON

  # npm packs LICENSE whatever `files` says, and NOTICE only if it is named
  # there. Apache-2.0 §4(d) has anyone who redistributes this pass NOTICE on,
  # which they can only do if the package carries it.
  cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$dir/"
done

# ** the package people install **

mkdir -p "$OUT/rtail"
cp -R "$ROOT/npm/rtail/bin" "$OUT/rtail/bin"
cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$ROOT/README.md" "$OUT/rtail/"

# The optional dependencies are pinned to this exact version. A caret range
# would let npm resolve a launcher and a binary from two different releases,
# which is the one failure mode this layout must not have.
deps=''
for row in $PLATFORMS; do
  rest="${row#*:}"; rest="${rest#*:}"
  nodeos="${rest%%:*}"
  nodearch="${rest##*:}"
  [ -n "$deps" ] && deps="${deps},"
  deps="${deps}
    \"@rtail/${nodeos}-${nodearch}\": \"${VERSION}\""
done

cat > "$OUT/rtail/package.json" <<JSON
{
  "name": "rtail",
  "version": "${VERSION}",
  "description": "Terminal output to the browser in seconds, using UNIX pipes.",
  "keywords": ["log", "logging", "logio", "logs", "tail", "udp"],
  "bin": { "rtail": "bin/rtail.js" },
  "files": ["bin", "NOTICE"],
  "engines": { "node": ">=20" },
  "optionalDependencies": {${deps}
  },
  "repository": {
    "type": "git",
    "url": "git+https://github.com/kilianc/rtail.git"
  },
  "homepage": "https://github.com/kilianc/rtail#readme",
  "bugs": "https://github.com/kilianc/rtail/issues",
  "author": "Kilian Ciuffolo <me@nailik.org>",
  "license": "Apache-2.0"
}
JSON

echo
echo "laid out in ${OUT_REL}:"
ls -1 "$OUT"
