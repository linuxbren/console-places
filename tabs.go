package main

// Tabs: several folders open at once. The model's fields always describe the
// active tab; switching saves them into tabs[tab] and loads another.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const tabNameMax = 24

// tabState is everything that belongs to one tab rather than the window.
type tabState struct {
	cwd      string
	focus    string // entry under the cursor
	cursors  map[string]string
	selected map[string]bool
	hist     []string
	hi       int
	filter   string
	arc      *arcView
	jump     jumpTarget
}

// saveTab stores the active tab's state.
func (m *model) saveTab() {
	if m.tab >= len(m.tabs) {
		m.tabs = append(m.tabs, tabState{})
	}
	m.tabs[m.tab] = tabState{cwd: m.cwd, focus: m.curName(), cursors: m.cursors, selected: m.selected,
		hist: m.hist, hi: m.hi, filter: m.filter, arc: m.arc, jump: m.jump}
}

// loadTab makes tab i active.
func (m *model) loadTab(i int) {
	t := m.tabs[i]
	m.tab = i
	m.cwd, m.cursors, m.selected, m.hist, m.hi, m.arc, m.jump = t.cwd, t.cursors, t.selected, t.hist, t.hi, t.arc, t.jump
	m.filter = t.filter
	m.offset, m.cursor = 0, 0
	if _, err := os.Stat(m.cwd); err != nil && m.arc == nil {
		m.cwd = nearestDir(m.cwd) // the folder went away while the tab was in the background
	}
	m.reload(t.focus)
	m.pvKey = ""
}

func (m *model) switchTab(i int) {
	if i < 0 || i >= len(m.tabs) || i == m.tab {
		return
	}
	m.saveTab()
	m.loadTab(i)
}

// newTab opens a tab on the current folder, right after the active one.
func (m *model) newTab() {
	m.saveTab()
	t := tabState{cwd: m.cwd, focus: m.curName(), cursors: map[string]string{}, selected: map[string]bool{}, hist: []string{m.cwd}}
	m.tabs = append(m.tabs[:m.tab+1], append([]tabState{t}, m.tabs[m.tab+1:]...)...)
	m.loadTab(m.tab + 1)
}

// closeTab closes the active tab; the last one stays open.
func (m *model) closeTab() bool {
	if len(m.tabs) <= 1 {
		return false
	}
	m.leaveArchive()
	m.tabs = append(m.tabs[:m.tab], m.tabs[m.tab+1:]...)
	m.loadTab(min(m.tab, len(m.tabs)-1))
	return true
}

// restoreTabs reopens the saved tab folders that still exist. Started
// with a folder (given), that folder (the current state) is the active tab:
// the matching saved tab if there is one, otherwise a tab added at the end.
// Started without one, it picks up where the last session left off: on the
// tab that was active then (the one before it, if that folder is gone).
func (m *model) restoreTabs(dirs []string, active int, given bool) {
	start := tabState{cwd: m.cwd, cursors: m.cursors, selected: m.selected, hist: m.hist, hi: m.hi}
	var tabs []tabState
	cur := -1
	for i, d := range dirs {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue
		}
		if given && d == m.cwd && cur < 0 {
			cur = len(tabs)
			tabs = append(tabs, start)
			continue
		}
		if !given && i <= active {
			cur = len(tabs)
		}
		tabs = append(tabs, tabState{cwd: d, cursors: map[string]string{}, selected: map[string]bool{}, hist: []string{d}})
	}
	switch {
	case given && cur < 0:
		cur = len(tabs)
		tabs = append(tabs, start)
	case len(tabs) == 0: // every saved folder is gone: just the starting one
		return
	}
	m.tabs = tabs
	if given {
		m.tab = cur // the model already shows this tab's folder
		return
	}
	m.loadTab(max(0, cur))
}

// tabDirs are the folders of all tabs, for saving.
func (m *model) tabDirs() []string {
	m.saveTab()
	dirs := make([]string, len(m.tabs))
	for i, t := range m.tabs {
		dirs[i] = t.cwd
	}
	return dirs
}

func nearestDir(p string) string {
	for {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// topOffset is the screen lines above the panes: the tab strip, shown only
// when more than one tab is open.
func (m model) topOffset() int {
	if len(m.tabs) > 1 {
		return 1
	}
	return 0
}

// tabLabel names a tab by its folder (or archive location).
func (m model) tabLabel(i int) string {
	t := m.tabs[i]
	cwd, arc := t.cwd, t.arc
	if i == m.tab {
		cwd, arc = m.cwd, m.arc
	}
	name := filepath.Base(cwd)
	switch {
	case arc != nil:
		name = arc.label()
	case cwd == "/":
		name = "/"
	default:
		if home, err := os.UserHomeDir(); err == nil && cwd == home {
			name = "~"
		}
	}
	name = sanitize(name)
	if ansi.StringWidth(name) > tabNameMax {
		name = ansi.Truncate(name, tabNameMax, "…")
	}
	return strconv.Itoa(i+1) + " " + name
}

// tabSpan is one tab's place on the strip, in screen columns.
type tabSpan struct {
	tab        int
	start, end int
	text       string
}

// tabSpans lays the tabs out across width, scrolling so the active one is
// always visible.
func (m model) tabSpans(width int) (spans []tabSpan, more bool) {
	cells := make([]string, len(m.tabs))
	for i := range m.tabs {
		cells[i] = " " + m.tabLabel(i) + " "
	}
	first := 0
	fits := func(from int) bool {
		w := 0
		for i := from; i <= m.tab; i++ {
			w += ansi.StringWidth(cells[i]) + 1
		}
		return w <= width
	}
	for first < m.tab && !fits(first) {
		first++
	}
	x := 0
	for i := first; i < len(cells); i++ {
		w := ansi.StringWidth(cells[i])
		if x+w > width {
			return spans, true
		}
		spans = append(spans, tabSpan{tab: i, start: x, end: x + w, text: cells[i]})
		x += w + 1
	}
	return spans, first > 0
}

// tabStrip renders the strip line.
func (m model) tabStrip() string {
	st := m.renderer.Styles
	spans, _ := m.tabSpans(m.width)
	var b strings.Builder
	x := 0
	for _, s := range spans {
		b.WriteString(strings.Repeat(" ", s.start-x))
		if s.tab == m.tab {
			b.WriteString(st.ItemSelected.UnsetPadding().Render(s.text))
		} else {
			b.WriteString(st.ItemMuted.UnsetPadding().Render(s.text))
		}
		x = s.end
	}
	return st.StatusBar.UnsetPadding().Width(m.width).Render(b.String())
}

// tabAt is the tab under screen column x on the strip, or -1.
func (m model) tabAt(x int) int {
	spans, _ := m.tabSpans(m.width)
	for _, s := range spans {
		if x >= s.start && x < s.end {
			return s.tab
		}
	}
	return -1
}
