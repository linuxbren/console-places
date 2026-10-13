package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// placesModel is a model in ~/work (two files) with ~/proj bookmarked.
func placesModel(t *testing.T, mode string) (model, string) {
	t.Helper()
	m, home := homeModel(t)
	mkfile(t, filepath.Join(home, "work", "a.txt"), "a")
	mkfile(t, filepath.Join(home, "work", "b.txt"), "b")
	mkfile(t, filepath.Join(home, "proj", "main.go"), "package main")
	m.cfg.Bookmarks = []string{filepath.Join(home, "proj")}
	m.cfg.PlacesInList = mode
	m.chdir(filepath.Join(home, "work"), "")
	return m, home
}

func TestPlacesInListOffByDefault(t *testing.T) {
	if defaultConfig().PlacesInList != placesListOff {
		t.Fatal("places in the list must be opt-in")
	}
	m, home := placesModel(t, placesListOff)
	if got := names(m.entries); got != "a.txt b.txt" {
		t.Fatalf("off, outside home: %q", got)
	}
	m.chdir(home, "")
	if got := names(m.entries); !strings.HasSuffix(got, "--- [Trash]") || strings.Contains(got, "<") {
		t.Fatalf("off, home keeps only its Trash row: %q", got)
	}
}

func TestPlacesInListBookmarks(t *testing.T) {
	m, home := placesModel(t, placesListBookmarks)
	if got := names(m.entries); got != "a.txt b.txt --- Bookmarks <proj> --- [Trash]" {
		t.Fatalf("rows: %q", got)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "─ Bookmarks ─") || !strings.Contains(v, "1/2") {
		t.Fatalf("view (labelled rule, count of files only):\n%s", v)
	}

	// Down past the last file skips the rules; G reaches the Trash.
	m = press(t, m, "down", "down")
	if e, _ := m.current(); e.kind != kindPlace || e.name != "proj" {
		t.Fatalf("down from b.txt: %+v", e)
	}
	if !strings.Contains(m.statusLeft(), "enter opens") || !strings.Contains(m.statusLeft(), "B removes it") {
		t.Fatalf("status: %q", m.statusLeft())
	}
	m.refreshPreview()
	if m.pv.title != "proj" || !strings.Contains(strings.Join(m.pv.lines, "\n"), "main.go") {
		t.Fatalf("preview: %+v", m.pv)
	}
	m = press(t, m, "G")
	if e, _ := m.current(); e.kind != kindTrashLink {
		t.Fatalf("G: %+v", e)
	}
	m = press(t, m, "k")
	if e, _ := m.current(); e.name != "proj" {
		t.Fatalf("up from Trash skips the rule: %+v", e)
	}

	// File actions refuse the row; nothing is touched.
	m = press(t, m, "d")
	if !strings.Contains(m.msg, "shortcut") {
		t.Fatalf("d on a place row: %q", m.msg)
	}
	if _, err := os.Stat(filepath.Join(home, "proj")); err != nil {
		t.Fatal("the bookmarked folder must stay")
	}

	m = press(t, m, "enter")
	if m.cwd != filepath.Join(home, "proj") {
		t.Fatalf("enter: %s", m.cwd)
	}
	if got := names(m.entries); got != "main.go --- Bookmarks <proj> --- [Trash]" {
		t.Fatalf("the rows follow into every folder: %q", got)
	}

	// B on the row removes that bookmark; B elsewhere adds one, at once.
	m = press(t, m, "G", "k", "B")
	if got := names(m.entries); got != "main.go --- [Trash]" || len(m.cfg.Bookmarks) != 0 {
		t.Fatalf("after B on the row: %q %v", got, m.cfg.Bookmarks)
	}
	m = press(t, m, "g", "B")
	if got := names(m.entries); got != "main.go --- Bookmarks <proj> --- [Trash]" {
		t.Fatalf("after B on a file: %q", got)
	}

	// A filter keeps the rows that match; the trash view has none.
	m.filter = "pro"
	m.applyView("")
	if got := names(m.entries); got != "(no matches) --- Bookmarks <proj>" {
		t.Fatalf("filtered: %q", got)
	}
	m.filter = ""
	m.openTrash()
	if got := names(m.entries); strings.Contains(got, "---") {
		t.Fatalf("in the trash: %q", got)
	}
}

func TestPlacesInListAll(t *testing.T) {
	log := fakeUdisks(t)
	m, _ := placesModel(t, placesListAll)
	mnt := t.TempDir()
	share := t.TempDir()
	mounted := false
	blockDevices = func() ([]blockDev, error) {
		var mps []string
		if mounted {
			mps = []string{mnt}
		}
		return []blockDev{
			{Name: "sdb", Path: "/dev/sdb", Type: "disk", Removable: true},
			{Name: "sdb1", Path: "/dev/sdb1", Parent: "sdb", Type: "part", FSType: "ext4", Label: "Backup", Size: 1 << 30, Mountpoints: mps},
		}, nil
	}
	gvfsMounts = func() []netMount { return []netMount{{name: "photos on nas", uri: "smb://nas/photos/", path: share}} }
	t.Cleanup(func() {
		blockDevices = func() ([]blockDev, error) { return nil, nil }
		gvfsMounts = func() []netMount { return nil }
	})
	m.cfg.Servers = []string{"smb://nas/photos", "sftp://me@box/srv"}

	// Drives and network wait for the background scan.
	if got := names(m.entries); got != "a.txt b.txt --- Bookmarks <proj> --- [Trash]" {
		t.Fatalf("before the scan: %q", got)
	}
	m.cursor = 0
	m.refreshPreview()
	pvKey := m.pvKey
	m, _ = drive1(t, m, m.rescanPlaces(true))
	want := "a.txt b.txt --- Bookmarks <proj> --- Drives <Backup> --- Network <photos on nas> <srv on me@box> --- [Trash]"
	if got := names(m.entries); got != want {
		t.Fatalf("after the scan:\n got %q\nwant %q", got, want)
	}
	if m.cursor != 0 || m.pvKey != pvKey {
		t.Fatal("a scan must leave the cursor and a file's preview alone")
	}
	if m.rescanPlaces(false) != nil {
		t.Fatal("rescans wait placesRescan")
	}
	v := ansi.Strip(m.View())
	for _, s := range []string{"─ Drives ─", "Backup", "not mounted", "─ Network ─", "not connected"} {
		if !strings.Contains(v, s) {
			t.Fatalf("view lacks %q:\n%s", s, v)
		}
	}

	// enter on the unmounted drive mounts it through udisks, then opens it.
	for i, e := range m.entries {
		if e.name == "Backup" {
			m.cursor = i
		}
	}
	if !strings.Contains(m.statusLeft(), "enter mounts") {
		t.Fatalf("status: %q", m.statusLeft())
	}
	m.refreshPreview()
	if !strings.Contains(strings.Join(m.pv.lines, "\n"), "enter mount") {
		t.Fatalf("preview: %+v", m.pv.lines)
	}
	m, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("enter on the drive: %q", m.msg)
	}
	mounted = true
	m, _ = drive1(t, m, cmd)
	if m.cwd != mnt {
		t.Fatalf("after mounting: %s (%q)", m.cwd, m.msg)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "mount -b /dev/sdb1") {
		t.Fatalf("udisksctl saw: %s", b)
	}
	// The mount starts a fresh scan at once: the row now says the size.
	if !m.scanning {
		t.Fatal("no rescan after mounting")
	}
	m, _ = drive1(t, m, scanPlacesCmd())
	v = ansi.Strip(m.View())
	if strings.Contains(v, "not mounted") || !strings.Contains(v, "1.0 GiB") || !strings.Contains(v, "(empty)") {
		t.Fatalf("after the rescan:\n%s", v)
	}
}

func TestPlacesInListSetting(t *testing.T) {
	m, _ := placesModel(t, placesListOff)
	m = press(t, m, "S")
	list := settingsList()
	for i, s := range list {
		if s.label == "Places in the file list" {
			m.modal.sel = i
		}
	}
	m, _ = m.handleSettingsKey(m.modal, "right")
	if m.cfg.PlacesInList != placesListBookmarks || loadConfig().PlacesInList != placesListBookmarks {
		t.Fatalf("setting: %q, saved %q", m.cfg.PlacesInList, loadConfig().PlacesInList)
	}
	if got := names(m.entries); !strings.Contains(got, "<proj>") {
		t.Fatalf("applies at once: %q", got)
	}
	m, cmd := m.handleSettingsKey(m.modal, "right")
	if m.cfg.PlacesInList != placesListAll || cmd == nil {
		t.Fatal("all starts a scan")
	}
}
