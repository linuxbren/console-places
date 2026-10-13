package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/linuxbren/tidefiles/internal/omarchy"
)

const (
	tickInterval = 2 * time.Second
	undoDepth    = 50
	doubleClick  = 400 * time.Millisecond
	wheelStep    = 3
)

const (
	sidebarRatio        = 0.20
	defaultPreviewRatio = 0.48
	minPreviewRatio     = 0.20
	maxPreviewRatio     = 0.75
	ratioStep           = 0.04
	previewDebounce     = 70 * time.Millisecond
)

type clipboard struct {
	paths []string
	cut   bool
}

type undoItem struct {
	desc string
	fn   func() error
}

type model struct {
	cwd     string
	cwdMod  time.Time
	all     []entry // sorted, hidden-filtered, before the name filter
	entries []entry // what is listed
	cursor  int
	offset  int
	filter  string

	parent       []entry
	parentCursor int

	cursors   map[string]string // dir -> last selected name, restored on re-entry
	selected  map[string]bool   // names in cwd
	clip      clipboard
	titleDir  string // folder the window title was last set for
	jump      jumpTarget
	job       *job     // the running background job, if any
	arc       *arcView // the archive being browsed, if any
	quitting  bool     // quit once the running job has stopped
	upd       updateState
	restart   bool                 // quit, then restart into an installed update
	tabs      []tabState           // all tabs; the fields above describe tabs[tab]
	trashN    int                  // items in the trash, for the home folder's Trash row
	trashMeta map[string]trashMeta // origins and dates, in the trash view
	tab       int
	undo      []undoItem
	conn      *connection // the network location being connected to
	rm        *rootMount  // a system mount being set up

	hist []string
	hi   int

	cfg config

	modal      *modal
	picker     tideui.ThemePicker
	pickerOpen bool

	width, height int

	themeMode string
	theme     tideui.Theme
	syn       syntax
	gfx       *gfxOut
	proto     gfxProto
	autoProto gfxProto // what detection picked at start, for image mode "auto"
	cellW     int
	cellH     int
	sig       string
	renderer  tideui.Renderer

	pv       preview
	pvKey    string
	psel     *previewSel // mouse selection in the preview
	pvPath   string
	pvScroll int

	msg    string
	msgErr bool

	lastClick    time.Time
	lastClickRow int

	cwdFile string
}

type (
	tickMsg   struct{}
	statusMsg struct {
		text string
		err  bool
	}
	opDoneMsg struct{ res opResult }
	undoMsg   struct {
		desc string
		err  error
	}
	reloadMsg struct{}

	previewTickMsg  struct{ key string }
	previewReadyMsg struct{ pv preview }
)

func newModel(dir string, cfg config, cwdFile string, gfx *gfxOut) model {
	cw, ch := cellPixels()
	m := model{
		gfx:       gfx,
		autoProto: detectProto(),
		cellW:     cw,
		cellH:     ch,
		cwd:       dir,
		cfg:       cfg,
		cursors:   map[string]string{},
		selected:  map[string]bool{},
		themeMode: cfg.Theme,
		cwdFile:   cwdFile,
		hist:      []string{dir},
	}
	m.proto = imageProto(cfg.ImageMode, m.autoProto)
	_, onOmarchy = omarchy.Current()
	m.upd = newUpdateState()
	m.applyTheme()
	if m.themeMode == themeOmarchy && !onOmarchy {
		m.setMsg("Omarchy theme not found, so using "+m.theme.Name+" · T picks a theme", false)
	}
	m.reload("")
	m.tabs = make([]tabState, 1)
	return m
}

func (m *model) applyTheme() {
	m.theme, m.syn = resolveTheme(m.themeMode)
	m.setRenderer(m.theme)
	if m.themeMode == themeOmarchy {
		m.sig = omarchy.Signature()
	}
}

func (m *model) setRenderer(t tideui.Theme) {
	m.renderer = tideui.NewRenderer(t, tideui.StyleOptions{Density: tideui.Compact})
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{writeTerm(setTerminalBg(m.theme)), tick()}
	if m.updateDue(time.Now()) {
		cmds = append(cmds, checkUpdate(false))
	}
	return tea.Batch(cmds...)
}

// windowTitle names the terminal window. Omarchy's Super+C override in
// ~/.config/hypr/bindings.lua recognises tidefiles by this prefix.
func windowTitle(dir string) string { return titlePrefix + tildePath(dir) }

const titlePrefix = "tidefiles — "

func tick() tea.Cmd { return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} }) }

func writeTerm(s string) tea.Cmd {
	return func() tea.Msg {
		if s != "" {
			fmt.Fprint(os.Stdout, s)
		}
		return nil
	}
}

// ---- geometry ----------------------------------------------------------

// ratios are the column shares: parent, current folder, preview.
func (m model) ratios() [3]float64 {
	p := m.cfg.PreviewRatio
	return [3]float64{sidebarRatio, 1 - sidebarRatio - p, p}
}

// columnWidths mirrors tideui's column split (three columns, or two when the
// preview is hidden).
func (m model) columnWidths() [3]int {
	width := m.width
	if m.cfg.HidePreview {
		a := max(1, min(width-1, int(float64(width)*0.28)))
		if width <= 1 {
			a = width
		}
		return [3]int{a, width - a, 0}
	}
	if width <= 2 {
		var out [3]int
		for i := 0; i < width; i++ {
			out[i] = 1
		}
		return out
	}
	r := m.ratios()
	total := r[0] + r[1] + r[2]
	avail := width - 3
	a := int(float64(avail) * r[0] / total)
	b := int(float64(avail) * r[1] / total)
	return [3]int{1 + a, 1 + b, 1 + avail - a - b}
}

// geometry returns each pane's inner width and the number of body rows.
func (m model) geometry() (inner [3]int, rows int) {
	for i, w := range m.columnWidths() {
		if w > 2 {
			w -= 2
		}
		inner[i] = max(1, w)
	}
	mainH := max(1, m.height-1-m.topOffset())
	if mainH > 2 {
		mainH -= 2
	}
	return inner, max(1, mainH-1)
}

// ---- state -------------------------------------------------------------

func (m model) current() (entry, bool) {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return entry{}, false
	}
	return m.entries[m.cursor], true
}

func (m model) curName() string { e, _ := m.current(); return e.name }

func (m model) inTrash() bool { return m.cwd == trashFilesDir() }

// reload re-reads cwd and its parent and rebuilds the listing, putting the
// cursor on focus (or the remembered selection when focus is empty).
func (m *model) reload(focus string) {
	if m.arc != nil {
		m.reloadArchive(focus)
		return
	}
	ents, err := readDir(m.cwd, m.cfg.ShowHidden)
	if err != nil {
		m.setMsg("cannot read "+tildePath(m.cwd)+": "+errText(err), true)
	}
	if fi, serr := os.Stat(m.cwd); serr == nil {
		m.cwdMod = fi.ModTime()
	}
	m.all = ents
	sortEntries(m.all, m.cfg.Sort, m.cfg.SortDesc)
	if m.showsTrashLink() {
		m.trashN = trashCount()
	}
	if m.inTrash() {
		m.loadTrashMeta()
	}
	for name := range m.selected {
		found := false
		for _, e := range m.all {
			if e.name == name {
				found = true
				break
			}
		}
		if !found {
			delete(m.selected, name)
		}
	}
	if focus == "" {
		focus = m.cursors[m.cwd]
	}
	m.applyView(focus)

	m.parent, m.parentCursor = nil, 0
	if pdir := filepath.Dir(m.cwd); pdir != m.cwd {
		m.parent, _ = readDir(pdir, m.cfg.ShowHidden)
		sortEntries(m.parent, m.cfg.Sort, m.cfg.SortDesc)
		base := filepath.Base(m.cwd)
		for i, e := range m.parent {
			if e.name == base {
				m.parentCursor = i
				break
			}
		}
	}
}

// applyView derives the visible list from all (name filter) and restores the
// cursor to focus, or the previously highlighted name.
func (m *model) applyView(focus string) {
	if focus == "" {
		focus = m.curName()
	}
	m.entries = m.entries[:0:0]
	needle := strings.ToLower(m.filter)
	for _, e := range m.all {
		if needle == "" || strings.Contains(strings.ToLower(e.name), needle) {
			m.entries = append(m.entries, e)
		}
	}
	m.entries = append(m.entries, m.trashRows()...) // pinned below everything, whatever the sort
	m.cursor = max(0, min(m.cursor, len(m.entries)-1))
	for i, e := range m.entries {
		if focus != "" && e.name == focus && e.kind != kindSeparator {
			m.cursor = i
			break
		}
	}
	if m.cursor < len(m.entries) && m.entries[m.cursor].kind == kindSeparator {
		m.cursor-- // never rest on the separator (it always has a row above it)
	}
	m.fixOffset()
	m.pvKey = ""
}

func (m *model) setMsg(text string, isErr bool) { m.msg, m.msgErr = text, isErr }

func (m *model) fixOffset() {
	_, rows := m.geometry()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.entries)-rows)))
}

func (m *model) move(delta int) {
	if len(m.entries) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.entries)-1, m.cursor+delta))
	if m.entries[m.cursor].kind == kindSeparator { // not a row you can stand on
		if delta < 0 || m.cursor == len(m.entries)-1 {
			m.cursor = max(0, m.cursor-1)
		} else {
			m.cursor++
		}
	}
	m.fixOffset()
}

// navigate changes directory. record adds the move to the back/forward history.
func (m *model) navigate(dir, focus string, record bool) {
	m.leaveArchive()
	if dir == trashFilesDir() {
		_ = ensureTrash() // the trash opens even before anything was deleted
	}
	if _, err := os.ReadDir(dir); err != nil {
		m.setMsg("cannot open "+tildePath(dir)+": "+errText(err), true)
		return
	}
	if e, ok := m.current(); ok {
		m.cursors[m.cwd] = e.name
	}
	if record && dir != m.cwd {
		m.hist = append(m.hist[:m.hi+1], dir)
		m.hi = len(m.hist) - 1
	}
	m.cwd, m.offset, m.cursor, m.filter = dir, 0, 0, ""
	m.selected = map[string]bool{}
	m.entries = nil
	m.reload(focus)
}

func (m *model) chdir(dir, focus string) { m.navigate(dir, focus, true) }

// jumpTarget is a file and line picked in content search: the preview shows
// that line, highlighted, until another file is selected.
type jumpTarget struct {
	path     string
	line     int
	scrolled bool // the preview has been scrolled to the line once
}

func (m model) previewOpts() previewOpts {
	inner, rows := m.geometry()
	line := 0
	if e, ok := m.current(); ok && filepath.Join(m.cwd, e.name) == m.jump.path {
		line = m.jump.line
	}
	return previewOpts{line: line, width: inner[2], rows: rows, wrap: m.cfg.Wrap, hidden: m.cfg.ShowHidden, mdSource: m.cfg.MarkdownSource,
		proto: m.proto, cellW: m.cellW, cellH: m.cellH, bg: m.theme.Bg, syn: m.syn}
}

// refreshPreview makes the preview pane match the selection. Cheap kinds build
// immediately; images, PDFs, archives and the like are built off the UI
// goroutine after a short debounce so scrolling stays smooth.
func (m *model) refreshPreview() tea.Cmd {
	e, ok := m.current()
	if m.jump.path != "" && (!ok || filepath.Join(m.cwd, e.name) != m.jump.path) {
		m.jump = jumpTarget{} // the content-search highlight lasts while its file is selected
	}
	if m.cfg.HidePreview {
		return nil
	}
	if ok && e.kind == kindTrashLink {
		if m.pvKey != "trash-link" {
			m.pv = dirPreview(preview{title: "Trash"}, trashFilesDir(), m.previewOpts())
			if m.trashN == 0 {
				m.pv.meta, m.pv.lines = "empty", []string{"  (empty)"}
			}
			m.pvKey, m.pvPath, m.pvScroll = "trash-link", "", 0
		}
		return nil
	}
	if !ok {
		m.pv, m.pvKey, m.pvPath = preview{title: "Preview", lines: []string{"  (nothing selected)"}}, "", ""
		return nil
	}
	o := m.previewOpts()
	where := m.cwd
	if m.arc != nil {
		where = m.arc.file + "|" + m.arc.dir // inside an archive
	}
	key := fmt.Sprintf("%s|%s|%d|%d|%t|%t|%t|%d|%d|%s|%dx%d|%s|%s", where, e.name, o.width, o.rows, o.wrap, o.hidden,
		o.mdSource, o.line, e.mod.UnixNano(), o.proto, o.cellW, o.cellH, o.bg, o.syn.kw)
	if key == m.pvKey {
		return nil
	}
	path := filepath.Join(m.cwd, e.name)
	if path != m.pvPath {
		m.pvScroll = 0
	}
	m.pvKey, m.pvPath = key, path
	if !classify(e).async() && m.arc == nil {
		m.pv = buildPreview(m.cwd, e, o)
		m.pv.key = key
		m.scrollToJump()
		m.clampPreviewScroll()
		return nil
	}
	m.pv = preview{key: key, title: e.name, meta: humanSize(e.size), lines: []string{"  loading…"}, loading: true}
	return tea.Tick(previewDebounce, func(time.Time) tea.Msg { return previewTickMsg{key} })
}

func (m model) buildPreviewCmd(key string) tea.Cmd {
	e, ok := m.current()
	if !ok {
		return nil
	}
	dir, o := m.cwd, m.previewOpts()
	if v := m.arc; v != nil {
		adir := v.dir
		return func() tea.Msg {
			pv := arcPreview(v, adir, e, o)
			pv.key = key
			return previewReadyMsg{pv}
		}
	}
	return func() tea.Msg {
		pv := buildPreview(dir, e, o)
		pv.key = key
		return previewReadyMsg{pv}
	}
}

// scrollToJump puts a freshly built preview's highlighted line a third of
// the way down the pane, once per jump so the user can scroll away.
func (m *model) scrollToJump() {
	if m.pv.hlTo <= m.pv.hlFrom || m.jump.scrolled || m.pvPath != m.jump.path {
		return
	}
	_, rows := m.geometry()
	m.pvScroll = max(0, m.pv.hlFrom-rows/3)
	m.jump.scrolled = true
}

func (m *model) clampPreviewScroll() {
	_, rows := m.geometry()
	m.pvScroll = max(0, min(m.pvScroll, len(m.pv.lines)-rows))
}

// run executes fn off the UI goroutine and reports its result as opDoneMsg.
func (m *model) run(fn func() opResult) tea.Cmd {
	m.setMsg("working…", false)
	return func() tea.Msg { return opDoneMsg{fn()} }
}

// ---- update ------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.cellW, m.cellH = cellPixels()
		m.fixOffset()
	case statusMsg:
		m.setMsg(msg.text, msg.err)
	case reloadMsg:
		m.reload(m.curName())
	case previewTickMsg:
		if msg.key == m.pvKey {
			cmd = m.buildPreviewCmd(msg.key)
		}
	case previewReadyMsg:
		if msg.pv.key != m.pvKey {
			if msg.pv.anim != nil {
				msg.pv.anim.stop()
			}
			break
		}
		m.pv = msg.pv
		m.scrollToJump()
		m.clampPreviewScroll()
		if m.pv.anim != nil {
			cmd = m.stepAnim(animTickMsg{a: m.pv.anim})
		}
	case animTickMsg:
		cmd = m.stepAnim(msg)
	case connectMsg:
		cmd = m.onConnect(msg)
	case firewallMsg:
		cmd = m.onFirewall(msg)
	case installMsg:
		m.onInstall(msg)
	case rootDoneMsg:
		m.onRootDone(msg)
	case discoverMsg:
		m.onDiscover(msg)
	case sharesMsg:
		m.onShares(msg)
	case placeMsg:
		if msg.cause != nil && m.offerInstall(msg.cause) {
			break
		}
		if msg.open != "" {
			m.chdir(msg.open, "")
		} else if m.cwd != "" {
			m.reload(m.curName())
		}
		m.setMsg(msg.text, msg.err)
	case opDoneMsg:
		m.finishOp(msg.res)
	case findBatchMsg:
		cmd = m.handleFindBatch(msg)
	case jobTickMsg:
		if m.job != nil && m.job.id == msg.id {
			cmd = jobTick(msg.id) // keeps the progress bar moving
		}
	case updateCheckedMsg:
		return m.onUpdateChecked(msg)
	case updateInstalledMsg:
		return m.onUpdateInstalled(msg)
	case jobDoneMsg:
		if m.job != nil && m.job.id == msg.id {
			m.job = nil
			if m.quitting {
				return m, m.quit()
			}
			m.finishOp(msg.res)
		}
	case arcOpenedMsg:
		switch {
		case msg.err != nil:
			m.setMsg(msg.err.Error(), true)
		case m.arc == nil && filepath.Dir(msg.file) == m.cwd:
			m.enterArchive(msg.view)
		default:
			msg.view.close() // the user moved on while it was listing
		}
	case grepBatchMsg:
		cmd = m.handleGrepBatch(msg)
	case grepTickMsg:
		cmd = m.handleGrepTick(msg)
	case undoMsg:
		if msg.err != nil {
			m.setMsg("undo failed: "+msg.err.Error(), true)
		} else {
			m.setMsg("Undid: "+msg.desc, false)
		}
		m.reload(m.curName())
	case tickMsg:
		cmd = tick()
		if m.themeMode == themeOmarchy && omarchy.Signature() != m.sig {
			m.applyTheme()
			cmd = tea.Batch(cmd, writeTerm(setTerminalBg(m.theme)))
		}
		if fi, err := os.Stat(m.cwd); err == nil && !fi.ModTime().Equal(m.cwdMod) {
			m.reload(m.curName())
		}
	case tea.MouseMsg:
		m, cmd = m.handleMouse(msg)
	case tea.KeyMsg:
		switch {
		case m.pickerOpen:
			m, cmd = m.handlePickerKey(msg)
		case m.modal != nil:
			m, cmd = m.handleModalKey(msg)
		default:
			m, cmd = m.handleKey(msg)
		}
	}
	if m.cwd != m.titleDir {
		m.titleDir = m.cwd
		cmd = tea.Batch(cmd, tea.SetWindowTitle(windowTitle(m.cwd)))
	}
	return m, tea.Batch(cmd, m.refreshPreview())
}

func (m *model) finishOp(res opResult) {
	switch {
	case res.err != nil:
		text := res.err.Error()
		if res.desc != "" {
			text = res.desc + ", then: " + text
		}
		m.setMsg(text, true)
	case res.desc != "":
		m.setMsg(res.desc, false)
	default:
		m.setMsg("", false)
	}
	if res.undo != nil {
		m.undo = append(m.undo, undoItem{res.desc, res.undo})
		if len(m.undo) > undoDepth {
			m.undo = m.undo[1:]
		}
	}
	if res.desc != "" || res.err != nil {
		m.selected = map[string]bool{}
	}
	m.reload(res.focus)
}

func (m model) handleKey(msg tea.KeyMsg) (model, tea.Cmd) {
	m.msg = ""
	m.psel = nil
	act := keyIndex[msg.String()]
	if msg.Paste {
		// The terminal's own paste (Omarchy's Super+V arrives as Shift+Insert,
		// which foot turns into a bracketed paste) means paste files here.
		act = actPaste
	}
	if m, cmd, stop := m.guard(act); stop {
		return m, cmd
	}
	return m.perform(act)
}

// guard stops actions that can't run now: file changes while a job runs
// (esc and q ask about the job instead), and changes inside an archive.
func (m model) guard(act action) (model, tea.Cmd, bool) {
	if m.job != nil {
		switch {
		case act == actEscape:
			label := strings.ToLower(m.job.prog.label())
			m.openConfirm("canceljob", "stop "+strings.ToLower(m.job.prog.getVerb()), []string{"Stop " + label + "?", m.job.stopNote()}, nil)
			return m, nil, true
		case act == actQuit:
			m.openConfirm("quitjob", "quit", []string{m.job.prog.label() + " is still running.", "Stop it and quit? " + m.job.stopNote()}, nil)
			return m, nil, true
		case changesFiles[act]:
			m.setMsg("busy: "+strings.ToLower(m.job.prog.label())+" (esc stops it)", true)
			return m, nil, true
		}
	}
	if cur, ok := m.current(); ok && cur.virtual() && onTheFile[act] && (len(m.selected) == 0 || currentOnly[act]) {
		m.setMsg("Trash is a shortcut, not a file: enter opens it", false)
		return m, nil, true
	}
	if m.arc != nil && !allowedInArchive[act] {
		m.setMsg("read-only inside an archive: X extracts it", true)
		return m, nil, true
	}
	return m, nil, false
}

// perform runs an action, from a key or the help palette. Callers check the
// busy and read-only guards first (see guard).
func (m model) perform(act action) (model, tea.Cmd) {
	_, rows := m.geometry()
	cur, hasCur := m.current()
	curPath := filepath.Join(m.cwd, cur.name)
	switch act {
	case actQuit:
		return m, m.quit()
	case actHelp:
		m.openPalette()
	case actUpdate:
		return m.updateAction()
	case actNewTab:
		m.newTab()
		m.setMsg("new tab ("+plural(len(m.tabs), "tab")+" open) · ctrl+w closes it", false)
	case actCloseTab:
		if !m.closeTab() {
			m.setMsg("that's the last tab (q quits)", false)
		}
	case actNextTab, actPrevTab:
		if len(m.tabs) > 1 {
			step := 1
			if act == actPrevTab {
				step = len(m.tabs) - 1
			}
			m.switchTab((m.tab + step) % len(m.tabs))
		}
	case actTab1, actTab2, actTab3, actTab4, actTab5, actTab6, actTab7, actTab8, actTab9:
		m.switchTab(int(act - actTab1))
	case actFind:
		m.openFind()
		return m, m.modal.find.next()
	case actGrep:
		m.openGrep()
	case actUp:
		m.move(-1)
	case actDown:
		m.move(1)
	case actTop:
		m.move(-len(m.entries))
	case actBottom:
		m.move(len(m.entries))
	case actHalfDown:
		m.move(rows / 2)
	case actHalfUp:
		m.move(-rows / 2)
	case actPageDown:
		m.move(rows)
	case actPageUp:
		m.move(-rows)
	case actParent:
		if m.arc != nil {
			m.archiveUp()
		} else if m.inTrash() {
			m.leaveTrash() // back to home, not into the trash's own internals
		} else if p := filepath.Dir(m.cwd); p != m.cwd {
			m.chdir(p, filepath.Base(m.cwd))
		}
	case actOpen:
		if !hasCur {
			break
		}
		switch {
		case cur.kind == kindTrashLink:
			m.openTrash()
		case m.arc != nil && cur.isDir:
			m.archiveInto(cur.name)
		case m.arc != nil:
			m.setMsg("read-only inside an archive: X extracts it", false)
		case cur.isDir:
			m.chdir(curPath, "")
		case isArchiveName(cur.name):
			m.setMsg("opening "+cur.name+"…", false)
			return m, openArchiveCmd(curPath)
		default:
			return m, openDefault(curPath)
		}
	case actExtract:
		var archives []string
		if m.arc != nil {
			archives = []string{m.arc.file}
			m.leaveArchive()
			m.reload(filepath.Base(archives[0]))
		} else {
			for _, p := range m.targetPaths() {
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() && isArchiveName(p) {
					archives = append(archives, p)
				}
			}
		}
		if len(archives) == 0 {
			m.setMsg("select an archive to extract (zip, tar, 7z, rar…)", true)
			break
		}
		dir := m.cwd
		return m, m.startJob("Extracting", plural(len(archives), "archive"), func(ctx context.Context, p *progress) opResult {
			return extractJob(ctx, p, dir, archives)
		})
	case actCompress:
		if paths := m.targetPaths(); len(paths) > 0 {
			m.openInput("compress", "compress", "Archive name (.zip or .tar.gz) for "+describe(m.targets()), defaultArchiveName(m.cwd, paths), paths)
		}
	case actBack:
		if m.hi > 0 {
			m.hi--
			m.navigate(m.hist[m.hi], "", false)
		}
	case actForward:
		if m.hi < len(m.hist)-1 {
			m.hi++
			m.navigate(m.hist[m.hi], "", false)
		}
	case actHome:
		if home, err := os.UserHomeDir(); err == nil {
			m.chdir(home, "")
		}
	case actGoto:
		m.openInput("goto", "go to", "Type a path (~ works)", tildePath(m.cwd)+"/", nil)
	case actPlaces:
		m.openPlaces()
	case actBookmark:
		m.toggleBookmark()
	case actSelect:
		if hasCur {
			if m.selected[cur.name] {
				delete(m.selected, cur.name)
			} else {
				m.selected[cur.name] = true
			}
			m.move(1)
		}
	case actSelectAll:
		for _, e := range m.entries {
			if !e.virtual() {
				m.selected[e.name] = true
			}
		}
	case actInvert:
		for _, e := range m.entries {
			if e.virtual() {
				continue
			}
			if m.selected[e.name] {
				delete(m.selected, e.name)
			} else {
				m.selected[e.name] = true
			}
		}
	case actEscape:
		switch {
		case m.filter != "":
			m.filter = ""
			m.applyView("")
		case len(m.selected) > 0:
			m.selected = map[string]bool{}
		}
	case actCopy, actCut:
		paths := m.targetPaths()
		if len(paths) == 0 {
			break
		}
		cut := act == actCut
		m.clip = clipboard{paths, cut}
		verb, doing := "Copied", "copying…"
		if cut {
			verb, doing = "Cut", "cutting…"
		}
		done := verb + " " + plural(len(paths), "item") + " — press v to paste"
		m.setMsg(doing, false)
		return m, func() tea.Msg {
			if err := clipWrite(cut, paths); err == errNoClipboard {
				// Not an error: there's just no system clipboard (console, ssh…).
				return statusMsg{done + " (inside tidefiles; no system clipboard here)", false}
			} else if err != nil {
				return statusMsg{done + " here only (system clipboard: " + err.Error() + ")", true}
			}
			return statusMsg{done, false}
		}
	case actPaste:
		dir, internal := m.cwd, m.clip
		return m, m.startJob("Pasting", "", func(ctx context.Context, p *progress) opResult {
			paths, cut, ok := clipRead()
			if !ok {
				paths, cut = internal.paths, internal.cut
			}
			if len(paths) == 0 {
				return opResult{desc: "Nothing to paste"}
			}
			res := pasteJob(ctx, p, dir, paths, cut)
			if res.desc == "" && res.err == nil {
				res.desc = "Nothing to paste"
			}
			return res
		})
	case actTrash:
		paths := m.targetPaths()
		if len(paths) == 0 {
			break
		}
		if m.inTrash() {
			m.openConfirm("purge", "delete permanently", []string{"Permanently delete " + describe(m.targets()) + "?", "This cannot be undone."}, paths)
			break
		}
		return m, m.run(func() opResult { return trashItems(paths) })
	case actDelete:
		if paths := m.targetPaths(); len(paths) > 0 {
			m.openConfirm("purge", "delete permanently", []string{"Permanently delete " + describe(m.targets()) + "?", "This cannot be undone."}, paths)
		}
	case actRename:
		if m.inTrash() { // in the trash, r restores
			return m.perform(actRestore)
		}
		if ts := m.targets(); len(m.selected) > 1 {
			m.openBulkRename(ts)
		} else if hasCur {
			m.openInput("rename", "rename", "New name for "+cur.name, cur.name, []string{curPath})
		}
	case actNewFile:
		m.openInput("newfile", "new file", "File name", "", nil)
	case actNewFolder:
		m.openInput("newfolder", "new folder", "Folder name", "", nil)
	case actUndo:
		if len(m.undo) == 0 {
			m.setMsg("nothing to undo", false)
			break
		}
		it := m.undo[len(m.undo)-1]
		m.undo = m.undo[:len(m.undo)-1]
		return m, func() tea.Msg { return undoMsg{it.desc, it.fn()} }
	case actRestore:
		if !m.inTrash() {
			m.setMsg("restore works in the trash (alt+t opens it)", false)
			break
		}
		var names []string
		for _, e := range m.targets() {
			names = append(names, e.name)
		}
		if len(names) > 0 {
			return m, m.run(func() opResult { return restoreItems(names) })
		}
	case actEmptyTrash:
		if n := trashCount(); n == 0 {
			m.setMsg("the trash is already empty", false)
		} else {
			m.openConfirm("emptytrash", "empty trash", []string{fmt.Sprintf("Permanently delete all %s in the trash?", plural(n, "item")), "This cannot be undone."}, nil)
		}
	case actGoTrash:
		m.openTrash()
	case actNewWindow:
		return m, newWindow(m.cwd)
	case actProps:
		m.openProps()
	case actPerms:
		m.openPerms()
	case actOpenWith:
		m.openWith()
	case actTerminal:
		return m, terminalHere(m.cwd)
	case actEdit:
		if hasCur && !cur.isDir {
			return m, editFile(curPath)
		}
	case actCopyPath:
		paths := m.targetPaths()
		if len(paths) > 0 {
			text := strings.Join(paths, "\n")
			return m, func() tea.Msg {
				if err := textWrite(text); err != nil {
					return statusMsg{"copy path failed: " + err.Error(), true}
				}
				return statusMsg{"Copied " + plural(len(paths), "path"), false}
			}
		}
	case actHidden:
		m.cfg.ShowHidden = !m.cfg.ShowHidden
		m.cfg.save()
		m.reload(m.curName())
		m.setMsg(map[bool]string{true: "showing hidden files", false: "hiding hidden files"}[m.cfg.ShowHidden], false)
	case actMarkdown:
		m.cfg.MarkdownSource = !m.cfg.MarkdownSource
		m.cfg.save()
		m.setMsg(map[bool]string{true: "markdown shown as source", false: "markdown rendered"}[m.cfg.MarkdownSource], false)
	case actWrap:
		m.cfg.Wrap = !m.cfg.Wrap
		m.cfg.save()
		m.setMsg(map[bool]string{true: "preview wrap on", false: "preview wrap off"}[m.cfg.Wrap], false)
	case actSort:
		keys := []string{"name", "size", "modified", "type"}
		for i, k := range keys {
			if k == m.cfg.Sort {
				m.cfg.Sort = keys[(i+1)%len(keys)]
				break
			}
		}
		m.cfg.save()
		m.reload(m.curName())
		m.setMsg("sorted by "+m.cfg.Sort, false)
	case actSortRev:
		m.cfg.SortDesc = !m.cfg.SortDesc
		m.cfg.save()
		m.reload(m.curName())
	case actFilter:
		m.openInput("filter", "filter", "Show only names containing…", m.filter, nil)
	case actRefresh:
		m.reload(m.curName())
		m.setMsg("refreshed", false)
	case actSettings:
		m.openSettings()
	case actTheme:
		m.openPicker()
	case actTogglePreview:
		m.cfg.HidePreview = !m.cfg.HidePreview
		m.cfg.save()
		m.pvKey = ""
		m.setMsg(map[bool]string{true: "preview hidden", false: "preview shown"}[m.cfg.HidePreview], false)
	case actPreviewWider, actPreviewNarrower:
		step := ratioStep
		if act == actPreviewNarrower {
			step = -step
		}
		m.cfg.PreviewRatio = math.Round(max(minPreviewRatio, min(maxPreviewRatio, m.cfg.PreviewRatio+step))*100) / 100
		m.cfg.HidePreview = false
		m.cfg.save()
	case actScrollDown:
		m.pvScroll++
		m.clampPreviewScroll()
	case actScrollUp:
		m.pvScroll = max(0, m.pvScroll-1)
	}
	return m, nil
}

func describe(ts []entry) string {
	if len(ts) == 1 {
		return "“" + ts[0].name + "”"
	}
	return plural(len(ts), "item")
}

// ---- theme picker ------------------------------------------------------

func (m *model) openPicker() {
	omarchyT, _ := resolveTheme(themeOmarchy)
	themes := append(append([]tideui.Theme(nil), tideui.BuiltinThemes...), omarchyT)
	if themes[len(themes)-1].Name != "match-omarchy" {
		themes = themes[:len(themes)-1]
	}
	m.picker = tideui.NewThemePicker(tideui.ThemePickerOptions{Themes: themes, Title: "THEME"})
	current := m.themeMode
	if current == themeOmarchy {
		current = "match-omarchy"
	}
	m.picker.Open(current)
	m.pickerOpen = true
}

func (m model) handlePickerKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch m.picker.Update(msg) {
	case tideui.ThemePickerConfirm:
		m.pickerOpen = false
		name := m.picker.ConfirmedTheme().Name
		if name == "match-omarchy" {
			name = themeOmarchy
		}
		m.themeMode, m.cfg.Theme = name, name
		m.cfg.save()
		m.applyTheme()
		return m, writeTerm(setTerminalBg(m.theme))
	case tideui.ThemePickerCancel:
		m.pickerOpen = false
		m.applyTheme()
		return m, writeTerm(setTerminalBg(m.theme))
	}
	m.setRenderer(m.picker.PreviewTheme())
	return m, writeTerm(setTerminalBg(m.picker.PreviewTheme()))
}

// ---- mouse -------------------------------------------------------------

func (m model) handleMouse(msg tea.MouseMsg) (model, tea.Cmd) {
	if m.modal != nil || m.pickerOpen {
		return m, nil
	}
	if cmd, used := m.mouseSelect(msg); used {
		return m, cmd
	}
	w := m.columnWidths()
	_, rows := m.geometry()
	if m.topOffset() > 0 && msg.Y == 0 {
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			m.switchTab(m.tabAt(msg.X))
		}
		return m, nil
	}
	row := msg.Y - 2 - m.topOffset() // tab strip, border, header
	inRows := row >= 0 && row < rows
	col := 0
	switch {
	case !m.cfg.HidePreview && msg.X >= w[0]+w[1]:
		col = 2
	case msg.X >= w[0]:
		col = 1
	}
	switch {
	case msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress:
		if col == 2 {
			m.pvScroll += wheelStep
			m.clampPreviewScroll()
		} else {
			m.move(wheelStep)
		}
	case msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress:
		if col == 2 {
			m.pvScroll = max(0, m.pvScroll-wheelStep)
		} else {
			m.move(-wheelStep)
		}
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && inRows:
		switch col {
		case 0:
			if m.arc != nil {
				break // the parent pane shows the archive's folders, not real paths
			}
			if i := row + max(0, min(m.parentCursor-rows/2, len(m.parent)-rows)); i < len(m.parent) {
				m.chdir(filepath.Join(filepath.Dir(m.cwd), m.parent[i].name), "")
			}
		case 1:
			i := row + m.offset
			if i >= len(m.entries) {
				break
			}
			if m.entries[i].kind == kindSeparator {
				break
			}
			double := i == m.lastClickRow && time.Since(m.lastClick) < doubleClick
			m.lastClick, m.lastClickRow = time.Now(), i
			m.cursor = i
			m.fixOffset()
			if double {
				return m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
			}
		}
	}
	return m, nil
}

// ---- misc --------------------------------------------------------------

func (m model) quit() tea.Cmd {
	m.cfg.Tabs = m.tabDirs()
	m.cfg.save()
	for _, t := range m.tabs {
		if t.arc != nil {
			t.arc.close()
		}
	}
	m.leaveArchive()
	m.gfx.set(nil, "")
	if m.cwdFile != "" {
		_ = os.WriteFile(m.cwdFile, []byte(m.cwd), 0o600)
	}
	return tea.Sequence(writeTerm(resetTerminalBg(m.theme)), tea.Quit)
}

func editFile(path string) tea.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "nvim"
	}
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], append(parts[1:], path)...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return statusMsg{"editor: " + err.Error(), true}
		}
		return reloadMsg{}
	})
}
