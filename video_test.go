package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeVideoTools puts fake ffprobe and ffmpeg (and nothing else of theirs)
// on PATH: a 64×36, 10 s video. ffmpeg logs its arguments and pid, answers a
// still-frame request with a PNG, and otherwise writes frames raw frames of
// the size its scale filter asks for, delay seconds apart.
func fakeVideoTools(t *testing.T, frames int, delay string) (dir, log string) {
	t.Helper()
	tools := t.TempDir()
	bin := filepath.Join(tools, "bin")
	os.MkdirAll(bin, 0o755)
	for _, tool := range []string{"sh", "cat", "head", "sleep", "echo"} {
		if p, err := execLook(tool); err == nil {
			os.Symlink(p, filepath.Join(bin, tool))
		}
	}
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 64, 36)))
	still := filepath.Join(tools, "still.png")
	os.WriteFile(still, pngBuf.Bytes(), 0o644)
	log = filepath.Join(tools, "ffmpeg.log")
	write := func(name, script string) {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755)
	}
	write("ffprobe", "echo codec_name=h264\necho width=64\necho height=36\necho duration=10.000000\n")
	write("ffmpeg", `echo "$$ $*" >> `+log+`
case "$*" in *image2pipe*) cat `+still+`; exit 0 ;; esac
for a in "$@"; do case "$a" in fps=*) vf=$a ;; esac; done
s=${vf#*scale=}; s=${s%%:flags*}; w=${s%%:*}; h=${s#*:}
i=0
while [ $i -lt `+strconv.Itoa(frames)+` ]; do
  head -c $((w*h*4)) /dev/zero
  sleep `+delay+`
  i=$((i+1))
done
`)
	t.Setenv("PATH", bin)
	dir = t.TempDir()
	mkfile(t, filepath.Join(dir, "clip.mp4"), "not really a video")
	return dir, log
}

func execLook(name string) (string, error) {
	for _, d := range []string{"/usr/bin", "/bin"} {
		if p := filepath.Join(d, name); fileExists(p) {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// videoModel shows clip.mp4's preview, built as Bubble Tea would.
func videoModel(t *testing.T, dir string) model {
	t.Helper()
	m := testModel(t, dir)
	m.refreshPreview()
	pv := m.buildPreviewCmd(m.pvKey)().(previewReadyMsg)
	next, _ := m.Update(pv)
	m = next.(model)
	if m.pv.video == nil {
		t.Fatalf("no player: %+v", m.pv)
	}
	return m
}

// frame waits for the player's next message.
func frame(t *testing.T, cmd tea.Cmd) videoFrameMsg {
	t.Helper()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg.(videoFrameMsg)
	case <-time.After(5 * time.Second):
		t.Fatal("no frame")
	}
	return videoFrameMsg{}
}

func TestVideoPlaysToTheEnd(t *testing.T) {
	dir, log := fakeVideoTools(t, 3, "0")
	m := videoModel(t, dir)
	if !strings.Contains(m.pv.meta, "64×36 · 0:10 · h264") || !m.pv.blocks {
		t.Fatalf("still preview: %q blocks=%v", m.pv.meta, m.pv.blocks)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "-ss 1.000 -i") {
		t.Fatalf("still from ffmpeg 10%% in (no ffmpegthumbnailer here): %s", b)
	}

	// Showing it armed autoplay; the timer plays it.
	if !m.pv.video.armed {
		t.Fatal("autoplay not armed")
	}
	cmd := m.onVideoAuto(videoAutoMsg{m.pv.video})
	shown := 0
	for cmd != nil {
		msg := frame(t, cmd)
		if msg.ok {
			shown++
		}
		cmd = m.onVideoFrame(msg)
	}
	v := m.pv.video
	if shown != 3 || !v.ended || v.playing() {
		t.Fatalf("shown %d ended %v playing %v", shown, v.ended, v.playing())
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(string(b)), "\n")[1]
	for _, want := range []string{"-ss 1.000", "-re", "-an", "-f rawvideo -pix_fmt rgba pipe:1", "fps=10,scale="} {
		if !strings.Contains(args, want) {
			t.Fatalf("ffmpeg args lack %q: %s", want, args)
		}
	}
	// An ended video doesn't start again by itself; alt+p replays it.
	if m.syncVideo() != nil {
		t.Fatal("an ended video must not autoplay")
	}
	if cmd := m.togglePlay(); cmd == nil || !v.playing() || v.shown != 0 {
		t.Fatal("alt+p replays from the start point")
	}
	v.stop()
}

// alive says whether ffmpeg's process (pid from the log, line n) runs.
func alive(t *testing.T, log string, n int) bool {
	t.Helper()
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	pid, _ := strconv.Atoi(strings.Fields(lines[n])[0])
	return syscall.Kill(pid, 0) == nil
}

func waitDead(t *testing.T, log string, n int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if !alive(t, log, n) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("ffmpeg was left running")
}

func TestVideoPauseResumeAndStopOnMove(t *testing.T) {
	dir, log := fakeVideoTools(t, 1000, "0.05")
	mkfile(t, filepath.Join(dir, "z.txt"), "after the clip")
	m := videoModel(t, dir)
	m.cfg.VideoPlay = videoPlayKey
	if m.syncVideo() != nil {
		t.Fatal("play with alt+p only: no autoplay")
	}
	cmd := m.togglePlay()
	m.onVideoFrame(frame(t, cmd))
	m.onVideoFrame(frame(t, m.pv.video.next(m.pv.video.run)))
	if m.pv.video.shown != 2 || m.vid != m.pv.video {
		t.Fatalf("shown %d", m.pv.video.shown)
	}

	m.togglePlay() // pause: ffmpeg is killed, the frame stays
	if !strings.Contains(m.msg, "paused at 0:01") || m.pv.video.playing() {
		t.Fatalf("pause: %q", m.msg)
	}
	waitDead(t, log, 1)

	cmd = m.togglePlay() // resume where it stopped: 1 s + 2 frames
	frame(t, cmd)
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "-ss 1.200") {
		t.Fatalf("resume: %s", b)
	}
	if !alive(t, log, 2) {
		t.Fatal("not playing")
	}

	// Moving to another file stops it.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if m.vid != nil {
		t.Fatal("still tracking the old video")
	}
	waitDead(t, log, 2)
}

func TestVideoStopsUnderADialogAndOnQuit(t *testing.T) {
	dir, log := fakeVideoTools(t, 1000, "0.05")
	m := videoModel(t, dir)
	m.vid = m.pv.video
	frame(t, m.pv.video.play())
	m.openSettings()
	m.syncVideo()
	waitDead(t, log, 1)
	if m.pv.video.paused {
		t.Fatal("a dialog isn't the user pausing: it may play again after")
	}
	m.modal = nil
	if m.syncVideo() == nil {
		t.Fatal("closing the dialog arms autoplay again")
	}

	frame(t, m.pv.video.play())
	m.quit()
	waitDead(t, log, 2)
}

func TestVideoStillFrameSetting(t *testing.T) {
	dir, _ := fakeVideoTools(t, 1, "0")
	m := testModel(t, dir)
	m.cfg.VideoPlay = videoPlayOff
	m.refreshPreview()
	pv := m.buildPreviewCmd(m.pvKey)().(previewReadyMsg)
	if pv.pv.video != nil || !pv.pv.blocks {
		t.Fatal("still frame only: no player")
	}
}
