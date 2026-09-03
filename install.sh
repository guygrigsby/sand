#!/usr/bin/env bash
# Install sand from its latest release. Two commands to get going, this and `sand config init`:
#
#   curl -fsSL https://raw.githubusercontent.com/guygrigsby/sand/main/install.sh | bash
#
# Runs on the machine that drives the box, which is a Mac unless it is not. The box itself needs
# none of this: it never runs sand, and the skill its agent reads is written over ssh by every
# `sand new` and every pull.
#
# SAND_VERSION=v0.1.0 pins a release, BIN_DIR moves where it lands.
set -euo pipefail

REPO="${SAND_REPO:-guygrigsby/sand}"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
VERSION="${SAND_VERSION:-latest}"

command -v curl >/dev/null || { echo "sand: curl is required" >&2; exit 1; }

# uname's names are not the release's names, and there are two spellings of arm64 in play.
os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "sand: no build for $os; it targets macOS and Linux" >&2; exit 1 ;;
esac
case "$arch" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "sand: no build for $arch" >&2; exit 1 ;;
esac
asset="sand-$os-$arch"

# SAND_BASE_URL is how the test serves a fake release over localhost: the whole point of this
# script is the part after the download, and none of that is reachable without a stand-in.
base="${SAND_BASE_URL:-https://github.com/$REPO/releases}"
if [ "$VERSION" = latest ]; then
  url="$base/latest/download/$asset"
else
  url="$base/download/$VERSION/$asset"
fi

tmp=$(mktemp "${TMPDIR:-/tmp}/sand.XXXXXX")
trap 'rm -f "$tmp"' EXIT

echo "sand: fetching $asset ($VERSION)"
# -f so a 404 is a failure rather than an HTML page written to the binary, which is the one way
# this ends with something installed that cannot run.
curl -fsSL -o "$tmp" "$url" || {
  echo "sand: could not download $url" >&2
  echo "      no release for this platform yet, or the tag does not exist" >&2
  exit 1
}
chmod +x "$tmp"

mkdir -p "$BIN_DIR"
dest="$BIN_DIR/sand"
mv "$tmp" "$dest"
trap - EXIT

# Run it before saying it is installed. A truncated download is the failure this catches, and
# the version is the thing the box side needs to be able to name anyway.
version=$("$dest" --version 2>/dev/null) || {
  echo "sand: installed $dest but it does not run" >&2
  exit 1
}
echo "sand: $dest ($version)"

# A harness on this machine gets the skill, since the text is already here and the install is
# idempotent. That is a convenience for whoever is running an agent locally, not how the box is
# served; nothing is installed for a harness that is not here.
if [ -d "$HOME/.claude" ] || [ -d "$HOME/.pi" ]; then
  "$dest" skill install
fi

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "sand: $BIN_DIR is not on PATH; add it in ~/.zshenv so ssh sees it too" ;;
esac

echo "sand: next, \`sand config init\`"
