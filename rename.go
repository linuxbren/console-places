package main

// console-places was called tidefiles until v0.11. Everything a tidefiles
// install left behind keeps working: its settings folder is moved over, the
// TIDEFILES_* variables are still read, its firewall rules are still
// recognised, and a release binary that updated itself under the old name
// renames itself (leaving a tidefiles link). Copies of tidefiles update
// through a "tidefiles" build published beside each release, which says
// "tidefiles vX" to their version check (see versionString).

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	appName = "console-places"
	oldName = "tidefiles"
)

// envVar reads CONSOLE_PLACES_<name>, else the old TIDEFILES_<name>.
func envVar(name string) string {
	if v := os.Getenv("CONSOLE_PLACES_" + name); v != "" {
		return v
	}
	return os.Getenv("TIDEFILES_" + name)
}

// invokedAsOld says whether this binary was started under tidefiles' name:
// as tidefiles, or by an old copy's updater checking the download
// (.tidefiles-update-…), which expects "tidefiles vX".
func invokedAsOld() bool {
	b := filepath.Base(os.Args[0])
	return strings.HasPrefix(b, oldName) || strings.HasPrefix(b, "."+oldName+"-update-")
}

// migrateConfigDir moves tidefiles' settings folder to console-places' once,
// when only the old one exists.
func migrateConfigDir(configDir string) {
	oldDir, newDir := filepath.Join(configDir, oldName), filepath.Join(configDir, appName)
	if _, err := os.Stat(newDir); err == nil {
		return
	}
	if st, err := os.Stat(oldDir); err == nil && st.IsDir() {
		_ = os.Rename(oldDir, newDir)
	}
}

// migrateBinary renames a self-updating release binary still called
// tidefiles to console-places, leaving tidefiles as a link so both commands
// work. It returns the new path ("" when nothing changed).
func migrateBinary(exe string) string {
	if filepath.Base(exe) != oldName {
		return ""
	}
	dir := filepath.Dir(exe)
	target := filepath.Join(dir, appName)
	if _, err := os.Lstat(target); err == nil {
		return ""
	}
	if os.Rename(exe, target) != nil {
		return ""
	}
	if os.Symlink(appName, exe) != nil {
		_ = os.Rename(target, exe) // put it back rather than lose the old command
		return ""
	}
	return target
}
