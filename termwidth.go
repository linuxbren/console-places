package main

// Terminals measure text one code point at a time (wcwidth), while the
// layout measures grapheme clusters, and the two disagree on spacing marks:
// Thai "ำ" or Devanagari "ा" add nothing to their cluster's width but take a
// cell of their own on screen. A line holding them comes out wider than the
// window; the terminal wraps it, every row below is drawn one lower and the
// screen scrolls (Brenden saw the panes jump and double up on a .desktop
// file's Thai comment). fitFrame cuts any such line to the window, measured
// the way the terminal measures, and sanitize splits Thai and Lao AM into
// their two parts so lines with them keep their full width.

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// splitAm turns Thai/Lao AM into nikhahit + aa (their compatibility
// decomposition): the same text, measured alike by terminals and layout.
var splitAm = strings.NewReplacer("ำ", "ํา", "ຳ", "ໍາ")

// cellWidth is how many cells a terminal gives s, code point by code point.
func cellWidth(s string) int {
	s = ansi.Strip(s)
	n := 0
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return len(s)
	}
	for _, r := range s {
		switch {
		case r < 0x80:
			n++
		case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		default:
			n += ansi.StringWidth(string(r))
		}
	}
	return n
}

// fitFrame cuts every line of a frame that a terminal would find wider than
// width.
func fitFrame(frame string, width int) string {
	lines := strings.Split(frame, "\n")
	changed := false
	for i, l := range lines {
		for cw := cellWidth(l); cw > width; cw = cellWidth(l) {
			l = ansi.Truncate(l, ansi.StringWidth(l)-max(1, cw-width), "")
			changed = true
		}
		lines[i] = l
	}
	if !changed {
		return frame
	}
	return strings.Join(lines, "\n")
}
