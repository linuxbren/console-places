package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitCount(t *testing.T, c *sizeCount) {
	t.Helper()
	for i := 0; i < 250 && !c.done.Load(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !c.done.Load() {
		t.Fatal("count never finished")
	}
}

func TestCountSize(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "a", "one.txt"), strings.Repeat("x", 1000))
	mkfile(t, filepath.Join(dir, "a", "b", "two.txt"), strings.Repeat("y", 3000))
	os.Link(filepath.Join(dir, "a", "one.txt"), filepath.Join(dir, "a", "b", "one-again.txt"))
	sparse, _ := os.Create(filepath.Join(dir, "a", "sparse.img"))
	sparse.Truncate(1 << 30) // 1 GiB, no blocks
	sparse.Close()
	locked := filepath.Join(dir, "a", "locked")
	os.Mkdir(locked, 0o755)
	mkfile(t, filepath.Join(locked, "secret"), "s")
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	os.Symlink(filepath.Join(dir, "a"), filepath.Join(dir, "link"))

	c := countSize([]string{filepath.Join(dir, "link")}) // a link to a folder is followed
	waitCount(t, c)
	if got := c.size.Load(); got != 1000+3000+1<<30 {
		t.Fatalf("size %d (the hard link counts once)", got)
	}
	if d := c.disk.Load(); d <= 0 || d > 1<<20 {
		t.Fatalf("on disk %d: the sparse file takes almost nothing", d)
	}
	if c.files.Load() != 4 || c.dirs.Load() != 2 {
		t.Fatalf("files %d dirs %d", c.files.Load(), c.dirs.Load())
	}
	if os.Getuid() != 0 && c.unreadable.Load() != 1 {
		t.Fatalf("unreadable %d", c.unreadable.Load())
	}
	if got := strings.Join(c.sizeLines("Size"), "\n"); !strings.Contains(got, "4 files, 2 folders") ||
		!strings.Contains(got, "(1,073,745,824 bytes)") || strings.Contains(got, "counting") {
		t.Fatalf("lines:\n%s", got)
	}
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := groupDigits(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

func TestPropertiesCountInTheBackground(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "big", "f.txt"), strings.Repeat("z", 2048))
	m := testModel(t, dir)
	m, cmd := m.perform(actProps)
	if cmd == nil || m.modal == nil || m.modal.count == nil {
		t.Fatal("a folder's properties count in the background")
	}
	waitCount(t, m.modal.count)
	if cmd := m.onSizeTick(sizeTickMsg{m.modal.count}); cmd != nil {
		t.Fatal("a finished count stops ticking")
	}
	if got := strings.Join(m.modal.lines, "\n"); !strings.Contains(got, "Size         2.0 KiB (2,048 bytes)") || !strings.Contains(got, "On disk") {
		t.Fatalf("lines:\n%s", got)
	}

	// Closing the dialog stops a count at the next tick.
	m, _ = m.perform(actProps)
	c := m.modal.count
	m.modal = nil
	m.onSizeTick(sizeTickMsg{c})
	waitCount(t, c)
}
