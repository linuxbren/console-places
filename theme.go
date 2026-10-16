package main

import (
	"math"
	"strconv"
	"strings"

	"github.com/allisonhere/tideui"
	"github.com/charmbracelet/lipgloss"

	"github.com/linuxbren/console-places/internal/omarchy"
)

const themeOmarchy = "omarchy"

// resolveTheme returns the tideui theme for mode: "omarchy" follows the live
// Omarchy desktop theme (falling back to Catppuccin Mocha when Omarchy is not
// present); anything else is a tideui built-in palette name.
func resolveTheme(mode string) (tideui.Theme, syntax) {
	if mode == themeOmarchy {
		if p, ok := omarchy.Current(); ok {
			t := omarchyTheme(p)
			return t, fromOmarchy(p).readable(t.Bg)
		}
		mode = tideui.CatppuccinMocha.Name
	}
	t, _ := tideui.ThemeByName(mode)
	return t, fromTheme(t.Bg, t.Fg, t.BorderFocus, t.Unread, t.Error, t.Dimmed).readable(t.Bg)
}

func omarchyTheme(p omarchy.Palette) tideui.Theme {
	c := func(s string) lipgloss.Color { return lipgloss.Color(s) }
	or := func(vals ...string) lipgloss.Color {
		for _, v := range vals {
			if v != "" {
				return c(v)
			}
		}
		return ""
	}
	bg, fg := c(p.Background), c(p.Foreground)
	accent := or(p.Accent, p.Foreground)

	// Omarchy's selection color is subtle; fall back to the accent when it
	// would be nearly invisible against the background.
	selected := or(p.Selection, p.Accent)
	if p.Selection == "" || ratio(selected, bg) < 1.3 {
		selected = accent
	}
	surface := or(p.Surface, p.Muted, p.Background)
	return tideui.Theme{
		Name:          "match-omarchy",
		Bg:            bg,
		Fg:            fg,
		Border:        or(p.Muted, p.Dim, p.Foreground),
		BorderFocus:   accent,
		Selected:      selected,
		Unread:        or(p.Ok, p.Accent),
		Dimmed:        or(p.Dim, p.Muted, p.Foreground),
		StatusBar:     surface,
		StatusFg:      fg,
		Error:         or(p.Error, p.Accent),
		Overlay:       surface,
		OverlayBorder: accent,
	}
}

func rgb(col lipgloss.Color) (r, g, b float64, ok bool) {
	s := strings.TrimPrefix(string(col), "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff), true
}

func luminance(col lipgloss.Color) float64 {
	r, g, b, ok := rgb(col)
	if !ok {
		return 0
	}
	lin := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// ratio is the WCAG contrast ratio between two colors.
func ratio(a, b lipgloss.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// setTerminalBg asks the terminal to paint its own background with the theme
// background so the margins around the UI match.
func setTerminalBg(t tideui.Theme) string {
	set, _ := tideui.TerminalBackgroundSequences(t)
	return set
}

func resetTerminalBg(t tideui.Theme) string {
	_, reset := tideui.TerminalBackgroundSequences(t)
	return reset
}
