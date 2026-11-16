#!/usr/bin/env bash
# Zen Mac Tab Switcher: install, update or uninstall on macOS and Linux.
#
#   curl -fsSL https://github.com/ayyfahim/Ctrl-Tab-Zen/releases/latest/download/install.sh | bash
#
# Pass options after "bash -s --", for example:
#   curl -fsSL .../install.sh | bash -s -- --uninstall
#   curl -fsSL .../install.sh | bash -s -- --yes --zen-dir ~/zen
#
# Environment:
#   ZMTS_VERSION   release to install, e.g. v1.1.0 (default: latest)
#   ZMTS_BASE_URL  download from here instead of GitHub (mirrors, testing)
set -euo pipefail

REPO="ayyfahim/Ctrl-Tab-Zen"
NAME="zen-mac-tab-switcher"

fail() {
  printf '\033[1;31m✗\033[0m %s\n' "$*" >&2
  exit 1
}

# Running through sudo would install into root's Zen profile, not yours. The
# installer asks for sudo itself when Zen's program folder needs it.
if [ "$(id -u)" = 0 ] && [ -n "${SUDO_USER:-}" ]; then
  fail "Run this without sudo. It asks for your password itself if it needs to."
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=macos ;;
  *) fail "This script is for macOS and Linux. On Windows, download ZenMacTabSwitcher-Setup.exe from https://github.com/$REPO/releases/latest" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "Unsupported processor: $(uname -m)" ;;
esac

version="${ZMTS_VERSION:-latest}"
if [ "$version" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/v${version#v}"
fi
base="${ZMTS_BASE_URL:-$base}"
asset="$NAME-$os-$arch"

if command -v curl >/dev/null; then
  fetch() { curl -fsSL --retry 2 -o "$2" "$1"; }
elif command -v wget >/dev/null; then
  fetch() { wget -q -O "$2" "$1"; }
else
  fail "Needs curl or wget."
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

printf 'Downloading %s (%s)...\n' "$asset" "$version"
fetch "$base/$asset" "$tmp/$asset" || fail "Download failed: $base/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || fail "Download failed: $base/SHA256SUMS"

expected="$(awk -v f="$asset" '$2 == f || $2 == "*"f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$expected" ] || fail "$asset is not listed in SHA256SUMS"
if command -v sha256sum >/dev/null; then
  actual="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
[ "$expected" = "$actual" ] || fail "Checksum mismatch for $asset. Nothing was changed."

chmod +x "$tmp/$asset"
# The installer reads answers from the terminal, so it works through a pipe.
# A failure exits non-zero; don't let set -e hide the installer's own message.
status=0
ZMTS_COMMAND="curl -fsSL $base/install.sh | bash -s --" "$tmp/$asset" "$@" || status=$?
exit "$status"
