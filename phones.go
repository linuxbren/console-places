package main

// Phones and cameras in places. They show up through gvfs (`gio mount -li`)
// as mtp:// (Android), afc:// (iPhone) and gphoto2:// (cameras), mounted or
// not. Mounting and unmounting go through `gio mount` like network locations;
// the local folder is under /run/user/<uid>/gvfs/. Listing uses placesTimeout
// so a stuck gvfs can't freeze the UI; mounting itself gets a longer timeout
// because MTP is slow. Previews never list a phone folder (same reason).

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	secPhones         = "Phones"
	phoneMountTimeout = 30 * time.Second
)

// phoneSchemes are the gvfs backends for phones and cameras.
var phoneSchemes = map[string]bool{"mtp": true, "afc": true, "gphoto2": true}

// phoneVol is a phone or camera shown in places.
type phoneVol struct {
	name, uri, path string // path: the gvfs-fuse folder, "" until mounted
	scheme          string
}

var (
	volumeLine     = regexp.MustCompile(`^\s*Volume\(\d+\): (.*)$`)
	activationRoot = regexp.MustCompile(`(?i)^\s*activation_root=(.+)$`)
)

// gvfsPhones lists phone and camera volumes gvfs knows about, mounted or not.
var gvfsPhones = func() []phoneVol {
	if _, err := exec.LookPath("gio"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), placesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gio", "mount", "-li").Output()
	if err != nil {
		return nil
	}
	return parseGioPhones(string(out), gioLocalPath)
}

// parseGioPhones picks mtp/afc/gphoto2 volumes and mounts out of `gio mount -li`.
func parseGioPhones(text string, localPath func(string) string) []phoneVol {
	type vol struct{ name, uri string }
	var volumes []vol
	var cur *vol
	var mounts []phoneVol

	for _, l := range strings.Split(text, "\n") {
		if mt := volumeLine.FindStringSubmatch(l); mt != nil {
			volumes = append(volumes, vol{name: mt[1]})
			cur = &volumes[len(volumes)-1]
			continue
		}
		if mt := mountLine.FindStringSubmatch(l); mt != nil {
			cur = nil
			scheme, _, _ := strings.Cut(mt[2], "://")
			if phoneSchemes[scheme] {
				path := ""
				if localPath != nil {
					path = localPath(mt[2])
				}
				mounts = append(mounts, phoneVol{name: mt[1], uri: mt[2], path: path, scheme: scheme})
			}
			continue
		}
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "Drive(") {
			cur = nil
			continue
		}
		if cur == nil {
			continue
		}
		if mt := activationRoot.FindStringSubmatch(l); mt != nil {
			cur.uri = strings.TrimSpace(mt[1])
		}
	}

	mounted := map[string]bool{}
	var out []phoneVol
	for _, m := range mounts {
		out = append(out, m)
		mounted[strings.TrimSuffix(m.uri, "/")] = true
	}
	for _, v := range volumes {
		if v.uri == "" {
			continue
		}
		scheme, _, _ := strings.Cut(v.uri, "://")
		if !phoneSchemes[scheme] || mounted[strings.TrimSuffix(v.uri, "/")] {
			continue
		}
		out = append(out, phoneVol{name: v.name, uri: v.uri, scheme: scheme})
	}
	return out
}

// phoneInfo is the phone's suffix in places.
func phoneInfo(ph phoneVol) string {
	if ph.path != "" {
		return ph.uri
	}
	switch ph.scheme {
	case "mtp":
		return "Android · not mounted"
	case "afc":
		return "iPhone · not mounted"
	case "gphoto2":
		return "camera · not mounted"
	}
	return "not mounted"
}

// phoneHint is what to try when mounting fails for a scheme.
func phoneHint(scheme string) string {
	switch scheme {
	case "mtp":
		return "unlock the phone and choose File transfer (MTP) in its USB notification"
	case "afc":
		return "unlock the iPhone, tap Trust, and install usbmuxd if needed"
	case "gphoto2":
		return "turn the camera on and install gvfs-gphoto2 if needed"
	}
	return ""
}

// phonePlace builds the places row for a phone or camera.
func phonePlace(ph phoneVol) place {
	return place{
		name: ph.name, path: ph.path, section: secPhones, kind: placePhone,
		net:  netMount{name: ph.name, uri: ph.uri, path: ph.path},
		info: phoneInfo(ph),
	}
}

// mountPhone mounts uri with gio (non-interactive) and opens its folder.
func mountPhone(ph phoneVol) tea.Cmd {
	return func() tea.Msg {
		if _, err := exec.LookPath("gio"); err != nil {
			err := missing("gvfs", "phones and cameras need gvfs (the gio command)")
			return placeMsg{text: "mount " + ph.name + ": " + err.Error(), err: true, cause: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), phoneMountTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "gio", "mount", ph.uri)
		out, err := cmd.CombinedOutput()
		if err != nil {
			e := phoneMountError(ph, string(out), err, ctx.Err())
			return placeMsg{text: "mount " + ph.name + ": " + e.Error(), err: true, cause: e}
		}
		path := gioLocalPath(ph.uri)
		if path == "" {
			return placeMsg{text: "mounted " + ph.name + ", but gvfs-fuse isn't running, so it has no folder to open", err: true}
		}
		return placeMsg{open: path, text: "mounted " + ph.name}
	}
}

// phoneMountError turns gio's failure into a sentence, with a missing-package
// or unlock hint where that fits.
func phoneMountError(ph phoneVol, out string, err, ctxErr error) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		text := "timed out"
		if h := phoneHint(ph.scheme); h != "" {
			text += " · " + h
		}
		return errors.New(text)
	}
	text := connectText(ph.uri, out, err)
	need := "gvfs-" + ph.scheme
	if requirements[need].what != "" && (strings.Contains(text, "needs "+need) ||
		strings.Contains(text, "not supported") || strings.Contains(text, "not mountable") ||
		strings.Contains(text, "doesn’t implement") || strings.Contains(text, "doesn't implement")) {
		if !strings.Contains(text, "needs "+need) {
			text = ph.scheme + ":// needs " + need
			if ph.scheme == "mtp" || ph.scheme == "afc" || ph.scheme == "gphoto2" {
				text += " (gvfs-backends on Debian/Ubuntu)"
			}
		}
		return missing(need, text)
	}
	if h := phoneHint(ph.scheme); h != "" && !strings.Contains(text, h) {
		text += " · " + h
	}
	return errors.New(text)
}

// phoneSchemeOf is the phone/camera scheme of a gvfs folder name's scheme
// field ("mtp", "afc", "gphoto2"), or "".
func phoneSchemeOf(scheme string) string {
	base, _, _ := strings.Cut(scheme, "-")
	if phoneSchemes[scheme] {
		return scheme
	}
	if phoneSchemes[base] {
		return base
	}
	return ""
}

// phoneLabel is a friendly name for a phone gvfs folder (Pixel_8 → "Pixel 8").
func phoneLabel(gv map[string]string) string {
	host := gv["host"]
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[usb:") || strings.HasPrefix(host, "usb:") {
		switch phoneSchemeOf(gv["scheme"]) {
		case "mtp":
			return "Phone"
		case "afc":
			return "iPhone"
		case "gphoto2":
			return "Camera"
		}
		return "Phone"
	}
	return strings.ReplaceAll(host, "_", " ")
}
