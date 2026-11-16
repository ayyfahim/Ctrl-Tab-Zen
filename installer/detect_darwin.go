// Finding Zen on macOS.
package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"zenmacinstaller/internal/core"
)

const (
	zenDirExample = "/Applications/Zen.app"
	closeZenHint  = " (Zen > Quit Zen, or Cmd+Q)"
	updateHint    = "Updating Zen replaces Zen.app, which removes the loader. Run this installer again after each Zen update."
)

func notFoundHint() string { return "" }

// appBundle returns the .app folder that contains p (p may be the bundle,
// its Resources folder or its binary).
func appBundle(p string) string {
	p = filepath.Clean(p)
	for dir := p; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if strings.HasSuffix(strings.ToLower(dir), ".app") {
			return dir
		}
	}
	return ""
}

// isZenApp reports whether app is a Zen bundle. Gecko reads config.js from
// Contents/Resources, next to application.ini.
func isZenApp(app string) bool {
	res := filepath.Join(app, "Contents", "Resources")
	return dirExistsAt(res) &&
		(fileExistsAt(filepath.Join(app, "Contents", "MacOS", "zen")) ||
			fileExistsAt(filepath.Join(res, "application.ini")) && strings.Contains(strings.ToLower(filepath.Base(app)), "zen"))
}

func installFor(app, source string) zenInstall {
	return zenInstall{Dir: filepath.Join(app, "Contents", "Resources"), Display: app, Source: source}
}

func findZenInstalls() []zenInstall {
	return findZenInstallsIn(homeDir(), "/", true)
}

// findZenInstallsIn is findZenInstalls with the home folder and file system
// root passed in, so tests can point it at a fake tree. system turns on the
// lookups that ask macOS (running processes, Spotlight).
func findZenInstallsIn(home, root string, system bool) []zenInstall {
	var out []zenInstall
	seen := map[string]bool{}
	add := func(app, source string) {
		app = appBundle(app)
		if app == "" || !isZenApp(app) {
			return
		}
		key := strings.ToLower(app) // APFS is case-insensitive by default
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, installFor(app, source))
	}

	// 1. A running Zen is the best proof of which install the user uses.
	if system {
		for _, exe := range runningZenPaths() {
			add(exe, "running Zen")
		}
	}

	// 2. The usual places.
	for _, dir := range []string{filepath.Join(root, "Applications"), filepath.Join(home, "Applications")} {
		for _, name := range []string{"Zen.app", "Zen Browser.app", "Zen Twilight.app"} {
			add(filepath.Join(dir, name), "Applications")
		}
		others, _ := filepath.Glob(filepath.Join(dir, "Zen*.app"))
		for _, app := range others {
			add(app, "Applications")
		}
	}

	// 3. Spotlight finds bundles anywhere else.
	if system {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if raw, err := exec.CommandContext(ctx, "mdfind",
			"kMDItemCFBundleIdentifier == 'app.zen-browser.*'").Output(); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				add(strings.TrimSpace(line), "Spotlight")
			}
		}
	}
	return out
}

// runningZenPaths lists executables of running processes inside a Zen bundle.
func runningZenPaths() []string {
	raw, err := exec.Command("ps", "-axww", "-o", "comm=").Output()
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		p := strings.TrimSpace(line)
		if strings.HasSuffix(p, ".app/Contents/MacOS/zen") {
			out = append(out, p)
		}
	}
	return out
}

func isZenRunning(z zenInstall) bool {
	prefix := strings.ToLower(z.shown()) + "/contents/macos/"
	raw, err := exec.Command("ps", "-axww", "-o", "comm=").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		// Matches Zen itself and its helper processes inside the bundle.
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), prefix) {
			return true
		}
	}
	return false
}

// zenInstallAt checks a folder given with --zen-dir: the .app bundle, or any
// path inside it.
func zenInstallAt(dir string) (zenInstall, error) {
	dir = expandHome(dir)
	app := appBundle(dir)
	if app == "" || !isZenApp(app) {
		return zenInstall{}, fmt.Errorf("%s is not a Zen app bundle (for example %s)", dir, zenDirExample)
	}
	return installFor(app, "command line"), nil
}

// writeHint explains macOS's App Management protection, which blocks other
// apps (here: the terminal) from changing signed apps. sudo doesn't help.
func writeHint(err error) string {
	if errors.Is(err, syscall.EPERM) {
		return "macOS blocked the change to Zen.app. Open System Settings > Privacy & Security > App Management, " +
			"turn on your terminal app (Terminal, iTerm...), then run the installer again."
	}
	return ""
}

// profileRoots returns ~/Library/Application Support/<name> and the matching
// ~/Library/Caches/<name>, where the startup cache lives.
func profileRoots(installDir string, opts options) []core.ProfileRoot {
	name := readProfileName(installDir)
	lib := filepath.Join(homeDir(), "Library")
	return overrideRoots([]core.ProfileRoot{{
		AppData: filepath.Join(lib, "Application Support", name),
		Local:   filepath.Join(lib, "Caches", name),
	}}, opts)
}
