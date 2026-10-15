package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	return fitFrame(m.frame(), m.width)
}

func (m model) frame() string {
	inner, rows := m.geometry()
	m.syncGraphics(inner, rows)

	parentTitle := tildePath(filepath.Dir(m.cwd))
	files := len(m.entries) - len(m.trashRows())
	hint := fmt.Sprintf("%d/%d", min(m.cursor+1, files), files)
	if m.cfg.Sort != "name" || m.cfg.SortDesc {
		arrow := "↑"
		if m.cfg.SortDesc {
			arrow = "↓"
		}
		hint = m.cfg.Sort + arrow + "  " + hint
	}
	if m.filter != "" {
		hint = "/" + m.filter + "  " + hint
	}
	title := tildePath(m.cwd)
	if m.arc != nil {
		// The archive location leads: titles are cut from the right.
		title = m.arc.label() + "  in " + tildePath(m.cwd)
		parentTitle = tildePath(m.cwd)
		if m.arc.dir != "" {
			parentTitle = tildePath(m.cwd) + "/" + filepath.Base(m.arc.file)
		}
	}
	if m.inTrash() {
		title = "Trash"
	}
	pvTitle := m.pv.title
	if pvTitle == "" {
		pvTitle = "Preview"
	}

	layout := tideui.Layout{
		Width: m.width, Height: m.height - m.topOffset(), Mode: tideui.ThreeColumn,
		ColumnRatios: m.ratios(),
		Panes: [3]tideui.Pane{
			{Title: parentTitle, Content: m.renderList(m.parent, m.parentCursor, max(0, min(m.parentCursor-rows/2, len(m.parent)-rows)), inner[0], rows, false)},
			{Title: title, Hint: hint, Focused: true, Content: m.renderList(m.entries, m.cursor, m.offset, inner[1], rows, true)},
			{Title: pvTitle, Hint: m.pv.meta, Content: strings.Join(m.previewLines(inner[2]), "\n"), ScrollOffset: m.pvScroll},
		},
		Status: &tideui.StatusBar{Left: m.statusLeft(), Right: statusHints()},
	}
	if m.cfg.HidePreview {
		layout.Mode, layout.SidebarRatio = tideui.SidebarOnly, 0.28
	}
	switch {
	case m.pickerOpen:
		h := max(6, m.height-4)
		ov := m.picker.SoftModal(m.renderer, min(40, m.width-4), h, "tidefiles")
		layout.Modal = &ov
	case m.modal != nil:
		ov := m.modalOverlay()
		layout.Modal = &ov
	}
	if m.topOffset() > 0 {
		return m.tabStrip() + "\n" + m.renderer.Render(layout)
	}
	return m.renderer.Render(layout)
}

func (m model) statusLeft() string {
	if m.job != nil && !m.msgErr {
		return m.job.status(time.Now())
	}
	if m.msg != "" {
		if m.msgErr {
			return m.renderer.Styles.StatusError.Render(m.msg)
		}
		return m.msg
	}
	if n := len(m.selected); n > 0 {
		var total int64
		for _, e := range m.all {
			if m.selected[e.name] && !e.isDir {
				total += e.size
			}
		}
		return fmt.Sprintf("%d selected  %s", n, humanSize(total))
	}
	e, ok := m.current()
	if ok && e.kind == kindTrashLink {
		return "Trash · " + plural(m.trashN, "item") + " · enter opens it"
	}
	if ok && m.inTrash() {
		if tm, found := m.trashMeta[e.name]; found {
			return "from " + tildePath(tm.origin) + " · deleted " + tm.deleted.Format("2006-01-02 15:04") + " · r restores"
		}
	}
	if !ok {
		if m.filter != "" {
			return "no matches — esc clears the filter"
		}
		return ""
	}
	if e.isDir {
		return fmt.Sprintf("%s  %s", e.mode, e.mod.Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf("%s  %s  %s", e.mode, humanSize(e.size), e.mod.Format("2006-01-02 15:04"))
}

func (m model) renderList(ents []entry, cursor, offset, width, rows int, main bool) string {
	if len(ents) == 0 {
		if main && m.filter != "" {
			return "  (no matches)"
		}
		return "  (empty)"
	}
	cutSet := map[string]bool{}
	if main && m.clip.cut {
		for _, p := range m.clip.paths {
			if filepath.Dir(p) == m.cwd {
				cutSet[filepath.Base(p)] = true
			}
		}
	}
	starred := map[string]bool{}
	for _, b := range m.cfg.Bookmarks {
		starred[b] = true
	}
	end := min(len(ents), offset+rows)
	lines := make([]string, 0, end-offset)
	for i := offset; i < end; i++ {
		e := ents[i]
		switch e.kind {
		case kindSeparator:
			lines = append(lines, m.renderer.Styles.DetailMeta.Render(" "+strings.Repeat("─", max(1, width-2))))
			continue
		case kindTrashLink:
			text := trashIcon + " Trash · " + strconv.Itoa(m.trashN)
			lines = append(lines, m.renderer.RenderRow(tideui.Row{Prefix: "  ", Text: text, Selected: i == cursor, Muted: true}, width))
			continue
		}
		name := e.name
		if e.isDir {
			name += "/"
		}
		prefix := " "
		if main && m.selected[e.name] {
			prefix = "✓"
		}
		row := tideui.Row{Prefix: prefix + " ", Text: sanitize(name), Selected: i == cursor, Muted: e.hidden() || cutSet[e.name]}
		switch {
		case main && m.inTrash() && width >= 24:
			// Where it came from and when it was deleted; narrow panes keep the date.
			row.Suffix = m.trashSuffix(e.name, max(16, width-ansi.StringWidth(name)-6)) + " "
		case main && !e.isDir && width >= 28:
			row.Suffix = humanSize(e.size) + " "
		case main && e.isDir && m.arc == nil && starred[filepath.Join(m.cwd, e.name)]:
			row.Suffix = "★ " // bookmarked
		}
		lines = append(lines, m.renderer.RenderRow(row, width))
	}
	return strings.Join(lines, "\n")
}

// syncGraphics tells the terminal wrapper which out-of-band image (sixel or
// kitty) belongs over the preview pane right now, if any.
func (m model) syncGraphics(inner [3]int, rows int) {
	img := m.pv.img
	if m.gfx == nil || img == nil || m.pv.key != m.pvKey || m.modal != nil || m.pickerOpen || m.cfg.HidePreview {
		if m.gfx != nil {
			m.gfx.set(nil, "")
		}
		return
	}
	w := m.columnWidths()
	left := w[0] + w[1] + 1  // past the pane's left border
	top := 2 + m.topOffset() // tab strip, top border, header row
	ov := &overlay{
		row: top + max(0, (rows-img.rows)/2), col: left + max(0, (inner[2]-img.cols)/2),
		seq: img.seq, kitty: m.proto == protoKitty,
		clrRow: top, clrCol: left, clrCols: inner[2], clrRows: rows,
	}
	ov.key = fmt.Sprintf("%s@%d,%d", m.pv.key, ov.row, ov.col)
	if m.pv.anim != nil {
		ov.frameOf = ov.key
		ov.key += fmt.Sprintf("#%d", m.pv.frame)
	}
	m.gfx.set(ov, m.theme.Bg)
}

// drawGraphicsNow puts the current image on screen without waiting for
// Bubble Tea to write a frame.
func (m model) drawGraphicsNow() {
	if m.gfx == nil {
		return
	}
	inner, rows := m.geometry()
	m.syncGraphics(inner, rows)
	m.gfx.flush()
}

// previewLines is the preview content with the jump target's line, if any,
// painted in the selection colour across the pane's width.
func (m model) previewLines(width int) []string {
	if m.pv.hlTo <= m.pv.hlFrom {
		return m.selectionLines(m.pv.lines)
	}
	bg := m.theme.Selected
	if bg == "" {
		bg = m.theme.BorderFocus
	}
	marker := lipgloss.NewStyle().Background(bg).Render("x")
	on := marker[:strings.Index(marker, "x")]
	out := append([]string(nil), m.pv.lines...)
	for i := m.pv.hlFrom; i < min(m.pv.hlTo, len(out)); i++ {
		out[i] = paintBackground(out[i], width, on)
	}
	return m.selectionLines(out)
}

// paintBackground applies the background sequence on to line, re-applying it
// after every reset inside the line and padding to width.
func paintBackground(line string, width int, on string) string {
	if on == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+on)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+on)
	pad := max(0, width-ansi.StringWidth(line))
	return on + line + strings.Repeat(" ", pad) + "\x1b[0m"
}
