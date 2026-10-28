package main

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestIconFor(t *testing.T) {
	for _, c := range []struct {
		e    entry
		kind iconKind
	}{
		{entry{name: "src", isDir: true}, iconFolder},
		{entry{name: "main.go"}, iconCode},
		{entry{name: "Photo.JPG"}, iconImage},
		{entry{name: "a.tar.gz"}, iconArchive},
		{entry{name: "notes.md"}, iconDoc},
		{entry{name: "run", mode: 0o755}, iconExec},
		{entry{name: "LICENSE", mode: 0o644}, iconFile},
		{entry{name: "dev", mode: fs.ModeDevice | 0o755}, iconFile},
	} {
		if got := iconFor(c.e); got.kind != c.kind || got.glyph == "" {
			t.Errorf("%s: got %+v, want kind %d", c.e.name, got, c.kind)
		}
	}
}

func TestFileIconsToggle(t *testing.T) {
	dir, _ := writeTemp(t, "main.go", "package main\n")
	m := testModel(t, dir)
	m.reload("main.go")
	glyph := iconFor(entry{name: "main.go"}).glyph
	if strings.Contains(ansi.Strip(m.View()), glyph) {
		t.Fatal("icons are off by default")
	}
	m.cfg.Icons = true
	if !strings.Contains(ansi.Strip(m.View()), glyph+" main.go") {
		t.Fatalf("icon missing:\n%s", ansi.Strip(m.View()))
	}
}
