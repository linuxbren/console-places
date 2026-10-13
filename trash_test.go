package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// homeModel is a model in a fake home folder (with its own trash).
func homeModel(t *testing.T) (model, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local/share"))
	mkfile(t, filepath.Join(home, "big.bin"), strings.Repeat("x", 100))
	mkfile(t, filepath.Join(home, "small.txt"), "x")
	os.Mkdir(filepath.Join(home, "zdir"), 0o755)
	return testModel(t, home), home
}

func names(es []entry) string {
	var out []string
	for _, e := range es {
		switch e.kind {
		case kindSeparator:
			out = append(out, strings.TrimSpace("--- "+e.name))
		case kindPlace:
			out = append(out, "<"+e.name+">")
		case kindTrashLink:
			out = append(out, "[Trash]")
		default:
			out = append(out, e.name)
		}
	}
	return strings.Join(out, " ")
}

func TestTrashRowPinnedInHome(t *testing.T) {
	m, home := homeModel(t)
	if got := names(m.entries); got != "zdir big.bin small.txt --- [Trash]" {
		t.Fatalf("home: %q", got)
	}
	m.cfg.Sort, m.cfg.SortDesc = "size", true
	m.reload("")
	if got := names(m.entries); !strings.HasSuffix(got, "--- [Trash]") {
		t.Fatalf("sorting moved the Trash row: %q", got)
	}
	m.filter = "tr"
	m.applyView("")
	if got := names(m.entries); got != "--- [Trash]" {
		t.Fatalf("filter matching trash: %q", got)
	}
	m.filter = "big"
	m.applyView("")
	if got := names(m.entries); got != "big.bin" {
		t.Fatalf("filter not matching trash: %q", got)
	}
	// Only in the home folder.
	m = press(t, testModel(t, filepath.Join(home, "zdir")))
	if strings.Contains(names(m.entries), "Trash") {
		t.Fatal("Trash row outside home")
	}
}

func TestCursorNeverStartsOnSeparator(t *testing.T) {
	m, _ := homeModel(t)
	if cur, ok := m.current(); !ok || cur.kind == kindSeparator || m.cursor != 0 {
		t.Fatalf("start: cursor %d on %+v", m.cursor, cur)
	}
	m.cursor = 3 // the separator, however it got there
	m.applyView("")
	if cur, _ := m.current(); cur.kind == kindSeparator {
		t.Fatal("applyView left the cursor on the separator")
	}
}

func TestTrashRowNavigationAndGuards(t *testing.T) {
	m, home := homeModel(t)
	m.reload("small.txt")
	m.move(1) // over the separator, onto Trash
	if cur, _ := m.current(); cur.kind != kindTrashLink {
		t.Fatalf("down from the last file: %+v", cur)
	}
	m.move(-1)
	if m.curName() != "small.txt" {
		t.Fatalf("up from Trash: %q", m.curName())
	}
	m.move(1)
	for _, k := range []string{"d", "r", "c", "space", "i"} {
		m = press(t, m, k)
		if !strings.Contains(m.msg, "not a file") || m.modal != nil {
			t.Fatalf("%s on the Trash row: msg %q modal %v", k, m.msg, m.modal)
		}
	}
	if !exists(filepath.Join(home, "small.txt")) {
		t.Fatal("something happened to a file")
	}
	// With a selection, d trashes the selection even from the Trash row.
	m.selected["small.txt"] = true
	m = press(t, m, "d")
	if strings.Contains(m.msg, "not a file") {
		t.Fatal("selection should still be trashable")
	}
	m.selected = map[string]bool{}
	m = press(t, m, "A")
	if m.selected["Trash"] || len(m.selected) != 3 {
		t.Fatalf("select all: %v", m.selected)
	}
	if exists(trashFilesDir()) {
		t.Fatal("trash folder shouldn't exist yet")
	}
	m.selected = map[string]bool{}
	m = press(t, m, "enter") // opens the (not yet existing) trash
	if m.cwd != trashFilesDir() || !exists(trashFilesDir()) || !m.inTrash() {
		t.Fatalf("enter on Trash: %s", m.cwd)
	}
	m = press(t, m, "left") // back home, onto the Trash row
	if m.cwd != home {
		t.Fatalf("left from the trash: %s", m.cwd)
	}
	if cur, _ := m.current(); cur.kind != kindTrashLink {
		t.Fatalf("not back on the Trash row: %+v", cur)
	}
}

func TestTrashViewDetailsAndRestore(t *testing.T) {
	m, home := homeModel(t)
	docs := filepath.Join(home, "Documents")
	mkfile(t, filepath.Join(docs, "notes.txt"), "v1")
	before := time.Now().Add(-time.Second)
	if res := trashItems([]string{filepath.Join(docs, "notes.txt")}); res.err != nil {
		t.Fatal(res.err)
	}
	m.reload("")
	if !strings.Contains(m.renderList(m.entries, len(m.entries)-1, 0, 60, 20, true), "Trash · 1") {
		t.Fatal("Trash row should count 1 item")
	}
	m = press(t, m, "alt+t")
	if !m.inTrash() || m.curName() != "notes.txt" {
		t.Fatalf("alt+t: %s %q", m.cwd, m.curName())
	}
	tm := m.trashMeta["notes.txt"]
	if tm.origin != filepath.Join(docs, "notes.txt") || tm.deleted.Before(before.Truncate(time.Second)) {
		t.Fatalf("trash info: %+v", tm)
	}
	if row := m.renderList(m.entries, 0, 0, 90, 10, true); !strings.Contains(row, "~/Documents") {
		t.Fatalf("trash row lacks the origin: %q", row)
	}
	if s := m.statusLeft(); !strings.Contains(s, "from ~/Documents/notes.txt") || !strings.Contains(s, "r restores") {
		t.Fatalf("status: %q", s)
	}

	// r in the trash restores (rather than renaming).
	m2, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m2.modal != nil || cmd == nil {
		t.Fatalf("r should restore, not open rename: %+v", m2.modal)
	}
	done, ok := cmd().(opDoneMsg)
	if !ok || done.res.err != nil || done.res.desc != "Restored to ~/Documents/notes.txt" {
		t.Fatalf("restore: %+v", done)
	}
	if b, _ := os.ReadFile(filepath.Join(docs, "notes.txt")); string(b) != "v1" {
		t.Fatal("not back where it came from")
	}
}

func TestRestoreItemsNaming(t *testing.T) {
	_, home := homeModel(t)
	docs := filepath.Join(home, "Documents")
	mkfile(t, filepath.Join(docs, "notes.txt"), "v1")
	trashItems([]string{filepath.Join(docs, "notes.txt")})
	mkfile(t, filepath.Join(docs, "notes.txt"), "v2")
	res := restoreItems([]string{"notes.txt"})
	if res.err != nil || res.desc != "Restored to ~/Documents/notes (2).txt" {
		t.Fatalf("restore beside: %v %q", res.err, res.desc)
	}
	if b, _ := os.ReadFile(filepath.Join(docs, "notes (2).txt")); string(b) != "v1" {
		t.Fatalf("restored content: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(docs, "notes.txt")); string(b) != "v2" {
		t.Fatal("the newer file was touched")
	}
	if exists(filepath.Join(trashDir(), "info", "notes.txt.trashinfo")) {
		t.Fatal("trashinfo left behind")
	}
	// The original folder is gone: restore recreates it.
	mkfile(t, filepath.Join(home, "gone/x.txt"), "x")
	trashItems([]string{filepath.Join(home, "gone")})
	os.RemoveAll(filepath.Join(home, "gone"))
	if res := restoreItems([]string{"gone"}); res.err != nil || !exists(filepath.Join(home, "gone/x.txt")) {
		t.Fatalf("restore into a missing folder: %v", res.err)
	}
}

func TestEmptyTrashConfirmCounts(t *testing.T) {
	m, home := homeModel(t)
	m = press(t, m, "E")
	if m.modal != nil || !strings.Contains(m.msg, "already empty") {
		t.Fatalf("empty trash with nothing in it: %q", m.msg)
	}
	for _, n := range []string{"a", "b", "c"} {
		mkfile(t, filepath.Join(home, n), "")
		trashItems([]string{filepath.Join(home, n)})
	}
	m = press(t, m, "E")
	if m.modal == nil || !strings.Contains(strings.Join(m.modal.lines, " "), "all 3 items") {
		t.Fatalf("confirm: %+v", m.modal)
	}
}

func TestPlacesAlwaysListTrash(t *testing.T) {
	m, _ := homeModel(t)
	found := false
	for _, p := range m.places() {
		if p.name == "Trash" && p.path == trashFilesDir() {
			found = true
		}
	}
	if !found || exists(trashFilesDir()) {
		t.Fatal("Trash should be listed before the folder exists")
	}
}

func TestNewWindowCmd(t *testing.T) {
	have := func(bins ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, b := range bins {
				if b == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	args, ok := newWindowCmd("/d", "/bin/tf", have("xdg-terminal-exec", "uwsm-app"))
	if !ok || strings.Join(args, " ") != "/usr/bin/uwsm-app -- /usr/bin/xdg-terminal-exec --dir=/d /bin/tf /d" {
		t.Errorf("omarchy: %v", args)
	}
	args, _ = newWindowCmd("/d", "/bin/tf", have("xdg-terminal-exec"))
	if strings.Join(args, " ") != "/usr/bin/xdg-terminal-exec --dir=/d /bin/tf /d" {
		t.Errorf("no uwsm: %v", args)
	}
	t.Setenv("TERMINAL", "foot")
	args, _ = newWindowCmd("/d", "/bin/tf", have("foot"))
	if strings.Join(args, " ") != "/usr/bin/foot -e /bin/tf /d" {
		t.Errorf("$TERMINAL: %v", args)
	}
	t.Setenv("TERMINAL", "")
	if _, ok := newWindowCmd("/d", "/bin/tf", have()); ok {
		t.Error("no terminal at all should say so")
	}
}

func TestNewWindowSpawns(t *testing.T) {
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "args")
	// A stand-in xdg-terminal-exec that records how it was called.
	mkfile(t, filepath.Join(bin, "xdg-terminal-exec"), "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > "+out+"\n")
	os.Chmod(filepath.Join(bin, "xdg-terminal-exec"), 0o755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	dir := t.TempDir()
	msg := newWindow(dir)()
	if sm, ok := msg.(statusMsg); !ok || sm.err || !strings.Contains(sm.text, "opened a new window") {
		t.Fatalf("status: %+v", msg)
	}
	var got []byte
	for i := 0; i < 100 && len(got) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		got, _ = os.ReadFile(out)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	exe, _ := os.Executable()
	if len(lines) != 4 || lines[0] != dir || lines[1] != "--dir="+dir || lines[2] != exe || lines[3] != dir {
		t.Fatalf("launched with %q", lines)
	}
}
