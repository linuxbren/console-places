package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-sixel"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	maxImagePixels = 80_000_000
	toolTimeout    = 8 * time.Second
	maxUpscale     = 3.0
)

// run executes a helper tool and returns its stdout.
func run(name string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s not installed", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	return exec.CommandContext(ctx, bin, args...).Output()
}

// loadImage decodes a still image or a PDF's first page (videos: videoStill).
// It returns the image and the source's true pixel size when known.
func loadImage(path string, kind previewKind) (img image.Image, srcW, srcH int, err error) {
	ext := strings.ToLower(filepath.Ext(path))
	var data []byte
	switch {
	case kind == kindPDF:
		data, err = run("pdftoppm", "-png", "-f", "1", "-l", "1", "-r", "110", "-singlefile", path)
	case ext == ".png" || ext == ".gif" || ext == ".bmp" || ext == ".webp":
		return decodeFile(path)
	default: // jpeg (needs EXIF rotation), svg, heic, avif, tiff, ico, psd…
		w, h := imageConfig(path)
		data, err = run("magick", path+"[0]", "-auto-orient", "-resize", "2400x2400>", "png:-")
		if err != nil {
			if w > 0 { // magick missing: try the standard decoders
				if i, _, _, derr := decodeFile(path); derr == nil {
					return i, w, h, nil
				}
			}
			return nil, 0, 0, err
		}
		img, err = png.Decode(bytes.NewReader(data))
		b := imgBounds(img)
		if w == 0 {
			w, h = b.w, b.h
		}
		return img, w, h, err
	}
	if err != nil {
		return nil, 0, 0, err
	}
	img, err = png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	b := imgBounds(img)
	return img, b.w, b.h, nil
}

// videoStill is a PNG frame from about 10% into a video: from
// ffmpegthumbnailer, or ffmpeg (at second at) when that isn't installed.
func videoStill(path string, at float64) ([]byte, error) {
	data, err := run("ffmpegthumbnailer", "-i", path, "-o", "-", "-c", "png", "-s", "1280", "-f")
	if _, lerr := exec.LookPath("ffmpegthumbnailer"); lerr == nil {
		return data, err
	}
	if _, lerr := exec.LookPath("ffmpeg"); lerr != nil {
		return nil, err // say ffmpegthumbnailer is missing
	}
	return run("ffmpeg", "-nostdin", "-loglevel", "error", "-ss", fmt.Sprintf("%.3f", at), "-i", path,
		"-frames:v", "1", "-vf", "scale='min(1280,iw)':-2", "-f", "image2pipe", "-vcodec", "png", "pipe:1")
}

type dims struct{ w, h int }

func imgBounds(img image.Image) dims {
	if img == nil {
		return dims{}
	}
	b := img.Bounds()
	return dims{b.Dx(), b.Dy()}
}

func imageConfig(path string) (w, h int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func decodeFile(path string) (image.Image, int, int, error) {
	w, h := imageConfig(path)
	if w*h > maxImagePixels {
		return nil, w, h, errors.New("image too large to preview")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, w, h, err
}

// fitSize scales w×h to fit inside maxW×maxH, preserving aspect ratio.
func fitSize(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0 {
		return 1, 1
	}
	scale := min(float64(maxW)/float64(w), float64(maxH)/float64(h), maxUpscale)
	return max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
}

// scaleOnto resamples img to w×h, composited over bg so transparent images
// look right on the theme background.
func scaleOnto(img image.Image, w, h int, bg lipgloss.Color) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if r, g, b, ok := rgb(bg); ok {
		draw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{uint8(r), uint8(g), uint8(b), 255}), image.Point{}, draw.Src)
	}
	sb := img.Bounds()
	kernel := draw.Interpolator(draw.CatmullRom)
	if w > sb.Dx() { // enlarging: avoid ringing
		kernel = draw.BiLinear
	}
	kernel.Scale(dst, dst.Bounds(), img, sb, draw.Over, nil)
	return dst
}

// renderBlocks draws img with half-block characters: each cell is two pixels
// stacked (foreground = top, background = bottom). cellW×cellH is the cell's
// pixel size, used to keep the picture's aspect ratio right.
func renderBlocks(img image.Image, cols, rows, cellW, cellH int, bg lipgloss.Color) []string {
	sb := img.Bounds()
	pxW, pxH := fitSize(sb.Dx(), sb.Dy(), cols*cellW, rows*cellH)
	w := max(1, min(cols, (pxW+cellW/2)/max(1, cellW)))
	h := max(2, min(rows*2, (pxH*2+cellH/2)/max(1, cellH)))
	if h%2 == 1 {
		h++
	}
	px := scaleOnto(img, w, h, bg)
	pad := strings.Repeat(" ", max(0, (cols-w)/2))
	lines := make([]string, 0, h/2)
	for y := 0; y < h; y += 2 {
		var b strings.Builder
		b.WriteString(pad)
		for x := 0; x < w; x++ {
			t, u := px.RGBAAt(x, y), px.RGBAAt(x, y+1)
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", t.R, t.G, t.B, u.R, u.G, u.B)
		}
		b.WriteString("\x1b[0m")
		lines = append(lines, b.String())
	}
	return lines
}

// renderSixel encodes img to fit cols×rows cells of cellW×cellH pixels.
func renderSixel(img image.Image, cols, rows, cellW, cellH int, bg lipgloss.Color) (*imgSeq, error) {
	sb := img.Bounds()
	w, h := fitSize(sb.Dx(), sb.Dy(), cols*cellW, rows*cellH)
	if h > 6 {
		h -= h % 6 // sixel bands are 6px tall; a partial last band is padded with black
	}
	px := scaleOnto(img, w, h, bg)
	var buf bytes.Buffer
	enc := sixel.NewEncoder(&buf)
	enc.Dither = true
	if err := enc.Encode(px); err != nil {
		return nil, err
	}
	return &imgSeq{seq: buf.String(), cols: ceilDiv(w, cellW), rows: ceilDiv(h, cellH)}, nil
}

// renderKitty encodes img as a kitty-graphics PNG placement at the cursor.
func renderKitty(img image.Image, cols, rows, cellW, cellH int, bg lipgloss.Color) (*imgSeq, error) {
	sb := img.Bounds()
	w, h := fitSize(sb.Dx(), sb.Dy(), cols*cellW, rows*cellH)
	px := scaleOnto(img, w, h, bg)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, px); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	c, r := ceilDiv(w, cellW), ceilDiv(h, cellH)
	var b strings.Builder
	const chunk = 4096
	for i := 0; i < len(enc); i += chunk {
		end := min(len(enc), i+chunk)
		more := 1
		if end == len(enc) {
			more = 0
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=T,f=100,i=1,c=%d,r=%d,C=1,q=2,m=%d;%s\x1b\\", c, r, more, enc[i:end])
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, enc[i:end])
		}
	}
	return &imgSeq{seq: b.String(), cols: c, rows: r}, nil
}

func ceilDiv(a, b int) int { return (a + b - 1) / max(1, b) }
