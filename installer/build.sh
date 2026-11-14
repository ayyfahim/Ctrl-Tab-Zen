#!/usr/bin/env bash
# Builds ZenMacTabSwitcher-Setup.exe (Windows x64) from any OS with Go 1.25+.
# Needs go-winres once:  go install github.com/tc-hib/go-winres@latest
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${VERSION:-1.0.0}"
cp ../zen-mac-tab-switcher.uc.js assets/zen-mac-tab-switcher.uc.js   # always ship the current script

go-winres make --arch amd64 >/dev/null
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$VERSION" \
  -o ZenMacTabSwitcher-Setup.exe .
ls -la ZenMacTabSwitcher-Setup.exe
