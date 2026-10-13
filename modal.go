package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
)

type modalKind int

const (
	mInput modalKind = iota
	mConfirm
	mMenu
	mInfo
	mHelp
	mPerms
	mFind
	mGrep
	mSettings
	mBulk
)

type menuItem struct {
	label, suffix, value string
	muted                bool
}

type modal struct {
	kind    modalKind
	purpose string // rename, newfile, newfolder, goto, filter, trash, purge, emptytrash, places, openwith
	title   string
	label   string   // prompt text above an input
	lines   []string // body of confirm / info modals
	in      textInput
	items   []menuItem
	sel     int
	scroll  int
	targets []string // absolute paths the modal acts on
	prev    string   // filter value to restore on esc
	perm    *permEdit
	secret  bool // the input is a password: shown as dots
	guest   bool // a connection prompt that can switch to guest (anonymous)
	find    *findState
	grep    *grepState
	pal     *paletteState
	places  []place // the places menu's rows, beside items
	bulk    *bulkState
}

// ---- text input --------------------------------------------------------

type textInput struct {
	r   []rune
	pos int
}

func newInput(s string) textInput { r := []rune(s); return textInput{r: r, pos: len(r)} }

func (t textInput) value() string { return string(t.r) }

// handle applies an editing key and reports whether it was consumed.
func (t *textInput) handle(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "left":
		t.pos = max(0, t.pos-1)
	case "right":
		t.pos = min(len(t.r), t.pos+1)
	case "home", "ctrl+a":
		t.pos = 0
	case "end", "ctrl+e":
		t.pos = len(t.r)
	case "backspace":
		if t.pos > 0 {
			t.r = append(t.r[:t.pos-1], t.r[t.pos:]...)
			t.pos--
		}
	case "delete", "ctrl+d":
		if t.pos < len(t.r) {
			t.r = append(t.r[:t.pos], t.r[t.pos+1:]...)
		}
	case "ctrl+u":
		t.r, t.pos = t.r[t.pos:], 0
	case "ctrl+w":
		i := t.pos
		for i > 0 && t.r[i-1] == ' ' {
			i--
		}
		for i > 0 && t.r[i-1] != ' ' {
			i--
		}
		t.r = append(t.r[:i], t.r[t.pos:]...)
		t.pos = i
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			ins := msg.Runes
			if msg.Type == tea.KeySpace {
				ins = []rune{' '}
			}
			t.r = append(t.r[:t.pos], append(append([]rune{}, ins...), t.r[t.pos:]...)...)
			t.pos += len(ins)
			return true
		}
		return false
	}
	return true
}

func (t textInput) view() string {
	return string(t.r[:t.pos]) + "▏" + string(t.r[t.pos:])
}

// ---- opening modals ----------------------------------------------------

// targets are the selected entries, or the entry under the cursor.
func (m model) targets() []entry {
	var out []entry
	for _, e := range m.all {
		if m.selected[e.name] {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		if e, ok := m.current(); ok {
			out = append(out, e)
		}
	}
	return out
}

func (m model) targetPaths() []string {
	var out []string
	for _, e := range m.targets() {
		out = append(out, filepath.Join(m.cwd, e.name))
	}
	return out
}

func (m *model) openInput(purpose, title, label, initial string, targets []string) {
	m.modal = &modal{kind: mInput, purpose: purpose, title: title, label: label, in: newInput(initial), targets: targets, prev: m.filter}
}

func (m *model) openConfirm(purpose, title string, lines []string, targets []string) {
	m.modal = &modal{kind: mConfirm, purpose: purpose, title: title, lines: lines, targets: targets}
}

func (m *model) openProps() {
	ts := m.targets()
	if len(ts) == 0 {
		return
	}
	m.modal = &modal{kind: mInfo, purpose: "props", title: "properties", lines: m.propLines(ts)}
}

func (m *model) openWith() {
	e, ok := m.current()
	if !ok || e.isDir {
		return
	}
	path := filepath.Join(m.cwd, e.name)
	apps := appsFor(path)
	if len(apps) == 0 {
		m.setMsg("no applications found for "+e.name, true)
		return
	}
	items := make([]menuItem, len(apps))
	for i, a := range apps {
		items[i] = menuItem{label: a.name, suffix: a.id, value: a.path}
	}
	m.modal = &modal{kind: mMenu, purpose: "openwith", title: "open " + e.name + " with", items: items, targets: []string{path}}
}

func (m model) propLines(ts []entry) []string {
	if len(ts) > 1 {
		var total int64
		dirs := 0
		for _, e := range ts {
			if e.isDir {
				dirs++
				total += dirSize(filepath.Join(m.cwd, e.name))
			} else {
				total += e.size
			}
		}
		return []string{
			fmt.Sprintf("%d items selected", len(ts)),
			fmt.Sprintf("%d folders, %d files", dirs, len(ts)-dirs),
			"Total size   " + humanSize(total),
		}
	}
	e := ts[0]
	path := filepath.Join(m.cwd, e.name)
	lines := []string{"Name         " + e.name, "Location     " + tildePath(m.cwd)}
	if target, err := os.Readlink(path); err == nil {
		lines = append(lines, "Link to      "+target)
	}
	if e.isDir {
		size, n := dirStats(path)
		lines = append(lines, "Type         folder", fmt.Sprintf("Contains     %s", plural(n, "item")), "Size         "+humanSize(size))
	} else {
		lines = append(lines, "Type         "+mimeOf(path), "Size         "+fmt.Sprintf("%s (%d bytes)", humanSize(e.size), e.size))
	}
	lines = append(lines, fmt.Sprintf("Permissions  %s (%04o)", e.mode, e.mode.Perm()))
	if st, ok := statOf(path); ok {
		owner, group := lookupIDs(st)
		lines = append(lines, "Owner        "+owner+":"+group,
			"Accessed     "+time.Unix(st.Atim.Sec, st.Atim.Nsec).Format("2006-01-02 15:04"))
	}
	lines = append(lines, "Modified     "+e.mod.Format("2006-01-02 15:04"))
	return lines
}

func statOf(path string) (*syscall.Stat_t, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

func lookupIDs(st *syscall.Stat_t) (owner, group string) {
	owner, group = fmt.Sprint(st.Uid), fmt.Sprint(st.Gid)
	if u, err := user.LookupId(owner); err == nil {
		owner = u.Username
	}
	if g, err := user.LookupGroupId(group); err == nil {
		group = g.Name
	}
	return
}

func dirStats(path string) (size int64, n int) {
	const limit = 200000
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == path {
			return nil
		}
		n++
		if info, ierr := d.Info(); ierr == nil && !d.IsDir() {
			size += info.Size()
		}
		if n >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	return
}

func dirSize(path string) int64 { s, _ := dirStats(path); return s }

func mimeOf(path string) string {
	out, err := exec.Command("gio", "info", "-a", "standard::content-type", path).Output()
	if err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if _, v, ok := strings.Cut(strings.TrimSpace(l), "standard::content-type: "); ok {
				return v
			}
		}
	}
	return "unknown"
}

// ---- key handling ------------------------------------------------------

func (m model) handleModalKey(msg tea.KeyMsg) (model, tea.Cmd) {
	md := m.modal
	key := msg.String()
	switch md.kind {
	case mHelp:
		return m.handlePaletteKey(md, msg)
	case mInfo:
		if md.purpose == "browsed" && m.browseOffer(key) {
			return m, nil
		}
		switch key {
		case "?", "esc", "q", "enter", "i":
			if md.purpose == "connecting" {
				if key != "esc" && key != "q" {
					break // still connecting: only esc cancels
				}
				m.cancelConnect()
			}
			m.modal = nil
		case "P": // same key as in the file list; p is preview there
			if md.purpose == "props" {
				m.openPerms()
			}
		case "down", "j":
			md.scroll++
		case "up", "k":
			md.scroll = max(0, md.scroll-1)
		}
	case mConfirm:
		switch key {
		case "y", "Y", "enter":
			m.modal = nil
			return m.runConfirmed(md)
		case "n", "N", "esc", "q":
			m.modal = nil
		}
	case mMenu:
		return m.handleMenuKey(md, key)
	case mPerms:
		return m.handlePermsKey(md, key)
	case mFind:
		return m.handleFindKey(md, msg)
	case mGrep:
		return m.handleGrepKey(md, msg)
	case mSettings:
		return m.handleSettingsKey(md, key)
	case mBulk:
		return m.handleBulkKey(md, msg)
	case mInput:
		switch key {
		case "ctrl+s":
			if md.purpose == "connectp" && md.secret && m.conn != nil && keyringSchemes[m.conn.scheme()] {
				m.conn.remember = !m.conn.remember
			}
		case "ctrl+r":
			if md.purpose == "connect" {
				m.modal = nil
				m.startRootMount(md.in.value())
				return m, nil
			}
		case "ctrl+g":
			if md.guest && m.conn != nil {
				uri := m.conn.uri
				m.conn.cancel()
				m.conn = nil
				return m, m.connect(uri, true)
			}
		case "esc":
			if md.purpose == "connectp" {
				m.cancelConnect()
			}
			if strings.HasPrefix(md.purpose, "root") {
				m.rm = nil
			}
			if md.purpose == "filter" {
				m.filter = md.prev
				m.applyView("")
			}
			m.modal = nil
		case "enter":
			m.modal = nil
			return m.acceptInput(md)
		default:
			md.in.handle(msg)
			if md.purpose == "filter" {
				m.filter = md.in.value()
				m.applyView("")
			}
		}
	}
	return m, nil
}

func (m model) handleMenuKey(md *modal, key string) (model, tea.Cmd) {
	if md.purpose == "places" {
		if mm, cmd, ok := m.handlePlacesKey(md, key); ok {
			return mm, cmd
		}
	}
	if md.purpose == "browse" && m.browseOffer(key) {
		return m, nil
	}
	switch key {
	case "esc", "q":
		if md.purpose == "connectq" {
			m.cancelConnect()
		}
		m.modal = nil
	case "up", "k":
		md.sel = max(0, md.sel-1)
	case "down", "j":
		md.sel = min(len(md.items)-1, md.sel+1)
	case "home", "g":
		md.sel = 0
	case "end", "G":
		md.sel = len(md.items) - 1
	case "enter", "right", "l":
		if md.sel < 0 || md.sel >= len(md.items) {
			m.modal = nil
			return m, nil
		}
		it := md.items[md.sel]
		m.modal = nil
		if md.purpose == "connectq" {
			return m, m.answerConnect(it.value, false)
		}
		if md.purpose == "browse" {
			return m, m.pickService(it.value)
		}
		return m, launchApp(it.value, md.targets[0])
	}
	return m, nil
}

func (m model) acceptInput(md *modal) (model, tea.Cmd) {
	val := strings.TrimSpace(md.in.value())
	switch md.purpose {
	case "connect":
		return m, m.connect(val, false)
	case "rootaddr":
		m.startRootMount(val)
	case "rootmp", "rootuser":
		return m, m.rootAnswer(md.purpose, val)
	case "rootpass":
		return m, m.rootAnswer(md.purpose, md.in.value())
	case "unlock":
		if len(md.places) == 1 {
			p := md.places[0]
			m.setMsg("unlocking "+p.name+"…", false)
			return m, mountDrive(p.drive, md.in.value())
		}
	case "connectp": // as typed: passwords may start or end with a space
		return m, m.answerConnect(md.in.value(), md.secret)
	case "filter":
		m.filter = val
		m.applyView("")
	case "rename":
		e, ok := m.current()
		if !ok || len(md.targets) != 1 {
			return m, nil
		}
		return m, m.run(func() opResult { return renameOp(m.cwd, e.name, strings.TrimSpace(md.in.value())) })
	case "newfile", "newfolder":
		dir, folder := m.cwd, md.purpose == "newfolder"
		return m, m.run(func() opResult { return createOp(dir, val, folder) })
	case "compress":
		if _, err := compressFormat(val); err != nil || validName(val) != nil || exists(filepath.Join(m.cwd, val)) {
			why := "the name must end in .zip or .tar.gz"
			if err == nil && validName(val) != nil {
				why = validName(val).Error()
			} else if err == nil {
				why = val + " already exists"
			}
			m.openInput("compress", "compress", "Archive name: "+why, val, md.targets)
			return m, nil
		}
		dir, items := m.cwd, md.targets
		return m, m.startJob("Compressing", plural(len(items), "item"), func(ctx context.Context, p *progress) opResult {
			return compressJob(ctx, p, dir, items, val)
		})
	case "goto":
		p := expandPath(val, m.cwd)
		if fi, err := os.Stat(p); err != nil {
			m.setMsg("no such path: "+val, true)
		} else if fi.IsDir() {
			m.chdir(p, "")
		} else {
			m.chdir(filepath.Dir(p), filepath.Base(p))
		}
	}
	return m, nil
}

func (m model) runConfirmed(md *modal) (model, tea.Cmd) {
	paths := md.targets
	switch md.purpose {
	case "trash":
		return m, m.run(func() opResult { return trashItems(paths) })
	case "purge":
		return m, m.run(func() opResult { return purgeItems(paths) })
	case "emptytrash":
		return m, m.run(emptyTrash)
	case "restart":
		if m.job != nil {
			m.setMsg("a job is running · press U to restart once it's done", false)
			return m, nil
		}
		m.restart = true
		return m, m.quit()
	case "allowfw":
		return m, m.allowDiscovery(md.targets[0])
	case "install":
		return m, m.install(md.targets[0])
	case "canceljob", "quitjob":
		if m.job != nil {
			m.job.cancel()
			m.quitting = md.purpose == "quitjob"
			m.setMsg("stopping…", false)
		} else if md.purpose == "quitjob" {
			return m, m.quit()
		}
	}
	return m, nil
}

func expandPath(p, cwd string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// ---- rendering ---------------------------------------------------------

func (m model) modalOverlay() tideui.Overlay {
	md := m.modal
	width := min(66, max(20, m.width-6))
	inner := width - 4
	r := m.renderer
	hints := func(h ...tideui.SoftHint) string { return r.RenderSoftHints(inner, h...) }
	body := []string{}
	switch md.kind {
	case mInput:
		if md.label != "" {
			body = append(body, md.label)
		}
		text := md.in.view()
		if md.secret {
			text = strings.Repeat("•", md.in.pos) + "▏" + strings.Repeat("•", len(md.in.r)-md.in.pos)
		}
		h := []tideui.SoftHint{{Key: "enter", Label: "confirm"}, {Key: "esc", Label: "cancel"}}
		if md.guest {
			h = append(h, tideui.SoftHint{Key: "ctrl+g", Label: "connect as guest"})
		}
		if md.purpose == "connect" {
			h = append(h, tideui.SoftHint{Key: "ctrl+r", Label: "system mount (root)"})
		}
		body = append(body, r.RenderSoftRow(tideui.SoftRow{Text: text, Selected: true}, inner))
		if md.purpose == "connectp" && md.secret && m.conn != nil && keyringSchemes[m.conn.scheme()] {
			box := "☐"
			if m.conn.remember {
				box = "☑"
			}
			body = append(body, box+" Remember the password in the keyring  (ctrl+s)")
		}
		body = append(body, "", hints(h...))
	case mConfirm:
		body = append(body, md.lines...)
		body = append(body, "", hints(tideui.SoftHint{Key: "y", Label: "yes"}, tideui.SoftHint{Key: "n", Label: "no"}))
	case mInfo:
		body = append(body, md.lines...)
		h := []tideui.SoftHint{{Key: "esc", Label: "close"}}
		if md.purpose == "props" {
			// Soft hints are rendered lowercase, so spell the capital out.
			h = append(h, tideui.SoftHint{Key: "shift+p", Label: "change permissions"})
		}
		body = append(body, "", hints(h...))
	case mPerms:
		body = append(body, m.permsBody(md.perm, inner)...)
	case mFind:
		body = append(body, m.findBody(md, inner)...)
	case mGrep:
		body = append(body, m.grepBody(md, inner)...)
	case mSettings:
		body = append(body, m.settingsBody(md, inner)...)
	case mBulk:
		body = append(body, m.bulkBody(md, inner)...)
	case mMenu:
		if md.purpose == "places" {
			body = append(body, m.placesBody(md, inner)...)
			break
		}
		body = append(body, md.lines...)
		if len(md.lines) > 0 {
			body = append(body, "")
		}
		rows := max(3, m.height-10)
		first := max(0, min(md.sel-rows/2, len(md.items)-rows))
		for i := first; i < min(len(md.items), first+rows); i++ {
			it := md.items[i]
			body = append(body, r.RenderSoftRow(tideui.SoftRow{Text: it.label, Suffix: it.suffix, Selected: i == md.sel, Muted: it.muted}, inner))
		}
		h := []tideui.SoftHint{{Key: "enter", Label: "open"}, {Key: "esc", Label: "close"}}
		switch md.purpose {
		case "connectq":
			h[0].Label = "choose"
		case "browse":
			h[0].Label = "connect"
		}
		body = append(body, "", hints(h...))
	case mHelp:
		body = append(body, m.paletteBody(md, inner)...)
	}
	return r.SoftPanelOverlay(tideui.SoftPanel{
		Prefix: "tidefiles", Title: md.title, Width: width,
		Content: r.RenderSoftBody(width, strings.Join(body, "\n")),
	})
}
