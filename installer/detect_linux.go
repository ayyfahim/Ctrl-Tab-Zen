// Finding Zen on Linux.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"zenmacinstaller/internal/core"
)

const (
	zenDirExample = "~/.tarball-installations/zen"
	closeZenHint  = ""
	updateHint    = ""
	flatpakID     = "app.zen_browser.zen"
)

// Zen's tarball ships both names; either can be the running process.
var zenBinaries = []string{"zen", "zen-bin"}

// isZenDir reports whether dir is Zen's program folder: it has the zen binary
// and the files Gecko keeps next to it. The second check keeps /usr/bin (which
// may hold a "zen" symlink) from counting.
func isZenDir(dir string) bool {
	hasBin := false
	for _, b := range zenBinaries {
		hasBin = hasBin || fileExistsAt(filepath.Join(dir, b))
	}
	return hasBin && (fileExistsAt(filepath.Join(dir, "application.ini")) ||
		fileExistsAt(filepath.Join(dir, "omni.ja")))
}

// isReadOnlyMount reports Zen folders that can never take config.js: a mounted
// AppImage or a Flatpak/Snap runtime.
func isReadOnlyMount(dir string) bool {
	return strings.HasPrefix(dir, "/tmp/.mount_") || strings.HasPrefix(dir, "/snap/") ||
		strings.Contains(dir, "/flatpak/app/")
}

// findZenInstalls returns every distinct Zen program folder it can find,
// most reliable source first.
func findZenInstalls() []zenInstall {
	return findZenInstallsIn(homeDir(), os.Getenv("PATH"), "/")
}

// findZenInstallsIn is findZenInstalls with the home folder, PATH and file
// system root passed in, so tests can point it at a fake tree.
func findZenInstallsIn(home, path, root string) []zenInstall {
	var out []zenInstall
	seen := map[string]bool{}
	add := func(dir, source string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" || dir == "." || isReadOnlyMount(dir) || !isZenDir(dir) {
			return
		}
		key := dir
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			key = real
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, zenInstall{Dir: dir, Source: source})
	}
	addBinary := func(bin, source string) {
		for _, dir := range dirsFromLauncher(bin) {
			add(dir, source)
		}
	}

	// 1. A running Zen is the best proof of which install the user uses.
	if root == "/" {
		for _, exe := range runningZenPaths() {
			add(filepath.Dir(exe), "running Zen")
		}
	}

	// 2. Launchers on PATH (symlinks or wrapper scripts).
	for _, dir := range filepath.SplitList(path) {
		for _, name := range []string{"zen", "zen-browser", "zen-twilight", "zen-bin"} {
			addBinary(filepath.Join(dir, name), "PATH")
		}
	}

	// 3. Desktop entries, which point at tarballs extracted anywhere.
	for _, dir := range []string{
		filepath.Join(home, ".local", "share", "applications"),
		filepath.Join(root, "usr", "share", "applications"),
		filepath.Join(root, "usr", "local", "share", "applications"),
	} {
		files, _ := filepath.Glob(filepath.Join(dir, "*[zZ]en*.desktop"))
		for _, f := range files {
			if exe := desktopExec(f); exe != "" {
				addBinary(exe, "desktop entry")
			}
		}
	}

	// 4. Default folders: Zen's install script, AUR and distro packages, and
	// common places to extract the tarball.
	for _, dir := range []string{
		filepath.Join(home, ".tarball-installations", "zen"),
		filepath.Join(home, ".tarball-installations", "zen-twilight"),
		filepath.Join(root, "opt", "zen-browser-bin"),
		filepath.Join(root, "opt", "zen-browser"),
		filepath.Join(root, "opt", "zen"),
		filepath.Join(root, "opt", "zen-twilight-bin"),
		filepath.Join(root, "opt", "zen-twilight"),
		filepath.Join(root, "usr", "lib", "zen-browser"),
		filepath.Join(root, "usr", "lib", "zen"),
		filepath.Join(root, "usr", "lib64", "zen-browser"),
		filepath.Join(root, "usr", "lib64", "zen"),
		filepath.Join(root, "usr", "local", "zen"),
		filepath.Join(home, ".local", "share", "zen"),
		filepath.Join(home, ".local", "opt", "zen"),
		filepath.Join(home, "Applications", "zen"),
		filepath.Join(home, "zen"),
	} {
		add(dir, "default folder")
	}
	return out
}

var pathInScript = regexp.MustCompile(`/[^\s"'$;:|&<>()=]+`)

// dirsFromLauncher returns Zen folders a launcher leads to: the folder of the
// binary it links to, or folders named in a wrapper script (for example
// /usr/bin/zen-browser running "exec /opt/zen-browser-bin/zen").
func dirsFromLauncher(bin string) []string {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return nil
	}
	if isZenDir(filepath.Dir(real)) {
		return []string{filepath.Dir(real)}
	}
	f, err := os.Open(real)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 2)
	if n, _ := f.Read(head); n < 2 || string(head) != "#!" {
		return nil
	}
	var dirs []string
	sc := bufio.NewScanner(f)
	for lines := 0; sc.Scan() && lines < 200; lines++ {
		for _, p := range pathInScript.FindAllString(sc.Text(), -1) {
			if isZenDir(p) {
				dirs = append(dirs, p)
			} else if isZenDir(filepath.Dir(p)) {
				dirs = append(dirs, filepath.Dir(p))
			}
		}
	}
	return dirs
}

// desktopExec returns the program of a .desktop file's Exec= line.
func desktopExec(file string) string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "Exec=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			if end := strings.Index(v[1:], `"`); end >= 0 {
				return v[1 : end+1]
			}
		}
		if exe, _, _ := strings.Cut(v, " "); strings.HasPrefix(exe, "/") {
			return exe
		}
		if exe, _, _ := strings.Cut(v, " "); exe != "" {
			if p, err := exec.LookPath(exe); err == nil {
				return p
			}
		}
		return ""
	}
	return ""
}

// runningZenPaths lists the executables of running Zen processes.
func runningZenPaths() []string {
	procs, _ := filepath.Glob("/proc/[0-9]*/exe")
	var out []string
	seen := map[string]bool{}
	for _, p := range procs {
		exe, err := os.Readlink(p)
		if err != nil {
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)") // binary replaced by an update
		base := filepath.Base(exe)
		if (base == "zen" || base == "zen-bin") && !seen[exe] {
			seen[exe] = true
			out = append(out, exe)
		}
	}
	return out
}

func isZenRunning(z zenInstall) bool {
	want := realPath(z.Dir)
	for _, exe := range runningZenPaths() {
		if realPath(filepath.Dir(exe)) == want {
			return true
		}
	}
	return false
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// zenInstallAt checks a folder given with --zen-dir. The zen binary or a
// launcher that leads to it is accepted too.
func zenInstallAt(dir string) (zenInstall, error) {
	dir = filepath.Clean(expandHome(dir))
	if fileExistsAt(dir) {
		if dirs := dirsFromLauncher(dir); len(dirs) > 0 {
			dir = dirs[0]
		}
	}
	if isReadOnlyMount(dir) {
		return zenInstall{}, fmt.Errorf("%s is read-only (AppImage, Flatpak or Snap).\n\n%s", dir, readOnlyAdvice)
	}
	if !isZenDir(dir) {
		return zenInstall{}, fmt.Errorf("%s is not Zen's program folder: it needs the zen binary and application.ini", dir)
	}
	return zenInstall{Dir: dir, Source: "command line"}, nil
}

const readOnlyAdvice = "fx-autoconfig has to put config.js into Zen's program folder, which these formats don't allow. " +
	"Install the tarball version of Zen instead: https://docs.zen-browser.app/guides/install-linux"

// notFoundHint explains the common reason Zen isn't found: it's a Flatpak,
// Snap or AppImage, whose program folders are read-only.
func notFoundHint() string {
	home := homeDir()
	for _, p := range []string{
		filepath.Join("/var/lib/flatpak/app", flatpakID),
		filepath.Join(home, ".local/share/flatpak/app", flatpakID),
	} {
		if dirExistsAt(p) {
			return "\n\nThe Flatpak version of Zen is installed. " + readOnlyAdvice
		}
	}
	for _, exe := range runningZenPaths() {
		if isReadOnlyMount(exe) {
			return "\n\nThe running Zen is an AppImage or Snap. " + readOnlyAdvice
		}
	}
	return ""
}

func writeHint(err error) string {
	if errors.Is(err, syscall.EROFS) {
		return "Zen's program folder is read-only. " + readOnlyAdvice
	}
	return ""
}

// profileRoots lists where Zen may keep profiles.ini: ~/.zen (older installs,
// still used while it exists) and $XDG_CONFIG_HOME/zen (newer installs).
// Caches, including the startup cache, live in $XDG_CACHE_HOME/zen.
func profileRoots(installDir string, opts options) []core.ProfileRoot {
	name := readProfileName(installDir)
	home := homeDir()
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(cache) {
		cache = filepath.Join(home, ".cache")
	}
	local := filepath.Join(cache, name)
	return overrideRoots([]core.ProfileRoot{
		{AppData: filepath.Join(home, "."+name), Local: local},
		{AppData: filepath.Join(config, name), Local: local},
	}, opts)
}
