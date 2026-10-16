package main

// The trash: a pinned "Trash" row at the bottom of the home folder, a jump
// key, and a trash view that shows where each item came from and when it
// was deleted, with restore, delete forever and empty trash.

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// entryKind marks list rows that aren't files.
type entryKind int

const (
	kindFile      entryKind = iota
	kindSeparator           // the thin line above the Trash row
	kindTrashLink           // the pinned "Trash" row in the home folder
	kindPlace               // a bookmark, drive or network row below the files (listplaces.go)
	kindNote                // "(empty)" over the places rows of an empty folder
)

const trashIcon = "" // nf-fa-trash (Nerd Fonts, which Omarchy ships)

func (e entry) virtual() bool { return e.kind != kindFile }

// standable: the cursor can rest on it (not a separator).
func (e entry) standable() bool { return e.kind != kindSeparator && e.kind != kindNote }

// trashMeta is what a .trashinfo file says about a trashed item.
type trashMeta struct {
	origin  string
	deleted time.Time
}

func readTrashInfo(name string) (trashMeta, error) {
	f, err := os.Open(filepath.Join(trashDir(), "info", name+".trashinfo"))
	if err != nil {
		return trashMeta{}, err
	}
	defer f.Close()
	var tm trashMeta
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		switch {
		case !ok:
		case k == "Path":
			tm.origin, _ = url.PathUnescape(v)
		case k == "DeletionDate":
			tm.deleted, _ = time.ParseInLocation("2006-01-02T15:04:05", v, time.Local)
		}
	}
	if tm.origin == "" {
		return tm, errors.New("no Path in trashinfo")
	}
	return tm, nil
}

// trashCount is how many items are in the trash (0 if it doesn't exist yet).
func trashCount() int {
	des, _ := os.ReadDir(trashFilesDir())
	return len(des)
}

// ensureTrash creates the freedesktop trash folders if they're missing, so
// the trash can be opened before anything was ever deleted.
func ensureTrash() error {
	for _, d := range []string{trashFilesDir(), filepath.Join(trashDir(), "info")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// showsTrashLink: the pinned Trash row belongs to the home folder only.
func (m model) showsTrashLink() bool {
	home, err := os.UserHomeDir()
	return err == nil && m.arc == nil && m.cwd == home
}

// trashRows are the separator and Trash row appended to the home listing,
// unless a filter is active that "trash" doesn't match; with places in the
// list, every folder's places rows.
func (m model) trashRows() []entry {
	if m.listsPlaces() {
		return m.placeRows()
	}
	if !m.showsTrashLink() || (m.filter != "" && !strings.Contains("trash", strings.ToLower(m.filter))) {
		return nil
	}
	return []entry{{kind: kindSeparator}, {name: "Trash", isDir: true, kind: kindTrashLink}}
}

func (m *model) openTrash() {
	if err := ensureTrash(); err != nil {
		m.setMsg("can't open the trash: "+errText(err), true)
		return
	}
	m.chdir(trashFilesDir(), "")
}

// leaveTrash goes back to the home folder, onto the Trash row.
func (m *model) leaveTrash() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	m.chdir(home, "")
	for i, e := range m.entries {
		if e.kind == kindTrashLink {
			m.cursor = i
			m.fixOffset()
		}
	}
}

// trashSuffix is the right-hand column of a trash-view row: where the item
// came from and when it was deleted.
func (m model) trashSuffix(name string, width int) string {
	tm, ok := m.trashMeta[name]
	if !ok {
		return ""
	}
	when := tm.deleted.Format("2006-01-02 15:04")
	from := tildePath(filepath.Dir(tm.origin))
	if room := width - ansi.StringWidth(when) - 3; ansi.StringWidth(from) > room {
		if room < 6 {
			return when
		}
		from = "…" + ansi.TruncateLeft(from, ansi.StringWidth(from)-room+1, "")
	}
	return from + " · " + when
}

func (m *model) loadTrashMeta() {
	m.trashMeta = map[string]trashMeta{}
	for _, e := range m.all {
		if tm, err := readTrashInfo(e.name); err == nil {
			m.trashMeta[e.name] = tm
		}
	}
}

// restoreTrashedTo moves a trashed item back to its original path, or to
// "name (2)" there if that's taken, and returns where it went.
func restoreTrashedTo(name string) (string, error) {
	tm, err := readTrashInfo(name)
	if err != nil {
		return "", fmt.Errorf("no restore info for %s", name)
	}
	dir := filepath.Dir(tm.origin)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, freeName(dir, filepath.Base(tm.origin)))
	if err := movePath(filepath.Join(trashFilesDir(), name), dst); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(trashDir(), "info", name+".trashinfo"))
	return dst, nil
}

// newWindowCmd is the command that opens console-places in dir in a new terminal
// window: the way Omarchy launches terminals (uwsm-app + xdg-terminal-exec)
// when available, else $TERMINAL -e. ok is false when there's no terminal.
func newWindowCmd(dir, exe string, look func(string) (string, error)) (args []string, ok bool) {
	if xte, err := look("xdg-terminal-exec"); err == nil {
		args = []string{xte, "--dir=" + dir, exe, dir}
		if uwsm, err := look("uwsm-app"); err == nil {
			args = append([]string{uwsm, "--"}, args...)
		}
		return args, true
	}
	if t := os.Getenv("TERMINAL"); t != "" {
		if bin, err := look(t); err == nil {
			return []string{bin, "-e", exe, dir}, true
		}
	}
	return nil, false
}

func newWindow(dir string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return statusMsg{"can't find the console-places binary: " + err.Error(), true} }
	}
	args, ok := newWindowCmd(dir, exe, exec.LookPath)
	if !ok {
		return func() tea.Msg {
			return statusMsg{"no terminal found for a new window (install xdg-terminal-exec or set $TERMINAL)", true}
		}
	}
	return func() tea.Msg {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		c.SysProcAttr = spawnAttr()
		if err := c.Start(); err != nil {
			return statusMsg{"new window: " + err.Error(), true}
		}
		go func() { _ = c.Wait() }()
		return statusMsg{"opened a new window in " + tildePath(dir), false}
	}
}
