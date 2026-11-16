// Zen Mac Tab Switcher installer.
//
// Finds Zen and its profile, copies the fx-autoconfig loader and the switcher
// script, and clears the startup cache. Run it again to update or uninstall.
//
// This file holds the flow shared by all platforms. The platform files
// (main_windows.go, ui_unix.go, detect_*.go) provide detection, prompts and
// elevation.
package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"zenmacinstaller/internal/core"
)

//go:embed all:assets
var embedded embed.FS

var version = "dev"

const title = "Zen Mac Tab Switcher"

// Exit codes (also used by the elevated helper process).
const (
	exitOK       = 0
	exitFailed   = 1
	exitCanceled = 2
	exitSine     = 3
)

type options struct {
	silent    bool
	uninstall bool
	onlyDir   bool // helper mode: only the install-dir half (runs elevated)
	zenDir    string
	profile   string
	appData   string // override for the profiles.ini folder, mainly for tests
	localData string
}

// zenInstall is one place Zen is installed, with how we found it.
type zenInstall struct {
	Dir     string // where config.js goes (Zen.app/Contents/Resources on macOS)
	Display string // what to show the user (Zen.app on macOS, Dir elsewhere)
	Source  string
}

func (z zenInstall) shown() string {
	if z.Display != "" {
		return z.Display
	}
	return z.Dir
}

type msgKind int

const (
	msgInfo  msgKind = iota
	msgPlain         // help and version text
	msgWarning
	msgError
)

type choice int

const (
	choiceCancel choice = iota
	choiceUpdate
	choiceUninstall
)

var (
	errHelp    = errors.New("help requested")
	errVersion = errors.New("version requested")
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	opts, err := parseArgs(args)
	switch {
	case errors.Is(err, errHelp):
		message(opts, msgPlain, usage())
		return exitOK
	case errors.Is(err, errVersion):
		message(opts, msgPlain, title+" "+version)
		return exitOK
	}
	if err != nil {
		message(opts, msgError, err.Error()+"\n\n"+usage())
		return exitFailed
	}
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		message(opts, msgError, err.Error())
		return exitFailed
	}

	// ---- find Zen
	var installs []zenInstall
	if opts.zenDir != "" {
		z, err := zenInstallAt(opts.zenDir)
		if err != nil {
			message(opts, msgError, err.Error())
			return exitFailed
		}
		installs = []zenInstall{z}
	} else {
		installs = findZenInstalls()
	}
	if len(installs) == 0 {
		message(opts, msgError, "Zen Browser was not found on this computer.\n\n"+
			"Install Zen first, start it once, then run this installer again."+
			notFoundHint()+
			"\n\nIf Zen is in an unusual place, run:\n  "+commandName()+" --zen-dir "+zenDirExample)
		return exitFailed
	}
	zen := installs[0]

	// ---- find the profile
	target := core.Target{InstallDir: zen.Dir}
	if opts.profile != "" {
		target.ProfileDir = opts.profile
	} else {
		prof, err := core.FindProfileIn(profileRoots(zen.Dir, opts))
		if err != nil {
			message(opts, msgError, "Zen was found, but not its profile.\n\n"+err.Error()+
				"\n\nStart Zen once so it creates a profile, close it, and run this installer again.")
			return exitFailed
		}
		target.ProfileDir, target.LocalProfileDir = prof.Dir, prof.LocalDir
	}

	if err := core.CheckSine(target); err != nil {
		message(opts, msgError, err.Error()+".\n\nNothing was changed.")
		return exitSine
	}

	state := core.Detect(target)
	uninstall := opts.uninstall

	// ---- ask (never in helper or silent mode)
	if !opts.silent && !opts.onlyDir {
		info := fmt.Sprintf("Zen:      %s\nProfile:  %s\n", zen.shown(), target.ProfileDir)
		if len(installs) > 1 {
			info += "\n(Other Zen installs were found; using the first. Use --zen-dir to pick another.)\n"
		}
		switch {
		case state.Installed() && !uninstall:
			switch askUpdateOrUninstall(title + " is already installed.\n\n" + info) {
			case choiceUpdate:
			case choiceUninstall:
				uninstall = true
			default:
				return canceled()
			}
		case uninstall:
			if !confirm("Uninstall " + title + "?\n\n" + info) {
				return canceled()
			}
		case state.ScriptInstalled:
			// Zen updates replace the program folder (always on macOS), which
			// removes the loader but keeps the script in the profile.
			if !confirm(title + " is in your profile, but the loader in Zen's program folder is missing (Zen updates can remove it).\n\n" +
				info + "\nRepair it?") {
				return canceled()
			}
		default:
			if !confirm("Install " + title + "?\n\n" + info +
				"\nThis adds a macOS-style Ctrl+Tab switcher to Zen.") {
				return canceled()
			}
		}
	}

	// ---- Zen must be closed so its startup cache can be cleared
	if !opts.onlyDir {
		for isZenRunning(zen) {
			if opts.silent {
				message(opts, msgError, "Zen is running. Close it and try again.")
				return exitFailed
			}
			if !askRetry("Zen is running.\n\nPlease close Zen completely" + closeZenHint + ", then try again.") {
				return canceled()
			}
		}
	}

	log := func(format string, args ...any) { progress(opts, fmt.Sprintf(format, args...)) }

	do := func(part core.Part) error {
		if uninstall {
			return core.Uninstall(target, part, log)
		}
		return core.Install(assets, target, part, log)
	}

	// ---- install-dir half (may need administrator rights)
	if opts.onlyDir {
		return finish(opts, do(core.PartInstallDir), "")
	}
	if code := installDirHalf(opts, assets, zen, target, uninstall, do); code != exitOK {
		return code
	}

	// ---- profile half
	if err := do(core.PartProfile); err != nil {
		return finish(opts, err, "")
	}

	done := "Installed.\n\nStart Zen, hold Ctrl and press Tab."
	if uninstall {
		done = "Uninstalled.\n\nZen's normal Ctrl+Tab is back."
	} else if updateHint != "" {
		done += "\n\n" + updateHint
	}
	return finish(opts, nil, done+"\n\n"+"Profile: "+target.ProfileDir)
}

// installDirHalf does the install-dir half directly, or through an elevated
// helper process when the folder isn't writable. It skips asking for
// administrator rights when the folder needs no change.
func installDirHalf(opts options, assets fs.FS, zen zenInstall, target core.Target, uninstall bool,
	do func(core.Part) error) int {
	if !core.InstallDirChangeNeeded(assets, target, uninstall) {
		return exitOK
	}
	err := canWrite(zen.Dir)
	if err == nil {
		return finishIfFailed(opts, do(core.PartInstallDir))
	}
	if hint := writeHint(err); hint != "" {
		// Elevation would not help (for example macOS App Management).
		message(opts, msgError, "Could not write to Zen's program folder:\n"+zen.shown()+"\n\n"+hint+"\n\nNothing was changed.")
		return exitFailed
	}
	helperArgs := []string{"--only-dir", "--silent", "--zen-dir", zen.Dir}
	if target.ProfileDir != "" {
		helperArgs = append(helperArgs, "--profile", target.ProfileDir)
	}
	if uninstall {
		helperArgs = append(helperArgs, "--uninstall")
	}
	switch code := runElevatedHelper(opts, zen.shown(), helperArgs); code {
	case exitOK:
		return exitOK
	case exitCanceled:
		message(opts, msgInfo, "Administrator permission was declined.\n\nNothing was changed.")
		return exitCanceled
	case exitSine:
		message(opts, msgError, core.ErrSine.Error()+".")
		return exitSine
	default:
		message(opts, msgError, "Could not write to Zen's program folder:\n"+zen.shown())
		return exitFailed
	}
}

// finishIfFailed reports err like finish does, and returns exitOK when err is nil.
func finishIfFailed(opts options, err error) int {
	if err == nil {
		return exitOK
	}
	return finish(opts, err, "")
}

func canceled() int {
	reportCanceled()
	return exitCanceled
}

// finish reports the outcome and returns the process exit code.
func finish(opts options, err error, success string) int {
	switch {
	case err == nil:
		if success != "" {
			message(opts, msgInfo, success)
		}
		return exitOK
	case errors.Is(err, core.ErrSine):
		message(opts, msgError, err.Error()+".\n\nNothing was changed.")
		return exitSine
	default:
		text := "Something went wrong:\n\n" + err.Error()
		if hint := writeHint(err); hint != "" {
			text += "\n\n" + hint
		}
		message(opts, msgError, text)
		return exitFailed
	}
}

// readProfileName returns the "Profile=" value from Zen's application.ini. It
// names the profiles folder (it is "zen" for release builds).
func readProfileName(installDir string) string {
	raw, err := os.ReadFile(filepath.Join(installDir, "application.ini"))
	if err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Profile="); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return "zen"
}

// commandName is how the user runs this installer, for hints. install.sh sets
// ZMTS_COMMAND so the hint shows the curl one-liner instead of a temp path.
func commandName() string {
	if c := strings.TrimSpace(os.Getenv("ZMTS_COMMAND")); c != "" {
		return c
	}
	return filepath.Base(os.Args[0])
}

// overrideRoots applies --app-data / --local-data on top of the detected roots.
func overrideRoots(roots []core.ProfileRoot, opts options) []core.ProfileRoot {
	if opts.appData == "" {
		return roots
	}
	local := opts.localData
	if local == "" {
		local = opts.appData
	}
	return []core.ProfileRoot{{AppData: opts.appData, Local: local}}
}

// ---------------------------------------------------------------- arguments

func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, inline, hasInline := strings.Cut(strings.TrimLeft(a, "-/"), "=")
		value := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch strings.ToLower(name) {
		case "s", "silent", "quiet", "y", "yes":
			o.silent = true
		case "uninstall", "u", "remove":
			o.uninstall = true
		case "only-dir":
			o.onlyDir = true
		case "zen-dir":
			o.zenDir, err = value()
		case "profile":
			o.profile, err = value()
		case "app-data":
			o.appData, err = value()
		case "local-data":
			o.localData, err = value()
		case "v", "version":
			return o, errVersion
		case "h", "help", "?":
			return o, errHelp
		default:
			return o, fmt.Errorf("unknown option: %s", a)
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func usage() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s installer %s\n\nUsage: %s [options]\n\nOptions:\n", title, version, commandName())
	for _, row := range [][2]string{
		{silentFlag, "no questions (install, or update if already installed)"},
		{"--uninstall", "remove instead of install"},
		{"--zen-dir DIR", "Zen's program folder, e.g. " + zenDirExample},
		{"--profile DIR", "Zen profile folder"},
		{"--version", "print the version"},
		{"--help", "show this help"},
	} {
		fmt.Fprintf(&b, "  %-15s %s\n", row[0], row[1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------------------------------------------------------------- files

// canWrite returns nil when files can be created in dir without elevation.
func canWrite(dir string) error {
	f, err := os.CreateTemp(dir, ".zmts-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

func fileExistsAt(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
