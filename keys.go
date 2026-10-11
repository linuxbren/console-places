package main

import "strings"

type action int

const (
	actNone action = iota
	actUp
	actDown
	actParent
	actOpen
	actTop
	actBottom
	actPageUp
	actPageDown
	actHalfUp
	actHalfDown
	actBack
	actForward
	actHome
	actGoTrash
	actNewWindow
	actGoto
	actFind
	actGrep
	actPlaces
	actBookmark
	actSelect
	actSelectAll
	actInvert
	actEscape
	actCopy
	actCut
	actPaste
	actTrash
	actDelete
	actRename
	actNewFile
	actNewFolder
	actUndo
	actRestore
	actEmptyTrash
	actProps
	actPerms
	actOpenWith
	actTerminal
	actEdit
	actCopyPath
	actNewTab
	actCloseTab
	actNextTab
	actPrevTab
	actTab1
	actTab2
	actTab3
	actTab4
	actTab5
	actTab6
	actTab7
	actTab8
	actTab9
	actExtract
	actCompress
	actHidden
	actWrap
	actMarkdown
	actSort
	actSortRev
	actFilter
	actRefresh
	actTheme
	actSettings
	actPreviewWider
	actPreviewNarrower
	actTogglePreview
	actScrollDown
	actScrollUp
	actHelp
	actUpdate
	actQuit
)

// binding ties keys to an action and drives both dispatch and the help
// overlay, so what is documented is what is bound. Arrow keys come first, as
// in most TUIs; vim-style letters remain as alternates.
type binding struct {
	act     action
	keys    []string
	display string // shown in help
	desc    string
}

type bindingGroup struct {
	title    string
	bindings []binding
}

var bindingGroups = []bindingGroup{
	{"Move", []binding{
		{actUp, []string{"up", "k"}, "↑ / ↓", "move  (also k / j)"},
		{actDown, []string{"down", "j"}, "", ""},
		{actParent, []string{"left", "backspace", "h", "alt+up"}, "←  backspace", "parent directory  (also h)"},
		{actOpen, []string{"right", "enter", "l"}, "→  enter", "open folder or file  (also l)"},
		{actBack, []string{"alt+left"}, "alt+← / alt+→", "history back / forward"},
		{actForward, []string{"alt+right"}, "", ""},
		{actTop, []string{"home", "g"}, "home / end", "first / last  (also g / G)"},
		{actBottom, []string{"end", "G"}, "", ""},
		{actPageUp, []string{"pgup", "ctrl+b"}, "pgup / pgdn", "page up / down"},
		{actPageDown, []string{"pgdown", "ctrl+f"}, "", ""},
		{actHalfUp, []string{"ctrl+u"}, "ctrl+u / ctrl+d", "half page up / down"},
		{actHalfDown, []string{"ctrl+d"}, "", ""},
	}},
	{"Go to", []binding{
		{actGoto, []string{"ctrl+l"}, "ctrl+l", "type a path"},
		{actFind, []string{"f"}, "f", "find files and folders below here (fuzzy)"},
		{actGrep, []string{"F"}, "F", "search inside files below here (alt+r: regex)"},
		{actPlaces, []string{"b"}, "b", "places and bookmarks"},
		{actBookmark, []string{"B"}, "B", "bookmark this folder"},
		{actHome, []string{"~"}, "~", "home folder"},
		{actGoTrash, []string{"alt+t"}, "alt+t", "trash"},
	}},
	{"Select", []binding{
		{actSelect, []string{" "}, "space", "select / unselect, move down"},
		{actSelectAll, []string{"A", "ctrl+a"}, "A", "select all"},
		{actInvert, []string{"*"}, "*", "invert selection"},
		{actEscape, []string{"esc"}, "esc", "clear selection / filter"},
	}},
	{"Files", []binding{
		// SUPER+C/X/V are Omarchy's system-wide shortcuts and lead in the docs;
		// they arrive as ctrl+y (via contrib/omarchy-super-c.lua), ctrl+x and a
		// terminal paste. c/x/v stay as a fallback that needs no desktop setup.
		{actCopy, []string{"c", "ctrl+y"}, "SUPER+C  c", "copy"},
		{actCut, []string{"x", "ctrl+x"}, "SUPER+X  x", "cut"},
		{actPaste, []string{"v", "ctrl+v"}, "SUPER+V  v", "paste  (works with other apps)"},
		{actTrash, []string{"d", "delete"}, "d  delete", "move to trash"},
		{actDelete, []string{"D", "shift+delete"}, "D", "delete permanently"},
		{actRename, []string{"f2", "r"}, "F2  r", "rename"},
		{actNewFile, []string{"n"}, "n", "new file"},
		{actNewFolder, []string{"N"}, "N", "new folder"},
		{actUndo, []string{"ctrl+z", "u"}, "ctrl+z  u", "undo last change"},
		{actRestore, []string{"R"}, "r  R", "restore from trash (in the trash, r restores)"},
		{actEmptyTrash, []string{"E"}, "E", "empty trash"},
		{actProps, []string{"i"}, "i", "properties"},
		{actPerms, []string{"P"}, "P", "permissions (chmod), here or in properties"},
		{actCopyPath, []string{"y"}, "y", "copy path as text"},
		{actExtract, []string{"X"}, "X", "extract archive(s) here  (enter on an archive browses it)"},
		{actCompress, []string{"Z"}, "Z", "compress into .zip or .tar.gz"},
	}},
	{"Tabs", []binding{
		{actNewTab, []string{"ctrl+t"}, "ctrl+t", "new tab (this folder)"},
		{actCloseTab, []string{"ctrl+w"}, "ctrl+w", "close tab"},
		{actNextTab, []string{"tab"}, "tab / shift+tab", "next / previous tab"},
		{actPrevTab, []string{"shift+tab"}, "", ""},
		{actTab1, []string{"1"}, "1 … 9", "go to tab 1 … 9"},
		{actTab2, []string{"2"}, "", ""},
		{actTab3, []string{"3"}, "", ""},
		{actTab4, []string{"4"}, "", ""},
		{actTab5, []string{"5"}, "", ""},
		{actTab6, []string{"6"}, "", ""},
		{actTab7, []string{"7"}, "", ""},
		{actTab8, []string{"8"}, "", ""},
		{actTab9, []string{"9"}, "", ""},
	}},
	{"Open", []binding{
		{actEdit, []string{"e"}, "e", "edit in $EDITOR"},
		{actOpenWith, []string{"o"}, "o", "open with…"},
		{actTerminal, []string{"t"}, "t", "terminal here"},
		{actNewWindow, []string{"ctrl+n"}, "ctrl+n", "new window (tidefiles here, in a new terminal)"},
	}},
	{"View", []binding{
		{actHidden, []string{"."}, ".", "show / hide hidden files"},
		{actWrap, []string{"w"}, "w", "preview word wrap (text and source)"},
		{actMarkdown, []string{"m"}, "m", "markdown preview: rendered / source"},
		{actSort, []string{"s"}, "s", "cycle sort: name, size, modified, type"},
		{actFilter, []string{"/"}, "/", "filter this folder"},
		{actTogglePreview, []string{"p"}, "p", "show / hide preview"},
		{actPreviewWider, []string{"shift+right"}, "shift+← / →", "narrower / wider preview"},
		{actPreviewNarrower, []string{"shift+left"}, "", ""},
		{actScrollDown, []string{"J"}, "J / K", "scroll preview"},
		{actScrollUp, []string{"K"}, "", ""},
		{actRefresh, []string{"ctrl+r"}, "ctrl+r", "refresh"},
		{actTheme, []string{"T"}, "T", "theme picker"},
		{actSettings, []string{"S"}, "S", "settings (tabs at startup, sort, previews…)"},
	}},
	{"App", []binding{
		{actHelp, []string{"?"}, "?", "help: every action (type to search, enter runs)"},
		{actUpdate, []string{"U"}, "U", "update tidefiles: check, install, or restart into a new version"},
		{actQuit, []string{"q", "ctrl+c"}, "q", "quit"},
	}},
}

var keyIndex = func() map[string]action {
	m := map[string]action{}
	for _, g := range bindingGroups {
		for _, b := range g.bindings {
			for _, k := range b.keys {
				m[k] = b.act
			}
		}
	}
	return m
}()

// statusHints is the always-visible shortcut strip. "? all keys" leads so it
// survives truncation on narrow terminals. SUPER+C/X/V are Omarchy's system
// shortcuts, so elsewhere the strip shows the plain keys.
func statusHints() string {
	if onOmarchy {
		return "? all keys  ↑↓←→ navigate  space select  SUPER+C/X/V copy cut paste  d trash  F2 rename  / filter  q quit"
	}
	return "? all keys  ↑↓←→ navigate  space select  c x v copy cut paste  d trash  F2 rename  / filter  q quit"
}

// onOmarchy is set at start: whether an Omarchy desktop (its theme) is here.
var onOmarchy bool

// keyLabel drops the Omarchy-only "SUPER+X" part of a key label off Omarchy.
func keyLabel(display string) string {
	if onOmarchy || !strings.HasPrefix(display, "SUPER+") {
		return display
	}
	if _, rest, ok := strings.Cut(display, "  "); ok {
		return rest
	}
	return display
}

// changesFiles are the actions that modify files; they wait while a
// background job is running.
var changesFiles = map[action]bool{
	actPaste: true, actTrash: true, actDelete: true, actRename: true, actNewFile: true,
	actNewFolder: true, actUndo: true, actRestore: true, actEmptyTrash: true, actPerms: true,
	actExtract: true, actCompress: true,
}

// allowedInArchive are the actions that make sense while browsing an
// archive, which is read-only; everything else explains how to extract.
var allowedInArchive = map[action]bool{
	actNone: true, actUp: true, actDown: true, actParent: true, actOpen: true, actTop: true, actBottom: true,
	actPageUp: true, actPageDown: true, actHalfUp: true, actHalfDown: true, actBack: true, actForward: true,
	actHome: true, actGoto: true, actPlaces: true, actSelect: true, actSelectAll: true, actInvert: true,
	actEscape: true, actFilter: true, actSort: true, actSortRev: true, actHidden: true, actWrap: true,
	actMarkdown: true, actTogglePreview: true, actPreviewWider: true, actPreviewNarrower: true,
	actScrollDown: true, actScrollUp: true, actRefresh: true, actTheme: true, actHelp: true, actUpdate: true, actQuit: true, actSettings: true,
	actExtract: true, actNewTab: true, actGoTrash: true, actNewWindow: true, actCloseTab: true, actNextTab: true, actPrevTab: true,
	actTab1: true, actTab2: true, actTab3: true, actTab4: true, actTab5: true, actTab6: true,
	actTab7: true, actTab8: true, actTab9: true,
}

// onTheFile are actions on the selection or the file under the cursor; on
// the home folder's Trash row (not a file) they explain instead.
var onTheFile = map[action]bool{
	actSelect: true, actCopy: true, actCut: true, actTrash: true, actDelete: true, actRename: true,
	actProps: true, actPerms: true, actCopyPath: true, actEdit: true, actOpenWith: true,
	actCompress: true, actExtract: true,
}

// currentOnly of those always use the cursor's entry, even with a selection.
var currentOnly = map[action]bool{actSelect: true, actRename: true, actEdit: true, actOpenWith: true}
