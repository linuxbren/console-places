package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testModel(t *testing.T, dir string) model {
	t.Helper()
	t.Setenv("CONSOLE_PLACES_IMAGES", "blocks") // no terminal probing in tests
	cfg := defaultConfig()
	cfg.Theme = "tokyo-night"
	m := newModel(dir, cfg, "", newGfxOut(os.Stdout))
	m.width, m.height = 120, 40
	return m
}

func press(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "ctrl+t":
			msg = tea.KeyMsg{Type: tea.KeyCtrlT}
		case "ctrl+w":
			msg = tea.KeyMsg{Type: tea.KeyCtrlW}
		case "alt+t":
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}, Alt: true}
		case "alt+left":
			msg = tea.KeyMsg{Type: tea.KeyLeft, Alt: true}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m, _ = m.handleKey(msg)
	}
	return m
}

func TestTabsKeepTheirOwnState(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a/x.txt", "a/y.txt", "b/z.txt"} {
		mkfile(t, filepath.Join(root, p), "")
	}
	m := testModel(t, root)
	if m.topOffset() != 0 {
		t.Fatal("no strip with one tab")
	}
	_, rows1 := m.geometry()

	m = press(t, m, "enter")  // into a/
	m = press(t, m, "space")  // select x.txt
	m = press(t, m, "ctrl+t") // tab 2, same folder, fresh selection
	if len(m.tabs) != 2 || m.tab != 1 || m.cwd != filepath.Join(root, "a") || len(m.selected) != 0 {
		t.Fatalf("new tab: %d tabs, tab %d, %s, selected %v", len(m.tabs), m.tab, m.cwd, m.selected)
	}
	if _, rows2 := m.geometry(); rows2 != rows1-1 || m.topOffset() != 1 {
		t.Fatalf("strip should take one row: %d → %d", rows1, rows2)
	}
	m = press(t, m, "left", "down", "enter") // tab 2 → root → b/
	if m.cwd != filepath.Join(root, "b") {
		t.Fatalf("tab 2 should be in b/: %s", m.cwd)
	}

	m = press(t, m, "1") // back to tab 1: a/, x.txt still selected
	if m.cwd != filepath.Join(root, "a") || !m.selected["x.txt"] {
		t.Fatalf("tab 1 lost its state: %s %v", m.cwd, m.selected)
	}
	m = press(t, m, "alt+left") // tab 1's own history: root
	if m.cwd != root {
		t.Fatalf("tab 1 history: %s", m.cwd)
	}
	m = press(t, m, "tab") // next → tab 2, still b/
	if m.tab != 1 || m.cwd != filepath.Join(root, "b") {
		t.Fatalf("tab key: tab %d %s", m.tab, m.cwd)
	}
	m = press(t, m, "tab") // wraps to tab 1
	if m.tab != 0 {
		t.Fatalf("tab should wrap: %d", m.tab)
	}
	m = press(t, m, "shift+tab") // wraps back to tab 2
	if m.tab != 1 {
		t.Fatalf("shift+tab: %d", m.tab)
	}
	if dirs := m.tabDirs(); strings.Join(dirs, "|") != root+"|"+filepath.Join(root, "b") {
		t.Fatalf("saved dirs: %v", dirs)
	}

	m = press(t, m, "ctrl+w")
	if len(m.tabs) != 1 || m.cwd != root || m.topOffset() != 0 {
		t.Fatalf("close: %d tabs, %s", len(m.tabs), m.cwd)
	}
	m = press(t, m, "ctrl+w")
	if len(m.tabs) != 1 || !strings.Contains(m.msg, "last tab") {
		t.Fatalf("the last tab stays: %d %q", len(m.tabs), m.msg)
	}
}

func TestRestoreTabs(t *testing.T) {
	root := t.TempDir()
	a, b, gone := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "gone")
	os.Mkdir(a, 0o755)
	os.Mkdir(b, 0o755)

	m := testModel(t, b) // started in b, which was one of the saved tabs
	m.restoreTabs([]string{a, gone, b}, 0, true)
	if len(m.tabs) != 2 || m.tab != 1 || m.cwd != b {
		t.Fatalf("restore with a match: %d tabs, active %d, %s", len(m.tabs), m.tab, m.cwd)
	}
	m = press(t, m, "1")
	if m.cwd != a {
		t.Fatalf("restored tab 1: %s", m.cwd)
	}

	m = testModel(t, root) // started in a new folder: added at the end, active
	m.restoreTabs([]string{a, b}, 0, true)
	if len(m.tabs) != 3 || m.tab != 2 || m.cwd != root {
		t.Fatalf("restore without a match: %d tabs, active %d, %s", len(m.tabs), m.tab, m.cwd)
	}

	// No folder given: back on the tab that was active, no extra tab.
	m = testModel(t, root)
	m.restoreTabs([]string{a, b}, 1, false)
	if len(m.tabs) != 2 || m.tab != 1 || m.cwd != b {
		t.Fatalf("resume: %d tabs, active %d, %s", len(m.tabs), m.tab, m.cwd)
	}
	// The active tab's folder is gone: the one before it.
	m = testModel(t, root)
	m.restoreTabs([]string{a, gone, b}, 1, false)
	if len(m.tabs) != 2 || m.tab != 0 || m.cwd != a {
		t.Fatalf("resume, active gone: %d tabs, active %d, %s", len(m.tabs), m.tab, m.cwd)
	}
	// Every folder gone: just the starting one.
	m = testModel(t, root)
	m.restoreTabs([]string{gone}, 0, false)
	if m.cwd != root || len(m.tabs) > 1 {
		t.Fatalf("all gone: %d tabs, %s", len(m.tabs), m.cwd)
	}
	// The active tab is saved on quit.
	m = testModel(t, root)
	m.restoreTabs([]string{a, b}, 1, false)
	m.quit()
	if c := loadConfig(); c.ActiveTab != 1 || len(c.Tabs) != 2 {
		t.Fatalf("saved: %+v", c)
	}
}

func TestTabStripKeepsActiveVisible(t *testing.T) {
	root := t.TempDir()
	m := testModel(t, root)
	for i := 0; i < 12; i++ {
		m = press(t, m, "ctrl+t")
	}
	m.width = 60
	spans, more := m.tabSpans(m.width)
	if !more {
		t.Fatal("13 tabs shouldn't fit in 60 columns")
	}
	visible := false
	for _, s := range spans {
		if s.tab == m.tab {
			visible = true
		}
		if s.end > m.width {
			t.Fatalf("span past the edge: %+v", s)
		}
	}
	if !visible {
		t.Fatalf("active tab %d not on the strip: %+v", m.tab, spans)
	}
	if got := m.tabAt(spans[0].start); got != spans[0].tab {
		t.Fatalf("tabAt: %d", got)
	}
	if got := m.tabAt(spans[0].end); got != -1 {
		t.Fatalf("the gap between tabs isn't a tab: %d", got)
	}
}
