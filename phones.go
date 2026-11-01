package main

// Phones and cameras in places. They show up through gvfs (`gio mount -li`)
// as mtp:// (Android), afc:// (iPhone) and gphoto2:// (cameras), mounted or
// not. Mounting and unmounting go through `gio mount` like network locations;
// the local folder is under /run/user/<uid>/gvfs/. Listing uses placesTimeout
// so a stuck gvfs can't freeze the UI; mounting itself gets a longer timeout
// because MTP is slow. Previews never list a phone folder (same reason).
//
// iPhones expose two AFC locations: afc://UDID/ (media: DCIM, Music…) and
// afc://UDID:3/ (app documents via house_arrest). gvfs often lists only the
// :3 volume ("Documents on Name"), sometimes twice (shadow + daemon mount);
// the media URI still mounts with gio, so we synthesize it and name them
// distinctly ("Name" and "App documents on Name").

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	uuidLine       = regexp.MustCompile(`(?i)^\s*uuid[= ](\S+)`)
	shadowedLine   = regexp.MustCompile(`(?i)^\s*is_shadowed=(\d+)`)
)

// phoneNames maps an AFC UDID (or MTP/gphoto2 host) to the friendly name
// last seen while scanning, so titles can say "Brenden: /DCIM" instead of
// the raw UDID.
var phoneNames sync.Map // string → string

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

type rawPhone struct {
	name, uri, uuid string
	shadowed        bool
}

// parseGioPhones picks mtp/afc/gphoto2 volumes and mounts out of `gio mount -li`.
func parseGioPhones(text string, localPath func(string) string) []phoneVol {
	var (
		volumes []rawPhone
		mounts  []rawPhone
		curVol  *rawPhone
		curMnt  *rawPhone
	)
	finishMount := func() {
		if curMnt == nil {
			return
		}
		if !curMnt.shadowed && curMnt.uri != "" {
			mounts = append(mounts, *curMnt)
		}
		curMnt = nil
	}
	for _, l := range strings.Split(text, "\n") {
		if mt := volumeLine.FindStringSubmatch(l); mt != nil {
			finishMount()
			volumes = append(volumes, rawPhone{name: mt[1]})
			curVol = &volumes[len(volumes)-1]
			continue
		}
		if mt := mountLine.FindStringSubmatch(l); mt != nil {
			finishMount()
			curVol = nil
			scheme, _, _ := strings.Cut(mt[2], "://")
			if phoneSchemes[scheme] {
				curMnt = &rawPhone{name: mt[1], uri: mt[2]}
			}
			continue
		}
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "Drive(") {
			finishMount()
			curVol = nil
			continue
		}
		if curMnt != nil {
			if mt := shadowedLine.FindStringSubmatch(l); mt != nil {
				curMnt.shadowed = mt[1] != "0"
			}
			continue
		}
		if curVol == nil {
			continue
		}
		if mt := activationRoot.FindStringSubmatch(l); mt != nil {
			curVol.uri = strings.TrimSpace(mt[1])
		}
		if mt := uuidLine.FindStringSubmatch(l); mt != nil {
			curVol.uuid = strings.TrimSpace(mt[1])
		}
	}
	finishMount()

	// Collect by trimmed URI; mounts win over unmounted volumes.
	byURI := map[string]phoneVol{}
	order := []string{}
	add := func(name, uri string) {
		scheme, _, _ := strings.Cut(uri, "://")
		if !phoneSchemes[scheme] || uri == "" {
			return
		}
		key := strings.TrimSuffix(uri, "/")
		path := ""
		if localPath != nil {
			path = localPath(uri)
		}
		if _, ok := byURI[key]; !ok {
			order = append(order, key)
		}
		byURI[key] = phoneVol{name: name, uri: uri, path: path, scheme: scheme}
	}
	for _, m := range mounts {
		add(m.name, m.uri)
	}
	for _, v := range volumes {
		if v.uri == "" {
			continue
		}
		key := strings.TrimSuffix(v.uri, "/")
		if _, ok := byURI[key]; ok {
			continue
		}
		add(v.name, v.uri)
	}

	// AFC: rename media vs app documents and synthesize the media URI when
	// gvfs only advertised the :3 documents volume.
	type afcDev struct {
		name  string
		media *phoneVol
		docs  *phoneVol
	}
	afc := map[string]*afcDev{}
	afcOrder := []string{}
	ensure := func(udid string) *afcDev {
		if d, ok := afc[udid]; ok {
			return d
		}
		d := &afcDev{}
		afc[udid] = d
		afcOrder = append(afcOrder, udid)
		return d
	}
	for _, key := range order {
		ph := byURI[key]
		if ph.scheme != "afc" {
			continue
		}
		udid, port := afcURIParts(ph.uri)
		if udid == "" {
			continue
		}
		d := ensure(udid)
		if d.name == "" {
			d.name = afcPhoneName(ph.name)
		}
		ph.name = d.name
		cp := ph
		if port == "" || port == "1" {
			d.media = &cp
		} else {
			d.docs = &cp
		}
		delete(byURI, key)
	}
	// Volumes that listed a uuid but no activation_root still seed a device
	// name (rare; normally activation_root carries the afc URI).
	for _, v := range volumes {
		if v.uuid == "" || v.uri != "" {
			continue
		}
		if !strings.Contains(strings.ToLower(v.name), "document") {
			continue
		}
		d := ensure(v.uuid)
		if d.name == "" {
			d.name = afcPhoneName(v.name)
		}
	}

	var out []phoneVol
	// Non-AFC phones/cameras keep their scan order.
	for _, key := range order {
		if ph, ok := byURI[key]; ok {
			rememberPhoneName(ph)
			out = append(out, ph)
		}
	}
	for _, udid := range afcOrder {
		d := afc[udid]
		if d.name == "" {
			d.name = "iPhone"
		}
		mediaURI := "afc://" + udid + "/"
		if d.media == nil {
			path := ""
			if localPath != nil {
				path = localPath(mediaURI)
			}
			d.media = &phoneVol{name: d.name, uri: mediaURI, path: path, scheme: "afc"}
		} else {
			d.media.name = d.name
			d.media.uri = mediaURI // normalize trailing slash / host form
			if d.media.path == "" && localPath != nil {
				d.media.path = localPath(mediaURI)
			}
		}
		rememberPhoneHost(udid, d.name)
		out = append(out, *d.media)
		if d.docs != nil {
			d.docs.name = "App documents on " + d.name
			rememberPhoneHost(udid, d.name)
			out = append(out, *d.docs)
		}
	}
	return out
}

// afcPhoneName strips gvfs' "Documents on " prefix from an AFC volume name.
func afcPhoneName(name string) string {
	for _, p := range []string{"Documents on ", "documents on "} {
		if n, ok := strings.CutPrefix(name, p); ok {
			n = strings.TrimSpace(n)
			if n != "" {
				return n
			}
		}
	}
	return name
}

// afcURIParts returns the device UDID and virtual port ("", "1", "3"…) of an
// afc:// URI. Port 1/empty is the media filesystem; 3 is app documents.
func afcURIParts(uri string) (udid, port string) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "afc" {
		return "", ""
	}
	host := u.Host
	if host == "" {
		host = u.Path // rare
	}
	host = strings.Trim(host, "/")
	if i := strings.LastIndex(host, ":"); i > 0 {
		if _, err := strconv.Atoi(host[i+1:]); err == nil {
			return host[:i], host[i+1:]
		}
	}
	return host, ""
}

// afcHostParts reads UDID and port from a gvfs-fuse folder's fields
// (host=UDID, host=UDID:3, or host=UDID,port=3).
func afcHostParts(gv map[string]string) (udid, port string) {
	host, port := gv["host"], gv["port"]
	if i := strings.LastIndex(host, ":"); i > 0 {
		if _, err := strconv.Atoi(host[i+1:]); err == nil {
			host, port = host[:i], host[i+1:]
		}
	}
	return host, port
}

func rememberPhoneName(ph phoneVol) {
	switch ph.scheme {
	case "afc":
		udid, _ := afcURIParts(ph.uri)
		if udid != "" {
			rememberPhoneHost(udid, afcPhoneName(ph.name))
		}
	case "mtp", "gphoto2":
		if u, err := url.Parse(ph.uri); err == nil && u.Host != "" {
			rememberPhoneHost(u.Host, ph.name)
		}
	}
}

func rememberPhoneHost(host, name string) {
	name = afcPhoneName(name)
	if host == "" || name == "" {
		return
	}
	// Don't store "App documents on X" as the device name.
	if strings.HasPrefix(name, "App documents on ") {
		name = strings.TrimPrefix(name, "App documents on ")
	}
	phoneNames.Store(host, name)
}

func lookupPhoneName(host string) string {
	if host == "" {
		return ""
	}
	if v, ok := phoneNames.Load(host); ok {
		return v.(string)
	}
	return ""
}

// phoneInfo is the phone's suffix in places.
func phoneInfo(ph phoneVol) string {
	if ph.path != "" {
		return ph.uri
	}
	if ph.scheme == "afc" {
		_, port := afcURIParts(ph.uri)
		if port == "3" {
			return "app documents · not mounted"
		}
		return "media · not mounted"
	}
	switch ph.scheme {
	case "mtp":
		return "Android · not mounted"
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
		rememberPhoneName(ph)
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

// phoneLabel is a friendly name for a phone gvfs folder (Pixel_8 → "Pixel 8",
// an AFC UDID → the phone's name from the last scan).
func phoneLabel(gv map[string]string) string {
	switch phoneSchemeOf(gv["scheme"]) {
	case "afc":
		udid, port := afcHostParts(gv)
		name := lookupPhoneName(udid)
		if name == "" {
			name = "iPhone"
		}
		if port != "" && port != "1" {
			return "App documents on " + name
		}
		return name
	case "mtp", "gphoto2":
		host := gv["host"]
		if name := lookupPhoneName(host); name != "" {
			return name
		}
		if strings.HasPrefix(host, "[usb:") || strings.HasPrefix(host, "usb:") {
			if phoneSchemeOf(gv["scheme"]) == "gphoto2" {
				return "Camera"
			}
			return "Phone"
		}
		return strings.ReplaceAll(host, "_", " ")
	}
	host := gv["host"]
	if host == "" {
		return ""
	}
	return strings.ReplaceAll(host, "_", " ")
}

// gvfsMountTop is the fuse folder of the gvfs mount containing path
// (/run/user/…/gvfs/sftp:host=…), or "".
func gvfsMountTop(path string) string {
	rest, ok := strings.CutPrefix(path, gvfsRoot()+"/")
	if !ok || rest == "" {
		return ""
	}
	top, _, _ := strings.Cut(rest, "/")
	if top == "" {
		return ""
	}
	return gvfsRoot() + "/" + top
}

// atGvfsMountRoot says whether path is the root of a gvfs mount (so its
// parent would be the raw /run/user/…/gvfs listing).
func atGvfsMountRoot(path string) bool {
	top := gvfsMountTop(path)
	return top != "" && path == top
}
