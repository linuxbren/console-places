package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestSelectTextShowsWholeLinesFullScreen(t *testing.T) {
	dir := t.TempDir()
	long := "start " + strings.Repeat("word ", 60) + "end"
	var body []string
	for i := range 100 {
		body = append(body, "line "+string(rune('a'+i%26)))
	}
	body[1] = long
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(strings.Join(body, "\n")), 0o644)
	m := testModel(t, dir)
	m.cfg.Wrap = true
	m.reload("notes.txt")

	m = send(m, "Y")
	if m.tsel == nil {
		t.Fatal("Y did not open select-text")
	}
	if got := ansi.Strip(m.tsel.lines[1]); got != long {
		t.Errorf("long line was wrapped or cut: %q", got)
	}
	view := ansi.Strip(m.View())
	for _, border := range []string{"│", "╭", "╰"} {
		if strings.Contains(view, border) {
			t.Fatalf("select-text view has pane borders (%s):\n%s", border, view)
		}
	}
	if lines := strings.Split(view, "\n"); len(lines) != m.height || !strings.HasPrefix(lines[2], "start word") {
		t.Errorf("%d lines, text row %q", len(lines), lines[2])
	}

	for range 5 { // far enough right to reach the end of the line
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = m2.(model)
	}
	if m.tsel.left == 0 || !strings.Contains(ansi.Strip(m.View()), "end") {
		t.Error("→ should scroll sideways to the end of the long line")
	}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = m2.(model)
	if m.tsel.top != 100-m.textSelRows() {
		t.Errorf("end: top %d", m.tsel.top)
	}

	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = m2.(model)
	if m.tsel != nil || cmd == nil {
		t.Error("esc should close select-text and turn the mouse back on")
	}
	if !strings.Contains(ansi.Strip(m.View()), "│") {
		t.Error("normal view not back")
	}
}

func TestSelectTextRefusesImages(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "pic.png", testImage(20, 10))
	m := testModel(t, dir)
	m.reload("pic.png")
	m = send(m, "Y")
	if m.tsel != nil || !strings.Contains(m.msg, "no text to select") {
		t.Errorf("image: tsel=%v msg=%q", m.tsel != nil, m.msg)
	}
}
