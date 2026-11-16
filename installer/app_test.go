package main

import (
	"errors"
	"testing"
)

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"/S", "--uninstall", "--zen-dir", "/a b", "--profile=/p"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.silent || !o.uninstall || o.zenDir != "/a b" || o.profile != "/p" {
		t.Fatalf("got %+v", o)
	}
	if o, _ := parseArgs([]string{"-y"}); !o.silent {
		t.Fatal("-y should mean silent")
	}
	if _, err := parseArgs([]string{"--help"}); !errors.Is(err, errHelp) {
		t.Fatalf("--help: %v", err)
	}
	if _, err := parseArgs([]string{"--version"}); !errors.Is(err, errVersion) {
		t.Fatalf("--version: %v", err)
	}
	if _, err := parseArgs([]string{"--zen-dir"}); err == nil {
		t.Fatal("want an error for a missing value")
	}
	if _, err := parseArgs([]string{"--bogus"}); err == nil {
		t.Fatal("want an error for an unknown option")
	}
}
