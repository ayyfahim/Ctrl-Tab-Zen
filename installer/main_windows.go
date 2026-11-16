// Windows front end: message boxes, UAC elevation and profile locations.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"zenmacinstaller/internal/core"
)

const (
	silentFlag    = "/S"
	zenDirExample = `"C:\path\to\Zen Browser"`
	closeZenHint  = " (including its tray/background windows)"
	updateHint    = ""
)

func notFoundHint() string { return "" }

// writeHint has no extra advice on Windows: elevation fixes write errors.
func writeHint(error) string { return "" }

// reportCanceled shows nothing: the user just clicked Cancel.
func reportCanceled() {}

// progress is silent on Windows: the result is shown in one message box.
func progress(options, string) {}

// profileRoots returns %APPDATA%\<name> and %LOCALAPPDATA%\<name>.
func profileRoots(installDir string, opts options) []core.ProfileRoot {
	name := readProfileName(installDir)
	return overrideRoots([]core.ProfileRoot{{
		AppData: filepath.Join(os.Getenv("APPDATA"), name),
		Local:   filepath.Join(os.Getenv("LOCALAPPDATA"), name),
	}}, opts)
}

// zenInstallAt checks a folder given with --zen-dir.
func zenInstallAt(dir string) (zenInstall, error) {
	dir = filepath.Clean(strings.Trim(strings.TrimSpace(dir), `"`))
	if !fileExistsAt(filepath.Join(dir, zenExe)) {
		return zenInstall{}, fmt.Errorf("%s is not Zen's program folder: it has no %s", dir, zenExe)
	}
	return zenInstall{Dir: dir, Source: "command line"}, nil
}

func isZenRunning(z zenInstall) bool { return isZenRunningFrom(z.Dir) }

// ---------------------------------------------------------------- UI

const (
	mbOKCancel      = 0x01
	mbYesNoCancel   = 0x03
	mbRetryCancel   = 0x05
	mbIconError     = 0x10
	mbIconQuestion  = 0x20
	mbIconWarning   = 0x30
	mbIconInfo      = 0x40
	mbSetForeground = 0x10000

	idOK, idRetry, idYes, idNo = 1, 4, 6, 7
)

func ask(flags uint32, text string) int32 {
	if flags&0xF0 == 0 {
		flags |= mbIconQuestion
	}
	r, _ := windows.MessageBox(0, utf16(text), utf16(title), flags|mbSetForeground)
	return r
}

func confirm(text string) bool { return ask(mbOKCancel, text) == idOK }

func askRetry(text string) bool { return ask(mbRetryCancel|mbIconWarning, text) == idRetry }

func askUpdateOrUninstall(text string) choice {
	switch ask(mbYesNoCancel, text+"\nYes = update it\nNo = uninstall it\nCancel = do nothing") {
	case idYes:
		return choiceUpdate
	case idNo:
		return choiceUninstall
	}
	return choiceCancel
}

func message(opts options, kind msgKind, text string) {
	if opts.silent || opts.onlyDir {
		fmt.Fprintln(os.Stderr, text)
		return
	}
	flags := uint32(mbIconInfo)
	switch kind {
	case msgWarning:
		flags = mbIconWarning
	case msgError:
		flags = mbIconError
	}
	windows.MessageBox(0, utf16(text), utf16(title), flags|mbSetForeground)
}

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
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
func runElevatedHelper(_ options, _ string, args []string) int {
	exe, err := os.Executable()
	if err != nil {
		return exitFailed
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}

	info := shellExecuteInfo{
		mask:       seeMaskNoCloseProcess,
		verb:       utf16("runas"),
		file:       utf16(exe),
		parameters: utf16(strings.Join(quoted, " ")),
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
