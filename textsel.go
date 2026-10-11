package main

// Select text (Y): the preview's text full screen, without borders or other
// panes, and with mouse capture off, so the terminal's own selection picks
// up only the file's text and its own copy works (foot: the selection goes
// to the primary clipboard, ctrl+shift+c copies). A terminal reports the
// mouse for the whole window or not at all, so turning capture off for just
// the preview pane is not possible; shift+drag in the normal view selects
// across every pane.

import (
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// selLineWidth is how much of a long line is kept: lines are not wrapped
// here (a wrap would copy as a line break), ←/→ scroll sideways instead.
const selLineWidth = 4096

type textSel struct {
	title     string
	lines     []string
	top, left int
}

func (m *model) openTextSelect() tea.Cmd {
	e, ok := m.current()
	if !ok {
		m.setMsg("nothing selected", false)
		return nil
	}
	o := m.previewOpts()
	o.wrap, o.width, o.rows = false, selLineWidth, m.height
	if classify(e) == kindMarkdown && !o.mdSource {
		o.width = max(20, m.width-2) // rendered markdown is prose: glamour wraps it
	}
	var pv preview
	switch {
	case e.kind == kindTrashLink:
		pv = dirPreview(preview{title: "Trash"}, trashFilesDir(), o)
	case m.arc != nil:
		pv = arcPreview(m.arc, m.arc.dir, e, o)
	default:
		switch classify(e) {
		case kindImage, kindPDF, kindVideo:
			m.setMsg("no text to select in "+e.name, false)
			return nil
		}
		pv = buildPreview(m.cwd, e, o)
	}
	if pv.anim != nil {
		pv.anim.stop()
	}
	if pv.img != nil || pv.anim != nil {
		m.setMsg("no text to select in "+e.name, false)
		return nil
	}
	m.tsel = &textSel{title: pv.title, lines: pv.lines, top: m.pvScroll}
	if m.pvPath != filepath.Join(m.cwd, e.name) {
		m.tsel.top = 0
	}
	m.clampTextSel()
	return tea.DisableMouse
}

func (m *model) closeTextSelect() tea.Cmd {
	m.tsel = nil
	return tea.EnableMouseCellMotion
}

// textSelRows is how many lines of text fit between the title and hint rows.
func (m model) textSelRows() int { return max(1, m.height-2) }

func (m *model) clampTextSel() {
	s := m.tsel
	s.top = max(0, min(s.top, len(s.lines)-m.textSelRows()))
	widest := 0
	for _, l := range s.lines {
		widest = max(widest, ansi.StringWidth(l))
	}
	s.left = max(0, min(s.left, widest-m.width+1))
}

func (m model) handleTextSelKey(msg tea.KeyMsg) (model, tea.Cmd) {
	s := m.tsel
	page := m.textSelRows()
	switch msg.String() {
	case "esc", "q", "Y", "ctrl+c":
		return m, m.closeTextSelect()
	case "up", "k":
		s.top--
	case "down", "j":
		s.top++
	case "pgup", "ctrl+b", "K":
		s.top -= page
	case "pgdown", "ctrl+f", " ", "J":
		s.top += page
	case "ctrl+u":
		s.top -= page / 2
	case "ctrl+d":
		s.top += page / 2
	case "home", "g":
		s.top, s.left = 0, 0
	case "end", "G":
		s.top = len(s.lines)
	case "left", "h":
		s.left -= max(1, m.width/2)
	case "right", "l":
		s.left += max(1, m.width/2)
	}
	m.clampTextSel()
	return m, nil
}

func (m model) textSelView() string {
	s := m.tsel
	meta := m.renderer.Styles.DetailMeta
	out := make([]string, 0, m.height)
	out = append(out, meta.Render(ansi.Truncate(" "+sanitize(s.title), m.width, "…")))
	for i := s.top; i < s.top+m.textSelRows(); i++ {
		line := ""
		if i < len(s.lines) {
			line = ansi.Cut(s.lines[i], s.left, s.left+m.width) + "\x1b[0m"
		}
		out = append(out, line)
	}
	hint := " select with the mouse, copy with your terminal (foot: ctrl+shift+c)  ↑↓ scroll  ←→ sideways  esc back"
	return strings.Join(append(out, meta.Render(ansi.Truncate(hint, m.width, "…"))), "\n")
}
