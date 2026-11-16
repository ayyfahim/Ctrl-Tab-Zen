package main

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const zenExe = "zen.exe"

// findZenInstalls returns every distinct Zen program folder it can find,
// most reliable source first. A folder only counts if it contains zen.exe.
func findZenInstalls() []zenInstall {
	var out []zenInstall
	seen := map[string]bool{}
	add := func(dir, source string) {
		dir = filepath.Clean(strings.Trim(strings.TrimSpace(dir), `"`))
		if dir == "" || dir == "." {
			return
		}
		key := strings.ToLower(dir)
		if seen[key] {
			return
		}
		if !fileExistsAt(filepath.Join(dir, zenExe)) {
			return
		}
		seen[key] = true
		out = append(out, zenInstall{Dir: dir, Source: source})
	}

	// 1. A running Zen is the best proof of which install the user uses.
	for _, exe := range runningZenPaths() {
		add(filepath.Dir(exe), "running Zen")
	}

	// 2. Windows' uninstall list (written by Zen's own installer).
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\Uninstall`, registry.ENUMERATE_SUB_KEYS|view)
			if err != nil {
				continue
			}
			names, _ := k.ReadSubKeyNames(-1)
			k.Close()
			for _, name := range names {
				sub, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+name, registry.QUERY_VALUE|view)
				if err != nil {
					continue
				}
				display, _, _ := sub.GetStringValue("DisplayName")
				loc, _, _ := sub.GetStringValue("InstallLocation")
				sub.Close()
				if strings.HasPrefix(strings.ToLower(display), "zen") && loc != "" {
					add(loc, "Windows uninstall list")
				}
			}
		}
	}

	// 3. "App Paths" registration for zen.exe.
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\`+zenExe, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		path, _, _ := k.GetStringValue("")
		k.Close()
		if path != "" {
			add(filepath.Dir(strings.Trim(path, `"`)), "App Paths")
		}
	}

	// 4. Mozilla-style registry keys: Software\Mozilla\Zen Browser\<ver>\Main.
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, brand := range []string{"Zen Browser", "Zen Twilight", "Zen"} {
			base := `Software\Mozilla\` + brand
			k, err := registry.OpenKey(root, base, registry.ENUMERATE_SUB_KEYS)
			if err != nil {
				continue
			}
			names, _ := k.ReadSubKeyNames(-1)
			k.Close()
			for _, n := range names {
				sub, err := registry.OpenKey(root, base+`\`+n+`\Main`, registry.QUERY_VALUE)
				if err != nil {
					continue
				}
				dir, _, _ := sub.GetStringValue("Install Directory")
				sub.Close()
				add(dir, "Zen registry keys")
			}
		}
	}

	// 5. Default folders.
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if base := os.Getenv(env); base != "" {
			add(filepath.Join(base, "Zen Browser"), "Program Files")
			add(filepath.Join(base, "Zen Twilight"), "Program Files")
		}
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		add(filepath.Join(local, "Programs", "Zen Browser"), "Local AppData")
		add(filepath.Join(local, "Zen Browser"), "Local AppData")
	}
	return out
}

// runningZenPaths lists the full paths of running zen.exe processes.
func runningZenPaths() []string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	var paths []string
	seen := map[string]bool{}
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if !strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), zenExe) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pe.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
			p := windows.UTF16ToString(buf[:n])
			if !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				paths = append(paths, p)
			}
		}
		windows.CloseHandle(h)
	}
	return paths
}

// isZenRunningFrom reports whether zen.exe from dir is running.
func isZenRunningFrom(dir string) bool {
	want := strings.ToLower(filepath.Join(dir, zenExe))
	for _, p := range runningZenPaths() {
		if strings.ToLower(filepath.Clean(p)) == want {
			return true
		}
	}
	return false
}
