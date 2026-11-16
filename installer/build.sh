#!/usr/bin/env bash
# Builds every release file into dist/ from any OS with Go 1.25+.
#
#   VERSION=1.1.0 installer/build.sh
#
# The Windows icon and version info need go-winres once:
#   go install github.com/tc-hib/go-winres@v0.3.3
# Without it the Windows installer is built without an icon.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${VERSION:-$(sed -n 's|^// @version *||p' ../zen-mac-tab-switcher.uc.js | head -1)}"
VERSION="${VERSION#v}"
DIST="$(pwd)/../dist"
NAME=zen-mac-tab-switcher
LDFLAGS="-s -w -X main.version=$VERSION"

rm -rf "$DIST"
mkdir -p "$DIST"
cp ../zen-mac-tab-switcher.uc.js assets/zen-mac-tab-switcher.uc.js # always ship the current script

build() { # GOOS GOARCH OUTPUT [extra ldflags]
  echo "  $1/$2 -> $(basename "$3")"
  GOOS=$1 GOARCH=$2 CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS ${4:-}" -o "$3" .
}

echo "Building $VERSION"

# ---- Windows: one-click .exe (x64 and ARM64)
WINRES="$(command -v go-winres || true)"
[ -z "$WINRES" ] && [ -x "$(go env GOPATH)/bin/go-winres" ] && WINRES="$(go env GOPATH)/bin/go-winres"
if [ -n "$WINRES" ]; then
  "$WINRES" make --arch amd64,arm64 --product-version "$VERSION.0" --file-version "$VERSION.0" >/dev/null
else
  echo "  (go-winres not found: Windows builds will have no icon)" >&2
fi
build windows amd64 "$DIST/ZenMacTabSwitcher-Setup.exe" "-H windowsgui"
build windows arm64 "$DIST/ZenMacTabSwitcher-Setup-arm64.exe" "-H windowsgui"
rm -f rsrc_windows_*.syso

# ---- Linux: plain binaries, used by install.sh
build linux amd64 "$DIST/$NAME-linux-amd64"
build linux arm64 "$DIST/$NAME-linux-arm64"

# ---- macOS: binaries for install.sh, plus a double-click zip
build darwin arm64 "$DIST/$NAME-macos-arm64"
build darwin amd64 "$DIST/$NAME-macos-amd64"

MAC="$(mktemp -d)"
trap 'rm -rf "$MAC"' EXIT
APPDIR="$MAC/Zen Mac Tab Switcher"
mkdir -p "$APPDIR/.bin"
cp "$DIST/$NAME-macos-arm64" "$DIST/$NAME-macos-amd64" "$APPDIR/.bin/"
cp macos/Install.command "$APPDIR/Install Zen Mac Tab Switcher.command"
cp macos/Uninstall.command "$APPDIR/Uninstall Zen Mac Tab Switcher.command"
cp macos/README.txt "$APPDIR/README.txt"
chmod 755 "$APPDIR"/*.command "$APPDIR"/.bin/*
(cd "$MAC" && zip -qry "$DIST/ZenMacTabSwitcher-macOS.zip" "Zen Mac Tab Switcher")

# ---- the curl | bash script and the bare userscript
cp install.sh "$DIST/install.sh"
cp ../zen-mac-tab-switcher.uc.js "$DIST/zen-mac-tab-switcher.uc.js"

# ---- checksums (install.sh verifies downloads against this file)
(
  cd "$DIST"
  if command -v sha256sum >/dev/null; then
    sha256sum -- * >SHA256SUMS
  else
    shasum -a 256 -- * >SHA256SUMS
  fi
)
echo
ls -la "$DIST"
