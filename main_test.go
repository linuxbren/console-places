package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the config, the trash (XDG_DATA_HOME) and the update
// check at throwaway places, so no test can read or change the user's real
// ones or reach GitHub.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tidefiles-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("TIDEFILES_CONFIG", filepath.Join(dir, "config.json"))
	os.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	os.Setenv("TIDEFILES_UPDATE_URL", "http://127.0.0.1:1/") // never ask GitHub
	// No real drives, mounts or gvfs locations: tests that need some set them.
	blockDevices = func() ([]blockDev, error) { return nil, nil }
	gvfsMounts = func() []netMount { return nil }
	gioLocalPath = func(string) string { return "" }
	mountsText = func() string { return "" }
	saveSecret = func(netSecret, string) error { return errors.New("no keyring in tests") }
	browseMDNS = func(context.Context) []netHost { return nil } // nor the network
	browseWSD = func(context.Context) []netHost { return nil }
	browseNetBIOS = func(context.Context) []netHost { return nil }
	browseTailscale = func(context.Context) []netHost { return nil }
	netbiosName = func(context.Context, string) string { return "" }
	lookupAddr = func(string) string { return "" }
	remoteSpace = func(place) (int64, int64, bool) { return 0, 0, false }
	ufwConf = filepath.Join(dir, "no-ufw.conf") // nor the firewall or package manager
	localLAN = func() string { return "" }
	packageManager = func() string { return "" }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
