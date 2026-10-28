package main

// File-type icons in the file list (Settings → File icons). Off by default:
// they are Nerd Font glyphs, which Omarchy ships but other terminals may not
// have. Everything lives here; renderList calls fileIcon and settings has the
// toggle.

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type iconKind int

const (
	iconFile iconKind = iota
	iconFolder
	iconCode
	iconImage
	iconVideo
	iconAudio
	iconArchive
	iconDoc
	iconExec
)

type fileIconDef struct {
	glyph string
	kind  iconKind
}

// Nerd Font glyphs (v3 code points).
const (
	glyphFolder  = "\uf07b" // nf-fa-folder
	glyphFile    = "\uf15b" // nf-fa-file
	glyphExec    = "\uf489" // nf-oct-terminal
	glyphImage   = "\uf03e" // nf-fa-image
	glyphVideo   = "\uf03d" // nf-fa-video_camera
	glyphAudio   = "\uf001" // nf-fa-music
	glyphArchive = "\uf410" // nf-oct-file_zip
	glyphText    = "\uf15c" // nf-fa-file_text
	glyphPDF     = "\uf1c1" // nf-fa-file_pdf_o
	glyphSheet   = "\uf1c3" // nf-fa-file_excel_o
	glyphSlides  = "\uf1c4" // nf-fa-file_powerpoint_o
	glyphWord    = "\uf1c2" // nf-fa-file_word_o
	glyphCode    = "\uf121" // nf-fa-code, for languages without their own
)

// extIcons maps a lower-case extension to its icon.
var extIcons = func() map[string]fileIconDef {
	m := map[string]fileIconDef{}
	add := func(glyph string, kind iconKind, exts ...string) {
		for _, e := range exts {
			m[e] = fileIconDef{glyph, kind}
		}
	}
	// Code, by language.
	add("\ue627", iconCode, ".go")                                     // nf-seti-go
	add("\ue606", iconCode, ".py")                                     // nf-seti-python
	add("\ue74e", iconCode, ".js", ".mjs", ".cjs", ".jsx")             // nf-dev-javascript
	add("\ue628", iconCode, ".ts", ".tsx")                             // nf-seti-typescript
	add("\ue7a8", iconCode, ".rs")                                     // nf-dev-rust
	add("\ue61e", iconCode, ".c", ".h")                                // nf-custom-c
	add("\ue61d", iconCode, ".cpp", ".cc", ".cxx", ".hpp", ".hh")      // nf-custom-cpp
	add("\ue620", iconCode, ".lua")                                    // nf-seti-lua
	add("\ue739", iconCode, ".rb")                                     // nf-dev-ruby
	add("\ue738", iconCode, ".java")                                   // nf-dev-java
	add("\ue736", iconCode, ".html", ".htm")                           // nf-dev-html5
	add("\ue749", iconCode, ".css", ".scss")                           // nf-dev-css3
	add("\ue60b", iconCode, ".json")                                   // nf-seti-json
	add("\ue615", iconCode, ".toml", ".yaml", ".yml", ".ini", ".conf") // nf-seti-config
	add(glyphExec, iconCode, ".sh", ".bash", ".zsh", ".fish")
	add(glyphCode, iconCode, ".zig", ".nix", ".php", ".swift", ".kt", ".cs", ".sql", ".xml", ".vim")
	// Documents.
	add("\uf48a", iconDoc, ".md", ".markdown") // nf-oct-markdown
	add(glyphText, iconDoc, ".txt", ".rtf", ".odt", ".log")
	add(glyphWord, iconDoc, ".doc", ".docx")
	add(glyphPDF, iconDoc, ".pdf")
	add(glyphSheet, iconDoc, ".xls", ".xlsx", ".ods", ".csv")
	add(glyphSlides, iconDoc, ".ppt", ".pptx", ".odp")
	// Media and archives.
	add(glyphImage, iconImage, ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".heic", ".avif", ".ico")
	add(glyphVideo, iconVideo, ".mp4", ".mkv", ".webm", ".mov", ".avi")
	add(glyphAudio, iconAudio, ".mp3", ".flac", ".ogg", ".opus", ".wav", ".m4a")
	add(glyphArchive, iconArchive, ".zip", ".tar", ".gz", ".tgz", ".xz", ".zst", ".bz2", ".7z", ".rar")
	return m
}()

// iconFor picks the icon for a file list entry.
func iconFor(e entry) fileIconDef {
	if e.isDir {
		return fileIconDef{glyphFolder, iconFolder}
	}
	if d, ok := extIcons[strings.ToLower(filepath.Ext(e.name))]; ok {
		return d
	}
	if e.mode.IsRegular() && e.mode.Perm()&0o111 != 0 {
		return fileIconDef{glyphExec, iconExec}
	}
	return fileIconDef{glyphFile, iconFile}
}

// iconColor is the active palette's colour for a kind of file.
func (m model) iconColor(k iconKind) string {
	switch k {
	case iconFolder:
		return string(m.theme.BorderFocus)
	case iconCode:
		return m.syn.fn
	case iconImage:
		return m.syn.builtin
	case iconVideo:
		return m.syn.num
	case iconAudio:
		return m.syn.typ
	case iconArchive:
		return m.syn.tag
	case iconDoc:
		return m.syn.str
	case iconExec:
		return m.syn.kw
	}
	return m.syn.comment
}

// fileIcon is the icon and a space to put before a name, or "" when icons
// are off. Plain (the row's own colour) on the cursor row and dimmed rows,
// so it never fights the highlight.
func (m model) fileIcon(e entry, plain bool) string {
	if !m.cfg.Icons {
		return ""
	}
	d := iconFor(e)
	if plain {
		return d.glyph + " "
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.iconColor(d.kind))).Render(d.glyph) + " "
}
