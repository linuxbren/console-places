package main

import (
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "v9.9.9"
	if got := versionString(); got != "console-places v9.9.9" {
		t.Fatalf("ldflags version: %q", got)
	}
	version = ""
	if got := versionString(); !strings.HasPrefix(got, "console-places ") || got == "console-places " {
		t.Fatalf("build-info version: %q", got)
	}
}
