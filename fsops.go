package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type entry struct {
	name  string
	isDir bool
	size  int64
	mod   time.Time
	mode  fs.FileMode
	kind  entryKind // a separator or a shortcut (Trash, a place) rather than a file
	pl    *place    // a place row's place
}

func (e entry) hidden() bool { return strings.HasPrefix(e.name, ".") }

// readDir lists dir with directories first, then names case-insensitively.
func readDir(dir string, showHidden bool) ([]entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		isDir := de.IsDir()
		if de.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir, info = st.IsDir(), st
			}
		}
		out = append(out, entry{name: name, isDir: isDir, size: info.Size(), mod: info.ModTime(), mode: info.Mode()})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir
		}
		return strings.ToLower(out[i].name) < strings.ToLower(out[j].name)
	})
	return out, nil
}

// sortEntries orders ents in place: directories first, then by key ("name",
// "size", "modified" or "type"), optionally reversed within each group.
func sortEntries(ents []entry, key string, desc bool) {
	ext := func(e entry) string { return strings.ToLower(filepath.Ext(e.name)) }
	less := func(a, b entry) bool {
		switch key {
		case "size":
			if a.size != b.size {
				return a.size < b.size
			}
		case "modified":
			if !a.mod.Equal(b.mod) {
				return a.mod.After(b.mod) // newest first when ascending
			}
		case "type":
			if ea, eb := ext(a), ext(b); ea != eb {
				return ea < eb
			}
		}
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	}
	sort.SliceStable(ents, func(i, j int) bool {
		a, b := ents[i], ents[j]
		if a.isDir != b.isDir {
			return a.isDir
		}
		if desc {
			return less(b, a)
		}
		return less(a, b)
	})
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// tildePath shortens the home directory prefix to ~.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(filepath.Separator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}
