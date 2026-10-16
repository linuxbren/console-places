// Package omarchy reads the palette of the currently active Omarchy desktop
// theme so console-places can follow it.
//
// Omarchy stages the active theme under ~/.local/state/omarchy/current/ and
// ships a resolver, `omarchy-theme-color`, that applies its alias cascade. We
// prefer that resolver and fall back to parsing colors.toml directly.
package omarchy

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Palette is the raw palette of the active Omarchy theme.
type Palette struct {
	Name       string
	Background string
	Foreground string
	Accent     string
	Selection  string
	Muted      string
	Dim        string // dimmed foreground
	Surface    string // a slightly lighter surface for bars and modals
	Error      string
	Ok         string
	Blue       string // extra hues, used for syntax highlighting
	Cyan       string
	Magenta    string
	Yellow     string
	Orange     string
}

func stateDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "omarchy")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "omarchy")
}

func themeName() string {
	b, err := os.ReadFile(filepath.Join(stateDir(), "current", "theme.name"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Signature changes whenever the active Omarchy theme changes; it is cheap
// enough to poll. Empty means Omarchy state was not found.
func Signature() string {
	fi, err := os.Stat(filepath.Join(stateDir(), "current", "theme.name"))
	if err != nil {
		return ""
	}
	return themeName() + "@" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

// Current returns the active palette. ok is false when Omarchy is absent or
// nothing parseable was found.
func Current() (Palette, bool) {
	m, ok := resolver()
	if !ok {
		b, err := os.ReadFile(filepath.Join(stateDir(), "current", "theme", "colors.toml"))
		if err != nil {
			return Palette{}, false
		}
		m = parseFlatTOML(string(b))
	}
	return fromMap(m)
}

func resolver() (map[string]string, bool) {
	bin, err := exec.LookPath("omarchy-theme-color")
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--all").Output()
	if err != nil {
		return nil, false
	}
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), "\t")
		if k, v = strings.TrimSpace(k), strings.TrimSpace(v); found && k != "" && v != "" {
			m[k] = v
		}
	}
	return m, len(m) > 0
}

// parseFlatTOML reads the flat `key = "value"` lines of colors.toml.
func parseFlatTOML(s string) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" && v != "" {
			m[k] = v
		}
	}
	return m
}

func fromMap(m map[string]string) (Palette, bool) {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(m[k]); v != "" {
				return v
			}
		}
		return ""
	}
	p := Palette{
		Name:       get("name", "theme_name"),
		Background: get("background", "bg", "color0"),
		Foreground: get("foreground", "fg", "color7"),
		Accent:     get("accent", "color4", "blue"),
		Selection:  get("selection", "selection_background"),
		Muted:      get("muted", "color8"),
		Dim:        get("dark_foreground", "dark_fg", "color8"),
		Surface:    get("lighter_background", "lighter_bg", "color8"),
		Error:      get("red", "color1"),
		Ok:         get("green", "color2"),
		Blue:       get("blue", "color4"),
		Cyan:       get("cyan", "color6"),
		Magenta:    get("magenta", "purple", "color5"),
		Yellow:     get("yellow", "color3"),
		Orange:     get("orange", "yellow", "color3"),
	}
	if p.Name == "" {
		p.Name = themeName()
	}
	if !isHex(p.Background) || !isHex(p.Foreground) {
		return Palette{}, false
	}
	return p, true
}

func isHex(s string) bool {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return false
	}
	_, err := strconv.ParseUint(s, 16, 32)
	return err == nil
}
