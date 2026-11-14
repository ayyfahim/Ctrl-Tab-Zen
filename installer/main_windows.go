// Zen Mac Tab Switcher installer.
//
// Finds Zen and its profile, copies the fx-autoconfig loader and the switcher
// script, and clears the startup cache. Run it again to update or uninstall.
package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

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
	appData   string // override for %APPDATA%\<zen profile root>, mainly for tests
	localData string
}

func main() { os.Exit(run()) }

func run() int {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		message(opts, mbError, err.Error())
		return exitFailed
	}
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		message(opts, mbError, err.Error())
		return exitFailed
	}

	// ---- find Zen
	var installs []zenInstall
	if opts.zenDir != "" {
		installs = []zenInstall{{Dir: opts.zenDir, Source: "command line"}}
	} else {
		installs = findZenInstalls()
	}
	if len(installs) == 0 {
		message(opts, mbError, "Zen Browser was not found on this computer.\n\n"+
			"Install Zen first, start it once, then run this installer again.\n\n"+
			"If Zen is in an unusual place, run:\n  "+filepath.Base(os.Args[0])+` --zen-dir "C:\path\to\Zen Browser"`)
		return exitFailed
	}
	zen := installs[0]

	// ---- find the profile
	target := core.Target{InstallDir: zen.Dir}
	if opts.profile != "" {
		target.ProfileDir = opts.profile
	} else {
		appData, local := profileRoots(zen.Dir, opts)
		prof, err := core.FindProfile(appData, local)
		if err != nil {
			message(opts, mbError, "Zen was found, but not its profile.\n\n"+err.Error()+
				"\n\nStart Zen once so it creates a profile, close it, and run this installer again.")
			return exitFailed
		}
		target.ProfileDir, target.LocalProfileDir = prof.Dir, prof.LocalDir
	}

	if err := core.CheckSine(target); err != nil {
		message(opts, mbError, err.Error()+".\n\nNothing was changed.")
		return exitSine
	}

	state := core.Detect(target)
	uninstall := opts.uninstall

	// ---- ask (never in helper or silent mode)
	if !opts.silent && !opts.onlyDir {
		info := fmt.Sprintf("Zen:      %s\nProfile:  %s\n", zen.Dir, target.ProfileDir)
		if len(installs) > 1 {
			info += "\n(Other Zen installs were found; using the first. Use --zen-dir to pick another.)\n"
		}
		if state.Installed() && !uninstall {
			switch ask(mbYesNoCancel, title+" is already installed.\n\n"+info+
				"\nYes = update it\nNo = uninstall it\nCancel = do nothing") {
			case idYes:
			case idNo:
				uninstall = true
			default:
				return exitCanceled
			}
		} else if uninstall {
			if ask(mbOKCancel, "Uninstall "+title+"?\n\n"+info) != idOK {
				return exitCanceled
			}
		} else {
			if ask(mbOKCancel, "Install "+title+"?\n\n"+info+
				"\nThis adds a macOS-style Ctrl+Tab switcher to Zen.") != idOK {
				return exitCanceled
			}
		}
	}

	// ---- Zen must be closed so its startup cache can be cleared
	if !opts.onlyDir {
		for isZenRunningFrom(zen.Dir) {
			if opts.silent {
				message(opts, mbError, "Zen is running. Close it and try again.")
				return exitFailed
			}
			if ask(mbRetryCancel|mbWarning, "Zen is running.\n\nPlease close Zen completely (including its tray/background windows), then click Retry.") != idRetry {
				return exitCanceled
			}
		}
	}

	var logLines []string
	log := func(format string, args ...any) { logLines = append(logLines, fmt.Sprintf(format, args...)) }

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
	if canWrite(zen.Dir) {
		if err := do(core.PartInstallDir); err != nil {
			return finish(opts, err, "")
		}
	} else {
		code := runElevatedHelper(zen.Dir, target.ProfileDir, uninstall)
		switch code {
		case exitOK:
		case exitCanceled:
			message(opts, mbInfo, "Administrator permission was declined.\n\nNothing was changed.")
			return exitCanceled
		case exitSine:
			message(opts, mbError, core.ErrSine.Error()+".")
			return exitSine
		default:
			message(opts, mbError, "Could not write to Zen's program folder:\n"+zen.Dir)
			return exitFailed
		}
	}

	// ---- profile half
	if err := do(core.PartProfile); err != nil {
		return finish(opts, err, "")
	}

	done := "Installed.\n\nStart Zen, hold Ctrl and press Tab."
	if uninstall {
		done = "Uninstalled.\n\nZen's normal Ctrl+Tab is back."
	}
	return finish(opts, nil, done+"\n\n"+"Profile: "+target.ProfileDir)
}

// finish reports the outcome and returns the process exit code.
func finish(opts options, err error, success string) int {
	switch {
	case err == nil:
		if success != "" {
			message(opts, mbInfo, success)
		}
		return exitOK
	case errors.Is(err, core.ErrSine):
		message(opts, mbError, err.Error()+".\n\nNothing was changed.")
		return exitSine
	default:
		message(opts, mbError, "Something went wrong:\n\n"+err.Error())
		return exitFailed
	}
}

// profileRoots returns %APPDATA%\<name> and %LOCALAPPDATA%\<name>. <name> is the
// "Profile=" value from Zen's application.ini (it is "zen" for release builds).
func profileRoots(installDir string, opts options) (appData, local string) {
	name := "zen"
	if raw, err := os.ReadFile(filepath.Join(installDir, "application.ini")); err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Profile="); ok && strings.TrimSpace(v) != "" {
				name = strings.TrimSpace(v)
				break
			}
		}
	}
	appData = opts.appData
	if appData == "" {
		appData = filepath.Join(os.Getenv("APPDATA"), name)
	}
	local = opts.localData
	if local == "" {
		local = filepath.Join(os.Getenv("LOCALAPPDATA"), name)
	}
	return
}

// ---------------------------------------------------------------- arguments

func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch strings.ToLower(strings.TrimLeft(a, "-/")) {
		case "s", "silent", "quiet":
			o.silent = true
		case "uninstall", "u":
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
		case "h", "help", "?":
			return o, fmt.Errorf("Options:\n  /S             silent (no questions)\n  /uninstall     remove instead of install\n  --zen-dir DIR  Zen's program folder\n  --profile DIR  Zen profile folder")
		default:
			return o, fmt.Errorf("unknown option: %s", a)
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// ---------------------------------------------------------------- UI

const (
	mbOKCancel      = 0x01
	mbYesNoCancel   = 0x03
	mbRetryCancel   = 0x05
	mbError         = 0x10 | mbSetForeground
	mbWarning       = 0x30
	mbInfo          = 0x40 | mbSetForeground
	mbSetForeground = 0x10000

	idOK, idCancel, idRetry, idYes, idNo = 1, 2, 4, 6, 7
)

func ask(flags uint32, text string) int32 {
	r, _ := windows.MessageBox(0, utf16(text), utf16(title), flags|mbSetForeground|0x20*boolToU32(flags&0xF0 == 0))
	return r
}

func message(opts options, flags uint32, text string) {
	if opts.silent || opts.onlyDir {
		fmt.Fprintln(os.Stderr, text)
		return
	}
	windows.MessageBox(0, utf16(text), utf16(title), flags)
}

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func boolToU32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- elevation

type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       windows.HWND
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    windows.Handle
	idList     uintptr
	class      *uint16
	hkeyClass  windows.Handle
	hotKey     uint32
	icon       windows.Handle
	process    windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	errorCancelled        = syscall.Errno(1223)
)

// runElevatedHelper re-runs this exe with administrator rights to do only the
// install-dir half, and returns its exit code.
func runElevatedHelper(zenDir, profileDir string, uninstall bool) int {
	exe, err := os.Executable()
	if err != nil {
		return exitFailed
	}
	args := []string{"--only-dir", "--silent", "--zen-dir", zenDir}
	if profileDir != "" {
		args = append(args, "--profile", profileDir)
	}
	if uninstall {
		args = append(args, "--uninstall")
	}
	for i, a := range args {
		args[i] = windows.EscapeArg(a)
	}

	info := shellExecuteInfo{
		mask:       seeMaskNoCloseProcess,
		verb:       utf16("runas"),
		file:       utf16(exe),
		parameters: utf16(strings.Join(args, " ")),
		show:       windows.SW_HIDE,
	}
	info.size = uint32(unsafe.Sizeof(info))

	shell32 := windows.NewLazySystemDLL("shell32.dll")
	r, _, callErr := shell32.NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(callErr, errorCancelled) {
			return exitCanceled
		}
		return exitFailed
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return exitFailed
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return exitFailed
	}
	return int(code)
}
