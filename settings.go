package main

// The settings page (S): every preference in one list. ↑↓ choose, ←→ or
// enter change the value, which applies and saves at once; esc closes.

import (
	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
)

// Values of config.TabsOnStart.
const (
	tabsRestoreAlways   = ""         // reopen last session's tabs every time
	tabsRestoreNoFolder = "nofolder" // only when console-places is started without a folder
	tabsRestoreNever    = "never"
)

// restoreTabsAtStart says whether to reopen saved tabs, given whether a
// folder was named on the command line.
func restoreTabsAtStart(cfg config, folderGiven bool) bool {
	switch cfg.TabsOnStart {
	case tabsRestoreNever:
		return false
	case tabsRestoreNoFolder:
		return !folderGiven
	}
	return true
}

type setting struct {
	label   string
	choices []string                  // shown values, in order
	current func(model) int           // which choice is in effect
	apply   func(*model, int) tea.Cmd // switch to choice i
	action  bool                      // enter runs apply instead of cycling (e.g. the theme picker)
}

func onOffIndex(on bool) int {
	if on {
		return 0
	}
	return 1
}

func settingsList() []setting {
	return []setting{
		{label: "Tabs at startup", choices: []string{"restore last tabs", "restore if no folder given", "don't restore"},
			current: func(m model) int {
				switch m.cfg.TabsOnStart {
				case tabsRestoreNoFolder:
					return 1
				case tabsRestoreNever:
					return 2
				}
				return 0
			},
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.TabsOnStart = []string{tabsRestoreAlways, tabsRestoreNoFolder, tabsRestoreNever}[i]
				m.cfg.save()
				return nil
			}},
		{label: "Theme", choices: []string{"…"}, action: true,
			current: func(model) int { return 0 },
			apply:   func(m *model, _ int) tea.Cmd { m.modal = nil; m.openPicker(); return nil }},
		{label: "Hidden files", choices: []string{"shown", "hidden"},
			current: func(m model) int { return onOffIndex(m.cfg.ShowHidden) },
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.ShowHidden = i == 0
				m.cfg.save()
				m.reload(m.curName())
				return nil
			}},
		{label: "Sort by", choices: []string{"name", "size", "modified", "type"},
			current: func(m model) int {
				for i, s := range []string{"name", "size", "modified", "type"} {
					if m.cfg.Sort == s {
						return i
					}
				}
				return 0
			},
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.Sort = []string{"name", "size", "modified", "type"}[i]
				m.cfg.save()
				m.reload(m.curName())
				return nil
			}},
		{label: "Sort order", choices: []string{"ascending", "descending"},
			current: func(m model) int { return 1 - onOffIndex(m.cfg.SortDesc) },
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.SortDesc = i == 1
				m.cfg.save()
				m.reload(m.curName())
				return nil
			}},
		{label: "Preview pane", choices: []string{"shown", "hidden"},
			current: func(m model) int { return 1 - onOffIndex(m.cfg.HidePreview) },
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.HidePreview = i == 1
				m.cfg.save()
				m.pvKey = ""
				return nil
			}},
		{label: "Preview word wrap", choices: []string{"on", "off"},
			current: func(m model) int { return onOffIndex(m.cfg.Wrap) },
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.Wrap = i == 0
				m.cfg.save()
				m.pvKey = ""
				return nil
			}},
		{label: "Markdown preview", choices: []string{"rendered", "source"},
			current: func(m model) int { return 1 - onOffIndex(m.cfg.MarkdownSource) },
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.MarkdownSource = i == 1
				m.cfg.save()
				m.pvKey = ""
				return nil
			}},
		{label: "Image previews", choices: []string{"auto", "kitty", "sixel", "blocks"},
			current: func(m model) int {
				for i, s := range []string{"", "kitty", "sixel", "blocks"} {
					if m.cfg.ImageMode == s {
						return i
					}
				}
				return 0
			},
			apply: func(m *model, i int) tea.Cmd {
				m.setImageMode([]string{"auto", "kitty", "sixel", "blocks"}[i])
				m.setMsg("", false)
				return nil
			}},
		{label: "Places in the file list", choices: []string{"off (Trash in home)", "bookmarks, Trash", "all places"},
			current: func(m model) int {
				switch m.cfg.PlacesInList {
				case placesListBookmarks:
					return 1
				case placesListAll:
					return 2
				}
				return 0
			},
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.PlacesInList = []string{placesListOff, placesListBookmarks, placesListAll}[i]
				m.cfg.save()
				m.reload(m.curName())
				return m.rescanPlaces(true)
			}},
		{label: "Video previews", choices: []string{"play after a moment", "play with alt+p", "still frame"},
			current: func(m model) int {
				switch m.cfg.VideoPlay {
				case videoPlayKey:
					return 1
				case videoPlayOff:
					return 2
				}
				return 0
			},
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.VideoPlay = []string{videoPlayAuto, videoPlayKey, videoPlayOff}[i]
				m.cfg.save()
				m.pvKey = ""
				return nil
			}},
		{label: "Updates", choices: []string{"install automatically", "notify only", "off"},
			current: func(m model) int {
				switch m.cfg.Updates {
				case updatesNotify:
					return 1
				case updatesOff:
					return 2
				}
				return 0
			},
			apply: func(m *model, i int) tea.Cmd {
				m.cfg.Updates = []string{updatesAuto, updatesNotify, updatesOff}[i]
				m.cfg.save()
				return nil
			}},
	}
}

func (m *model) openSettings() {
	m.modal = &modal{kind: mSettings, title: "settings"}
}

func (m model) handleSettingsKey(md *modal, key string) (model, tea.Cmd) {
	list := settingsList()
	switch key {
	case "esc", "q", "S":
		m.modal = nil
		return m, nil
	case "up", "k", "shift+tab":
		md.sel = max(0, md.sel-1)
		return m, nil
	case "down", "j", "tab":
		md.sel = min(len(list)-1, md.sel+1)
		return m, nil
	}
	s := list[md.sel]
	step := 0
	switch key {
	case "right", "l", "enter", " ":
		step = 1
	case "left", "h":
		step = -1
	}
	if step == 0 {
		return m, nil
	}
	if s.action {
		if key == "enter" || key == " " {
			return m, s.apply(&m, 0)
		}
		return m, nil
	}
	n := len(s.choices)
	i := ((s.current(m)+step)%n + n) % n
	return m, s.apply(&m, i)
}

func (m model) settingsBody(md *modal, inner int) []string {
	r := m.renderer
	var body []string
	for i, s := range settingsList() {
		value := s.choices[s.current(m)]
		if s.action {
			value = m.themeMode + "  (enter)"
		} else {
			value = "‹ " + value + " ›"
		}
		body = append(body, r.RenderSoftRow(tideui.SoftRow{Text: s.label, Suffix: value, Selected: i == md.sel}, inner))
	}
	return append(body, "", r.RenderSoftHints(inner,
		tideui.SoftHint{Key: "↑↓", Label: "choose"},
		tideui.SoftHint{Key: "← → enter", Label: "change"},
		tideui.SoftHint{Key: "esc", Label: "close"}))
}
