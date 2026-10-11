package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const selSource = "package main\n\nfunc main() {\n\tfmt.Println(\"a long line that has to wrap in a narrow preview pane, several times over\")\n\tx := 1\t// trailing tab\n}\n"

// selModel shows selSource (as main.go) in a narrow, wrapping preview.
func selModel(t *testing.T, wrap bool) model {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(selSource), 0o644)
	m := testModel(t, dir)
	m.width = 100
	m.cfg.Wrap = wrap
	m.reload("main.go")
	m.refreshPreview()
	if m.pv.segs == nil {
		t.Fatal("text preview has no source mapping")
	}
	return m
}

// screen returns the terminal cell of preview line k, column c.
func screen(m model, k, c int) (int, int) {
	w := m.columnWidths()
	return w[0] + w[1] + 1 + c, 2 + m.topOffset() + k - m.pvScroll
}

func mouse(m model, action tea.MouseAction, x, y int) (model, tea.Cmd) {
	m2, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: action, Button: tea.MouseButtonLeft})
	return m2.(model), cmd
}

func lastCell(m model, k int) int { return ansi.StringWidth(ansi.Strip(m.pv.lines[k])) - 1 }

func TestPreviewPositionMatchesScreen(t *testing.T) {
	m := selModel(t, true)
	view := strings.Split(ansi.Strip(m.View()), "\n")
	x, y := screen(m, 2, 0)
	if got := string([]rune(view[y])[x:]); !strings.HasPrefix(got, "func main()") {
		t.Fatalf("line 2 is drawn at %d,%d as %q", x, y, got)
	}
	if p, inside := m.previewPos(x+5, y); !inside || p != (textPos{2, 5}) {
		t.Errorf("previewPos = %v %v", p, inside)
	}
}

func TestSelectionCopiesTheFileText(t *testing.T) {
	for _, wrap := range []bool{true, false} {
		m := selModel(t, wrap)
		last := len(m.pv.lines) - 1
		m.psel = &previewSel{key: m.pv.key, anchor: textPos{0, 0}, head: textPos{last, lastCell(m, last)}}
		if got, want := m.selectionText(), strings.TrimSuffix(selSource, "\n"); got != want {
			t.Errorf("wrap=%v: whole file copied as\n%q\nwant\n%q", wrap, got, want)
		}
	}

	m := selModel(t, true)
	k := m.pv.hlFrom // find the Println line and select just "Println"
	for i, l := range m.pv.lines {
		if strings.Contains(ansi.Strip(l), "fmt.Println") {
			k = i
			break
		}
	}
	c := strings.Index(ansi.Strip(m.pv.lines[k]), "Println")
	m.psel = &previewSel{key: m.pv.key, anchor: textPos{k, c + 6}, head: textPos{k, c}}
	if got := m.selectionText(); got != "Println" {
		t.Errorf("partial selection = %q", got)
	}
	// From inside the tab-indented line to the next: the tab of the second
	// line is copied as a tab.
	m.psel = &previewSel{key: m.pv.key, anchor: textPos{k, c}, head: textPos{len(m.pv.lines) - 2, 6}}
	if got := m.selectionText(); !strings.HasPrefix(got, "Println(") || !strings.HasSuffix(got, "\n\tx :") {
		t.Errorf("across lines = %q", got)
	}
}

func TestDragSelectsAndHighlights(t *testing.T) {
	m := selModel(t, true)
	x0, y0 := screen(m, 2, 0)
	x1, y1 := screen(m, 2, 3)
	m, _ = mouse(m, tea.MouseActionPress, x0, y0)
	m, _ = mouse(m, tea.MouseActionMotion, x1, y1)
	if m.psel == nil || m.selectionText() != "func" {
		t.Fatalf("dragging over \"func\": %+v", m.psel)
	}
	if !strings.Contains(m.View(), "func\x1b") && !strings.Contains(strings.Join(m.selectionLines(m.pv.lines), ""), "\x1b[0m") {
		t.Error("no highlight drawn")
	}
	m, cmd := mouse(m, tea.MouseActionRelease, x1, y1)
	if cmd == nil || m.psel == nil || m.psel.dragging {
		t.Error("release should copy and keep the highlight")
	}

	// A key clears it; so does a click without a drag.
	m = send(m, "j")
	if m.psel != nil {
		t.Error("a key should clear the selection")
	}
	m, _ = mouse(m, tea.MouseActionPress, x0, y0)
	m, cmd = mouse(m, tea.MouseActionRelease, x0, y0)
	if m.psel != nil || cmd != nil {
		t.Error("a plain click should not select or copy")
	}
}

func TestDragPastTheBottomScrolls(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := range 200 {
		b.WriteString("line " + strings.Repeat("x", i%7) + "\n")
	}
	os.WriteFile(filepath.Join(dir, "long.txt"), []byte(b.String()), 0o644)
	m := testModel(t, dir)
	m.reload("long.txt")
	m.refreshPreview()
	x, y := screen(m, 0, 0)
	m, _ = mouse(m, tea.MouseActionPress, x, y)
	m, _ = mouse(m, tea.MouseActionMotion, x, m.height-1)
	if m.pvScroll != 1 {
		t.Errorf("pvScroll %d after dragging below the pane", m.pvScroll)
	}
}

func TestNothingToSelectInImages(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "pic.png", testImage(40, 30))
	m := testModel(t, dir)
	m.reload("pic.png")
	m.pv = buildPreview(dir, m.entries[m.cursor], m.previewOpts())
	m.pvKey = m.pv.key
	x, y := screen(m, 1, 1)
	m, _ = mouse(m, tea.MouseActionPress, x, y)
	if m.psel != nil {
		t.Error("a half-block picture was selectable")
	}
}
