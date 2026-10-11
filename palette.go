package main

// The help palette (?): every action grouped by category with its key.
// Typing filters (fuzzy, on names, synonyms and keys), enter runs the chosen
// action, esc closes. It also holds the actions that have no key (sort
// modes, image mode), so it doubles as the settings menu.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const maxRecent = 5

type command struct {
	id       string // stable, for the recent list
	title    string
	synonyms string // more words to match on
	group    string
	act      action               // run with perform (after the guards)…
	run      func(*model) tea.Cmd // …or this, for actions without a key
	valid    func(model) bool     // nil: always available
}

type paletteState struct {
	all  []command // the commands valid when the palette opened
	rows []int     // indexes into all, in display order
	head map[int]string
}

var paletteGroups = []string{"Recent", "Navigate", "Files", "Search", "Archives", "Tabs", "View", "Settings", "App"}

// keysFor is the key column for an action, from the keymap table, so the
// palette shows what is really bound. Help rows that describe two actions
// ("↑ / ↓") give way to the action's own keys.
func keysFor(act action) string {
	pretty := strings.NewReplacer("up", "↑", "down", "↓", "left", "←", "right", "→", "pgdown", "pgdn", " ", "space")
	for _, g := range bindingGroups {
		for _, b := range g.bindings {
			if b.act != act {
				continue
			}
			if b.display != "" && !strings.Contains(b.display, " / ") && !strings.Contains(b.display, "…") {
				return keyLabel(b.display)
			}
			var ks []string
			for _, k := range b.keys[:min(2, len(b.keys))] {
				if strings.Contains(k, "+") || len(k) <= 1 {
					ks = append(ks, k)
				} else {
					ks = append(ks, pretty.Replace(k))
				}
			}
			return strings.Join(ks, "  ")
		}
	}
	return ""
}

func (m model) hasCur() bool { _, ok := m.current(); return ok }
func (m model) curIsFile() bool {
	e, ok := m.current()
	return ok && !e.isDir
}

// commands lists everything the palette can run, with what makes each valid.
func (m model) commands() []command {
	cur := func(mm model) bool { return mm.hasCur() }
	file := func(mm model) bool { return mm.curIsFile() }
	multiTab := func(mm model) bool { return len(mm.tabs) > 1 }
	onOff := func(on bool, yes, no string) string {
		if on {
			return yes
		}
		return no
	}
	c := []command{
		{title: "Move up", act: actUp, group: "Navigate"},
		{title: "Move down", act: actDown, group: "Navigate"},
		{title: "Parent folder", synonyms: "up back out", act: actParent, group: "Navigate"},
		{title: "Open", synonyms: "enter folder file launch", act: actOpen, group: "Navigate", valid: cur},
		{title: "Back", synonyms: "history previous folder", act: actBack, group: "Navigate"},
		{title: "Forward", synonyms: "history next folder", act: actForward, group: "Navigate"},
		{title: "First item", synonyms: "top", act: actTop, group: "Navigate"},
		{title: "Last item", synonyms: "bottom end", act: actBottom, group: "Navigate"},
		{title: "Page up", act: actPageUp, group: "Navigate"},
		{title: "Page down", act: actPageDown, group: "Navigate"},
		{title: "Half page up", act: actHalfUp, group: "Navigate"},
		{title: "Half page down", act: actHalfDown, group: "Navigate"},
		{title: "Go to a path…", synonyms: "type path location cd jump", act: actGoto, group: "Navigate"},
		{title: "Places and bookmarks", synonyms: "sidebar home downloads drives mounts", act: actPlaces, group: "Navigate"},
		{title: "Bookmark this folder", synonyms: "favourite favorite pin", act: actBookmark, group: "Navigate"},
		{title: "Home folder", synonyms: "~", act: actHome, group: "Navigate"},
		{title: "Trash", synonyms: "bin recycle deleted wastebasket", act: actGoTrash, group: "Navigate"},

		{title: "Select / unselect", synonyms: "mark toggle", act: actSelect, group: "Files", valid: cur},
		{title: "Select all", synonyms: "mark everything", act: actSelectAll, group: "Files"},
		{title: "Invert selection", act: actInvert, group: "Files"},
		{title: "Clear selection", synonyms: "unselect deselect escape", act: actEscape, group: "Files"},
		{title: "Copy", synonyms: "clipboard duplicate", act: actCopy, group: "Files", valid: cur},
		{title: "Cut", synonyms: "move clipboard", act: actCut, group: "Files", valid: cur},
		{title: "Paste", synonyms: "clipboard insert", act: actPaste, group: "Files"},
		{title: "Move to trash", synonyms: "delete remove bin", act: actTrash, group: "Files", valid: cur},
		{title: "Delete permanently", synonyms: "remove erase rm", act: actDelete, group: "Files", valid: cur},
		{title: "Rename", synonyms: "name move", act: actRename, group: "Files", valid: cur},
		{title: "New file", synonyms: "create touch", act: actNewFile, group: "Files"},
		{title: "New folder", synonyms: "create directory mkdir", act: actNewFolder, group: "Files"},
		{title: "Undo", synonyms: "revert", act: actUndo, group: "Files"},
		{title: "Restore from trash", synonyms: "undelete recover", act: actRestore, group: "Files", valid: func(mm model) bool { return mm.inTrash() && mm.hasCur() }},
		{title: "Empty trash", synonyms: "clear bin purge", act: actEmptyTrash, group: "Files", valid: func(model) bool { return trashCount() > 0 }},
		{title: "Properties", synonyms: "info details size owner", act: actProps, group: "Files", valid: cur},
		{title: "Permissions (chmod)", synonyms: "mode rights executable access", act: actPerms, group: "Files", valid: cur},
		{title: "Copy path", synonyms: "clipboard location filename text", act: actCopyPath, group: "Files", valid: cur},
		{title: "Edit in $EDITOR", synonyms: "vim nvim text", act: actEdit, group: "Files", valid: file},
		{title: "Open with…", synonyms: "application app program", act: actOpenWith, group: "Files", valid: file},
		{title: "Terminal here", synonyms: "shell console command line", act: actTerminal, group: "Files"},
		{title: "New window", synonyms: "open another tidefiles terminal", act: actNewWindow, group: "Files"},

		{title: "Filter this folder", synonyms: "narrow search names", act: actFilter, group: "Search"},
		{title: "Find files and folders", synonyms: "search names fuzzy locate recursive", act: actFind, group: "Search"},
		{title: "Search inside files", synonyms: "grep ripgrep content text find in files", act: actGrep, group: "Search"},

		{title: "Browse archive (read-only)", synonyms: "open zip tar look inside", act: actOpen, group: "Archives",
			valid: func(mm model) bool {
				e, ok := mm.current()
				return ok && !e.isDir && mm.arc == nil && isArchiveName(e.name)
			}},
		{title: "Extract here", synonyms: "unzip untar unpack decompress", act: actExtract, group: "Archives", valid: func(mm model) bool {
			if mm.arc != nil {
				return true
			}
			for _, p := range mm.targetPaths() {
				if isArchiveName(p) {
					return true
				}
			}
			return false
		}},
		{title: "Compress…", synonyms: "zip tar archive pack", act: actCompress, group: "Archives", valid: cur},

		{title: "New tab", synonyms: "open tab", act: actNewTab, group: "Tabs"},
		{title: "Close tab", act: actCloseTab, group: "Tabs", valid: multiTab},
		{title: "Next tab", act: actNextTab, group: "Tabs", valid: multiTab},
		{title: "Previous tab", act: actPrevTab, group: "Tabs", valid: multiTab},

		{title: onOff(m.cfg.HidePreview, "Show preview", "Hide preview"), synonyms: "preview pane toggle", act: actTogglePreview, group: "View"},
		{title: "Wider preview", act: actPreviewWider, group: "View"},
		{title: "Narrower preview", act: actPreviewNarrower, group: "View"},
		{title: "Scroll preview down", act: actScrollDown, group: "View"},
		{title: "Scroll preview up", act: actScrollUp, group: "View"},
		{title: onOff(m.cfg.Wrap, "Preview word wrap: turn off", "Preview word wrap: turn on"), synonyms: "wrap lines truncate", act: actWrap, group: "View"},
		{title: onOff(m.cfg.MarkdownSource, "Render markdown", "Show markdown source"), synonyms: "md preview raw", act: actMarkdown, group: "View"},
		{title: "Refresh", synonyms: "reload", act: actRefresh, group: "View"},

		{title: "Settings…", synonyms: "preferences options configure tabs startup", act: actSettings, group: "Settings"},
		{title: "Theme…", synonyms: "colors colours appearance omarchy", act: actTheme, group: "Settings"},
		{title: onOff(m.cfg.ShowHidden, "Hide hidden files", "Show hidden files"), synonyms: "dotfiles hidden toggle", act: actHidden, group: "Settings"},
		{id: "sort:reverse", title: onOff(m.cfg.SortDesc, "Sort ascending", "Sort descending (reverse)"), synonyms: "order reverse", act: actSortRev, group: "Settings"},
		{title: "Quit", synonyms: "exit close", act: actQuit, group: "App"},
	}
	for _, key := range []string{"name", "size", "modified", "type"} {
		key := key
		title := "Sort by " + key
		if m.cfg.Sort == key {
			title += "  ✓"
		}
		c = append(c, command{id: "sort:" + key, title: title, synonyms: "order sorting " + key, group: "Settings",
			run: func(mm *model) tea.Cmd {
				mm.cfg.Sort = key
				mm.cfg.save()
				mm.reload(mm.curName())
				mm.setMsg("sorted by "+key, false)
				return nil
			}})
	}
	for _, mode := range []string{"auto", "kitty", "sixel", "blocks"} {
		mode := mode
		title := "Image previews: " + mode
		if (m.cfg.ImageMode == "" && mode == "auto") || m.cfg.ImageMode == mode {
			title += "  ✓"
		}
		c = append(c, command{id: "images:" + mode, title: title, synonyms: "pictures graphics protocol sixel kitty blocks", group: "Settings",
			run: func(mm *model) tea.Cmd { mm.setImageMode(mode); return nil }})
	}
	for i := range m.tabs {
		i := i
		c = append(c, command{id: fmt.Sprintf("tab:%d", i+1), title: "Go to tab " + m.tabLabel(i), group: "Tabs",
			act: actTab1 + action(i), valid: func(mm model) bool { return i < 9 && len(mm.tabs) > 1 && i != mm.tab }})
	}

	var out []command
	for _, cm := range c {
		if cm.id == "" {
			// Recent actions are saved by id, so it must outlast versions: an
			// action's key does (one key, one meaning), while titles change
			// with state and action numbers shift as actions come and go.
			cm.id = "key:" + actionKey[cm.act] + ":" + cm.group
		}
		if cm.valid != nil && !cm.valid(m) {
			continue
		}
		if cm.run == nil && (m.arc != nil && !allowedInArchive[cm.act] || m.job != nil && changesFiles[cm.act]) {
			continue // the guards would refuse it right now
		}
		out = append(out, cm)
	}
	return out
}

func (cm command) keys() string {
	if cm.run != nil {
		return ""
	}
	return keysFor(cm.act)
}

func (m *model) openPalette() {
	p := &paletteState{all: m.commands()}
	m.modal = &modal{kind: mHelp, title: "keys and actions", in: newInput(""), pal: p}
	p.filter("", m.cfg.Recent)
}

// filter fills rows: grouped (recent first) for an empty query, ranked by
// match otherwise.
func (p *paletteState) filter(query string, recent []string) {
	p.rows, p.head = nil, map[int]string{}
	rank := map[string]int{}
	for i, id := range recent {
		rank[id] = len(recent) - i
	}
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		var rec []int
		for i, cm := range p.all {
			if rank[cm.id] > 0 {
				rec = append(rec, i)
			}
		}
		sort.SliceStable(rec, func(a, b int) bool { return rank[p.all[rec[a]].id] > rank[p.all[rec[b]].id] })
		add := func(group string, idx []int) {
			if len(idx) > 0 {
				p.head[len(p.rows)] = group
				p.rows = append(p.rows, idx...)
			}
		}
		add("Recent", rec)
		for _, g := range paletteGroups[1:] {
			var idx []int
			for i, cm := range p.all {
				if cm.group == g {
					idx = append(idx, i)
				}
			}
			add(g, idx)
		}
		return
	}
	type scored struct{ i, score int }
	var hits []scored
	for i, cm := range p.all {
		hay := strings.ToLower(cm.title + " | " + cm.keys() + " | " + cm.group + " " + cm.synonyms)
		total, ok := 0, true
		for _, t := range terms {
			s, _, matched := fuzzyScore(hay, t)
			if !matched {
				ok = false
				break
			}
			if strings.HasPrefix(strings.ToLower(cm.title), t) {
				s += 30 // the title starts with what you typed
			}
			if strings.EqualFold(cm.keys(), t) {
				s += 40 // typed the key itself
			}
			total += s
		}
		if ok {
			hits = append(hits, scored{i, total + 5*rank[cm.id]})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	for _, h := range hits {
		p.rows = append(p.rows, h.i)
	}
}

func (m model) handlePaletteKey(md *modal, msg tea.KeyMsg) (model, tea.Cmd) {
	p := md.pal
	switch msg.String() {
	case "esc":
		m.modal = nil
	case "?":
		if md.in.value() == "" {
			m.modal = nil // ? again closes, like before
		} else {
			md.in.handle(msg)
			p.filter(md.in.value(), m.cfg.Recent)
			md.sel = 0
		}
	case "up", "ctrl+p", "shift+tab":
		md.sel = max(0, md.sel-1)
	case "down", "ctrl+n", "tab":
		md.sel = min(max(0, len(p.rows)-1), md.sel+1)
	case "pgup":
		md.sel = max(0, md.sel-10)
	case "pgdown":
		md.sel = min(max(0, len(p.rows)-1), md.sel+10)
	case "enter":
		if md.sel >= len(p.rows) {
			break
		}
		cm := p.all[p.rows[md.sel]]
		m.modal = nil
		m.noteRecent(cm.id)
		if cm.run != nil {
			return m, cm.run(&m)
		}
		if mm, cmd, stop := m.guard(cm.act); stop {
			return mm, cmd
		}
		return m.perform(cm.act)
	default:
		if md.in.handle(msg) {
			p.filter(md.in.value(), m.cfg.Recent)
			md.sel = 0
		}
	}
	return m, nil
}

func (m *model) noteRecent(id string) {
	r := []string{id}
	for _, x := range m.cfg.Recent {
		if x != id && len(r) < maxRecent {
			r = append(r, x)
		}
	}
	m.cfg.Recent = r
	m.cfg.save()
}

func (m model) paletteBody(md *modal, inner int) []string {
	p := md.pal
	r := m.renderer
	st := r.Styles
	body := []string{r.RenderSoftRow(tideui.SoftRow{Text: md.in.view(), Selected: true}, inner)}
	if md.in.value() == "" {
		body = append(body, st.DetailMeta.Render("type to filter · enter runs · esc closes"))
	} else {
		body = append(body, st.DetailMeta.Render(plural(len(p.rows), "action")))
	}
	body = append(body, "")
	if len(p.rows) == 0 {
		return append(body, `  No action matches "`+sanitize(md.in.value())+`"`)
	}
	// Lines with group headers between; keep the selection in view.
	type line struct {
		text string
		row  int // -1 for a header
	}
	var lines []line
	for i, idx := range p.rows {
		if h, ok := p.head[i]; ok {
			if len(lines) > 0 {
				lines = append(lines, line{"", -1})
			}
			lines = append(lines, line{st.DetailMeta.Render(h), -1})
		}
		cm := p.all[idx]
		lines = append(lines, line{r.RenderSoftRow(tideui.SoftRow{Text: ansi.Truncate(cm.title, inner-20, "…"), Suffix: cm.keys(), Selected: i == md.sel}, inner), i})
	}
	selLine := 0
	for j, l := range lines {
		if l.row == md.sel {
			selLine = j
		}
	}
	rows := max(3, m.height-10)
	first := max(0, min(selLine-rows/2, len(lines)-rows))
	for j := first; j < min(len(lines), first+rows); j++ {
		body = append(body, lines[j].text)
	}
	return body
}

// setImageMode picks how images are drawn: a fixed protocol, or "auto"
// (what was detected at start).
func (m *model) setImageMode(mode string) {
	m.cfg.ImageMode = mode
	if mode == "auto" {
		m.cfg.ImageMode = ""
	}
	m.cfg.save()
	m.proto = imageProto(m.cfg.ImageMode, m.autoProto)
	m.gfx.set(nil, "") // drop the image drawn with the old protocol
	m.pvKey = ""
	m.setMsg("image previews: "+mode, false)
}

// imageProto resolves the configured mode; TIDEFILES_IMAGES still wins.
func imageProto(mode string, auto gfxProto) gfxProto {
	if os.Getenv("TIDEFILES_IMAGES") != "" {
		return auto // detectProto already applied the override
	}
	switch mode {
	case "kitty":
		return protoKitty
	case "sixel":
		return protoSixel
	case "blocks":
		return protoBlocks
	}
	return auto
}
