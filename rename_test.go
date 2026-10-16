package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvVarFallsBackToOldName(t *testing.T) {
	t.Setenv("CONSOLE_PLACES_IMAGES", "")
	t.Setenv("TIDEFILES_IMAGES", "sixel")
	if got := envVar("IMAGES"); got != "sixel" {
		t.Fatalf("old variable not read: %q", got)
	}
	t.Setenv("CONSOLE_PLACES_IMAGES", "kitty")
	if got := envVar("IMAGES"); got != "kitty" {
		t.Fatalf("new variable should win: %q", got)
	}
}

// An old copy's updater runs the download as .tidefiles-update-… --version
// and only accepts "tidefiles vX".
func TestVersionNameForOldUpdaters(t *testing.T) {
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })
	for arg0, want := range map[string]string{
		"/home/u/.local/bin/console-places":              "console-places ",
		"/home/u/.local/bin/tidefiles":                   "tidefiles ",
		"/home/u/.local/bin/.tidefiles-update-123456789": "tidefiles ",
	} {
		os.Args[0] = arg0
		if got := versionString(); got[:len(want)] != want {
			t.Errorf("%s: %q", arg0, got)
		}
	}
}

func TestMigrateConfigDir(t *testing.T) {
	cfg := t.TempDir()
	mkfile(t, filepath.Join(cfg, "tidefiles", "config.json"), `{"bookmarks":["/x"]}`)
	migrateConfigDir(cfg)
	if b, err := os.ReadFile(filepath.Join(cfg, "console-places", "config.json")); err != nil || string(b) != `{"bookmarks":["/x"]}` {
		t.Fatalf("not moved: %q %v", b, err)
	}
	// Once the new folder exists, an old one is left alone.
	mkfile(t, filepath.Join(cfg, "tidefiles", "config.json"), `{}`)
	migrateConfigDir(cfg)
	if b, _ := os.ReadFile(filepath.Join(cfg, "console-places", "config.json")); string(b) != `{"bookmarks":["/x"]}` {
		t.Fatalf("overwritten: %q", b)
	}
}

func TestMigrateBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tidefiles")
	mkfile(t, exe, "binary")
	got := migrateBinary(exe)
	if got != filepath.Join(dir, "console-places") {
		t.Fatalf("renamed to %q", got)
	}
	if b, _ := os.ReadFile(got); string(b) != "binary" {
		t.Fatal("binary lost")
	}
	if l, err := os.Readlink(exe); err != nil || l != "console-places" {
		t.Fatalf("tidefiles link: %q %v", l, err)
	}
	if migrateBinary(exe) != "" || migrateBinary(got) != "" {
		t.Fatal("migrated twice")
	}
}

func TestFirewallRecognisesOldLabel(t *testing.T) {
	f := filepath.Join(t.TempDir(), "user.rules")
	old := hex.EncodeToString([]byte("tidefiles discovery"))
	cur := hex.EncodeToString([]byte("console-places discovery"))
	mkfile(t, f, "### tuple ### allow udp any 0.0.0.0/0 3702 192.168.1.0/24 in comment="+old+"\n"+
		"### tuple ### allow udp any 0.0.0.0/0 137 10.0.0.0/24 in comment="+cur+"\n"+
		"### tuple ### allow udp any 0.0.0.0/0 137 10.0.0.0/24 in comment=6f74686572\n")
	orig := ufwRules
	ufwRules = f
	t.Cleanup(func() { ufwRules = orig })
	if rs := ourRules(); len(rs) != 2 {
		t.Fatalf("rules: %v", rs)
	}
}
