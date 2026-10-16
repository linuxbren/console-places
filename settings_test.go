package main

import (
	"encoding/json"
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRestoreTabsAtStart(t *testing.T) {
	for _, c := range []struct {
		mode        string
		folderGiven bool
		want        bool
	}{
		{tabsRestoreAlways, false, true}, {tabsRestoreAlways, true, true},
		{tabsRestoreNoFolder, false, true}, {tabsRestoreNoFolder, true, false},
		{tabsRestoreNever, false, false}, {tabsRestoreNever, true, false},
	} {
		if got := restoreTabsAtStart(config{TabsOnStart: c.mode}, c.folderGiven); got != c.want {
			t.Errorf("mode %q, folder given %v: %v", c.mode, c.folderGiven, got)
		}
	}
}

// Every setting can be set to each of its choices and reads back as set.
func TestSettingsRoundTrip(t *testing.T) {
	m := testModel(t, t.TempDir())
	t.Setenv("CONSOLE_PLACES_IMAGES", "") // let the image setting decide
	for _, s := range settingsList() {
		if s.action {
			continue
		}
		for i := range s.choices {
			s.apply(&m, i)
			if got := s.current(m); got != i {
				t.Errorf("%s: set %q, reads back %q", s.label, s.choices[i], s.choices[got])
			}
		}
	}
	var saved config
	b, _ := os.ReadFile(os.Getenv("CONSOLE_PLACES_CONFIG"))
	if err := json.Unmarshal(b, &saved); err != nil || saved.TabsOnStart != tabsRestoreNever || saved.ImageMode != "blocks" {
		t.Fatalf("not saved: %v %+v", err, saved)
	}
}

func TestSettingsPageKeys(t *testing.T) {
	m := testModel(t, t.TempDir())
	m = press(t, m, "S")
	if m.modal == nil || m.modal.kind != mSettings {
		t.Fatal("S should open settings")
	}
	key := func(k tea.KeyMsg) { m, _ = m.handleModalKey(k) }
	right, left := tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyLeft}
	// Row 0: tabs at startup. Right steps through the choices and wraps.
	key(right)
	if m.cfg.TabsOnStart != tabsRestoreNoFolder {
		t.Fatalf("right: %q", m.cfg.TabsOnStart)
	}
	key(right)
	key(right)
	if m.cfg.TabsOnStart != tabsRestoreAlways {
		t.Fatalf("wrap: %q", m.cfg.TabsOnStart)
	}
	key(left)
	if m.cfg.TabsOnStart != tabsRestoreNever {
		t.Fatalf("left wraps backwards: %q", m.cfg.TabsOnStart)
	}
	key(tea.KeyMsg{Type: tea.KeyDown})
	key(tea.KeyMsg{Type: tea.KeyDown}) // hidden files
	before := m.cfg.ShowHidden
	key(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.ShowHidden == before || m.modal == nil {
		t.Fatal("enter should change the value and keep the page open")
	}
	key(tea.KeyMsg{Type: tea.KeyUp}) // theme: enter opens the picker
	key(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.pickerOpen || m.modal != nil {
		t.Fatal("enter on Theme should open the theme picker")
	}
	m.pickerOpen = false
	m = press(t, m, "S")
	key(tea.KeyMsg{Type: tea.KeyEsc})
	if m.modal != nil {
		t.Fatal("esc closes")
	}
}

func TestSettingsReachableFromPalette(t *testing.T) {
	m := testModel(t, t.TempDir())
	m.openPalette()
	m = typeInto(t, m, "settings")
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modal == nil || m.modal.kind != mSettings {
		t.Fatal("palette 'settings' should open the settings page")
	}
	if keysFor(actSortRev) != "" {
		t.Fatal("reverse sort no longer has a key (S is settings)")
	}
	m.modal = nil
	m.openPalette()
	m = typeInto(t, m, "descending")
	if got := paletteTitles(m); len(got) == 0 || got[0] != "Sort descending (reverse)" {
		t.Fatalf("reverse sort still in the palette: %v", got)
	}
}
