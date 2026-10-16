package main

import (
	"bytes"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"

	"github.com/linuxbren/console-places/internal/omarchy"
)

// syntax is the palette used to highlight code in the preview.
type syntax struct {
	fg, kw, typ, fn, builtin, str, num, comment, op, tag, err string
}

func fromOmarchy(p omarchy.Palette) syntax {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return p.Foreground
	}
	return syntax{
		fg: p.Foreground, kw: pick(p.Accent, p.Magenta), typ: pick(p.Cyan, p.Blue), fn: pick(p.Blue, p.Accent),
		builtin: pick(p.Magenta, p.Cyan), str: pick(p.Ok, p.Yellow), num: pick(p.Orange, p.Yellow),
		comment: pick(p.Dim, p.Muted), op: pick(p.Cyan, p.Foreground), tag: pick(p.Yellow, p.Orange), err: pick(p.Error),
	}
}

// fromTheme derives a serviceable palette from any tideui theme.
func fromTheme(bg, fg, accent, green, red, dim lipgloss.Color) syntax {
	s := func(c lipgloss.Color) string { return string(c) }
	return syntax{fg: s(fg), kw: s(accent), typ: s(green), fn: s(accent), builtin: s(green), str: s(green),
		num: s(red), comment: s(dim), op: s(fg), tag: s(red), err: s(red)}
}

// readable falls back to fg when c would be hard to read on bg.
func (s syntax) readable(bg lipgloss.Color) syntax {
	fix := func(c string) string {
		if ratio(lipgloss.Color(c), bg) < 3.0 {
			return s.fg
		}
		return c
	}
	s.kw, s.typ, s.fn, s.builtin = fix(s.kw), fix(s.typ), fix(s.fn), fix(s.builtin)
	s.str, s.num, s.comment, s.op, s.tag, s.err = fix(s.str), fix(s.num), fix(s.comment), fix(s.op), fix(s.tag), fix(s.err)
	return s
}

func (s syntax) style() *chroma.Style {
	st, err := chroma.NewStyle("console-places", chroma.StyleEntries{
		chroma.Text:              s.fg,
		chroma.Keyword:           s.kw,
		chroma.KeywordType:       s.typ,
		chroma.Name:              s.fg,
		chroma.NameFunction:      s.fn,
		chroma.NameClass:         "bold " + s.typ,
		chroma.NameBuiltin:       s.builtin,
		chroma.NameTag:           s.tag,
		chroma.NameAttribute:     s.fn,
		chroma.NameDecorator:     s.builtin,
		chroma.LiteralString:     s.str,
		chroma.LiteralNumber:     s.num,
		chroma.Comment:           "italic " + s.comment,
		chroma.Operator:          s.op,
		chroma.Punctuation:       s.fg,
		chroma.GenericHeading:    "bold " + s.kw,
		chroma.GenericSubheading: "bold " + s.fn,
		chroma.GenericInserted:   s.str,
		chroma.GenericDeleted:    s.err,
		chroma.Error:             s.err,
	})
	if err != nil {
		return nil
	}
	return st
}

// highlight returns text with ANSI colors for the language of filename, and
// the language name. Unknown languages come back unchanged.
func highlight(text, filename string, s syntax) (string, string) {
	lexer := lexers.Match(filename)
	if lexer == nil || lexer.Config().Name == "plaintext" {
		return text, ""
	}
	style := s.style()
	if style == nil {
		return text, ""
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return text, ""
	}
	var buf bytes.Buffer
	if err := formatters.TTY16m.Format(&buf, style, it); err != nil {
		return text, ""
	}
	return strings.TrimRight(buf.String(), "\n"), lexer.Config().Name
}
