package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func assets() fstest.MapFS {
	return fstest.MapFS{
		"fx/config.js":                     {Data: []byte("// config v1")},
		"fx/defaults/pref/config-prefs.js": {Data: []byte("pref('x', 1);")},
		"fx/utils/boot.sys.mjs":            {Data: []byte("boot")},
		"fx/utils/chrome.manifest":         {Data: []byte("content userscripts ../JS/")},
		"fx/utils/utils.sys.mjs":           {Data: []byte("utils")},
		ScriptName:                         {Data: []byte("// script v1")},
	}
}

func newTarget(t *testing.T) Target {
	root := t.TempDir()
	tg := Target{
		InstallDir:      filepath.Join(root, "Zen Browser"),
		ProfileDir:      filepath.Join(root, "appdata", "Profiles", "abc.Default"),
		LocalProfileDir: filepath.Join(root, "local", "Profiles", "abc.Default"),
	}
	for _, d := range []string{tg.InstallDir, tg.ProfileDir, tg.LocalProfileDir} {
		mustMkdir(t, d)
	}
	return tg
}

func mustMkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallAndReinstall(t *testing.T) {
	tg := newTarget(t)
	mustWrite(t, filepath.Join(tg.LocalProfileDir, "startupCache", "x.bin"), "old")

	if err := Install(assets(), tg, PartAll, nil); err != nil {
		t.Fatal(err)
	}
	if !Detect(tg).Installed() {
		t.Fatalf("state after install: %+v", Detect(tg))
	}
	if got := read(t, filepath.Join(tg.ProfileDir, "chrome", "JS", ScriptName)); got != "// script v1" {
		t.Fatalf("script = %q", got)
	}
	if _, err := os.Stat(filepath.Join(tg.LocalProfileDir, "startupCache")); !os.IsNotExist(err) {
		t.Fatal("startup cache was not cleared")
	}

	// Update: new script replaces the old one without a backup file.
	a := assets()
	a[ScriptName] = &fstest.MapFile{Data: []byte("// script v2")}
	if err := Install(a, tg, PartAll, nil); err != nil {
		t.Fatal(err)
	}
	js := filepath.Join(tg.ProfileDir, "chrome", "JS")
	if read(t, filepath.Join(js, ScriptName)) != "// script v2" {
		t.Fatal("script not updated")
	}
	if _, err := os.Stat(filepath.Join(js, ScriptName+backupExt)); err == nil {
		t.Fatal("unexpected backup of our own script")
	}
}

func TestKeepsUsersExistingLoaderAndBacksUpOtherConfig(t *testing.T) {
	tg := newTarget(t)
	mustWrite(t, filepath.Join(tg.InstallDir, "config.js"), "// someone else's autoconfig")
	mustWrite(t, filepath.Join(tg.ProfileDir, "chrome", "utils", "boot.sys.mjs"), "user's newer loader")

	if err := Install(assets(), tg, PartAll, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(tg.ProfileDir, "chrome", "utils", "boot.sys.mjs")) != "user's newer loader" {
		t.Fatal("existing loader was overwritten")
	}
	if read(t, filepath.Join(tg.InstallDir, "config.js"+backupExt)) != "// someone else's autoconfig" {
		t.Fatal("foreign config.js was not backed up")
	}
	if read(t, filepath.Join(tg.InstallDir, "config.js")) != "// config v1" {
		t.Fatal("config.js not installed")
	}
}

func TestRefusesSine(t *testing.T) {
	tg := newTarget(t)
	mustWrite(t, filepath.Join(tg.InstallDir, "config.js"),
		`ChromeUtils.importESModule("chrome://userscripts/content/sine.sys.mjs");`)
	if err := Install(assets(), tg, PartAll, nil); !errors.Is(err, ErrSine) {
		t.Fatalf("want ErrSine, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(tg.ProfileDir, "chrome")); err == nil {
		t.Fatal("profile was touched even though install was refused")
	}

	tg2 := newTarget(t)
	mustWrite(t, filepath.Join(tg2.ProfileDir, "chrome", "utils", "chrome.manifest"), "content sine ../sine-mods/")
	if err := Install(assets(), tg2, PartProfile, nil); !errors.Is(err, ErrSine) {
		t.Fatalf("profile half: want ErrSine, got %v", err)
	}
}

func TestPartsAreIndependent(t *testing.T) {
	tg := newTarget(t)
	if err := Install(assets(), tg, PartInstallDir, nil); err != nil {
		t.Fatal(err)
	}
	if s := Detect(tg); !s.LoaderInstalled || s.ScriptInstalled {
		t.Fatalf("after dir half: %+v", s)
	}
	if err := Install(assets(), tg, PartProfile, nil); err != nil {
		t.Fatal(err)
	}
	if !Detect(tg).Installed() {
		t.Fatal("not installed after both halves")
	}
}

func TestUninstallKeepsLoaderForOtherScripts(t *testing.T) {
	tg := newTarget(t)
	if err := Install(assets(), tg, PartAll, nil); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(tg.ProfileDir, "chrome", "JS", "other.uc.js")
	mustWrite(t, other, "// another script")

	if err := Uninstall(tg, PartAll, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tg.ProfileDir, "chrome", "JS", ScriptName)); err == nil {
		t.Fatal("script still there")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("other script was removed")
	}
	if _, err := os.Stat(filepath.Join(tg.ProfileDir, "chrome", "utils", "boot.sys.mjs")); err != nil {
		t.Fatal("loader removed although another script needs it")
	}
	if _, err := os.Stat(filepath.Join(tg.InstallDir, "config.js")); err != nil {
		t.Fatal("config.js removed although another script needs it")
	}
}

func TestUninstallRemovesEverythingWhenAlone(t *testing.T) {
	tg := newTarget(t)
	if err := Install(assets(), tg, PartAll, nil); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(tg, PartAll, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(tg.InstallDir, "config.js"),
		filepath.Join(tg.InstallDir, "defaults", "pref", "config-prefs.js"),
		filepath.Join(tg.ProfileDir, "chrome", "utils"),
		filepath.Join(tg.ProfileDir, "chrome", "JS"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("still exists: %s", p)
		}
	}
	if err := Uninstall(tg, PartAll, nil); err != nil {
		t.Fatalf("second uninstall should be a no-op: %v", err)
	}
}

func TestFindProfile(t *testing.T) {
	root := t.TempDir()
	app, local := filepath.Join(root, "app"), filepath.Join(root, "local")
	for _, p := range []string{"Profiles/aaa.Default (release)", "Profiles/bbb.Default (alpha)", "Profiles/ccc.Other"} {
		mustMkdir(t, filepath.Join(app, filepath.FromSlash(p)))
	}
	old := time.Now().Add(-48 * time.Hour)
	mustWrite(t, filepath.Join(app, "Profiles", "aaa.Default (release)", "prefs.js"), "")
	mustWrite(t, filepath.Join(app, "Profiles", "bbb.Default (alpha)", "prefs.js"), "")
	os.Chtimes(filepath.Join(app, "Profiles", "bbb.Default (alpha)", "prefs.js"), old, old)

	mustWrite(t, filepath.Join(app, "profiles.ini"), "[Install1234]\r\nDefault=Profiles/bbb.Default (alpha)\r\nLocked=1\r\n\r\n"+
		"[Profile2]\r\nName=Other\r\nIsRelative=1\r\nPath=Profiles/ccc.Other\r\n\r\n"+
		"[Profile1]\r\nName=default\r\nIsRelative=1\r\nPath=Profiles/aaa.Default (release)\r\nDefault=1\r\n\r\n"+
		"[Profile0]\r\nName=default-alpha\r\nIsRelative=1\r\nPath=Profiles/bbb.Default (alpha)\r\n")

	p, err := FindProfile(app, local)
	if err != nil {
		t.Fatal(err)
	}
	// aaa has the newest prefs.js, so it wins over the install default bbb.
	if want := filepath.Join(app, "Profiles", "aaa.Default (release)"); p.Dir != want {
		t.Fatalf("Dir = %s, want %s", p.Dir, want)
	}
	if want := filepath.Join(local, "Profiles", "aaa.Default (release)"); p.LocalDir != want {
		t.Fatalf("LocalDir = %s, want %s", p.LocalDir, want)
	}
}

func TestFindProfileSingleAndErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := FindProfile(root, root); err == nil {
		t.Fatal("want an error without profiles.ini")
	}
	mustMkdir(t, filepath.Join(root, "Profiles", "x.Default"))
	mustWrite(t, filepath.Join(root, "profiles.ini"), "[Profile0]\nName=default\nIsRelative=1\nPath=Profiles/x.Default\nDefault=1\n")
	p, err := FindProfile(root, filepath.Join(root, "L"))
	if err != nil || filepath.Base(p.Dir) != "x.Default" {
		t.Fatalf("got %+v, %v", p, err)
	}
	// Absolute path profile
	abs := filepath.Join(root, "elsewhere")
	mustMkdir(t, abs)
	mustWrite(t, filepath.Join(root, "profiles.ini"), "[Profile0]\nName=d\nIsRelative=0\nPath="+filepath.ToSlash(abs)+"\nDefault=1\n")
	p, err = FindProfile(root, filepath.Join(root, "L"))
	if err != nil || p.Dir != abs || p.LocalDir != abs {
		t.Fatalf("absolute: got %+v, %v", p, err)
	}
	// Listed but missing
	mustWrite(t, filepath.Join(root, "profiles.ini"), "[Profile0]\nName=d\nIsRelative=1\nPath=Profiles/gone\nDefault=1\n")
	if _, err := FindProfile(root, root); err == nil {
		t.Fatal("want an error when the profile folder is missing")
	}
}
