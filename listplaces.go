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
	if m.cfg.PlacesInList != placesListAll || m.scanning || (!force && time.Since(m.scannedAt) < placesRescan) {
		return nil
	}
	m.scanning = true
	return scanPlacesCmd()
}

func (m *model) onPlacesScanned(msg placesScannedMsg) {
	m.scanning, m.scannedAt = false, time.Now()
	m.scan = &msg.scan
	m.refreshPlaceRows()
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
	if files == 0 && len(rows) > 0 && m.listsPlaces() {
		note := "(empty)"
		if m.filter != "" {
			note = "(no matches)"
		}
		rows = append([]entry{{name: note, kind: kindNote}}, rows...)
	}
	return rows
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
		if a[i].kind != b[i].kind || a[i].name != b[i].name || a[i].info() != b[i].info() {
			return false
		}
	}
	return true
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
		section(secNetwork, append(append([]place(nil), m.scan.nets...), m.scan.servers(m.cfg.Servers)...))
	}
	if keep("trash") {
		out = append(out, entry{kind: kindSeparator}, entry{name: "Trash", isDir: true, kind: kindTrashLink})
	}
	return out
}

// shortcutAction is what enter does on a Trash or place row.
func shortcutAction(e entry) string {
	if e.pl != nil {
		return placeAction(*e.pl) + " it"
	}
	return "opens it"
}

// placeIcon marks a place row's kind (Nerd Font icons, like the Trash row).
func placeIcon(p place) string {
	switch {
	case p.bookmark:
		return "★"
	case p.kind == placeDrive || p.section == secDrives:
		return "\uf0a0" // nf-fa-hdd_o
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
		pv.lines = []string{"  " + p.net.uri, "  " + tildePath(p.path), "", "  enter opens it · b to disconnect"}
	case p.kind == placeServer:
		pv.lines = []string{"  " + p.uri, "", "  saved server, not connected", "  enter connects · b to forget it"}
	default:
		pv.lines = []string{"  " + p.drive.dev + " · " + p.drive.fstype, "", "  not mounted · enter " + strings.TrimSuffix(placeAction(p), "s")}
	}
	return pv
}
