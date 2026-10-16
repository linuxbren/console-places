package main

// Mouse text selection in the preview pane. A terminal reports the mouse for
// the whole window or not at all, so its own selection would run across all
// three panes; console-places keeps the mouse and selects inside the preview
// itself. Dragging highlights text; releasing copies it to the clipboard as
// the file's own text: tabs intact and wrapped lines joined. A click without
// a drag, a key, or a different preview clears the selection.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// textPos is a cell in the preview's display lines: line index, column.
type textPos struct{ line, col int }

func (a textPos) before(b textPos) bool {
	return a.line < b.line || (a.line == b.line && a.col < b.col)
}

type previewSel struct {
	key          string // the preview it belongs to
	anchor, head textPos
	dragging     bool
}

// ordered returns the selection's first and last cell (both included).
func (s *previewSel) ordered() (textPos, textPos) {
	if s.head.before(s.anchor) {
		return s.head, s.anchor
	}
	return s.anchor, s.head
}

// selectable says whether the preview shows text a selection can copy.
func (m model) selectable() bool {
	return m.pv.img == nil && m.pv.anim == nil && !m.pv.loading && !m.pv.blocks && len(m.pv.lines) > 0
}

// previewPos converts a mouse position to a cell of the preview's lines,
// clamped to the pane; inside reports whether it was over the text area.
func (m model) previewPos(x, y int) (textPos, bool) {
	w := m.columnWidths()
	inner, rows := m.geometry()
	left := w[0] + w[1] + 1
	row := y - 2 - m.topOffset()
	col := x - left
	inside := row >= 0 && row < rows && col >= 0 && col < inner[2]
	row = max(0, min(row, rows-1))
	line := max(0, min(m.pvScroll+row, len(m.pv.lines)-1))
	return textPos{line, max(0, min(col, inner[2]-1))}, inside
}

// mouseSelect handles left-button presses, drags and releases over the
// preview. It reports whether the event was used.
func (m *model) mouseSelect(msg tea.MouseMsg) (tea.Cmd, bool) {
	s := m.psel
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		pos, inside := m.previewPos(msg.X, msg.Y)
		if !inside || m.cfg.HidePreview || !m.selectable() {
			m.psel = nil
			return nil, false
		}
		m.psel = &previewSel{key: m.pv.key, anchor: pos, head: pos, dragging: true}
		return nil, true
	case s == nil || !s.dragging:
		return nil, false
	case msg.Action == tea.MouseActionMotion:
		// Dragging past the top or bottom edge scrolls the preview.
		_, rows := m.geometry()
		switch row := msg.Y - 2 - m.topOffset(); {
		case row < 0:
			m.pvScroll = max(0, m.pvScroll-1)
		case row >= rows:
			m.pvScroll++
			m.clampPreviewScroll()
		}
		s.head, _ = m.previewPos(msg.X, msg.Y)
		return nil, true
	case msg.Action == tea.MouseActionRelease:
		s.dragging = false
		if s.anchor == s.head {
			m.psel = nil // a plain click
			return nil, true
		}
		text := m.selectionText()
		n := strings.Count(text, "\n") + 1
		desc := plural(n, "line")
		if n == 1 {
			desc = plural(len([]rune(text)), "character")
		}
		return func() tea.Msg {
			if err := textWrite(text); err != nil {
				return statusMsg{"copy failed: " + err.Error(), true}
			}
			return statusMsg{"copied " + desc, false}
		}, true
	}
	return nil, false
}

// selectionText is the selected text: from the file itself when the preview
// maps back to it, otherwise as shown.
func (m model) selectionText() string {
	from, to := m.psel.ordered()
	if m.pv.segs == nil {
		var out []string
		for k := from.line; k <= to.line && k < len(m.pv.lines); k++ {
			plain := ansi.Strip(m.pv.lines[k])
			a, b := 0, ansi.StringWidth(plain)
			if k == from.line {
				a = from.col
			}
			if k == to.line {
				b = to.col + 1
			}
			out = append(out, strings.TrimRight(cutCells(plain, a, b), " "))
		}
		return strings.Join(out, "\n")
	}
	si, sr, ok := m.sourceAt(from, false)
	ei, er, ok2 := m.sourceAt(to, true)
	for !ok && from.line < to.line { // started on a line that isn't file text
		from = textPos{from.line + 1, 0}
		si, sr, ok = m.sourceAt(from, false)
	}
	for !ok2 && to.line > from.line {
		to = textPos{to.line - 1, 1 << 30}
		ei, er, ok2 = m.sourceAt(to, true)
	}
	if !ok || !ok2 {
		return ""
	}
	var out []string
	for i := si; i <= ei; i++ {
		r := []rune(m.pv.src[i])
		a, b := 0, len(r)
		if i == si {
			a = min(sr, len(r))
		}
		if i == ei {
			b = min(er, len(r))
		}
		out = append(out, string(r[a:max(a, b)]))
	}
	return strings.Join(out, "\n")
}

// sourceAt maps a display cell to (source line, rune index in the raw line
// with tabs). With end set the index is just past the cell, and a cell at or
// past a line's end (or on its "…") reaches the end of the source line.
func (m model) sourceAt(p textPos, end bool) (int, int, bool) {
	if p.line >= len(m.pv.segs) || m.pv.segs[p.line].src < 0 {
		return 0, 0, false
	}
	sg := m.pv.segs[p.line]
	raw := m.pv.src[sg.src]
	plain := ansi.Strip(m.pv.lines[p.line])
	exp := []rune(expandTabs(raw))
	last := p.line+1 >= len(m.pv.segs) || m.pv.segs[p.line+1].src != sg.src
	width := ansi.StringWidth(plain)
	truncated := strings.HasSuffix(plain, "…") && sg.off+len([]rune(plain)) <= len(exp)
	var e int
	switch {
	case end && (p.col >= width || (truncated && p.col >= width-1)) && last:
		return sg.src, len([]rune(raw)), true
	case p.col >= width:
		e = sg.off + len([]rune(plain))
	default:
		e = sg.off + runesInCells(plain, p.col)
		if end {
			e++
		}
	}
	return sg.src, rawIndex(raw, min(e, len(exp))), true
}

// runesInCells counts the runes of s that start before cell col.
func runesInCells(s string, col int) int {
	n, w := 0, 0
	for _, r := range s {
		if w >= col {
			break
		}
		w += max(1, ansi.StringWidth(string(r)))
		n++
	}
	return n
}

// cutCells returns the runes of plain s from cell a up to cell b.
func cutCells(s string, a, b int) string {
	r := []rune(s)
	i, j := runesInCells(s, a), runesInCells(s, b)
	return string(r[min(i, len(r)):min(j, len(r))])
}

// rawIndex converts a rune index in expandTabs(raw) to one in raw; a
// position inside a tab's spaces lands on the tab.
func rawIndex(raw string, e int) int {
	col := 0
	for j, r := range []rune(raw) {
		w := 1
		if r == '\t' {
			w = tabWidth - col%tabWidth
		}
		if e < col+w {
			if e == col {
				return j
			}
			return j + 1 // inside a tab: the tab is included
		}
		col += w
	}
	return len([]rune(raw))
}

// segmentsOf finds where each display line (wrapped or cut from the expanded
// source lines) starts in its source line. Wrapping drops the spaces at a
// break, so those are skipped.
func segmentsOf(lines []string, starts []int, expanded []string) []segment {
	segs := make([]segment, len(lines))
	for i := range segs {
		segs[i].src = -1
	}
	for i, st := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		src := []rune(expanded[i])
		off := 0
		for k := st; k < end && k < len(lines); k++ {
			plain := []rune(ansi.Strip(lines[k]))
			if k > st {
				for off < len(src) && src[off] == ' ' && (len(plain) == 0 || plain[0] != ' ') {
					off++
				}
			}
			segs[k] = segment{i, off}
			off += len(plain)
		}
	}
	return segs
}

// selectionLines paints the selection over the preview's lines.
func (m model) selectionLines(lines []string) []string {
	s := m.psel
	if s == nil || s.key != m.pv.key {
		return lines
	}
	// Inverted theme colours, as a terminal's own selection looks; one
	// colour reads better over syntax highlighting than reversing each token.
	style := lipgloss.NewStyle().Background(m.theme.Fg).Foreground(m.theme.Bg)
	from, to := s.ordered()
	out := append([]string(nil), lines...)
	for k := from.line; k <= to.line && k < len(out); k++ {
		plain := ansi.Strip(out[k])
		width := ansi.StringWidth(plain)
		a, b := 0, max(width, 1)
		if k == from.line {
			a = from.col
		}
		if k == to.line {
			b = min(to.col+1, max(width, 1))
		}
		if a >= b {
			continue
		}
		mid := cutCells(plain, a, b)
		if mid == "" {
			mid = " "
		}
		out[k] = ansi.Cut(out[k], 0, a) + "\x1b[0m" + style.Render(mid) + ansi.Cut(out[k], b, width)
	}
	return out
}
