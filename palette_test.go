package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func paletteTitles(m model) []string {
	var out []string
	for _, i := range m.modal.pal.rows {
		out = append(out, m.modal.pal.all[i].title)
	}
	return out
}

func typeInto(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			k = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		m, _ = m.handleModalKey(k)
	}
	return m
}

func TestPaletteGroupsAndFilter(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "a.txt"), "")
	m := testModel(t, dir)
	m = press(t, m, "?")
	if m.modal == nil || m.modal.kind != mHelp {
		t.Fatal("? should open the palette")
	}
	p := m.modal.pal
	var heads []string
	for i := 0; i < len(p.rows); i++ {
		if h, ok := p.head[i]; ok {
			heads = append(heads, h)
		}
	}
	if got := strings.Join(heads, " "); got != "Navigate Files Search Archives Tabs View Settings App" {
		t.Fatalf("groups: %q", got) // Archives: "Compress…" applies to the selected file
	}
	body := strings.Join(m.paletteBody(m.modal, 60), "\n")
	if !strings.Contains(body, "Move to trash") || !strings.Contains(body, "d  delete") {
		t.Fatalf("rows should show the action and its key:\n%s", body)
	}

	m = typeInto(t, m, "trash")
	if got := paletteTitles(m); len(got) < 2 || got[0] != "Trash" || got[1] != "Move to trash" {
		t.Fatalf("trash: %v", got)
	}
	for _, title := range paletteTitles(m) {
		if title == "Empty trash" {
			t.Fatal("Empty trash offered with an empty trash")
		}
	}
	m.modal.in = newInput("")
	m = typeInto(t, m, "sort size")
	if got := paletteTitles(m); len(got) == 0 || !strings.HasPrefix(got[0], "Sort by size") {
		t.Fatalf("sort size: %v", got)
	}
	m.modal.in = newInput("")
	m = typeInto(t, m, "grep") // a synonym
	if got := paletteTitles(m); len(got) == 0 || got[0] != "Search inside files" {
		t.Fatalf("synonym: %v", got)
	}
	m.modal.in = newInput("")
	m = typeInto(t, m, "zzqx")
	if len(m.modal.pal.rows) != 0 || !strings.Contains(strings.Join(m.paletteBody(m.modal, 60), "\n"), `No action matches "zzqx"`) {
		t.Fatalf("no match: %v", paletteTitles(m))
	}
}

func TestPaletteOnlyValidActions(t *testing.T) {
	empty := t.TempDir()
	m := testModel(t, empty)
	m.openPalette()
	titles := strings.Join(paletteTitles(m), "|")
	for _, hidden := range []string{"Rename", "Move to trash", "Close tab", "Restore from trash", "Extract here", "Edit in $EDITOR"} {
		if strings.Contains(titles, hidden) {
			t.Errorf("%q shouldn't be offered in an empty folder with one tab", hidden)
		}
	}
	for _, shown := range []string{"New folder", "Paste", "Sort by name", "Image previews: auto", "Show hidden files"} {
		if !strings.Contains(titles, shown) {
			t.Errorf("%q should be offered", shown)
		}
	}

	dir := t.TempDir()
	writeTestZip(t, filepath.Join(dir, "a.zip"), map[string]string{"x": "x"})
	m = testModel(t, dir)
	m.reload("a.zip")
	m.openPalette()
	titles = strings.Join(paletteTitles(m), "|")
	if !strings.Contains(titles, "Extract here") || !strings.Contains(titles, "Browse archive") {
		t.Errorf("archive actions missing: %s", titles)
	}
	m.job = &job{prog: &progress{verb: "Copying"}}
	m.openPalette()
	if titles := strings.Join(paletteTitles(m), "|"); strings.Contains(titles, "Paste") || strings.Contains(titles, "Move to trash") {
		t.Errorf("file changes offered during a job: %s", titles)
	}
}

func TestPaletteRunsActionsAndRemembers(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "b.txt"), "bb")
	mkfile(t, filepath.Join(dir, "a.txt"), "a")
	m := testModel(t, dir)
	m.openPalette()
	m = typeInto(t, m, "show hidden")
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.cfg.ShowHidden || m.modal != nil {
		t.Fatalf("enter should run 'Show hidden files' and close: hidden=%v", m.cfg.ShowHidden)
	}
	m.openPalette()
	m = typeInto(t, m, "sort by size")
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.Sort != "size" || m.entries[0].name != "a.txt" {
		t.Fatalf("sort by size: %s, first %s", m.cfg.Sort, m.entries[0].name)
	}
	m.openPalette() // most recent first
	p := m.modal.pal
	if p.head[0] != "Recent" || !strings.HasPrefix(p.all[p.rows[0]].title, "Sort by size") || p.all[p.rows[1]].title != "Hide hidden files" {
		t.Fatalf("recent: %v", paletteTitles(m))
	}
	saved, _ := os.ReadFile(os.Getenv("TIDEFILES_CONFIG"))
	if !strings.Contains(string(saved), `"recent"`) || !strings.Contains(string(saved), "sort:size") {
		t.Fatalf("recent not saved: %s", saved)
	}
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.modal != nil {
		t.Fatal("? with nothing typed closes the palette")
	}
	// An action that opens its own modal: the palette hands over to it.
	m.openPalette()
	m = typeInto(t, m, "new folder")
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modal == nil || m.modal.purpose != "newfolder" {
		t.Fatalf("new folder prompt: %+v", m.modal)
	}
}

func TestPaletteImageMode(t *testing.T) {
	m := testModel(t, t.TempDir())
	t.Setenv("TIDEFILES_IMAGES", "") // let the setting decide
	m.setImageMode("sixel")
	if m.proto != protoSixel || m.cfg.ImageMode != "sixel" {
		t.Fatalf("sixel: %v %q", m.proto, m.cfg.ImageMode)
	}
	m.setImageMode("auto")
	if m.proto != m.autoProto || m.cfg.ImageMode != "" {
		t.Fatalf("auto: %v %q", m.proto, m.cfg.ImageMode)
	}
}

func TestKeysFor(t *testing.T) {
	was := onOmarchy
	defer func() { onOmarchy = was }()
	onOmarchy = false
	if got := keysFor(actCopy); got != "c" {
		t.Errorf("copy off Omarchy: %q", got)
	}
	if strings.Contains(statusHints(), "SUPER") {
		t.Error("hint strip mentions SUPER off Omarchy")
	}
	onOmarchy = true
	if !strings.Contains(statusHints(), "SUPER+C/X/V") {
		t.Error("hint strip lost SUPER on Omarchy")
	}
	for act, want := range map[action]string{actUp: "↑  k", actDown: "↓  j", actCopy: "SUPER+C  c", actTab2: "2", actNextTab: "tab", actPrevTab: "shift+tab", actPageDown: "pgdn  ctrl+f"} {
		if got := keysFor(act); got != want {
			t.Errorf("action %d: %q, want %q", act, got, want)
		}
	}
}

// Recent actions are saved by id, so ids must be unique and must not be
// action numbers, which shift whenever an action is added or removed.
func TestPaletteIDsAreUniqueAndStable(t *testing.T) {
	m := testModel(t, t.TempDir())
	seen := map[string]bool{}
	for _, cm := range m.commands() {
		if seen[cm.id] {
			t.Errorf("duplicate palette id %q", cm.id)
		}
		seen[cm.id] = true
		if strings.HasPrefix(cm.id, "act:") || strings.HasPrefix(cm.id, "key::") {
			t.Errorf("palette id %q (%s) is not stable", cm.id, cm.title)
		}
	}
}
