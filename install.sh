#!/bin/sh
#
# install.sh — put the rtail pipe on this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/kilianc/rtail/main/install.sh | sh
#
# Options, as environment variables because this is usually run through a pipe
# and has no argv worth speaking of:
#
#   RTAIL_VERSION=v2.0.0-rc.3   install exactly this tag
#   RTAIL_PRERELEASE=1          take the newest release even if it is a prerelease
#   RTAIL_INSTALL_DIR=~/bin     where to put the binary
#   RTAIL_SERVER=1              also install rtail-server
#
# ** On piping a script from the internet into a shell. **
#
# It is a real risk and this file does not pretend otherwise: you are running
# whatever is at that URL, today, as you. Three things are done to make it
# auditable rather than asking for faith:
#
#   - every download is checksummed against the release's own checksums.txt,
#     and a mismatch aborts before anything is written;
#   - nothing is installed outside the install directory, nothing is appended
#     to your shell profile, and sudo is never invoked — if the directory is
#     not writable the script falls back to ~/.local/bin and says so;
#   - it is a hundred lines of POSIX sh, so `curl … | less` is a complete
#     audit, and that is the recommended way to read it first.
#
# If you would rather not pipe at all: the tarball on the releases page is the
# same artifact this downloads, `go install` builds from source, and npm
# carries the same binary. None of those need this file.

set -eu

REPO='kilianc/rtail'
VERSION="${RTAIL_VERSION:-}"
INSTALL_DIR="${RTAIL_INSTALL_DIR:-}"
WANT_SERVER="${RTAIL_SERVER:-}"
PRERELEASE="${RTAIL_PRERELEASE:-}"

say()  { printf '%s\n' "$*"; }
note() { printf '\033[2m%s\033[0m\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required and was not found"; }

need uname
need tar
need mkdir

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1"; }
  fetch_to() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO- "$1"; }
  fetch_to() { wget -qO "$2" "$1"; }
else
  die 'neither curl nor wget is available'
fi

# ** which build **

os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
  Linux)  os=linux  ;;
  Darwin) os=darwin ;;
  *)
    die "no tarball is published for $os.
  The client cross-compiles anywhere, so Windows is on npm instead:
      npm install -g rtail@next
  (@next until 2.0.0 is tagged: without it, npm hands you the v1 Node client)
  or build it yourself:
      go install github.com/kilianc/rtail/v2/cmd/rtail@latest"
    ;;
esac

case "$arch" in
  x86_64 | amd64)  arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "no build for $arch — try: go install github.com/kilianc/rtail/v2/cmd/rtail@latest" ;;
esac

# ** which version **
#
# GitHub's /releases/latest skips prereleases, which during a release
# candidate means it points at something older than what people are being
# asked to try. So the default is still the latest *stable* release — a
# surprise-free default — and RTAIL_PRERELEASE asks for the newest of any
# kind, which is what the README tells you to use until 2.0.0 is out.
#
# Until then the latest stable release is v0.3.0: the v1 Node client, from
# before releases carried tarballs at all. Asking it for one fails with a
# download error that blames the wrong thing, so anything older than 2 is
# turned away here, with the line that does work.

if [ -z "$VERSION" ]; then
  if [ -n "$PRERELEASE" ]; then
    VERSION="$(fetch "https://api.github.com/repos/$REPO/releases?per_page=1" \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  else
    VERSION="$(fetch "https://api.github.com/repos/$REPO/releases/latest" \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  fi
fi

[ -n "$VERSION" ] || die 'could not work out which version to install; set RTAIL_VERSION'

v="${VERSION#v}"

case "$v" in
  0.* | 1.*)
    if [ -n "${RTAIL_VERSION:-}" ]; then
      die "$VERSION is v1, the Node client, from before releases carried tarballs; this installs 2.0 and later"
    fi
    die "no stable 2.x release yet — GitHub's latest is $VERSION, the v1 Node client.
  Rerun with RTAIL_PRERELEASE=1 to take the 2.0 release candidate:
      curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | RTAIL_PRERELEASE=1 sh"
    ;;
esac

name="rtail_${v}_${os}_${arch}"
base="https://github.com/$REPO/releases/download/$VERSION"

# ** where it goes **
#
# sudo is never invoked. A script fetched over the network should not be the
# thing that escalates, so an unwritable /usr/local/bin falls back to the
# user's own bin rather than asking for a password.

if [ -z "$INSTALL_DIR" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then
    INSTALL_DIR=/usr/local/bin
  else
    INSTALL_DIR="$HOME/.local/bin"
  fi
fi

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
[ -w "$INSTALL_DIR" ] || die "$INSTALL_DIR is not writable; set RTAIL_INSTALL_DIR"

# ** fetch, verify, install **

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "==> rtail $VERSION ($os/$arch)"

fetch_to "$base/$name.tar.gz" "$tmp/$name.tar.gz" \
  || die "could not download $name.tar.gz — is $VERSION a real release?"

# The checksum is the only thing standing between a corrupted or substituted
# download and your PATH, so a missing checksums.txt is fatal rather than a
# warning somebody scrolls past.
fetch_to "$base/checksums.txt" "$tmp/checksums.txt" \
  || die 'the release has no checksums.txt; refusing to install unverified'

want="$(sed -n "s/^\([0-9a-f]\{64\}\)[[:space:]][[:space:]]*$name\.tar\.gz$/\1/p" "$tmp/checksums.txt")"
[ -n "$want" ] || die "checksums.txt does not list $name.tar.gz"

if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1)"
elif command -v shasum >/dev/null 2>&1; then
  got="$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1)"
else
  die 'neither sha256sum nor shasum is available; refusing to install unverified'
fi

[ "$want" = "$got" ] || die "checksum mismatch for $name.tar.gz
  expected $want
  got      $got"

note "    sha256 ok"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"

install_one() {
  [ -f "$tmp/$name/$1" ] || die "$1 is not in the tarball"
  # Written to a temporary name and moved into place, so a running rtail is
  # not overwritten underneath itself and a failure leaves the old one.
  cp "$tmp/$name/$1" "$INSTALL_DIR/.$1.new"
  chmod 755 "$INSTALL_DIR/.$1.new"
  mv "$INSTALL_DIR/.$1.new" "$INSTALL_DIR/$1"
  say "==> $INSTALL_DIR/$1"
}

install_one rtail
[ -n "$WANT_SERVER" ] && install_one rtail-server

# ** does it work, and can they reach it **

"$INSTALL_DIR/rtail" --version >/dev/null 2>&1 \
  || die "$INSTALL_DIR/rtail does not run on this machine"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    say ''
    say "$INSTALL_DIR is not on your PATH. Add it:"
    say "    export PATH=\"$INSTALL_DIR:\$PATH\""
    ;;
esac

say ''
say 'Try it:'
say '    your-server | rtail --id my-app'
