package main

// Places in the file list: an opt-in setting (Settings → "Places in the file
// list") that extends the home folder's Trash row to every folder. Below the
// files come bookmarks, then (with "all") drives and network locations, then
// the Trash, so moving down past the last file reaches them; enter opens,
// mounts or connects as in places (b), which stays the full list with the
// actions (unmount, forget, browse, connect…).
//
// Drawing never waits on lsblk or gio: drives and network locations come from
// a scan made in the background, at start, every placesRescan while shown,
// and right after anything is mounted, unmounted or connected.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Values of config.PlacesInList.
const (
	placesListOff       = ""          // the Trash row in the home folder only
	placesListBookmarks = "bookmarks" // bookmarks and the Trash, in every folder
	placesListAll       = "all"       // bookmarks, drives, network and the Trash
)

const placesRescan = 5 * time.Second

type placesScannedMsg struct{ scan placeScan }

// scanPlacesCmd scans drives and network locations off the UI goroutine.
func scanPlacesCmd() tea.Cmd {
	return func() tea.Msg { return placesScannedMsg{scanPlaces()} }
}

// listsPlaces says whether the list shows places below the files here: not
// inside an archive or the trash itself.
func (m model) listsPlaces() bool {
	return m.cfg.PlacesInList != placesListOff && m.arc == nil && !m.inTrash()
}

// rescanPlaces starts a background scan if the list needs one and none is
// running; force skips the wait between scans.
func (m *model) rescanPlaces(force bool) tea.Cmd {
	if m.cfg.PlacesInList != placesListAll || (!force && time.Since(m.scannedAt) < placesRescan) {
		return nil
	}
	if m.scanning { // that scan may have looked before the change: look again after it
		m.rescanAfter = m.rescanAfter || force
		return nil
	}
	m.scanning = true
	return scanPlacesCmd()
}

func (m *model) onPlacesScanned(msg placesScannedMsg) tea.Cmd {
	m.scanning, m.scannedAt = false, time.Now()
	m.scan = &msg.scan
	m.refreshPlaceRows()
	if m.rescanAfter {
		m.rescanAfter = false
		return m.rescanPlaces(true)
	}
	return nil
}

// refreshPlaceRows rebuilds the rows below the files when they changed,
// keeping the cursor on the same row (and the preview as it is otherwise).
func (m *model) refreshPlaceRows() {
	old := virtualRows(m.entries)
	files := len(m.entries) - len(old)
	now := m.tailRows(files)
	if sameRows(old, now) {
		return
	}
	cur, ok := m.current()
	m.entries = append(m.entries[:files:files], now...)
	if ok && cur.virtual() {
		m.cursor = files - 1
		for i, e := range now {
			if e.kind == cur.kind && e.name == cur.name {
				m.cursor = files + i
				break
			}
		}
		m.cursor = max(0, m.cursor)
		m.settle()
		m.pvKey = ""
	}
	m.cursor = min(m.cursor, max(0, len(m.entries)-1))
	m.fixOffset()
}

// tailRows are the rows below the files (files of them): the Trash or
// places rows, under an "(empty)" note when there are no files.
func (m model) tailRows(files int) []entry {
	rows := m.trashRows()
	if len(rows) == 0 || !m.listsPlaces() {
		return rows
	}
	note := "" // a blank line between the files and the places
	switch {
	case files > 0:
	case m.filter != "":
		note = "(no matches)"
	default:
		note = "(empty)"
	}
	return append([]entry{{name: note, kind: kindNote}}, rows...)
}

// placesHold: a down key repeating faster than this is being held, and
// doesn't cross from the last file into the places.
const placesHold = 250 * time.Millisecond

// firstPlaceRow is the index of the first row after the files when places
// are listed below some (-1 otherwise).
func (m model) firstPlaceRow() int {
	if !m.listsPlaces() {
		return -1
	}
	i := len(m.entries) - len(virtualRows(m.entries))
	if i == 0 || i == len(m.entries) {
		return -1
	}
	return i
}

// moveDown moves down, stopping at the last file: going on into the places
// takes another press (one that isn't the key held down), so scrolling
// through a folder doesn't run into them by accident.
func (m *model) moveDown(delta int) {
	wall := m.firstPlaceRow()
	switch {
	case wall < 0 || m.cursor >= wall || m.cursor+delta < wall:
		m.move(delta)
	case m.cursor < wall-1: // land on the last file
		m.move(wall - 1 - m.cursor)
		m.bumped = time.Now()
	case !m.bumped.IsZero() && time.Since(m.bumped) > placesHold:
		m.move(delta)
	default:
		m.bumped = time.Now()
		m.setMsg("last file · ↓ again for places", false)
	}
}

// virtualRows is the tail of ents that isn't files.
func virtualRows(ents []entry) []entry {
	i := len(ents)
	for i > 0 && ents[i-1].virtual() {
		i--
	}
	return ents[i:]
}

func sameRows(a, b []entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].kind != b[i].kind || a[i].name != b[i].name || a[i].placeSig() != b[i].placeSig() {
			return false
		}
	}
	return true
}

// placeSig is everything a place row shows or does: a saved server and the
// same server connected share a name and an address but not this.
func (e entry) placeSig() string {
	if e.pl == nil {
		return ""
	}
	p := e.pl
	return fmt.Sprintf("%d|%s|%s|%s|%t", p.kind, p.info, p.path, placeSuffix(*p), p.missing)
}

// info is a place row's suffix ("" for other rows).
func (e entry) info() string {
	if e.pl == nil {
		return ""
	}
	return e.pl.info
}

// placeRows are the rows below the files: a labelled separator over each
// section that has something, then the Trash. A name filter keeps the rows
// that match.
func (m model) placeRows() []entry {
	keep := func(name string) bool {
		return m.filter == "" || strings.Contains(strings.ToLower(name), strings.ToLower(m.filter))
	}
	var out []entry
	section := func(label string, ps []place) {
		var rows []entry
		for _, p := range ps {
			if keep(p.name) {
				p := p
				rows = append(rows, entry{name: p.name, isDir: true, kind: kindPlace, pl: &p})
			}
		}
		if len(rows) > 0 {
			out = append(append(out, entry{name: label, kind: kindSeparator}), rows...)
		}
	}
	var marks []place
	for _, b := range m.cfg.Bookmarks {
		marks = append(marks, place{name: filepath.Base(b), path: b, section: secBookmarks, bookmark: true, info: tildePath(b)})
	}
	section(secBookmarks, marks)
	if m.cfg.PlacesInList == placesListAll && m.scan != nil {
		section(secDrives, m.scan.drives)
		net := append(append([]place(nil), m.scan.nets...), m.scan.servers(m.cfg.Servers)...)
		net = append(net, place{name: "Browse network…", section: secNetwork, kind: placeBrowse, info: "find computers"})
		if m.scan.tailscale {
			net = append(net, tailnetPlace)
		}
		section(secNetwork, append(net, place{name: "Connect to server…", section: secNetwork, kind: placeConnect, info: "smb · sftp · nfs"}))
	}
	if keep("trash") {
		out = append(out, entry{kind: kindSeparator}, entry{name: "Trash", isDir: true, kind: kindTrashLink})
	}
	return out
}

// shortcutAction is what enter does on a Trash or place row.
func shortcutAction(e entry) string {
	switch {
	case e.pl == nil:
		return "opens it"
	case e.pl.kind == placeConnect || e.pl.kind == placeBrowse || e.pl.kind == placeTailnet:
		return placeAction(*e.pl)
	}
	return placeAction(*e.pl) + " it"
}

// placeKeys is what enter and d do on a place row, for the status bar.
func placeKeys(p place) string {
	text := "enter " + placeAction(p)
	switch {
	case p.bookmark:
		text += " · d (or B) removes the bookmark"
	case p.kind == placeServer:
		text += " · d forgets it"
	case p.kind == placeDrive && p.path != "":
		text += " · d unmounts"
	case p.kind == placeNet:
		text += " · d disconnects"
	}
	return text
}

// dropPlace is d on a place row: unmount a drive, disconnect a network
// location, remove a bookmark or forget a saved server.
func (m *model) dropPlace(p place) tea.Cmd {
	switch {
	case unmountable(p):
		return m.unmountPlace(p)
	case forgettable(p):
		m.forgetPlace(p)
		if p.bookmark {
			m.setMsg("removed bookmark "+tildePath(p.path), false)
		} else {
			m.setMsg("forgot "+p.name, false)
		}
		m.refreshPlaceRows()
		return nil
	}
	m.setMsg(p.name+": nothing to remove · enter "+shortcutAction(entry{pl: &p}), false)
	return nil
}

// placeIcon marks a place row's kind (Nerd Font icons, like the Trash row).
func placeIcon(p place) string {
	switch {
	case p.bookmark:
		return "★"
	case p.kind == placeDrive || p.section == secDrives:
		return "\uf0a0" // nf-fa-hdd_o
	case p.kind == placeConnect:
		return "\uf067" // nf-fa-plus
	case p.kind == placeBrowse:
		return "\uf002" // nf-fa-search
	case p.kind == placeTailnet:
		return "\uf0c2" // nf-fa-cloud
	}
	return "\uf233" // nf-fa-server
}

// placeSuffix is the short right-hand note on a place row.
func placeSuffix(p place) string {
	switch {
	case p.kind == placeServer:
		return "not connected"
	case p.kind == placeDrive && p.drive.locked:
		return "locked"
	case p.kind == placeDrive && p.path == "":
		return "not mounted"
	case p.kind == placeDrive:
		return humanSize(p.drive.size)
	}
	return ""
}

// placeAction is what enter does on a place row, for the status bar.
func placeAction(p place) string {
	switch {
	case p.kind == placeServer:
		return "connects"
	case p.kind == placeConnect:
		return "asks for a server address"
	case p.kind == placeBrowse:
		return "looks for computers nearby"
	case p.kind == placeTailnet:
		return "lists your Tailscale computers"
	case p.kind == placeDrive && p.path == "" && p.drive.locked:
		return "unlocks"
	case p.kind == placeDrive && p.path == "":
		return "mounts"
	}
	return "opens"
}

// placePreview is the preview of a place row: the folder, or what enter will
// do. Network folders aren't read here (a slow server would stall the UI).
func (m model) placePreview(p place) preview {
	pv := preview{title: p.name, meta: p.info}
	switch {
	case p.path != "" && p.kind != placeNet:
		if _, err := os.Stat(p.path); err != nil {
			pv.lines = []string{"  " + tildePath(p.path), "", "  (missing) · B removes the bookmark"}
			return pv
		}
		return dirPreview(pv, p.path, m.previewOpts())
	case p.kind == placeNet:
		pv.lines = []string{"  " + p.net.uri, "", "  asking " + p.name + " about itself…", "", "  enter opens it · d disconnects"}
	case p.kind == placeServer:
		pv.lines = []string{"  " + p.uri, "", "  saved server, not connected", "  enter connects · d forgets it"}
	case p.kind == placeConnect:
		pv.lines = []string{"  Connect to a Windows, Mac or Samba share (smb://),", "  an SSH server (sftp://) or an NFS export (nfs://).", "", "  enter asks for the address"}
	case p.kind == placeBrowse:
		pv.lines = []string{"  Look for computers and shares on your network.", "", "  enter starts looking"}
	case p.kind == placeTailnet:
		pv.lines = []string{"  The computers on your tailnet that are online,", "  to connect to over SFTP or SMB wherever you are.", "", "  enter lists them"}
	default:
		pv.lines = []string{"  " + p.drive.dev + " · " + p.drive.fstype, "", "  not mounted · enter " + strings.TrimSuffix(placeAction(p), "s")}
	}
	o := m.previewOpts()
	o.wrap = false
	pv.lines = fitLines(pv.lines, o, len(pv.lines)) // a long path is cut, not wrapped
	return pv
}
