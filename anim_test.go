package main

import (
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

var (
	red   = color.RGBA{255, 0, 0, 255}
	blue  = color.RGBA{0, 0, 255, 255}
	green = color.RGBA{0, 255, 0, 255}
)

func gifFrame(r image.Rectangle, c color.RGBA) *image.Paletted {
	img := image.NewPaletted(r, color.Palette{color.Transparent, red, blue, green})
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// testGIF is 4×4: a red frame, a blue 2×2 patch top-left that is cleared
// afterwards, then a green dot bottom-right drawn over the restored canvas.
func testGIF(disposal byte) *gif.GIF {
	return &gif.GIF{
		Image: []*image.Paletted{
			gifFrame(image.Rect(0, 0, 4, 4), red),
			gifFrame(image.Rect(0, 0, 2, 2), blue),
			gifFrame(image.Rect(3, 3, 4, 4), green),
		},
		Delay:    []int{5, 0, 10},
		Disposal: []byte{gif.DisposalNone, disposal, gif.DisposalNone},
		Config:   image.Config{Width: 4, Height: 4},
	}
}

func writeGIF(t *testing.T, dir, name string, g *gif.GIF) entry {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := gif.EncodeAll(f, g); err != nil {
		t.Fatal(err)
	}
	f.Close()
	ents, _ := readDir(dir, true)
	for _, e := range ents {
		if e.name == name {
			return e
		}
	}
	t.Fatal("gif not listed")
	return entry{}
}

func TestGifCanvasFollowsDisposal(t *testing.T) {
	at := func(img *image.RGBA, x, y int) color.RGBA { return img.RGBAAt(x, y) }
	clear := color.RGBA{}
	for _, tc := range []struct {
		name     string
		disposal byte
		topLeft  color.RGBA // after the third frame
	}{
		{"background", gif.DisposalBackground, clear},
		{"previous", gif.DisposalPrevious, red},
		{"none", gif.DisposalNone, blue},
	} {
		var got []color.RGBA
		gifCanvas(testGIF(tc.disposal), func(i int, img *image.RGBA) bool {
			got = append(got, at(img, 0, 0), at(img, 3, 3))
			return true
		})
		want := []color.RGBA{red, red, blue, red, tc.topLeft, green}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: pixel %d = %v, want %v", tc.name, i, got[i], want[i])
			}
		}
	}
}

func TestGifDelay(t *testing.T) {
	g := testGIF(gif.DisposalNone)
	if d := gifDelay(g, 0); d != 50*time.Millisecond {
		t.Errorf("frame 0: %v", d)
	}
	if d := gifDelay(g, 1); d != animDefaultDelay {
		t.Errorf("0 delay should be clamped, got %v", d)
	}
}

// playing returns a model showing dir's GIF with its frames all encoded.
func playing(t *testing.T, dir string, e entry) model {
	t.Helper()
	m := testModel(t, dir)
	m.pv = buildPreview(dir, e, m.previewOpts())
	if m.pv.anim == nil {
		t.Fatal("no animation for a multi-frame GIF")
	}
	m.pv.key, m.pvKey = "k", "k"
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, _, more := m.pv.anim.frame(0); !more {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("frames never finished encoding")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return m
}

func TestGifPreviewPlaysAndLoops(t *testing.T) {
	dir := t.TempDir()
	m := playing(t, dir, writeGIF(t, dir, "a.gif", testGIF(gif.DisposalBackground)))
	if len(m.pv.anim.frames) != 3 {
		t.Fatalf("%d frames, want 3", len(m.pv.anim.frames))
	}
	a := m.pv.anim
	var seen []int
	msg := animTickMsg{a: a}
	for range 5 {
		if m.stepAnim(msg) == nil {
			t.Fatal("a looping GIF stopped")
		}
		seen = append(seen, m.pv.frame)
		if m.pv.lines == nil || len(m.pv.lines[0]) == 0 {
			t.Fatal("frame has no picture")
		}
		msg.i = m.pv.frame + 1 // past the end wraps to frame 0
	}
	if want := []int{0, 1, 2, 0, 1}; !slices.Equal(seen, want) {
		t.Errorf("frames %v, want %v", seen, want)
	}
}

func TestGifPlaysOnceWhenAsked(t *testing.T) {
	dir := t.TempDir()
	g := testGIF(gif.DisposalNone)
	g.LoopCount = -1
	m := playing(t, dir, writeGIF(t, dir, "once.gif", g))
	if m.stepAnim(animTickMsg{a: m.pv.anim, i: 2}) == nil {
		t.Fatal("the last frame should still be shown")
	}
	if m.stepAnim(animTickMsg{a: m.pv.anim, i: 3}) != nil || !m.pv.anim.stopped.Load() {
		t.Error("a play-once GIF kept going after its last frame")
	}
	if m.pv.frame != 2 {
		t.Errorf("stopped on frame %d, want the last", m.pv.frame)
	}
}

func TestAnimationStopsWhenPreviewChanges(t *testing.T) {
	dir := t.TempDir()
	m := playing(t, dir, writeGIF(t, dir, "a.gif", testGIF(gif.DisposalNone)))
	a := m.pv.anim
	m.pv = preview{key: "other"}
	if m.stepAnim(animTickMsg{a: a, i: 1}) != nil || !a.stopped.Load() {
		t.Error("a tick for an animation no longer shown should end it")
	}

	// A preview that arrives after the selection moved on is dropped and its
	// encoder stopped.
	m = playing(t, dir, writeGIF(t, dir, "b.gif", testGIF(gif.DisposalNone)))
	pv := m.pv
	pv.key = "stale"
	m.pv = preview{}
	m2, _ := m.Update(previewReadyMsg{pv})
	if m2.(model).pv.anim != nil || !pv.anim.stopped.Load() {
		t.Error("a stale animated preview was kept running")
	}
}

func TestSingleFrameGifIsStill(t *testing.T) {
	dir := t.TempDir()
	g := testGIF(gif.DisposalNone)
	g.Image, g.Delay, g.Disposal = g.Image[:1], g.Delay[:1], g.Disposal[:1]
	e := writeGIF(t, dir, "still.gif", g)
	m := testModel(t, dir)
	pv := buildPreview(dir, e, m.previewOpts())
	if pv.anim != nil || len(pv.lines) == 0 {
		t.Errorf("single frame: anim=%v lines=%d", pv.anim != nil, len(pv.lines))
	}
}
