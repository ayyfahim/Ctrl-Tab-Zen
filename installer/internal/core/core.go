// Package core holds the installer's file logic. It has no Windows-specific
// code, so it can be unit-tested anywhere.
package core

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ScriptName = "zen-mac-tab-switcher.uc.js"
	backupExt  = ".zen-mac-backup"
)

// ErrSine is returned when Sine's bootloader owns the autoconfig slot.
var ErrSine = errors.New("Sine's bootloader is installed in this Zen. Installing fx-autoconfig over it would break Sine")

// Target is where things go. InstallDir is Zen's program folder (it contains
// zen.exe). ProfileDir is the profile folder. LocalProfileDir holds the
// profile's caches and may be empty.
type Target struct {
	InstallDir      string
	ProfileDir      string
	LocalProfileDir string
}

// Part selects which half of the work to do. The split exists because only the
// install-dir half may need administrator rights.
type Part int

const (
	PartAll Part = iota
	PartInstallDir
	PartProfile
)

func (p Part) dir() bool     { return p == PartAll || p == PartInstallDir }
func (p Part) profile() bool { return p == PartAll || p == PartProfile }

type Logger func(format string, args ...any)

func (l Logger) printf(format string, args ...any) {
	if l != nil {
		l(format, args...)
	}
}

// ---------------------------------------------------------------- state

type State struct {
	LoaderInstalled bool // config.js + config-prefs.js in the install dir
	ScriptInstalled bool // our script in <profile>/chrome/JS
}

func (s State) Installed() bool { return s.LoaderInstalled && s.ScriptInstalled }

func Detect(t Target) State {
	return State{
		LoaderInstalled: fileExists(filepath.Join(t.InstallDir, "config.js")) &&
			fileExists(filepath.Join(t.InstallDir, "defaults", "pref", "config-prefs.js")),
		ScriptInstalled: t.ProfileDir != "" &&
			fileExists(filepath.Join(t.ProfileDir, "chrome", "JS", ScriptName)),
	}
}

// ---------------------------------------------------------------- install

// Install copies the loader and the script. assets must contain fx/ (the
// fx-autoconfig files) and zen-mac-tab-switcher.uc.js.
func Install(assets fs.FS, t Target, part Part, log Logger) error {
	if part.dir() {
		if err := installLoaderDir(assets, t, log); err != nil {
			return err
		}
	}
	if part.profile() {
		if err := installProfile(assets, t, log); err != nil {
			return err
		}
	}
	return nil
}

// CheckSine returns ErrSine if Sine's bootloader is set up in this Zen. It only
// reads files, so it works without administrator rights.
func CheckSine(t Target) error {
	if existing, err := os.ReadFile(filepath.Join(t.InstallDir, "config.js")); err == nil &&
		bytes.Contains(bytes.ToLower(existing), []byte("sine.sys.mjs")) {
		return ErrSine
	}
	if t.ProfileDir != "" {
		if m, err := os.ReadFile(filepath.Join(t.ProfileDir, "chrome", "utils", "chrome.manifest")); err == nil &&
			bytes.Contains(bytes.ToLower(m), []byte("sine")) {
			return ErrSine
		}
	}
	return nil
}

func installLoaderDir(assets fs.FS, t Target, log Logger) error {
	if t.InstallDir == "" {
		return errors.New("no Zen install folder")
	}
	if err := CheckSine(t); err != nil {
		return err
	}
	cfg := filepath.Join(t.InstallDir, "config.js")
	prefs := filepath.Join(t.InstallDir, "defaults", "pref", "config-prefs.js")
	if err := copyAsset(assets, "fx/config.js", cfg, log); err != nil {
		return err
	}
	return copyAsset(assets, "fx/defaults/pref/config-prefs.js", prefs, log)
}

func installProfile(assets fs.FS, t Target, log Logger) error {
	if t.ProfileDir == "" {
		return errors.New("no Zen profile folder")
	}
	chrome := filepath.Join(t.ProfileDir, "chrome")
	utils := filepath.Join(chrome, "utils")

	if m, err := os.ReadFile(filepath.Join(utils, "chrome.manifest")); err == nil &&
		bytes.Contains(bytes.ToLower(m), []byte("sine")) {
		return ErrSine
	}

	// Keep a loader the user already has (it may be newer); otherwise install ours.
	if fileExists(filepath.Join(utils, "boot.sys.mjs")) {
		log.printf("Keeping the fx-autoconfig loader already in the profile")
	} else {
		entries, err := fs.ReadDir(assets, "fx/utils")
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := copyAsset(assets, "fx/utils/"+e.Name(), filepath.Join(utils, e.Name()), log); err != nil {
				return err
			}
		}
	}
	if err := copyAsset(assets, ScriptName, filepath.Join(chrome, "JS", ScriptName), log); err != nil {
		return err
	}
	return ClearStartupCache(t, log)
}

// ClearStartupCache removes Firefox's startup cache. Without this, Zen keeps
// using its cached startup state and does not notice the new loader.
func ClearStartupCache(t Target, log Logger) error {
	seen := map[string]bool{}
	for _, base := range []string{t.LocalProfileDir, t.ProfileDir} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		dir := filepath.Join(base, "startupCache")
		if _, err := os.Stat(dir); err == nil {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("could not clear the startup cache (is Zen still running?): %w", err)
			}
			log.printf("Cleared %s", dir)
		}
	}
	return nil
}

// ---------------------------------------------------------------- uninstall

// Uninstall removes the script. The loader is only removed when nothing else
// uses it, so other userChrome scripts keep working.
func Uninstall(t Target, part Part, log Logger) error {
	if part.profile() && t.ProfileDir != "" {
		chrome := filepath.Join(t.ProfileDir, "chrome")
		if err := removeFile(filepath.Join(chrome, "JS", ScriptName), log); err != nil {
			return err
		}
		if !loaderStillUsed(chrome) {
			if err := removeUtils(filepath.Join(chrome, "utils"), log); err != nil {
				return err
			}
			_ = os.Remove(filepath.Join(chrome, "JS")) // only succeeds when empty
		}
		if err := ClearStartupCache(t, log); err != nil {
			return err
		}
	}
	if part.dir() && t.InstallDir != "" {
		// Leave the loader in place if some other profile still has scripts.
		if !anyProfileUsesLoader(t) {
			for _, p := range []string{
				filepath.Join(t.InstallDir, "config.js"),
				filepath.Join(t.InstallDir, "defaults", "pref", "config-prefs.js"),
			} {
				if err := removeFile(p, log); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func loaderStillUsed(chrome string) bool {
	for _, sub := range []string{"JS", "CSS"} {
		entries, _ := os.ReadDir(filepath.Join(chrome, sub))
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if n == ScriptName {
				continue // ours doesn't count as "someone else needs the loader"
			}
			if strings.HasSuffix(n, ".uc.js") || strings.HasSuffix(n, ".uc.mjs") ||
				strings.HasSuffix(n, ".sys.mjs") || strings.HasSuffix(n, ".uc.css") {
				return true
			}
		}
	}
	return false
}

func anyProfileUsesLoader(t Target) bool {
	// The profile half already ran for the default profile. Other profiles under
	// the same profiles root might still use the loader.
	if t.ProfileDir == "" {
		return true // can't tell, so keep the loader
	}
	root := filepath.Dir(t.ProfileDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if loaderStillUsed(filepath.Join(root, e.Name(), "chrome")) {
			return true
		}
	}
	return false
}

func removeUtils(dir string, log Logger) error {
	for _, n := range []string{"boot.sys.mjs", "fs.sys.mjs", "module_loader.mjs", "uc_api.sys.mjs", "utils.sys.mjs", "chrome.manifest"} {
		if err := removeFile(filepath.Join(dir, n), log); err != nil {
			return err
		}
	}
	_ = os.Remove(dir)
	return nil
}

// ---------------------------------------------------------------- profile detection

type iniSection struct {
	Name string
	Keys map[string]string
}

func parseINI(text string) []iniSection {
	var out []iniSection
	var cur *iniSection
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == ';' || line[0] == '#':
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			out = append(out, iniSection{Name: line[1 : len(line)-1], Keys: map[string]string{}})
			cur = &out[len(out)-1]
		case cur != nil:
			if k, v, ok := strings.Cut(line, "="); ok {
				cur.Keys[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

type Profile struct {
	Dir      string // absolute
	LocalDir string
}

// FindProfile reads <appData>/profiles.ini (appData is e.g. %APPDATA%\zen) and
// picks the profile Zen is most likely to use. localAppData is the matching
// %LOCALAPPDATA%\zen folder.
func FindProfile(appData, localAppData string) (Profile, error) {
	raw, err := os.ReadFile(filepath.Join(appData, "profiles.ini"))
	if err != nil {
		return Profile{}, fmt.Errorf("no profiles.ini in %s: open Zen once first (%w)", appData, err)
	}
	sections := parseINI(string(raw))
	byPath := map[string]iniSection{}
	for _, s := range sections {
		if strings.HasPrefix(s.Name, "Profile") {
			byPath[norm(s.Keys["Path"])] = s
		}
	}

	resolve := func(path string, relative bool) Profile {
		if relative {
			rel := filepath.FromSlash(path)
			return Profile{Dir: filepath.Join(appData, rel), LocalDir: filepath.Join(localAppData, rel)}
		}
		p := filepath.FromSlash(path)
		return Profile{Dir: p, LocalDir: p}
	}

	// Candidates: each [Install...] default, then the profile marked Default=1.
	var cands []Profile
	for _, s := range sections {
		if strings.HasPrefix(s.Name, "Install") && s.Keys["Default"] != "" {
			p := s.Keys["Default"]
			if prof, ok := byPath[norm(p)]; ok {
				cands = append(cands, resolve(prof.Keys["Path"], prof.Keys["IsRelative"] != "0"))
			} else {
				cands = append(cands, resolve(p, true))
			}
		}
	}
	for _, s := range sections {
		if strings.HasPrefix(s.Name, "Profile") && s.Keys["Default"] == "1" {
			cands = append(cands, resolve(s.Keys["Path"], s.Keys["IsRelative"] != "0"))
		}
	}
	for _, s := range sections {
		if strings.HasPrefix(s.Name, "Profile") && s.Keys["Path"] != "" {
			cands = append(cands, resolve(s.Keys["Path"], s.Keys["IsRelative"] != "0"))
		}
	}

	var existing []Profile
	seen := map[string]bool{}
	for _, c := range cands {
		key := strings.ToLower(c.Dir)
		if seen[key] || !dirExists(c.Dir) {
			continue
		}
		seen[key] = true
		existing = append(existing, c)
	}
	if len(existing) == 0 {
		return Profile{}, fmt.Errorf("profiles.ini in %s lists no profile folder that exists", appData)
	}
	// With several installs (for example Zen and Zen Twilight) use the profile
	// that was used most recently.
	sort.SliceStable(existing, func(i, j int) bool {
		return lastUsed(existing[i].Dir).After(lastUsed(existing[j].Dir))
	})
	return existing[0], nil
}

func lastUsed(profileDir string) time.Time {
	var newest time.Time
	for _, n := range []string{"prefs.js", "sessionstore.jsonlz4", "places.sqlite", "zen-sessions.jsonlz4"} {
		if st, err := os.Stat(filepath.Join(profileDir, n)); err == nil && st.ModTime().After(newest) {
			newest = st.ModTime()
		}
	}
	return newest
}

func norm(p string) string { return strings.ToLower(strings.ReplaceAll(p, "\\", "/")) }

// ---------------------------------------------------------------- helpers

func copyAsset(assets fs.FS, name, dst string, log Logger) error {
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(dst); err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		// Back up anything of the user's we are about to replace (once).
		if !isOurs(name) {
			if err := os.WriteFile(dst+backupExt, old, 0o644); err != nil {
				return err
			}
			log.printf("Backed up %s", dst)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	log.printf("Wrote %s", dst)
	return nil
}

// isOurs reports assets we ship (so replacing an older copy needs no backup).
func isOurs(name string) bool {
	return name == ScriptName || strings.HasPrefix(name, "fx/utils/")
}

func removeFile(path string, log Logger) error {
	err := os.Remove(path)
	if err == nil {
		log.printf("Removed %s", path)
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
