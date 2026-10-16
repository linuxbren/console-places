package main

// Bulk rename: r with several items selected. As in Nautilus, two ways:
// a template ("Holiday {n}", "{name}-{n:3}") or find and replace. The
// extension is kept, so only names change; tab moves between the template
// and the find/replace fields, and the field in use picks the way. Every
// old → new name is listed as it is typed, with what's wrong (an empty
// name, a /, two items getting one name, a name that's taken); enter
// renames only when nothing is. The renames are one undoable step, done in
// two passes through temporary names so swaps (a↔b) and case changes work,
// and never overwrite: RENAME_NOREPLACE, rolled back if anything fails.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

type bulkState struct {
	names []string        // the items, in listing order
	dirs  map[string]bool // which of them are folders (no extension)
	taken map[string]bool // every name in the folder
	tmpl  textInput
	find  textInput
	repl  textInput
	field int // 0 template, 1 find, 2 replace with
	top   int // first preview row shown
}

// bulkRow is one item's new name and what's wrong with it ("" if nothing).
type bulkRow struct {
	from, to, problem string
	base              string // to without the extension
}

func (m *model) openBulkRename(ts []entry) {
	b := &bulkState{dirs: map[string]bool{}, taken: map[string]bool{}, tmpl: newInput("{name}")}
	for _, e := range ts {
		b.names = append(b.names, e.name)
		b.dirs[e.name] = e.isDir
	}
	des, _ := os.ReadDir(m.cwd) // hidden files too: a name can be taken by one
	for _, de := range des {
		b.taken[de.Name()] = true
	}
	m.modal = &modal{kind: mBulk, title: "rename " + plural(len(ts), "item"), bulk: b}
}

// nameParts splits a name into base and extension (folders have none).
func nameParts(name string, dir bool) (string, string) {
	if dir {
		return name, ""
	}
	return splitExt(name)
}

var counterToken = regexp.MustCompile(`\{n(?::(\d+))?\}`)

// applyTemplate makes item i's new base name: {name} is the old one, {n}
// the number from 1, {n:3} zero-padded to 3 digits.
func applyTemplate(tmpl, base string, i int) string {
	out := counterToken.ReplaceAllStringFunc(tmpl, func(tok string) string {
		w, _ := strconv.Atoi(counterToken.FindStringSubmatch(tok)[1])
		return fmt.Sprintf("%0*d", w, i+1)
	})
	return strings.ReplaceAll(out, "{name}", base)
}

// rows works out every item's new name and checks them all.
func (b *bulkState) rows() []bulkRow {
	rows := make([]bulkRow, len(b.names))
	count := map[string]int{}
	for i, n := range b.names {
		base, ext := nameParts(n, b.dirs[n])
		if b.field == 0 {
			base = applyTemplate(b.tmpl.value(), base, i)
		} else if f := b.find.value(); f != "" {
			base = strings.ReplaceAll(base, f, b.repl.value())
		}
		rows[i] = bulkRow{from: n, to: base + ext, base: base}
		count[rows[i].to]++
	}
	renamed := map[string]bool{}
	for _, r := range rows {
		if r.to != r.from {
			renamed[r.from] = true
		}
	}
	for i, r := range rows {
		switch {
		case r.to == r.from:
		case strings.TrimSpace(r.base) == "" || r.to == "." || r.to == "..":
			rows[i].problem = "no name"
		case strings.ContainsRune(r.to, '/'):
			rows[i].problem = "contains /"
		case count[r.to] > 1:
			rows[i].problem = "same name as another"
		case b.taken[r.to] && !renamed[r.to]:
			rows[i].problem = "already exists"
		}
	}
	return rows
}

func (m model) handleBulkKey(md *modal, msg tea.KeyMsg) (model, tea.Cmd) {
	b := md.bulk
	_, rows := m.geometry()
	switch msg.String() {
	case "esc":
		m.modal = nil
	case "tab", "down":
		b.field = (b.field + 1) % 3
	case "shift+tab", "up":
		b.field = (b.field + 2) % 3
	case "pgdown", "ctrl+f":
		b.top = min(b.top+rows/2, max(0, len(b.names)-1))
	case "pgup", "ctrl+b":
		b.top = max(0, b.top-rows/2)
	case "enter":
		var pairs [][2]string
		for _, r := range b.rows() {
			if r.problem != "" {
				m.setMsg(fmt.Sprintf("can't rename %s to %s: %s", r.from, r.to, r.problem), true)
				return m, nil
			}
			if r.to != r.from {
				pairs = append(pairs, [2]string{r.from, r.to})
			}
		}
		m.modal = nil
		if len(pairs) == 0 {
			m.setMsg("nothing to rename: every name stays the same", false)
			return m, nil
		}
		dir := m.cwd
		return m, m.run(func() opResult { return bulkRenameOp(dir, pairs) })
	default:
		[]*textInput{&b.tmpl, &b.find, &b.repl}[b.field].handle(msg)
	}
	return m, nil
}

func (m model) bulkBody(md *modal, inner int) []string {
	r := m.renderer
	b := md.bulk
	field := func(i int, label string, in textInput) string {
		text := in.value()
		if i == b.field {
			text = in.view()
		}
		return r.RenderSoftRow(tideui.SoftRow{Text: fmt.Sprintf("%-14s %s", label, text), Selected: i == b.field, Muted: i != b.field}, inner)
	}
	help := func(s string) string { return r.Styles.DetailMeta.Render(ansi.Truncate("  "+s, inner, "…")) }
	body := []string{
		field(0, "New names:", b.tmpl),
		help("Type over {name} with any name; the extension stays."),
		help("{name} = the old name · {n} = 1, 2, 3… · {n:2} = 01, 02…"),
		help("e.g. Beach {n:2} → Beach 01.jpg, Beach 02.jpg, …"),
		"",
		help("Or change part of every name (tab to get here):"),
		field(1, "Find:", b.find),
		field(2, "Replace with:", b.repl),
		"",
	}
	rows := b.rows()
	bad := 0
	for _, row := range rows {
		if row.problem != "" {
			bad++
		}
	}
	show := max(3, m.height-25)
	b.top = max(0, min(b.top, len(rows)-show))
	for _, row := range rows[b.top:min(len(rows), b.top+show)] {
		line := row.from + "  →  " + row.to
		switch {
		case row.problem != "":
			line = r.Styles.StatusError.Render("✗ " + line + "  (" + row.problem + ")")
		case row.to == row.from:
			line = r.Styles.DetailMeta.Render("  " + row.from + "  (unchanged)")
		default:
			line = "  " + line
		}
		body = append(body, ansi.Truncate(line, inner, "…"))
	}
	if more := len(rows) - b.top - show; more > 0 {
		body = append(body, r.Styles.DetailMeta.Render(fmt.Sprintf("  … %d more (pgdn)", more)))
	}
	h := []tideui.SoftHint{{Key: "enter", Label: "rename"}, {Key: "tab", Label: "next box"}, {Key: "esc", Label: "cancel"}}
	if bad > 0 {
		h[0].Label = fmt.Sprintf("fix %d first", bad)
	}
	return append(body, "", r.RenderSoftHints(inner, h...))
}

// renameNoReplace renames from to to, failing if to exists (atomically
// where the filesystem can; checked first where it can't).
func renameNoReplace(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		if _, serr := os.Lstat(to); serr == nil {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrExist}
		}
		err = os.Rename(from, to)
	}
	if err != nil {
		var le *os.LinkError
		if !errors.As(err, &le) {
			err = &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
		}
	}
	return err
}

// renameAll renames each pair's name to the next (both in dir): first all
// to temporary names, then to the new ones. On a failure it puts back what
// it moved and returns the error.
func renameAll(dir string, pairs [][2]string) error {
	tmp := make([]string, len(pairs))
	var done []int // pairs that are at their temporary name
	undoTmp := func() {
		for _, i := range done {
			_ = renameNoReplace(tmp[i], filepath.Join(dir, pairs[i][0]))
		}
	}
	for i, p := range pairs {
		tmp[i] = filepath.Join(dir, fmt.Sprintf(".console-places-rename-%d-%d", os.Getpid(), i))
		if err := renameNoReplace(filepath.Join(dir, p[0]), tmp[i]); err != nil {
			undoTmp()
			return err
		}
		done = append(done, i)
	}
	for k, i := range done {
		if err := renameNoReplace(tmp[i], filepath.Join(dir, pairs[i][1])); err != nil {
			for _, j := range done[:k] { // back to the temporary names, then home
				_ = renameNoReplace(filepath.Join(dir, pairs[j][1]), tmp[j])
			}
			undoTmp()
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s already exists (nothing was renamed)", pairs[i][1])
			}
			return fmt.Errorf("%w (nothing was renamed)", err)
		}
	}
	return nil
}

func bulkRenameOp(dir string, pairs [][2]string) opResult {
	if err := renameAll(dir, pairs); err != nil {
		return opResult{err: err}
	}
	back := make([][2]string, len(pairs))
	for i, p := range pairs {
		back[i] = [2]string{p[1], p[0]}
	}
	return opResult{desc: "Renamed " + plural(len(pairs), "item"), focus: pairs[0][1],
		undo: func() error { return renameAll(dir, back) }}
}
