# Changelog

## 1.1.0

### New

- **macOS and Linux installer.** One terminal command (`curl … | bash`) finds Zen and your profile, installs, updates or uninstalls. It asks for `sudo` only when Zen's program folder belongs to the system, and checks each download against `SHA256SUMS`.
- **macOS double-click installer** (`ZenMacTabSwitcher-macOS.zip`) for people who'd rather not use the terminal.
- **Windows ARM64** installer (`ZenMacTabSwitcher-Setup-arm64.exe`).
- **Releases on GitHub.** Every installer is attached to each release, built and published by GitHub Actions when a version tag is pushed. CI tests the installer on Windows, macOS and Linux.
- The installer offers to **repair** the loader when a Zen update removed it (on macOS every Zen update does).
- `--help`, `--version`, `-y`/`--yes` and `--option=value` on the command line.

### Fixes

- Updating no longer overwrites the backup of your own `config.js`. Before, the second install replaced the backup with the installer's copy, and your original file was lost.
- Uninstalling now puts back the `config.js` / `config-prefs.js` that were there before the install, instead of only deleting ours.
- Updating no longer asks for administrator permission when only the script changed.
- Profiles with an absolute path in `profiles.ini` (`[Install…] Default=/full/path`) are found.
- `--zen-dir` now checks that the folder really contains Zen.
- Closing or renaming an **Essentials** or pinned tab while the switcher is open now updates the switcher. Zen keeps those tabs outside the normal tab strip, so the switcher missed their events.
- **W**/**Q** close the highlighted tab on non-Latin keyboard layouts (Cyrillic, Greek…).
- A right click on a preview no longer switches tabs on Windows and Linux (Ctrl+click on macOS still does).

## 1.0.0

- Initial release: macOS Cmd+Tab-style Ctrl+Tab tab switcher for Zen Browser (`zen-mac-tab-switcher.uc.js`).
- Windows one-click installer (`installer/ZenMacTabSwitcher-Setup.exe`).
