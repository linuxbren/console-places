package main

// System clipboard. A copy is offered in several formats at once so whatever
// app pastes it finds one it understands: file managers take the GNOME file
// list or text/uri-list, terminals and editors take the paths as text (Claude
// Code attaches a pasted image path as the image).
//
// A copy never carries image data: Nautilus pastes any image on the clipboard
// as a new "Pasted image.png" in preference to the file list, so an image file
// would arrive renamed. Nautilus's own file copies carry none either.
//
// Serving several types from one copy needs the Wayland data-control protocol,
// which wl-copy cannot do. The selection is owned by a detached copy of this
// binary (see serveClipboard) so it outlives console-places, the way wl-copy's own
// background process does. Compositors without ext-data-control fall back to
// wl-copy with the single most useful type.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	wlclient "github.com/AvengeMedia/dankgo/wayland/client"
	"github.com/AvengeMedia/dankgo/wayland/ext_data_control"
	"github.com/AvengeMedia/dankgo/wlclipboard"
)

const (
	clipType       = "x-special/gnome-copied-files"
	uriListType    = "text/uri-list"
	clipServeEnv   = "CONSOLE_PLACES_CLIPBOARD_SERVE"
	maxClipOffered = 512 << 20 // sanity cap on what the owner will read
)

type clipOffer = wlclipboard.Offer

// Variables so tests can stub the desktop.
var (
	clipWrite = func(cut bool, paths []string) error { return setClipboard(fileOffers(cut, paths)) }
	textWrite = func(s string) error { return setClipboard(wlclipboard.TextOffers(s)) }
	clipRead  = readClipboardFiles
)

// fileOffers builds every format a file copy is offered in.
func fileOffers(cut bool, paths []string) []clipOffer {
	verb := "copy"
	if cut {
		verb = "cut"
	}
	uris := make([]string, len(paths))
	for i, p := range paths {
		uris[i] = "file://" + (&url.URL{Path: p}).EscapedPath()
	}
	offers := []clipOffer{
		{MimeType: clipType, Data: []byte(verb + "\n" + strings.Join(uris, "\n"))},
		{MimeType: uriListType, Data: []byte(strings.Join(uris, "\r\n") + "\r\n")},
	}
	return append(offers, wlclipboard.TextOffers(strings.Join(paths, "\n"))...)
}

// setClipboard takes the selection with all offers, or falls back to wl-copy
// with one of them.
func setClipboard(offers []clipOffer) error {
	switch clipBackend(os.Getenv, exec.LookPath) {
	case "xclip", "xsel":
		return x11Copy(offers)
	case "":
		return errNoClipboard
	}
	err := spawnClipOwner(offers)
	if err == nil {
		return nil
	}
	if ferr := wlCopyFallback(offers); ferr != nil {
		return fmt.Errorf("%v; wl-copy fallback: %v", err, ferr)
	}
	return nil
}

// wlCopyFallback offers a single type: the URI list for files (wl-copy also
// advertises it as text/plain), otherwise plain text.
func wlCopyFallback(offers []clipOffer) error {
	for _, o := range offers {
		if o.MimeType == uriListType {
			return runWithStdin(o.Data, "wl-copy", "--type", uriListType)
		}
	}
	if len(offers) == 0 {
		return errors.New("nothing to copy")
	}
	return runWithStdin(offers[0].Data, "wl-copy")
}

func runWithStdin(input []byte, name string, args ...string) error {
	bin, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Run()
}

// spawnClipOwner starts the detached owner process, hands it the offers, and
// waits until it holds the selection.
func spawnClipOwner(offers []clipOffer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), clipServeEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive the terminal closing
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = writeOffers(stdin, offers)
		stdin.Close()
	}()
	reply := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		reply <- strings.TrimSpace(line)
	}()
	select {
	case line := <-reply:
		if line == "ok" {
			go cmd.Wait() // reap it once another copy replaces it
			return nil
		}
		_ = cmd.Wait()
		if line == "" {
			line = "clipboard owner exited"
		}
		return errors.New(line)
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("clipboard owner timed out")
	}
}

// Offers travel to the owner as: mime NUL length NUL data, repeated.
func writeOffers(w io.Writer, offers []clipOffer) error {
	bw := bufio.NewWriter(w)
	for _, o := range offers {
		fmt.Fprintf(bw, "%s\x00%d\x00", o.MimeType, len(o.Data))
		if _, err := bw.Write(o.Data); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func readOffers(r io.Reader) ([]clipOffer, error) {
	br := bufio.NewReader(r)
	var offers []clipOffer
	total := 0
	for {
		mime, err := br.ReadString(0)
		if err == io.EOF && mime == "" {
			return offers, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read offer type: %w", err)
		}
		lenStr, err := br.ReadString(0)
		if err != nil {
			return nil, fmt.Errorf("read offer length: %w", err)
		}
		n, err := strconv.Atoi(strings.TrimSuffix(lenStr, "\x00"))
		if total += n; err != nil || n < 0 || total > maxClipOffered {
			return nil, errors.New("bad offer length")
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(br, data); err != nil {
			return nil, fmt.Errorf("read offer data: %w", err)
		}
		offers = append(offers, clipOffer{MimeType: strings.TrimSuffix(mime, "\x00"), Data: data})
	}
}

// serveClipboard runs in the detached owner process: it reads offers from
// stdin, takes the selection, prints "ok" (or an error) on stdout, and serves
// paste requests until another client takes the selection.
func serveClipboard() int {
	fail := func(err error) int { fmt.Println(err); return 1 }
	offers, err := readOffers(os.Stdin)
	if err != nil {
		return fail(err)
	}
	if len(offers) == 0 {
		return fail(errors.New("nothing to copy"))
	}
	display, err := wlclient.Connect("")
	if err != nil {
		return fail(fmt.Errorf("wayland connect: %w", err))
	}
	defer display.Destroy()
	ctx := display.Context()
	registry, err := display.GetRegistry()
	if err != nil {
		return fail(err)
	}
	var (
		seat    *wlclient.Seat
		mgr     *ext_data_control.ExtDataControlManagerV1
		bindErr error
	)
	registry.SetGlobalHandler(func(e wlclient.RegistryGlobalEvent) {
		switch {
		case e.Interface == ext_data_control.ExtDataControlManagerV1InterfaceName && mgr == nil:
			mgr = ext_data_control.NewExtDataControlManagerV1(ctx)
			bindErr = errors.Join(bindErr, registry.Bind(e.Name, e.Interface, e.Version, mgr))
		case e.Interface == "wl_seat" && seat == nil:
			seat = wlclient.NewSeat(ctx)
			bindErr = errors.Join(bindErr, registry.Bind(e.Name, e.Interface, e.Version, seat))
		}
	})
	if err := display.Roundtrip(); err != nil {
		return fail(err)
	}
	switch {
	case bindErr != nil:
		return fail(bindErr)
	case mgr == nil:
		return fail(errors.New("compositor lacks ext-data-control"))
	case seat == nil:
		return fail(errors.New("no wayland seat"))
	}
	device, err := mgr.GetDataDevice(seat)
	if err != nil {
		return fail(err)
	}
	source, err := mgr.CreateDataSource()
	if err != nil {
		return fail(err)
	}
	data := make(map[string][]byte, len(offers))
	for _, o := range offers {
		if _, dup := data[o.MimeType]; dup {
			continue
		}
		if err := source.Offer(o.MimeType); err != nil {
			return fail(err)
		}
		data[o.MimeType] = o.Data
	}
	source.SetSendHandler(func(e ext_data_control.ExtDataControlSourceV1SendEvent) {
		f := os.NewFile(uintptr(e.Fd), "clipboard-pipe")
		_ = syscall.SetNonblock(e.Fd, false)
		// Write off the event loop so a slow reader can't stall cancellation.
		go func(b []byte) { _, _ = f.Write(b); f.Close() }(data[e.MimeType])
	})
	cancelled := false
	source.SetCancelledHandler(func(ext_data_control.ExtDataControlSourceV1CancelledEvent) { cancelled = true })
	if err := device.SetSelection(source); err != nil {
		return fail(err)
	}
	if err := display.Roundtrip(); err != nil {
		return fail(err)
	}
	fmt.Println("ok")
	os.Stdout.Close()
	_ = os.Chdir("/") // don't keep a mount busy
	for !cancelled {
		if err := ctx.Dispatch(); err != nil {
			return 0
		}
	}
	return 0
}

// readClipboardFiles returns the files on the system clipboard, preferring
// formats that carry cut vs copy.
func readClipboardFiles() (paths []string, cut bool, ok bool) {
	backend := clipBackend(os.Getenv, exec.LookPath)
	types, err := pasteTypes(backend)
	if err != nil {
		return nil, false, false
	}
	has := map[string]bool{}
	for _, t := range strings.Split(string(types), "\n") {
		has[strings.TrimSpace(t)] = true
	}
	if has["UTF8_STRING"] || has["STRING"] { // X11's names for text
		has["text/plain"] = true
	}
	for _, t := range []string{clipType, uriListType, "text/plain;charset=utf-8", "text/plain"} {
		if !has[t] {
			continue
		}
		out, err := pasteType(backend, t)
		if err != nil {
			continue
		}
		switch t {
		case clipType:
			paths, cut, ok = parseClip(string(out))
		case uriListType:
			paths, ok = parseURIList(string(out))
		default:
			paths, ok = parsePathText(string(out))
		}
		if ok {
			return paths, cut, true
		}
	}
	return nil, false, false
}

func wlPaste(args ...string) ([]byte, error) { return clipTool("wl-paste", args...) }

func clipTool(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// ---- X11 and no clipboard -------------------------------------------------

var errNoClipboard = errors.New("no system clipboard here (needs Wayland, or X11 with xclip or xsel)")

// clipBackend picks the system clipboard: "wayland", "xclip" or "xsel" (X11),
// or "" when there's none (a console, ssh without X forwarding…).
func clipBackend(getenv func(string) string, look func(string) (string, error)) string {
	if getenv("WAYLAND_DISPLAY") != "" {
		return "wayland"
	}
	if getenv("DISPLAY") != "" {
		for _, tool := range []string{"xclip", "xsel"} {
			if _, err := look(tool); err == nil {
				return tool
			}
		}
	}
	return ""
}

// x11Copy puts the copy on the X11 clipboard. xclip and xsel serve one
// format, so it's the text form (the paths, for a file copy): what
// terminals and editors paste.
func x11Copy(offers []clipOffer) error {
	text := offers[0].Data
	for _, o := range offers {
		if o.MimeType == "text/plain;charset=utf-8" || o.MimeType == "text/plain" {
			text = o.Data
			break
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return runWithStdin(text, "xclip", "-selection", "clipboard", "-in")
	}
	return runWithStdin(text, "xsel", "--clipboard", "--input")
}

// pasteTypes lists the clipboard's formats, newline-separated.
func pasteTypes(backend string) ([]byte, error) {
	switch backend {
	case "wayland":
		return wlPaste("--list-types")
	case "xclip":
		return clipTool("xclip", "-selection", "clipboard", "-out", "-target", "TARGETS")
	case "xsel":
		return []byte("text/plain"), nil // xsel only reads text
	}
	return nil, errNoClipboard
}

// pasteType reads one format from the clipboard.
func pasteType(backend, mime string) ([]byte, error) {
	switch backend {
	case "wayland":
		return wlPaste("--no-newline", "--type", mime)
	case "xclip":
		if strings.HasPrefix(mime, "text/plain") {
			mime = "UTF8_STRING"
		}
		return clipTool("xclip", "-selection", "clipboard", "-out", "-target", mime)
	case "xsel":
		return clipTool("xsel", "--clipboard", "--output")
	}
	return nil, errNoClipboard
}

func parseClip(s string) (paths []string, cut bool, ok bool) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 2 || (lines[0] != "copy" && lines[0] != "cut") {
		return nil, false, false
	}
	paths, ok = parseURIList(strings.Join(lines[1:], "\n"))
	return paths, lines[0] == "cut", ok
}

func parseURIList(s string) (paths []string, ok bool) {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		u, err := url.Parse(l)
		if err != nil || u.Scheme != "file" || u.Path == "" {
			continue
		}
		paths = append(paths, u.Path)
	}
	return paths, len(paths) > 0
}

// parsePathText accepts text only when every line is an absolute path that
// exists, so pasting unrelated copied text does nothing.
func parsePathText(s string) (paths []string, ok bool) {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		if !filepath.IsAbs(l) {
			return nil, false
		}
		if _, err := os.Lstat(l); err != nil {
			return nil, false
		}
		paths = append(paths, l)
	}
	return paths, len(paths) > 0
}
