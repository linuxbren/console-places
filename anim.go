package main

// Animated GIF previews. The preview build decodes every frame, composites
// each onto a full canvas (GIF frames are often partial rectangles with a
// disposal rule) and encodes it for the image protocol in use. The first
// frame is ready with the preview; the rest are encoded in the background
// while playback starts, and a tea.Tick steps through them. Playback stops
// as soon as the preview shows something else.

import (
	"errors"
	"fmt"
	"image"
	"image/gif"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/image/draw"
)

const (
	maxAnimFrames = 1000
	maxAnimBytes  = 64 << 20 // encoded frames kept in memory, all protocols
	// GIFs with a delay of 0 or 1 hundredths are shown at 10 fps, as browsers do.
	animDefaultDelay = 100 * time.Millisecond
	animMinDelay     = 20 * time.Millisecond
	// Sixel and kitty send the whole picture every frame: cap the frame rate
	// (frames in between are skipped, so timing stays right) and the size.
	animGfxInterval = 50 * time.Millisecond
	animGfxMaxPx    = 640 * 480
	animWait        = 40 * time.Millisecond  // frame not encoded yet
	animPaused      = 250 * time.Millisecond // a modal covers the preview
)

type animFrame struct {
	img   *imgSeq  // sixel / kitty
	lines []string // half blocks
	delay time.Duration
}

// animation holds a GIF's encoded frames; a background encoder appends to it.
type animation struct {
	mu      sync.Mutex
	frames  []animFrame
	done    bool // no more frames are coming
	loops   int  // gif.GIF.LoopCount: 0 forever, -1 once, n: n extra times
	stopped atomic.Bool
}

// frame returns frame i if it is encoded yet, and whether more may come.
func (a *animation) frame(i int) (animFrame, bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i < len(a.frames) {
		return a.frames[i], true, !a.done
	}
	return animFrame{}, false, !a.done
}

func (a *animation) stop() { a.stopped.Store(true) }

type animTickMsg struct {
	a    *animation
	i    int // frame to show
	loop int // restarts so far
}

func animTick(a *animation, i, loop int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return animTickMsg{a, i, loop} })
}

// decodeGIF reads every frame of a GIF, refusing ones too big to animate.
func decodeGIF(path string) (*gif.GIF, error) {
	w, h := imageConfig(path)
	if w*h > maxImagePixels {
		return nil, errors.New("image too large to preview")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return gif.DecodeAll(f)
}

// gifDelay converts a frame delay (hundredths of a second) to a duration.
func gifDelay(g *gif.GIF, i int) time.Duration {
	if i >= len(g.Delay) {
		return animDefaultDelay
	}
	d := time.Duration(g.Delay[i]) * 10 * time.Millisecond
	if d < animMinDelay {
		return animDefaultDelay
	}
	return d
}

// gifCanvas composites a GIF's frames in order, calling emit with the full
// picture after each one. emit must not keep the image: it is reused.
func gifCanvas(g *gif.GIF, emit func(i int, img *image.RGBA) bool) {
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if bounds.Empty() {
		for _, fr := range g.Image {
			bounds = bounds.Union(fr.Bounds())
		}
	}
	canvas := image.NewRGBA(bounds)
	var saved *image.RGBA
	for i, fr := range g.Image {
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		if disposal == gif.DisposalPrevious {
			saved = image.NewRGBA(bounds)
			copy(saved.Pix, canvas.Pix)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		if !emit(i, canvas) {
			return
		}
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, fr.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			copy(canvas.Pix, saved.Pix)
		}
	}
}

// gifPreview shows a GIF; one with several frames gets an animation whose
// first frame is in the returned preview and the rest are encoded behind it.
func gifPreview(p preview, path string, e entry, o previewOpts) preview {
	g, err := decodeGIF(path)
	if err != nil || len(g.Image) < 2 {
		return imagePreview(p, path, e, kindImage, o) // still image, or let the usual decoder report the error
	}
	w, h := g.Config.Width, g.Config.Height
	p.meta = fmt.Sprintf("%d×%d · %d frames · %s", w, h, len(g.Image), humanSize(e.size))

	cols, rows := o.width, o.rows
	if o.proto != protoBlocks {
		pw, ph := fitSize(w, h, cols*o.cellW, rows*o.cellH)
		if pw*ph > animGfxMaxPx {
			s := math.Sqrt(float64(animGfxMaxPx) / float64(pw*ph))
			cols, rows = max(1, int(float64(cols)*s)), max(1, int(float64(rows)*s))
		}
	}
	encode := func(img image.Image) (animFrame, int, error) {
		switch o.proto {
		case protoSixel:
			s, err := renderSixel(img, cols, rows, o.cellW, o.cellH, o.bg)
			if err != nil {
				return animFrame{}, 0, err
			}
			return animFrame{img: s}, len(s.seq), nil
		case protoKitty:
			s, err := renderKitty(img, cols, rows, o.cellW, o.cellH, o.bg)
			if err != nil {
				return animFrame{}, 0, err
			}
			return animFrame{img: s}, len(s.seq), nil
		}
		lines := renderBlocks(img, cols, rows, o.cellW, o.cellH, o.bg)
		n := 0
		for _, l := range lines {
			n += len(l)
		}
		return animFrame{lines: lines}, n, nil
	}

	a := &animation{loops: g.LoopCount}
	first := make(chan error, 1)
	go func() {
		size := 0
		gifCanvas(g, func(i int, img *image.RGBA) bool {
			if a.stopped.Load() {
				return false
			}
			f, n, err := encode(img)
			if err != nil {
				if i == 0 {
					first <- err
				}
				return false
			}
			f.delay = gifDelay(g, i)
			size += n
			a.mu.Lock()
			a.frames = append(a.frames, f)
			a.mu.Unlock()
			if i == 0 {
				first <- nil
			}
			return i+1 < maxAnimFrames && size < maxAnimBytes
		})
		a.mu.Lock()
		a.done = true
		a.mu.Unlock()
	}()
	if err := <-first; err != nil {
		a.stop()
		p.lines = []string{"  cannot encode image: " + errText(err)}
		return p
	}
	f, _, _ := a.frame(0)
	p.img, p.lines, p.anim = f.img, f.lines, a
	return p
}

// stepAnim shows frame msg.i of the playing GIF and schedules the next one.
// A tick for an animation that is no longer on screen ends it.
func (m *model) stepAnim(msg animTickMsg) tea.Cmd {
	a := msg.a
	if a != m.pv.anim || m.cfg.HidePreview {
		a.stop()
		return nil
	}
	if m.modal != nil || m.pickerOpen || m.tsel != nil {
		return animTick(a, msg.i, msg.loop, animPaused)
	}
	f, ok, more := a.frame(msg.i)
	if !ok {
		if more {
			return animTick(a, msg.i, msg.loop, animWait)
		}
		if a.loops != 0 && msg.loop >= max(0, a.loops) {
			a.stop() // played as often as the GIF asks; the last frame stays
			return nil
		}
		msg.i, msg.loop = 0, msg.loop+1
		f, _, _ = a.frame(0)
	}
	m.pv.img, m.pv.lines, m.pv.frame = f.img, f.lines, msg.i
	if f.img != nil {
		m.drawGraphicsNow() // the text frame is unchanged, so Bubble Tea would not redraw
	}
	wait, next := f.delay, msg.i+1
	if m.proto != protoBlocks {
		for wait < animGfxInterval {
			g, ok, _ := a.frame(next)
			if !ok {
				break
			}
			wait += g.delay
			next++
		}
	}
	return animTick(a, next, msg.loop, wait)
}
