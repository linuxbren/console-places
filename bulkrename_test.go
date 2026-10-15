package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func listDir(t *testing.T, dir string) string {
	t.Helper()
	des, _ := os.ReadDir(dir)
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestApplyTemplate(t *testing.T) {
	for _, c := range []struct {
		tmpl, base string
		i          int
		want       string
	}{
		{"{name}", "IMG_1", 0, "IMG_1"},
		{"Holiday {n}", "IMG_1", 1, "Holiday 2"},
		{"{name}-{n:3}", "a", 9, "a-010"},
		{"{n:2}_{n}", "a", 0, "01_1"},
	} {
		if got := applyTemplate(c.tmpl, c.base, c.i); got != c.want {
			t.Errorf("%q on %q #%d: %q, want %q", c.tmpl, c.base, c.i, got, c.want)
		}
	}
}

// bulkModel selects every file in a folder of three photos, a folder and a
// note, and presses r.
func bulkModel(t *testing.T) (model, string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"b.jpg", "a.jpg", "c.tar.gz", "notes.txt"} {
		mkfile(t, filepath.Join(dir, n), n)
	}
	os.Mkdir(filepath.Join(dir, "my.dir"), 0o755)
	m := testModel(t, dir)
	for _, n := range []string{"a.jpg", "b.jpg", "c.tar.gz", "my.dir"} {
		m.selected[n] = true
	}
	m = press(t, m, "r")
	if m.modal == nil || m.modal.kind != mBulk {
		t.Fatalf("r with several selected: %+v", m.modal)
	}
	return m, dir
}

func bulkType(m model, s string) model {
	for _, r := range s {
		m, _ = m.handleBulkKey(m.modal, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func bulkKey(m model, k tea.KeyType) (model, tea.Cmd) {
	return m.handleBulkKey(m.modal, tea.KeyMsg{Type: k})
}

func TestBulkRenameTemplateAndUndo(t *testing.T) {
	m, dir := bulkModel(t)
	b := m.modal.bulk
	if got := strings.Join(b.names, " "); got != "my.dir a.jpg b.jpg c.tar.gz" {
		t.Fatalf("listing order: %q", got)
	}
	m.modal.bulk.tmpl = newInput("")
	m = bulkType(m, "pic {n:2}")
	var got []string
	for _, r := range b.rows() {
		got = append(got, r.to+"|"+r.problem)
	}
	if want := "pic 01||pic 02.jpg||pic 03.jpg||pic 04.tar.gz|"; strings.Join(got, "|") != want {
		t.Fatalf("rows: %q", strings.Join(got, "|"))
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "a.jpg  →  pic 02.jpg") {
		t.Fatalf("preview:\n%s", v)
	}
	m, cmd := bulkKey(m, tea.KeyEnter)
	m, _ = drive1(t, m, cmd)
	if got := listDir(t, dir); got != "notes.txt pic 01 pic 02.jpg pic 03.jpg pic 04.tar.gz" || m.msg != "Renamed 4 items" {
		t.Fatalf("after: %q (%q)", got, m.msg)
	}
	m, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m, _ = drive1(t, m, cmd)
	if got := listDir(t, dir); got != "a.jpg b.jpg c.tar.gz my.dir notes.txt" {
		t.Fatalf("after undo: %q (%q)", got, m.msg)
	}
}

func TestBulkRenameFindReplaceAndProblems(t *testing.T) {
	m, dir := bulkModel(t)
	m, _ = bulkKey(m, tea.KeyTab) // find
	m = bulkType(m, "a")
	m, _ = bulkKey(m, tea.KeyTab) // replace with
	m = bulkType(m, "notes")
	rows := m.modal.bulk.rows()
	if rows[1].to != "notes.jpg" || rows[0].to != "my.dir" || rows[0].problem != "" {
		t.Fatalf("find & replace: %+v", rows)
	}

	// Template without {n}: everything gets one name.
	m, _ = bulkKey(m, tea.KeyTab)
	m.modal.bulk.tmpl = newInput("notes")
	rows = m.modal.bulk.rows()
	if rows[1].problem != "same name as another" {
		t.Fatalf("duplicates: %+v", rows)
	}
	// One name only: notes.txt is taken by a file that isn't renamed.
	m.modal.bulk.names = []string{"x.txt"}
	m.modal.bulk.dirs["x.txt"] = false
	if rows = m.modal.bulk.rows(); rows[0].problem != "already exists" {
		t.Fatalf("taken: %+v", rows)
	}
	m.modal.bulk.tmpl = newInput("")
	if rows = m.modal.bulk.rows(); rows[0].problem != "no name" {
		t.Fatalf("empty: %+v", rows)
	}
	m.modal.bulk.tmpl = newInput("a/b")
	if rows = m.modal.bulk.rows(); rows[0].problem != "contains /" {
		t.Fatalf("slash: %+v", rows)
	}
	m, cmd := bulkKey(m, tea.KeyEnter)
	if cmd != nil || m.modal == nil || !m.msgErr {
		t.Fatal("enter must refuse while there are problems")
	}
	if got := listDir(t, dir); got != "a.jpg b.jpg c.tar.gz my.dir notes.txt" {
		t.Fatalf("nothing may change: %q", got)
	}
}

func TestRenameAllSwapsAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "a"), "A")
	mkfile(t, filepath.Join(dir, "b"), "B")
	mkfile(t, filepath.Join(dir, "c"), "C")
	if err := renameAll(dir, [][2]string{{"a", "b"}, {"b", "a"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a")); string(b) != "B" {
		t.Fatalf("swap: a holds %q", b)
	}
	// b → x is fine, a → c would overwrite c: nothing changes.
	err := renameAll(dir, [][2]string{{"b", "x"}, {"a", "c"}})
	if err == nil || !strings.Contains(err.Error(), "c already exists (nothing was renamed)") {
		t.Fatalf("overwrite: %v", err)
	}
	if got := listDir(t, dir); got != "a b c" {
		t.Fatalf("after the rollback: %q", got)
	}
	for n, want := range map[string]string{"a": "B", "b": "A", "c": "C"} {
		if b, _ := os.ReadFile(filepath.Join(dir, n)); string(b) != want {
			t.Fatalf("%s holds %q", n, b)
		}
	}
	// A case-only change.
	if err := renameAll(dir, [][2]string{{"c", "C"}}); err != nil || listDir(t, dir) != "C a b" {
		t.Fatalf("case: %v %q", err, listDir(t, dir))
	}
}
