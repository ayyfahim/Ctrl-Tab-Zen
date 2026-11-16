package main

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeZenApp(t *testing.T, app string) {
	t.Helper()
	for p, data := range map[string]string{
		"Contents/MacOS/zen":                 "\xcf\xfa\xed\xfe",
		"Contents/Resources/application.ini": "[App]\nProfile=zen\n",
	} {
		full := filepath.Join(app, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindZenInstallsDarwin(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "Users", "me")
	sys := filepath.Join(root, "Applications", "Zen.app")
	user := filepath.Join(home, "Applications", "Zen Twilight.app")
	fakeZenApp(t, sys)
	fakeZenApp(t, user)
	if err := os.MkdirAll(filepath.Join(root, "Applications", "Safari.app", "Contents", "Resources"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := findZenInstallsIn(home, root, false)
	if len(got) != 2 || got[0].Display != sys || got[1].Display != user {
		t.Fatalf("got %+v", got)
	}
	if got[0].Dir != filepath.Join(sys, "Contents", "Resources") {
		t.Fatalf("Dir = %s", got[0].Dir)
	}
}

func TestZenInstallAtDarwin(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Zen.app")
	fakeZenApp(t, app)
	for _, arg := range []string{app, app + "/", filepath.Join(app, "Contents", "Resources"), filepath.Join(app, "Contents", "MacOS", "zen")} {
		z, err := zenInstallAt(arg)
		if err != nil || z.Display != app || z.Dir != filepath.Join(app, "Contents", "Resources") {
			t.Errorf("zenInstallAt(%q) = %+v, %v", arg, z, err)
		}
	}
	if _, err := zenInstallAt(filepath.Dir(app)); err == nil {
		t.Error("want an error outside a bundle")
	}
}
