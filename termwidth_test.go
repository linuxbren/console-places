package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestFitFrameCutsWhatTerminalsFindTooWide(t *testing.T) {
	// Devanagari aa (a spacing mark): one cell for the layout, two on screen.
	line := "\x1b[1m" + strings.Repeat("x", 8) + "का" + "\x1b[0m"
	if ansi.StringWidth(line) != 9 || cellWidth(line) != 10 {
		t.Fatalf("widths: layout %d, terminal %d", ansi.StringWidth(line), cellWidth(line))
	}
	got := fitFrame("short\n"+line, 9)
	if parts := strings.Split(got, "\n"); len(parts) != 2 || cellWidth(parts[1]) > 9 || parts[0] != "short" {
		t.Fatalf("fitted: %q", got)
	}
	if f := "plain\nlines"; fitFrame(f, 5) != f {
		t.Fatal("lines that fit stay as they are")
	}
	// Thai AM is split, so the layout and the terminal agree on it.
	th := sanitize("Comment[th]=โปรแกรมสำหรับ")
	if ansi.StringWidth(th) != cellWidth(th) {
		t.Fatalf("Thai: layout %d, terminal %d", ansi.StringWidth(th), cellWidth(th))
	}
}

// The whole window, with a Thai line in the preview, fits the terminal.
func TestViewFitsWithThaiPreview(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir+"/steam.desktop", "[Desktop Entry]\nComment[th]=โปรแกรมสำหรับจัดการและเล่นเกมบน Steam\nName=Steam\n")
	m := testModel(t, dir)
	m.width, m.height = 184, 40
	m.refreshPreview()
	for i, l := range strings.Split(m.View(), "\n") {
		if cellWidth(l) > m.width {
			t.Fatalf("line %d is %d cells: %q", i, cellWidth(l), ansi.Strip(l))
		}
	}
}
