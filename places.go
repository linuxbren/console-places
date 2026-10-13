package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
)

// Places (b) is tidefiles' sidebar, in Nautilus' sections: the usual
// folders and the Trash, bookmarks, drives (mounted or not) and network
// locations (connected, saved, or a new one).

type placeKind int

const (
	placeDir     placeKind = iota // a folder to open
	placeDrive                    // a drive: open, or mount (unlock) first
	placeNet                      // a connected network location
	placeServer                   // a saved server, not connected
	placeConnect                  // "Connect to server…"
	placeBrowse                   // "Browse network…"
)

const (
	secBookmarks = "Bookmarks"
	secDrives    = "Drives"
	secNetwork   = "Network"
)

type place struct {
	name, path string // path: the folder; "" until a drive is mounted
	section    string
	kind       placeKind
	info       string
	bookmark   bool
	missing    bool // a bookmark whose folder is gone
	drive      drive
	net        netMount
	uri        string // a saved server
}

func (m model) places() []place {
	var out []place
	seen := map[string]bool{}
	add := func(p place) {
		if p.path != "" {
			if seen[p.path] {
				return
			}
			seen[p.path] = true
		}
		out = append(out, p)
	}
	isDir := func(p string) bool { fi, err := os.Stat(p); return p != "" && err == nil && fi.IsDir() }
	home, _ := os.UserHomeDir()
	if isDir(home) {
		add(place{name: "Home", path: home})
	}
	for _, d := range []string{"Desktop", "Documents", "Downloads", "Music", "Pictures", "Videos"} {
		if p := filepath.Join(home, d); isDir(p) {
			add(place{name: d, path: p})
		}
	}
	add(place{name: "Trash", path: trashFilesDir()}) // listed even before anything was deleted; opening it creates it

	for _, b := range m.cfg.Bookmarks {
		p := place{name: filepath.Base(b), path: b, section: secBookmarks, bookmark: true, info: tildePath(b)}
		if !isDir(b) {
			p.missing, p.info = true, "missing · "+tildePath(b)
		}
		out = append(out, p) // a bookmark of a place above is still listed
	}

	add(place{name: "Computer", path: "/", section: secDrives, info: "/"})
	sc := scanPlaces()
	for _, p := range sc.drives {
		add(p)
	}
	for _, p := range sc.nets {
		add(p)
	}
	for _, p := range sc.servers(m.cfg.Servers) {
		add(p)
	}
	add(place{name: "Browse network…", section: secNetwork, kind: placeBrowse, info: "find computers"})
	add(place{name: "Connect to server…", section: secNetwork, kind: placeConnect, info: "smb · sftp · nfs"})
	return out
}

// placeScan is what places reads from the system: drives (lsblk, statfs)
// and connected network locations (gio, /proc/self/mounts). Each lookup
// can take a moment, so the file list's places rows use a copy made in the
// background (see listplaces.go).
type placeScan struct {
	drives, nets []place
	connected    map[string]bool // connected URIs, without a trailing slash
}

func scanPlaces() placeScan {
	sc := placeScan{connected: map[string]bool{}}
	if devs, err := blockDevices(); err == nil {
		for _, d := range drives(devs) {
			sc.drives = append(sc.drives, place{name: d.name, path: d.mount, section: secDrives, kind: placeDrive, drive: d, info: d.info()})
		}
	} else {
		for _, mp := range mountPoints(mountsText()) { // no lsblk: what is mounted
			sc.drives = append(sc.drives, place{name: filepath.Base(mp), path: mp, section: secDrives, info: tildePath(mp)})
		}
	}
	for _, nm := range append(gvfsMounts(), systemNetMounts(mountsText())...) {
		sc.connected[strings.TrimSuffix(nm.uri, "/")] = true
		if nm.path != "" {
			sc.nets = append(sc.nets, place{name: nm.name, path: nm.path, section: secNetwork, kind: placeNet, net: nm, info: nm.uri})
		}
	}
	return sc
}

// servers are the saved servers that aren't connected now.
func (sc placeScan) servers(saved []string) []place {
	var out []place
	for _, s := range saved {
		if !sc.connected[strings.TrimSuffix(s, "/")] {
			out = append(out, place{name: serverName(s), section: secNetwork, kind: placeServer, uri: s, info: s})
		}
	}
	return out
}

func (m *model) openPlaces() {
	m.openPlacesAt(-1)
}

// openPlacesAt opens places with row sel selected (-1: the current folder).
func (m *model) openPlacesAt(sel int) {
	ps := m.places()
	items := make([]menuItem, len(ps))
	for i, p := range ps {
		items[i] = menuItem{label: p.name, suffix: p.info, value: p.path,
			muted: p.missing || p.kind == placeServer || p.kind == placeConnect || p.kind == placeBrowse || (p.kind == placeDrive && p.path == "")}
		if p.path == trashFilesDir() {
			items[i].suffix = ""
		}
	}
	m.modal = &modal{kind: mMenu, purpose: "places", title: "places", items: items, places: ps}
	if sel >= 0 {
		m.modal.sel = min(sel, len(items)-1)
		return
	}
	for i, p := range ps {
		if p.path == m.cwd && p.path != "" {
			m.modal.sel = i
			break
		}
	}
}

// placesBody draws places with a heading over each section.
func (m model) placesBody(md *modal, inner int) []string {
	r := m.renderer
	type line struct {
		text string
		item int
	}
	var lines []line
	section, selLine := "", 0
	for i, p := range md.places {
		if p.section != section {
			section = p.section
			lines = append(lines, line{r.Styles.DetailMeta.Render(" " + section), -1})
		}
		if i == md.sel {
			selLine = len(lines)
		}
		it := md.items[i]
		lines = append(lines, line{r.RenderSoftRow(tideui.SoftRow{Text: it.label, Suffix: it.suffix, Selected: i == md.sel, Muted: it.muted}, inner), i})
	}
	rows := max(3, m.height-10)
	first := max(0, min(selLine-rows/2, len(lines)-rows))
	var body []string
	for _, l := range lines[first:min(len(lines), first+rows)] {
		body = append(body, l.text)
	}
	h := []tideui.SoftHint{{Key: "enter", Label: "open"}}
	if md.sel < len(md.places) {
		switch p := md.places[md.sel]; {
		case p.bookmark:
			h = append(h, tideui.SoftHint{Key: "d", Label: "remove bookmark"})
		case p.kind == placeDrive && p.path == "" && p.drive.locked:
			h[0].Label = "unlock"
		case p.kind == placeDrive && p.path == "":
			h[0].Label = "mount"
		case p.kind == placeDrive:
			h = append(h, tideui.SoftHint{Key: "u", Label: "unmount"})
		case p.kind == placeNet:
			h = append(h, tideui.SoftHint{Key: "u", Label: "disconnect"})
		case p.kind == placeServer:
			h[0].Label = "connect"
			h = append(h, tideui.SoftHint{Key: "d", Label: "forget"})
		}
	}
	h = append(h, tideui.SoftHint{Key: "c", Label: "connect to server"}, tideui.SoftHint{Key: "esc", Label: "close"})
	body = append(body, "", r.RenderSoftHints(inner, h...))
	if m.cfg.PlacesInList == placesListOff {
		body = append(body, r.Styles.DetailMeta.Render(" Settings (S) can also list these below every folder"))
	}
	return body
}

// handlePlacesKey handles the keys places adds to a menu; ok is false for
// the rest (moving, esc).
func (m model) handlePlacesKey(md *modal, key string) (model, tea.Cmd, bool) {
	if md.sel < 0 || md.sel >= len(md.places) {
		return m, nil, false
	}
	p := md.places[md.sel]
	switch key {
	case "enter", "right", "l":
		m.modal = nil
		if p.missing {
			m.setMsg(tildePath(p.path)+" no longer exists · d removes the bookmark", true)
			m.openPlacesAt(md.sel)
			return m, nil, true
		}
		return m, m.openPlace(p), true
	case "u":
		switch {
		case p.kind == placeDrive && p.path != "":
			m.modal = nil
			m.leaveMount(p.path)
			m.setMsg("unmounting "+p.name+"…", false)
			return m, unmountDrive(p.drive), true
		case p.kind == placeNet && p.net.system && !hasFuseMount(p.net.path):
			m.modal = nil
			return m, m.unmountAsRoot(p.net), true
		case p.kind == placeNet:
			m.modal = nil
			m.setMsg("disconnecting "+p.name+"…", false)
			return m, m.disconnect(p.net), true
		}
		m.setMsg("only mounted drives and network locations can be unmounted", true)
		return m, nil, true
	case "d", "delete":
		switch {
		case p.bookmark:
			m.removeBookmark(p.path)
		case p.kind == placeServer:
			m.forgetServer(p.uri)
		default:
			m.setMsg("only bookmarks and saved servers can be removed", true)
			return m, nil, true
		}
		m.openPlacesAt(md.sel)
		return m, nil, true
	case "c", "+":
		m.openConnect("", "")
		return m, nil, true
	}
	return m, nil, false
}

// openPlace goes to a place: opens its folder, or mounts, unlocks or
// connects to it first.
func (m *model) openPlace(p place) tea.Cmd {
	switch {
	case p.kind == placeConnect:
		m.openConnect("", "")
	case p.kind == placeBrowse:
		return m.openBrowse()
	case p.kind == placeServer:
		return m.connect(p.uri, false)
	case p.path != "":
		m.chdir(p.path, "")
	case p.drive.locked:
		m.modal = &modal{kind: mInput, purpose: "unlock", title: "unlock " + p.name, secret: true,
			label: "Passphrase for " + p.name + " (" + p.drive.dev + "):", places: []place{p}}
	default:
		m.setMsg("mounting "+p.name+"…", false)
		return mountDrive(p.drive, "")
	}
	return nil
}

// mountsText is /proc/self/mounts (a variable so tests can supply their own).
var mountsText = readMounts

// mountPoints lists removable/user mounts (udisks, /media, /mnt).
func mountPoints(mounts string) []string {
	var out []string
	for _, l := range strings.Split(mounts, "\n") {
		fields := strings.Fields(l)
		if len(fields) < 2 {
			continue
		}
		mp := strings.ReplaceAll(fields[1], `\040`, " ")
		if strings.HasPrefix(mp, "/run/media/") || strings.HasPrefix(mp, "/media/") || strings.HasPrefix(mp, "/mnt/") {
			out = append(out, mp)
		}
	}
	return out
}

func (m model) isBookmarked(dir string) bool {
	for _, b := range m.cfg.Bookmarks {
		if b == dir {
			return true
		}
	}
	return false
}

// toggleBookmark bookmarks this folder, or removes its bookmark.
func (m *model) toggleBookmark() {
	if m.arc != nil {
		m.setMsg("archives can't be bookmarked", true)
		return
	}
	if m.isBookmarked(m.cwd) {
		m.removeBookmark(m.cwd)
		m.setMsg("removed bookmark "+tildePath(m.cwd), false)
		return
	}
	m.cfg.Bookmarks = append(m.cfg.Bookmarks, m.cwd)
	m.cfg.save()
	m.setMsg("bookmarked "+tildePath(m.cwd)+" · b shows places", false)
}

func (m *model) removeBookmark(path string) {
	var keep []string
	for _, b := range m.cfg.Bookmarks {
		if b != path {
			keep = append(keep, b)
		}
	}
	m.cfg.Bookmarks = keep
	m.cfg.save()
}

// leaveMount moves every tab out of a folder about to be unmounted, so it
// isn't kept busy, and tidefiles' own working folder too.
func (m *model) leaveMount(mp string) {
	if mp == "" {
		return
	}
	inside := func(p string) bool { return p == mp || strings.HasPrefix(p, strings.TrimSuffix(mp, "/")+"/") }
	home, _ := os.UserHomeDir()
	if wd, err := os.Getwd(); err == nil && inside(wd) {
		_ = os.Chdir(home)
	}
	m.saveTab()
	for i := range m.tabs {
		if i != m.tab && inside(m.tabs[i].cwd) {
			m.tabs[i] = tabState{cwd: home, cursors: map[string]string{}, selected: map[string]bool{}, hist: []string{home}}
		}
	}
	if inside(m.cwd) {
		m.chdir(home, "")
	}
}

// ---- open with / terminal ---------------------------------------------

type app struct{ id, name, path string }

// appsFor lists applications registered for path's content type (via gio).
func appsFor(path string) []app {
	mime := mimeOf(path)
	out, err := exec.Command("gio", "mime", mime).Output()
	if err != nil {
		return nil
	}
	var apps []app
	seen := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(l, "\t") {
			continue
		}
		id := strings.TrimSpace(l)
		if !strings.HasSuffix(id, ".desktop") || seen[id] {
			continue
		}
		seen[id] = true
		p, name := findDesktop(id)
		if p != "" {
			apps = append(apps, app{id: id, name: name, path: p})
		}
	}
	return apps
}

func findDesktop(id string) (path, name string) {
	home, _ := os.UserHomeDir()
	dirs := []string{filepath.Join(home, ".local/share/applications"), "/usr/local/share/applications", "/usr/share/applications"}
	for _, d := range dirs {
		p := filepath.Join(d, id)
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		name = strings.TrimSuffix(id, ".desktop")
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "Name="); ok {
				name = v
				break
			}
		}
		f.Close()
		return p, name
	}
	return "", ""
}

func launchApp(desktopPath, file string) tea.Cmd {
	return spawn("", "gio", "launch", desktopPath, file)
}

func terminalHere(dir string) tea.Cmd {
	if bin, err := exec.LookPath("xdg-terminal-exec"); err == nil {
		return spawn(dir, bin)
	}
	if t := os.Getenv("TERMINAL"); t != "" {
		return spawn(dir, t)
	}
	return func() tea.Msg { return statusMsg{"no terminal launcher found (xdg-terminal-exec or $TERMINAL)", true} }
}

func openDefault(path string) tea.Cmd { return spawn("", "xdg-open", path) }

// spawn starts a detached process and reports the outcome in the status bar.
func spawn(dir, name string, args ...string) tea.Cmd {
	return func() tea.Msg {
		c := exec.Command(name, args...)
		c.Dir = dir
		c.SysProcAttr = spawnAttr()
		if err := c.Start(); err != nil {
			return statusMsg{fmt.Sprintf("%s: %v", name, err), true}
		}
		go func() { _ = c.Wait() }()
		return statusMsg{"", false}
	}
}
