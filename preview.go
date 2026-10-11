package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	previewMaxBytes = 128 * 1024
	previewMaxLines = 2000
	archiveMaxLines = 500
	tabWidth        = 4
)

type previewKind int

const (
	kindText previewKind = iota
	kindDir
	kindImage
	kindPDF
	kindVideo
	kindAudio
	kindArchive
	kindMarkdown // rendered with glamour, which is too slow for the UI goroutine
)

// Async kinds shell out to helper tools or decode large files, so they are
// built off the UI goroutine.
func (k previewKind) async() bool { return k >= kindImage }

var (
	imageExts   = setOf(".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".svg", ".avif", ".heic", ".heif", ".ico", ".psd", ".xcf", ".jxl")
	videoExts   = setOf(".mp4", ".mkv", ".webm", ".mov", ".avi", ".m4v", ".flv", ".wmv", ".mpg", ".mpeg")
	audioExts   = setOf(".mp3", ".flac", ".ogg", ".oga", ".opus", ".wav", ".m4a", ".aac", ".wma")
	archiveExts = setOf(".zip", ".tar", ".tgz", ".gz", ".bz2", ".xz", ".zst", ".7z", ".rar", ".jar", ".apk", ".deb", ".rpm", ".iso", ".cbz", ".whl")
)

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

func classify(e entry) previewKind {
	if e.isDir {
		return kindDir
	}
	ext := strings.ToLower(filepath.Ext(e.name))
	switch {
	case imageExts[ext]:
		return kindImage
	case ext == ".pdf":
		return kindPDF
	case videoExts[ext]:
		return kindVideo
	case audioExts[ext]:
		return kindAudio
	case archiveExts[ext]:
		return kindArchive
	case markdownExts[ext]:
		return kindMarkdown
	}
	return kindText
}

// previewOpts is everything a preview depends on besides the file itself.
type previewOpts struct {
	width, rows  int
	wrap, hidden bool
	mdSource     bool // show markdown as highlighted source instead of rendered
	line         int  // 1-based line to show and highlight (from content search); 0 for none
	proto        gfxProto
	cellW, cellH int
	bg           lipgloss.Color
	syn          syntax
}

// preview is the rendered content of the right-hand pane.
type preview struct {
	key     string
	title   string
	meta    string
	lines   []string
	img     *imgSeq    // out-of-band image (sixel/kitty) drawn over the pane
	anim    *animation // a playing GIF: img or lines is its current frame
	frame   int
	loading bool
	hlFrom  int // lines[hlFrom:hlTo] are the highlighted source line, when hlTo > hlFrom
	hlTo    int
}

// buildPreview renders e (inside dir) for the preview pane.
func buildPreview(dir string, e entry, o previewOpts) preview {
	path := filepath.Join(dir, e.name)
	p := preview{title: e.name}
	o.width = max(1, o.width)
	kind := classify(e)
	switch kind {
	case kindDir:
		return dirPreview(p, path, o)
	case kindImage, kindPDF, kindVideo:
		if kind == kindImage && strings.EqualFold(filepath.Ext(e.name), ".gif") {
			return gifPreview(p, path, e, o)
		}
		return imagePreview(p, path, e, kind, o)
	case kindArchive:
		if out, err := run("bsdtar", "-tf", path); err == nil {
			p.meta = humanSize(e.size)
			p.lines = fitLines(strings.Split(strings.TrimRight(string(out), "\n"), "\n"), o, archiveMaxLines)
			return p
		}
	case kindAudio:
		return audioPreview(p, path, e, o)
	case kindMarkdown:
		if !o.mdSource && o.line == 0 { // a line to show means source view: rendered lines don't map back
			return markdownPreview(p, path, e, o)
		}
	}
	return textPreview(p, path, e, o)
}

func dirPreview(p preview, path string, o previewOpts) preview {
	p.meta = "folder"
	ents, err := readDir(path, o.hidden)
	switch {
	case err != nil:
		p.lines = []string{"  cannot read: " + errText(err)}
	case len(ents) == 0:
		p.lines = []string{"  (empty)"}
	default:
		p.meta = plural(len(ents), "item")
		for i, c := range ents {
			if i >= previewMaxLines {
				break
			}
			name := c.name
			if c.isDir {
				name += "/"
			}
			p.lines = append(p.lines, ansi.Truncate(" "+sanitize(name), o.width, "…"))
		}
	}
	return p
}

func imagePreview(p preview, path string, e entry, kind previewKind, o previewOpts) preview {
	p.meta = humanSize(e.size)
	img, w, h, err := loadImage(path, kind)
	if err != nil || img == nil {
		msg := "cannot preview"
		if err != nil {
			msg += ": " + errText(err)
		}
		p.lines = []string{"  " + msg}
		return p
	}
	if kind == kindImage && w > 0 {
		p.meta = fmt.Sprintf("%d×%d · %s", w, h, humanSize(e.size))
	}
	switch o.proto {
	case protoSixel:
		p.img, err = renderSixel(img, o.width, o.rows, o.cellW, o.cellH, o.bg)
	case protoKitty:
		p.img, err = renderKitty(img, o.width, o.rows, o.cellW, o.cellH, o.bg)
	default:
		p.lines = renderBlocks(img, o.width, o.rows, o.cellW, o.cellH, o.bg)
	}
	if err != nil {
		p.img, p.lines = nil, []string{"  cannot encode image: " + errText(err)}
	}
	return p
}

func audioPreview(p preview, path string, e entry, o previewOpts) preview {
	p.meta = humanSize(e.size)
	out, err := run("ffprobe", "-v", "error", "-show_entries", "format=duration,bit_rate:format_tags", "-of", "default=noprint_wrappers=1", path)
	if err != nil {
		p.lines = []string{"  audio file", "  " + humanSize(e.size)}
		return p
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || v == "" {
			continue
		}
		k = strings.TrimPrefix(strings.TrimPrefix(k, "TAG:"), "tag:")
		switch k {
		case "duration":
			var secs float64
			fmt.Sscanf(v, "%f", &secs)
			v = fmt.Sprintf("%d:%02d", int(secs)/60, int(secs)%60)
		case "bit_rate":
			var bps int
			fmt.Sscanf(v, "%d", &bps)
			v = fmt.Sprintf("%d kbps", bps/1000)
		}
		p.lines = append(p.lines, ansi.Truncate(fmt.Sprintf("  %-10s %s", strings.ToUpper(k[:1])+k[1:], sanitize(v)), o.width, "…"))
	}
	return p
}

func textPreview(p preview, path string, e entry, o previewOpts) preview {
	p.meta = humanSize(e.size)
	f, err := os.Open(path)
	if err != nil {
		p.lines = []string{"  cannot open: " + errText(err)}
		return p
	}
	defer f.Close()
	first := 1 // line number of the first line read
	var buf []byte
	if o.line > previewMaxLines/2 {
		// Content search can point anywhere in a big file: read a window
		// around the line instead of the start.
		first = o.line - previewMaxLines/4
		buf, err = readLinesFrom(f, first, previewMaxBytes)
	} else {
		buf, err = io.ReadAll(io.LimitReader(f, previewMaxBytes))
	}
	if err != nil {
		p.lines = []string{"  cannot read: " + errText(err)}
		return p
	}
	if len(buf) == 0 {
		p.lines = []string{"  (empty file)"}
		return p
	}
	if bytes.IndexByte(buf, 0) >= 0 {
		p.lines = []string{"  binary file", "  " + humanSize(e.size)}
		if out, err := run("file", "-b", path); err == nil {
			p.lines = append(p.lines, "", "  "+strings.TrimSpace(string(out)))
			p.lines = fitLines(p.lines, o, previewMaxLines)
		}
		return p
	}

	text := strings.ToValidUTF8(string(buf), "�")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.EqualFold(filepath.Ext(e.name), ".json") && e.size <= previewMaxBytes {
		var out bytes.Buffer
		if json.Indent(&out, []byte(text), "", "  ") == nil {
			text = out.String()
		}
	}
	src := strings.Split(text, "\n")
	if n := len(src); n > 1 && src[n-1] == "" {
		src = src[:n-1]
	}
	for i, l := range src {
		src[i] = expandTabs(sanitize(l))
	}
	joined := strings.Join(src, "\n")
	if hl, lang := highlight(joined, e.name, o.syn); lang != "" {
		joined = hl
		p.meta = lang + " · " + humanSize(e.size)
	}
	var starts []int
	p.lines, starts = fitLinesMap(strings.Split(joined, "\n"), o, previewMaxLines)
	head := 0
	if first > 1 {
		head = 2
		p.lines = append([]string{fmt.Sprintf("… from line %d", first), ""}, p.lines...)
	}
	if o.line > 0 {
		p.meta += fmt.Sprintf(" · line %d", o.line)
		if i := o.line - first; i >= 0 && i < len(starts) {
			p.hlFrom, p.hlTo = head+starts[i], head+len(p.lines)-head
			if i+1 < len(starts) {
				p.hlTo = head + starts[i+1]
			}
		}
	}
	if first > 1 || e.size > previewMaxBytes || len(p.lines) >= previewMaxLines {
		p.lines = append(p.lines, "", "… truncated")
	}
	return p
}

// readLinesFrom returns up to limit bytes of r starting at 1-based line n.
func readLinesFrom(r io.Reader, n, limit int) ([]byte, error) {
	br := bufio.NewReader(r)
	for line := 1; line < n; line++ {
		if _, err := br.ReadSlice('\n'); err != nil && err != bufio.ErrBufferFull {
			if err == io.EOF {
				return nil, nil
			}
			return nil, err
		} else if err == bufio.ErrBufferFull {
			line-- // a very long line: keep consuming it
		}
	}
	return io.ReadAll(io.LimitReader(br, int64(limit)))
}

// fitLines wraps (or truncates) each line to o.width, keeping ANSI styling.
func fitLines(src []string, o previewOpts, limit int) []string {
	out, _ := fitLinesMap(src, o, limit)
	return out
}

// fitLinesMap is fitLines that also reports where each source line starts in
// the output (source lines past the limit are missing from starts).
func fitLinesMap(src []string, o previewOpts, limit int) ([]string, []int) {
	out := make([]string, 0, len(src))
	starts := make([]int, 0, len(src))
	for _, line := range src {
		if len(out) >= limit {
			break
		}
		starts = append(starts, len(out))
		switch {
		case line == "":
			out = append(out, "")
		case o.wrap:
			out = append(out, strings.Split(ansi.Wrap(line, o.width, ""), "\n")...)
		default:
			out = append(out, ansi.Truncate(line, o.width, "…"))
		}
	}
	return out, starts
}

// sanitize drops control characters (notably ESC) so file content can never
// inject terminal escape sequences; tabs are kept for expandTabs.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			return r
		}
		return -1
	}, s)
}

func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

func errText(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return fmt.Sprint(err)
}
