Zen Mac Tab Switcher for macOS
==============================

1. Quit Zen (Cmd+Q).
2. Right-click "Install Zen Mac Tab Switcher.command" and choose Open.
   (macOS asks the first time because the file isn't from the App Store.
   Click Open in that dialog. If there is no Open button, go to
   System Settings > Privacy & Security and click "Open Anyway".)
3. Follow the questions in the Terminal window.
4. Start Zen, hold Ctrl and press Tab.

If macOS says it blocked the change to Zen.app: open System Settings >
Privacy & Security > App Management, turn on Terminal, and run the
installer again.

Updating Zen replaces Zen.app and removes the loader. Run the installer
again after each Zen update; your settings are kept.

To remove it, open "Uninstall Zen Mac Tab Switcher.command" the same way.

Prefer the terminal? This does the same thing:
  curl -fsSL https://github.com/ayyfahim/Ctrl-Tab-Zen/releases/latest/download/install.sh | bash

More: https://github.com/ayyfahim/Ctrl-Tab-Zen
