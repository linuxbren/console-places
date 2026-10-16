package main

// Video previews that play. The preview shows a still frame first; after a
// moment on the file (or alt+p) ffmpeg decodes the video, muted, at
// videoFPS and at the preview's own size, into raw RGBA frames on a pipe.
// A goroutine encodes each frame for the image protocol (as GIF frames are)
// and hands it over; Bubble Tea shows it and waits for the next. ffmpeg
// paces itself (-re), and a full pipe holds it back, so nothing piles up.
//
// One decoder at most: it is killed when the preview shows something else,
// is hidden, a dialog opens, on alt+p, and on quit (and with console-places, via
// Pdeathsig). Pausing kills it too; playing again restarts ffmpeg where it
// stopped. No seeking and no sound: enter opens a real player.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	videoFPS   = 10
	videoDelay = 1200 * time.Millisecond // resting on a video this long plays it
	// Videos start this far in, where the still frame (ffmpegthumbnailer's
	// default) comes from: the first seconds are often black or a title.
	videoStartAt = 0.10
)

// Values of config.VideoPlay.
const (
	videoPlayAuto = ""    // play after a moment on the file
	videoPlayKey  = "key" // play with alt+p only
	videoPlayOff  = "off" // a still frame, never played
)

// videoPlayer is a video in the preview, playing or not.
type videoPlayer struct {
	path     string
	w, h     int // decoded size in pixels
	start    float64
	duration float64 // seconds, 0 if unknown
	encode   func(image.Image) (animFrame, int, error)

	run    *videoRun // the running decoder, nil when stopped
	shown  int       // frames shown since start
	paused bool      // by the user (alt+p): don't start by itself
	armed  bool      // an autoplay timer is pending
	ended  bool
	err    string
}

// videoRun is one ffmpeg process; its pointer tells frames from a stopped
// run apart from the current one.
type videoRun struct {
	cancel context.CancelFunc
	frames chan animFrame
	stderr *bytes.Buffer
}

type (
	videoFrameMsg struct {
		v   *videoPlayer
		run *videoRun
		f   animFrame
		ok  bool // false: the run ended
	}
	videoAutoMsg struct{ v *videoPlayer }
)

// pos is where playback has got to, in seconds.
func (v *videoPlayer) pos() float64 { return v.start + float64(v.shown)/videoFPS }

// videoArgs decodes from second at, muted, to w×h RGBA frames on stdout.
func videoArgs(path string, at float64, w, h int) []string {
	return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-hwaccel", "auto",
		"-ss", fmt.Sprintf("%.3f", at), "-re", "-i", path,
		"-an", "-sn", "-dn", "-vf", fmt.Sprintf("fps=%d,scale=%d:%d:flags=bilinear", videoFPS, w, h),
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1"}
}

// play starts ffmpeg where playback stopped and returns the command that
// waits for the first frame.
func (v *videoPlayer) play() tea.Cmd {
	v.stop()
	if v.ended {
		v.ended, v.shown = false, 0 // play again from the start point
	}
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		v.err = "ffmpeg not installed"
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := exec.CommandContext(ctx, bin, videoArgs(v.path, v.pos(), v.w, v.h)...)
	c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} // never outlive console-places
	r := &videoRun{cancel: cancel, frames: make(chan animFrame, 1), stderr: &bytes.Buffer{}}
	c.Stderr = r.stderr
	out, err := c.StdoutPipe()
	if err == nil {
		err = c.Start()
	}
	if err != nil {
		cancel()
		v.err = errText(err)
		return nil
	}
	v.run, v.err, v.paused = r, "", false
	go func() {
		defer close(r.frames)
		defer c.Wait() //nolint:errcheck // killed on stop; errors are in stderr
		rd := bufio.NewReaderSize(out, v.w*v.h*4)
		for ctx.Err() == nil {
			img := image.NewRGBA(image.Rect(0, 0, v.w, v.h))
			if _, err := io.ReadFull(rd, img.Pix); err != nil {
				return
			}
			f, _, err := v.encode(img)
			if err != nil {
				return
			}
			select {
			case r.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return v.next(r)
}

// next waits for the run's next frame.
func (v *videoPlayer) next(r *videoRun) tea.Cmd {
	return func() tea.Msg {
		f, ok := <-r.frames
		return videoFrameMsg{v: v, run: r, f: f, ok: ok}
	}
}

// stop kills the decoder, if one runs; the frame on screen stays.
func (v *videoPlayer) stop() {
	if v.run != nil {
		v.run.cancel()
		v.run = nil
	}
}

func (v *videoPlayer) playing() bool { return v.run != nil }

// onVideoFrame shows a frame of the playing video and waits for the next.
func (m *model) onVideoFrame(msg videoFrameMsg) tea.Cmd {
	v := msg.v
	if msg.run != v.run { // from a run that was stopped
		return nil
	}
	if v != m.pv.video {
		v.stop()
		return nil
	}
	if !msg.ok {
		v.run = nil
		if v.shown == 0 {
			v.err = lastLine(strings.TrimSpace(msg.run.stderr.String()))
			if v.err == "" {
				v.err = "ffmpeg sent no frames"
			}
			m.setMsg("can't play "+m.pv.title+": "+v.err, true)
			return nil
		}
		v.ended = true
		return nil
	}
	v.shown++
	m.pv.img, m.pv.lines, m.pv.blocks, m.pv.frame = msg.f.img, msg.f.lines, msg.f.img == nil, v.shown
	if msg.f.img != nil {
		m.drawGraphicsNow() // the text frame is unchanged, so Bubble Tea would not redraw
	}
	return v.next(msg.run)
}

// videoCanPlay says whether the preview's video may play now: on screen,
// with no dialog over it.
func (m model) videoCanPlay() bool {
	return m.pv.video != nil && !m.cfg.HidePreview && m.modal == nil && !m.pickerOpen && m.pv.key == m.pvKey
}

// syncVideo keeps playback in step with the screen after every update: a
// video that is no longer shown (or is covered) stops, and one that is
// shown and resting gets an autoplay timer.
func (m *model) syncVideo() tea.Cmd {
	if m.vid != nil && (m.vid != m.pv.video || !m.videoCanPlay()) {
		m.vid.stop()
		m.vid.armed = false // a covered video may start again later
		m.vid = nil
	}
	v := m.pv.video
	if v == nil || !m.videoCanPlay() {
		return nil
	}
	if v.playing() {
		m.vid = v
		return nil
	}
	if m.cfg.VideoPlay != videoPlayAuto || v.paused || v.ended || v.armed || v.err != "" {
		return nil
	}
	v.armed = true
	return tea.Tick(videoDelay, func(time.Time) tea.Msg { return videoAutoMsg{v} })
}

func (m *model) onVideoAuto(msg videoAutoMsg) tea.Cmd {
	v := msg.v
	v.armed = false
	if v != m.pv.video || !m.videoCanPlay() || v.playing() || v.paused || v.ended {
		return nil
	}
	m.vid = v
	return v.play()
}

// togglePlay is alt+p: play or pause the video or GIF in the preview.
func (m *model) togglePlay() tea.Cmd {
	switch {
	case m.pv.video != nil && m.pv.video.playing():
		v := m.pv.video
		v.stop()
		v.paused = true
		m.vid = nil
		m.setMsg("paused at "+clock(v.pos())+" · alt+p plays", false)
	case m.pv.video != nil:
		if m.cfg.HidePreview {
			m.setMsg("the preview is hidden (p shows it)", false)
			return nil
		}
		v := m.pv.video
		cmd := v.play()
		if cmd == nil {
			m.setMsg("can't play: "+v.err, true)
			return nil
		}
		m.vid = v
		return cmd
	case m.pv.anim != nil:
		a := m.pv.anim
		if a.paused.Load() {
			a.paused.Store(false)
			return animTick(a, m.pv.frame+1, a.loop, 0)
		}
		a.paused.Store(true)
		m.setMsg("paused · alt+p plays", false)
	default:
		m.setMsg("alt+p plays a video or an animated GIF in the preview", false)
	}
	return nil
}

// videoPreview shows a video's details and a still frame, and makes it
// playable when ffmpeg is here.
func videoPreview(p preview, path string, e entry, o previewOpts) preview {
	vm := probeVideo(path)
	var info []string
	p.meta, info = videoInfo(vm, e)
	data, err := videoStill(path, vm.secs*videoStartAt)
	var img image.Image
	if err == nil {
		img, err = png.Decode(bytes.NewReader(data))
	}
	p = stillPreview(p, img, 0, 0, err, kindVideo, e, o, info)
	if err != nil || o.videoPlay == videoPlayOff {
		return p
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return p
	}
	w, h := vm.w, vm.h
	if w == 0 || h == 0 { // no ffprobe: the still has the shape
		b := img.Bounds()
		w, h = b.Dx(), b.Dy()
	}
	encode, pw, ph := frameEncoder(o, w, h)
	p.video = &videoPlayer{path: path, w: pw, h: ph, start: vm.secs * videoStartAt, duration: vm.secs, encode: encode}
	return p
}
