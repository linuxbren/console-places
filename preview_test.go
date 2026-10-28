package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func writeTemp(t *testing.T, name, content string) (string, entry) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ents, err := readDir(dir, true)
	if err != nil || len(ents) != 1 {
		t.Fatalf("readDir: %v %v", ents, err)
	}
	return dir, ents[0]
}

func TestPreviewWrapsAtWordBoundaries(t *testing.T) {
	dir, e := writeTemp(t, "a.txt", "the quick brown fox jumps over the lazy dog\n")
	p := buildPreview(dir, e, testOpts(16, true))
	if len(p.lines) < 3 {
		t.Fatalf("expected wrapped lines, got %q", p.lines)
	}
	for _, l := range p.lines {
		if ansi.StringWidth(l) > 16 {
			t.Errorf("line %q wider than 16", l)
		}
	}
	if strings.Join(p.lines, " ") != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("words split mid-word: %q", p.lines)
	}
}

func TestPreviewNoWrapTruncates(t *testing.T) {
	dir, e := writeTemp(t, "a.txt", strings.Repeat("x", 100)+"\n")
	p := buildPreview(dir, e, testOpts(20, false))
	if len(p.lines) != 1 || ansi.StringWidth(p.lines[0]) > 20 {
		t.Errorf("got %q", p.lines)
	}
}

func TestPreviewStripsEscapesAndDetectsBinary(t *testing.T) {
	dir, e := writeTemp(t, "a.txt", "hi \x1b[31mred\x1b[0m\n")
	p := buildPreview(dir, e, testOpts(40, true))
	if strings.ContainsRune(strings.Join(p.lines, ""), 0x1b) {
		t.Errorf("escape leaked into preview: %q", p.lines)
	}
	dir, e = writeTemp(t, "b.bin", "abc\x00def")
	if p := buildPreview(dir, e, testOpts(40, true)); !strings.Contains(p.lines[0], "binary") {
		t.Errorf("expected binary notice, got %q", p.lines)
	}
}

func testOpts(width int, wrap bool) previewOpts {
	return previewOpts{width: width, rows: 20, wrap: wrap, proto: protoBlocks, cellW: 10, cellH: 20, bg: "#000000",
		syn: syntax{fg: "#ffffff", kw: "#ff0000", str: "#00ff00", comment: "#888888"}}
}

func TestHighlightKeepsWrapWithinWidth(t *testing.T) {
	dir, e := writeTemp(t, "a.go", "package main\n\n// a rather long comment line that must wrap inside the pane width\nfunc main() {}\n")
	o := testOpts(24, true)
	o.wrapCode = true
	p := buildPreview(dir, e, o)
	if !strings.Contains(p.meta, "Go") {
		t.Errorf("expected Go in meta, got %q", p.meta)
	}
	if !strings.Contains(strings.Join(p.lines, ""), "\x1b[") {
		t.Error("expected ANSI highlighting")
	}
	for _, l := range p.lines {
		if ansi.StringWidth(l) > 24 {
			t.Errorf("line %q wider than 24", ansi.Strip(l))
		}
	}
}

// Code truncates long lines unless code wrap is on; prose still wraps.
func TestCodeTruncatesByDefault(t *testing.T) {
	long := "// " + strings.Repeat("word ", 20)
	dir, e := writeTemp(t, "a.go", "package main\n"+long+"\n")
	p := buildPreview(dir, e, testOpts(24, true)) // text wrap on, code wrap off
	if !p.code || len(p.lines) != 2 || !strings.HasSuffix(ansi.Strip(p.lines[1]), "…") {
		t.Errorf("code should truncate: %q", p.lines)
	}
	o := testOpts(24, true)
	o.wrapCode = true
	if p := buildPreview(dir, e, o); len(p.lines) < 3 {
		t.Errorf("code wrap on should wrap: %q", p.lines)
	}
	dir, e = writeTemp(t, "a.txt", long+"\n")
	if p := buildPreview(dir, e, testOpts(24, true)); p.code || len(p.lines) < 3 {
		t.Errorf("text should wrap: %q", p.lines)
	}
}

func TestJSONIsPrettyPrinted(t *testing.T) {
	dir, e := writeTemp(t, "a.json", `{"a":1,"b":[1,2]}`)
	p := buildPreview(dir, e, testOpts(40, true))
	if len(p.lines) < 4 {
		t.Errorf("expected indented json, got %q", p.lines)
	}
}

func TestVideoInfo(t *testing.T) {
	fakeTool(t, "ffprobe", "printf 'codec_name=h264\\nwidth=1920\\nheight=1080\\nduration=4000.5\\n'\n")
	fakeTool(t, "ffmpegthumbnailer", "exit 1\n")
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "clip.mp4"), "not really a video")
	ents, _ := readDir(dir, true)
	pv := buildPreview(dir, ents[0], previewOpts{width: 60, rows: 20})
	if want := "1920×1080 · 1:06:40 · h264 · 18 B"; pv.meta != want {
		t.Errorf("meta %q, want %q", pv.meta, want)
	}
	text := strings.Join(pv.lines, "\n")
	for _, s := range []string{"Length", "1:06:40", "Codec", "h264", "cannot preview"} {
		if !strings.Contains(text, s) {
			t.Errorf("no thumbnail: lines lack %q:\n%s", s, text)
		}
	}
	if clock(65) != "1:05" {
		t.Errorf("clock(65) = %s", clock(65))
	}
}
