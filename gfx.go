package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/sys/unix"
)

// gfxProto is how images are drawn in the preview pane.
type gfxProto int

const (
	protoBlocks gfxProto = iota // half-block characters, works in any truecolor terminal
	protoSixel                  // foot, wezterm, mlterm, contour, xterm
	protoKitty                  // kitty, ghostty
)

func (p gfxProto) String() string { return [...]string{"blocks", "sixel", "kitty"}[p] }

// detectProto picks the best image protocol for the running terminal.
// CONSOLE_PLACES_IMAGES=blocks|sixel|kitty overrides detection. Otherwise the
// terminal is asked directly (kitty-graphics probe + device attributes), which
// works even when $TERM does not name the terminal.
func detectProto() gfxProto {
	switch strings.ToLower(envVar("IMAGES")) {
	case "blocks", "off":
		return protoBlocks
	case "sixel":
		return protoSixel
	case "kitty":
		return protoKitty
	}
	term, prog := os.Getenv("TERM"), os.Getenv("TERM_PROGRAM")
	switch {
	case strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux"):
		return protoBlocks // multiplexers mangle graphics escapes
	case os.Getenv("KITTY_WINDOW_ID") != "" || strings.Contains(term, "kitty") ||
		strings.Contains(term, "ghostty") || prog == "ghostty":
		return protoKitty
	}
	return probeTerminal()
}

// probeTimeout bounds the wait for the terminal's answer to probeTerminal.
// The wait ends as soon as the answer arrives (normally a few milliseconds),
// but a terminal that is still starting up can take much longer: foot opened
// through Omarchy's launcher (uwsm-app, as a floating window) has answered
// after the old 250 ms limit, which silently dropped console-places to blurry
// half-block images for the whole session, and its late answer then arrived
// as keystrokes.
const probeTimeout = 1500 * time.Millisecond

// probeTerminal asks the terminal what it can draw. It sends a kitty-graphics
// query followed by a Primary Device Attributes request; every terminal
// answers the latter, so its reply marks the end of the wait.
func probeTerminal() gfxProto { return probeTerminalOn(os.Stdin, os.Stdout, probeTimeout) }

func probeTerminalOn(in, out *os.File, timeout time.Duration) gfxProto {
	fd := int(in.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return protoBlocks // not a terminal
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return protoBlocks
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, old)

	out.WriteString("\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[c")
	var reply []byte
	buf := make([]byte, 256)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && !bytes.Contains(reply, []byte("c")) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if n, _ := unix.Poll(fds, int(time.Until(deadline).Milliseconds())); n <= 0 {
			break
		}
		n, err := unix.Read(fd, buf)
		if err != nil || n == 0 {
			break
		}
		reply = append(reply, buf[:n]...)
	}
	return protoFromReply(string(reply))
}

// protoFromReply interprets the terminal's answer to probeTerminal.
func protoFromReply(reply string) gfxProto {
	if strings.Contains(reply, "\x1b_Gi=31;OK") {
		return protoKitty
	}
	if i := strings.Index(reply, "\x1b[?"); i >= 0 {
		attrs, _, _ := strings.Cut(reply[i+3:], "c")
		for _, a := range strings.Split(attrs, ";") {
			if a == "4" {
				return protoSixel
			}
		}
	}
	return protoBlocks
}

// cellPixels returns the size of one terminal cell in pixels.
func cellPixels() (w, h int) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err == nil && ws.Col > 0 && ws.Row > 0 && ws.Xpixel > 0 && ws.Ypixel > 0 {
		return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
	}
	return 10, 20
}

// imgSeq is an encoded image ready to place, and the cells it covers.
type imgSeq struct {
	seq        string
	cols, rows int
}

// overlay is an image to keep on screen at a cell position.
type overlay struct {
	key              string
	seq              string
	row, col         int // top-left cell of the image (0-based)
	clrRow, clrCol   int // cell rectangle to blank when the image goes away
	clrCols, clrRows int
	kitty            bool
	frameOf          string // set on animation frames: the animation's key
}

// gfxOut wraps the terminal so out-of-band images (sixel/kitty) can be drawn
// right after each frame Bubble Tea flushes. Because the frame and the image
// go out in one write, the image is never erased by a line redraw and never
// flickers. It embeds *os.File so Bubble Tea still sees a real terminal.
type gfxOut struct {
	*os.File
	mu    sync.Mutex // held across each terminal write: Write (renderer) and flush (animation)
	want  *overlay
	drawn *overlay
	bg    string // SGR sequence for the blank fill when erasing
}

func newGfxOut(f *os.File) *gfxOut { return &gfxOut{File: f} }

// forget is for after the screen was cleared: the image is gone, so the
// next flush draws it again.
func (g *gfxOut) forget() {
	g.mu.Lock()
	g.drawn = nil
	g.mu.Unlock()
}

type gfxRedrawMsg struct{}

// set declares which image should currently be visible (nil for none).
func (g *gfxOut) set(o *overlay, bg lipgloss.Color) {
	g.mu.Lock()
	g.want, g.bg = o, bgSGR(bg)
	g.mu.Unlock()
}

func bgSGR(c lipgloss.Color) string {
	if r, gr, b, ok := rgb(c); ok {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", int(r), int(gr), int(b))
	}
	return ""
}

func (g *gfxOut) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.want == nil && g.drawn == nil {
		return g.File.Write(p)
	}
	var b bytes.Buffer
	g.eraseStale(&b)
	b.Write(p)
	if g.want != nil {
		b.WriteString(drawSeq(g.want))
		g.drawn = g.want
	}
	if _, err := g.File.Write(b.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// flush draws the wanted image now if it is not the one on screen: an
// animation frame changes only the image, so Bubble Tea has nothing to write.
func (g *gfxOut) flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.want == nil || (g.drawn != nil && g.drawn.key == g.want.key) {
		return
	}
	var b bytes.Buffer
	g.eraseStale(&b)
	b.WriteString(drawSeq(g.want))
	g.drawn = g.want
	g.File.Write(b.Bytes())
}

// eraseStale blanks the drawn image unless the wanted one replaces it in
// place (the next animation frame), which would only make it flicker.
func (g *gfxOut) eraseStale(b *bytes.Buffer) {
	d, w := g.drawn, g.want
	if d == nil || (w != nil && (w.key == d.key || samePlace(w, d))) {
		return
	}
	b.WriteString(eraseSeq(d, g.bg))
	g.drawn = nil
}

func samePlace(a, b *overlay) bool {
	return a.frameOf != "" && a.frameOf == b.frameOf && a.row == b.row && a.col == b.col
}

func drawSeq(o *overlay) string {
	var b strings.Builder
	b.WriteString("\x1b7") // save cursor
	if !o.kitty {
		// Scrolling mode (the default): sixels are drawn at the cursor. With
		// mode 80 set they would all land at the top-left corner instead.
		b.WriteString("\x1b[?80l")
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH", o.row+1, o.col+1)
	b.WriteString(o.seq)
	b.WriteString("\x1b8")
	return b.String()
}

func eraseSeq(o *overlay, bg string) string {
	var b strings.Builder
	b.WriteString("\x1b7")
	if o.kitty {
		b.WriteString("\x1b_Ga=d,d=A,q=2\x1b\\")
	}
	blank := strings.Repeat(" ", o.clrCols)
	for r := 0; r < o.clrRows; r++ {
		fmt.Fprintf(&b, "\x1b[%d;%dH%s%s\x1b[0m", o.clrRow+r+1, o.clrCol+1, bg, blank)
	}
	b.WriteString("\x1b8")
	return b.String()
}
