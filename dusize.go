package main

// Folder sizes in properties (i), counted in the background so a huge
// folder never freezes the screen: the dialog opens at once and its numbers
// grow as the count goes (with "counting…"); closing it stops the count.
// Two sizes, as du and Nautilus give them: the bytes in the files, and the
// space they take on disk (allocated blocks: sparse files take less,
// small files more). A file with several hard links is counted once.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const sizeTick = 150 * time.Millisecond

// sizeCount is a running (or finished) count of some paths.
type sizeCount struct {
	cancel     context.CancelFunc
	size, disk atomic.Int64
	files      atomic.Int64
	dirs       atomic.Int64 // folders inside (not the ones counted)
	unreadable atomic.Int64 // folders that couldn't be listed
	done       atomic.Bool
}

type sizeTickMsg struct{ c *sizeCount }

// countSize starts counting paths (symlinks are counted as links, except a
// path itself that links to a folder, which is followed).
func countSize(paths []string) *sizeCount {
	ctx, cancel := context.WithCancel(context.Background())
	c := &sizeCount{cancel: cancel}
	go func() {
		defer c.done.Store(true)
		seen := map[[2]uint64]bool{} // device, inode of files with several links
		for _, root := range paths {
			if fi, err := os.Stat(root); err == nil && fi.IsDir() {
				if r, err := filepath.EvalSymlinks(root); err == nil {
					root = r
				}
			}
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if ctx.Err() != nil {
					return filepath.SkipAll
				}
				if err != nil {
					if d != nil && d.IsDir() {
						c.unreadable.Add(1)
					}
					return nil
				}
				fi, ierr := d.Info()
				if ierr != nil {
					return nil
				}
				if d.IsDir() {
					if p != root {
						c.dirs.Add(1)
					}
				} else {
					c.files.Add(1)
				}
				st, ok := fi.Sys().(*syscall.Stat_t)
				if ok && !d.IsDir() && st.Nlink > 1 {
					id := [2]uint64{uint64(st.Dev), st.Ino}
					if seen[id] {
						return nil
					}
					seen[id] = true
				}
				if !d.IsDir() {
					c.size.Add(fi.Size())
				}
				if ok {
					c.disk.Add(st.Blocks * 512)
				}
				return nil
			})
		}
	}()
	return c
}

func (c *sizeCount) tick() tea.Cmd {
	return tea.Tick(sizeTick, func(time.Time) tea.Msg { return sizeTickMsg{c} })
}

// sizeLines describe the count so far.
func (c *sizeCount) sizeLines(label string) []string {
	more := ""
	if !c.done.Load() {
		more = " · counting…"
	}
	size := c.size.Load()
	lines := []string{
		fmt.Sprintf("Contains     %s, %s%s", count(c.files.Load(), "file"), count(c.dirs.Load(), "folder"), more),
		fmt.Sprintf("%-12s %s (%s bytes)", label, humanSize(size), groupDigits(size)),
		"On disk      " + humanSize(c.disk.Load()),
	}
	if n := c.unreadable.Load(); n > 0 {
		lines = append(lines, fmt.Sprintf("             (%s couldn't be read)", plural(int(n), "folder")))
	}
	return lines
}

// count is n things, with thousands separators: "12,345 files".
func count(n int64, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return groupDigits(n) + " " + thing + "s"
}

// groupDigits writes n with thousands separators: 1,234,567.
func groupDigits(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// onSizeTick refreshes the properties dialog while its count runs, and
// stops the count once the dialog is gone.
func (m *model) onSizeTick(msg sizeTickMsg) tea.Cmd {
	md := m.modal
	if md == nil || md.count != msg.c {
		msg.c.cancel()
		return nil
	}
	md.lines = m.propLines(md.props, md.count)
	if msg.c.done.Load() {
		return nil
	}
	return msg.c.tick()
}
