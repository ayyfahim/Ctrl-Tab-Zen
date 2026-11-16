package main

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

func fakeZen(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "zen"), "\x7fELF", 0o755)
	write(t, filepath.Join(dir, "application.ini"), "[App]\nProfile=zen\n", 0o644)
}

func TestFindZenInstallsLinux(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")

	// Zen's own install script location.
	tarball := filepath.Join(home, ".tarball-installations", "zen")
	fakeZen(t, tarball)
	// An AUR-style package with a wrapper script on PATH.
	aur := filepath.Join(root, "opt", "zen-browser-bin")
	fakeZen(t, aur)
	write(t, filepath.Join(bin, "zen-browser"), "#!/bin/sh\nexec "+aur+"/zen \"$@\"\n", 0o755)
	// A tarball extracted somewhere odd, found through its desktop entry.
	odd := filepath.Join(root, "stuff", "Zen Browser")
	fakeZen(t, odd)
	write(t, filepath.Join(home, ".local", "share", "applications", "zen.desktop"),
		"[Desktop Entry]\nName=Zen\nExec=\""+odd+"/zen\" %u\n", 0o644)
	// A symlink on PATH to the tarball (same install, must not repeat).
	if err := os.Symlink(filepath.Join(tarball, "zen"), filepath.Join(bin, "zen")); err != nil {
		t.Fatal(err)
	}
	// Not Zen: a folder with only a "zen" file.
	write(t, filepath.Join(home, "zen", "zen"), "", 0o755)

	got := findZenInstallsIn(home, bin, root)
	var dirs []string
	for _, z := range got {
		dirs = append(dirs, z.Dir)
	}
	want := []string{tarball, aur, odd}
	if len(dirs) != len(want) {
		t.Fatalf("got %q, want %q", dirs, want)
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Fatalf("got %q, want %q", dirs, want)
		}
	}
}

func TestZenInstallAtLinux(t *testing.T) {
	root := t.TempDir()
	zen := filepath.Join(root, "zen")
	fakeZen(t, zen)
	for _, arg := range []string{zen, zen + "/", filepath.Join(zen, "zen")} {
		z, err := zenInstallAt(arg)
		if err != nil || z.Dir != zen {
			t.Errorf("zenInstallAt(%q) = %+v, %v", arg, z, err)
		}
	}
	if _, err := zenInstallAt(root); err == nil {
		t.Error("want an error for a folder without Zen")
	}
	if _, err := zenInstallAt("/tmp/.mount_zenABC"); err == nil {
		t.Error("want an error for a mounted AppImage")
	}
}

func TestProfileRootsLinux(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	zen := filepath.Join(root, "zen")
	fakeZen(t, zen)
	got := profileRoots(zen, options{})
	if len(got) != 2 ||
		got[0].AppData != filepath.Join(root, ".zen") ||
		got[1].AppData != filepath.Join(root, ".config", "zen") ||
		got[0].Local != filepath.Join(root, "cache", "zen") {
		t.Fatalf("got %+v", got)
	}
}
