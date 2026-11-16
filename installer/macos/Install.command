#!/bin/bash
# Double-click to install, update or uninstall Zen Mac Tab Switcher.
cd "$(dirname "$0")" || exit 1

case "$(uname -m)" in
  arm64) bin=".bin/zen-mac-tab-switcher-macos-arm64" ;;
  *) bin=".bin/zen-mac-tab-switcher-macos-amd64" ;;
esac

# Files downloaded with a browser are quarantined, and macOS refuses to run a
# quarantined command-line tool. You already allowed this script, so allow
# the installer it comes with too.
xattr -dr com.apple.quarantine .bin 2>/dev/null

ZMTS_COMMAND="\"$PWD/Install Zen Mac Tab Switcher.command\"" "$bin" "$@"
status=$?

echo
read -r -n 1 -s -p "Press any key to close this window."
echo
exit "$status"
