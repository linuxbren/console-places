package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// config is the small amount of state tidefiles remembers between runs.
type config struct {
	Theme      string   `json:"theme,omitempty"`
	ShowHidden bool     `json:"show_hidden"`
	Wrap       bool     `json:"wrap"`
	Sort       string   `json:"sort,omitempty"`
	SortDesc   bool     `json:"sort_desc,omitempty"`
	Bookmarks  []string `json:"bookmarks,omitempty"`
	Servers    []string `json:"servers,omitempty"` // network locations connected to, most recent first

	PlacesInList string `json:"places_in_list,omitempty"` // "" off, "bookmarks" or "all": rows below every folder's files

	PreviewRatio float64 `json:"preview_ratio,omitempty"` // share of the width given to the preview
	HidePreview  bool    `json:"hide_preview,omitempty"`

	MarkdownSource bool `json:"markdown_source,omitempty"` // preview markdown as source, not rendered

	Tabs        []string `json:"tabs,omitempty"`          // tab folders at the last quit
	TabsOnStart string   `json:"tabs_on_start,omitempty"` // "" restore, "nofolder" only without a folder argument, "never"

	ImageMode string   `json:"image_mode,omitempty"` // "", kitty, sixel or blocks ("" detects)
	Recent    []string `json:"recent,omitempty"`     // help-palette actions, most recent first

	VideoPlay string `json:"video_play,omitempty"` // "" play after a moment, "key" with alt+p only, "off" still frame

	Updates       string `json:"updates,omitempty"`        // "" install automatically, "notify", "off"
	UpdateChecked int64  `json:"update_checked,omitempty"` // unix time of the last check for a new release
}

func defaultConfig() config {
	return config{Theme: themeOmarchy, Wrap: true, Sort: "name", PreviewRatio: defaultPreviewRatio}
}

func configPath() string {
	if p := os.Getenv("TIDEFILES_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tidefiles", "config.json")
}

func loadConfig() config {
	cfg := defaultConfig()
	if p := configPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &cfg)
		}
	}
	if cfg.Theme == "" {
		cfg.Theme = themeOmarchy
	}
	if cfg.PreviewRatio < minPreviewRatio || cfg.PreviewRatio > maxPreviewRatio {
		cfg.PreviewRatio = defaultPreviewRatio
	}
	if cfg.Sort == "" {
		cfg.Sort = "name"
	}
	return cfg
}

func (c config) save() {
	p := configPath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	if b, err := json.MarshalIndent(c, "", "  "); err == nil {
		_ = os.WriteFile(p, append(b, '\n'), 0o644)
	}
}
