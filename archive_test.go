package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func needBsdtar(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bsdtar"); err != nil {
		t.Skip("bsdtar not installed")
	}
}

// writeZip writes a zip with the given members (names ending in "/" are
// folders).
func writeTestZip(t *testing.T, path string, members map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	var names []string
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, members[n])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func writeTestTarGz(t *testing.T, path string, members map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	var names []string
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		hdr := &tar.Header{Name: n, Mode: 0o644, Size: int64(len(members[n])), Typeflag: tar.TypeReg}
		if strings.HasSuffix(n, "/") {
			hdr = &tar.Header{Name: n, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		tw.WriteHeader(hdr)
		io.WriteString(tw, members[n])
	}
	tw.Close()
	gz.Close()
	f.Close()
}

var sampleMembers = map[string]string{
	"docs/":             "",
	"docs/a b.txt":      "spaces work\n",
	"docs/odd[1]*?.txt": "glob characters work\n",
	"src/deep/main.go":  "package main\n", // src/ and src/deep/ are only implied
	"README.md":         "# hello\n",
	".hidden":           "secret\n",
}

func TestListArchiveFormats(t *testing.T) {
	needBsdtar(t)
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "s.zip")
	tgzPath := filepath.Join(dir, "s.tar.gz")
	writeTestZip(t, zipPath, sampleMembers)
	writeTestTarGz(t, tgzPath, sampleMembers)
	// tar.zst via bsdtar itself, from an extracted copy.
	src := filepath.Join(dir, "tree")
	for n, c := range sampleMembers {
		if strings.HasSuffix(n, "/") {
			os.MkdirAll(filepath.Join(src, n), 0o755)
		} else {
			mkfile(t, filepath.Join(src, n), c)
		}
	}
	zstPath := filepath.Join(dir, "s.tar.zst")
	if out, err := exec.Command("bsdtar", "-a", "-cf", zstPath, "-C", src, ".").CombinedOutput(); err != nil {
		t.Fatalf("make tar.zst: %v %s", err, out)
	}
	for _, a := range []string{zipPath, tgzPath, zstPath} {
		t.Run(filepath.Base(a), func(t *testing.T) {
			ms, engine, err := listArchive(context.Background(), a)
			if err != nil || engine != "bsdtar" {
				t.Fatalf("list: %v %s", err, engine)
			}
			v := newArcView(a, ms)
			names := func(dir string) string {
				var out []string
				for _, e := range v.list(dir, true) {
					n := e.name
					if e.isDir {
						n += "/"
					}
					out = append(out, n)
				}
				sort.Strings(out)
				return strings.Join(out, " ")
			}
			if got := names(""); got != ".hidden README.md docs/ src/" {
				t.Errorf("top: %q", got)
			}
			if got := names("docs"); got != "a b.txt odd[1]*?.txt" {
				t.Errorf("docs: %q", got)
			}
			if got := names("src/deep"); got != "main.go" {
				t.Errorf("implied folders: %q", got)
			}
			if n := len(v.list("", false)); n != 3 {
				t.Errorf("hidden members should hide: %d", n)
			}
			if m := v.members["docs/a b.txt"]; m.size != int64(len("spaces work\n")) {
				t.Errorf("size: %+v", m)
			}
			// Previews extract the exact member, even with glob characters.
			for _, p := range []string{"docs/odd[1]*?.txt", "docs/a b.txt"} {
				tmp, err := v.extractForPreview(v.members[p])
				if err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(filepath.Join(tmp, filepath.Base(p)))
				if string(got) != sampleMembers[p] {
					t.Errorf("%s extracted %q", p, got)
				}
			}
			v.close()
			if v.tmp != "" {
				t.Error("close should remove the preview files")
			}
		})
	}
}

func TestParseListings(t *testing.T) {
	bsd := "drwxr-xr-x  0 user   group       0 Sep 28 21:41 folder/\n" +
		"-rw-r--r--  0 user   group    1234 Jan  2  2024 folder/a  b.txt\n" +
		"lrwxrwxrwx  0 user   group       0 Sep 28 21:41 link -> folder/a  b.txt\n" +
		"bsdtar: some warning\n"
	ms := parseBsdtarList(bsd)
	if len(ms) != 3 || ms[1].path != "folder/a  b.txt" || ms[1].size != 1234 || !ms[0].isDir || ms[2].path != "link" {
		t.Fatalf("bsdtar: %+v", ms)
	}
	if ms[1].mod.Year() != 2024 || ms[1].mode.Perm() != 0o644 {
		t.Errorf("time/mode: %v %v", ms[1].mod, ms[1].mode)
	}
	sz := "Path = dir\nFolder = +\nSize = 0\nModified = 2024-05-01 10:00:00\n\nPath = dir/x.txt\nFolder = -\nSize = 42\nModified = 2024-05-01 10:00:00.1234567\n"
	ms = parse7zList(sz)
	if len(ms) != 2 || !ms[0].isDir || ms[1].path != "dir/x.txt" || ms[1].size != 42 || ms[1].mod.Year() != 2024 {
		t.Fatalf("7z: %+v", ms)
	}
}

func TestArchiveStem(t *testing.T) {
	for in, want := range map[string]string{"x.tar.gz": "x", "Photos.TGZ": "Photos", "a.b.zip": "a.b", "notes.txt": ""} {
		got, ok := archiveStem(in)
		if want == "" {
			if ok {
				t.Errorf("%s is not an archive", in)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s → %q %v", in, got, ok)
		}
	}
}

func dirNames(t *testing.T, dir string) string {
	t.Helper()
	des, _ := os.ReadDir(dir)
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestExtractJob(t *testing.T) {
	needBsdtar(t)
	dir := t.TempDir()
	multi := filepath.Join(dir, "multi.zip")
	single := filepath.Join(dir, "single.tar.gz")
	writeTestZip(t, multi, map[string]string{"a.txt": "a", "b/c.txt": "c"})
	writeTestTarGz(t, single, map[string]string{"proj/": "", "proj/x.txt": "x"})

	p := &progress{}
	res := extractJob(context.Background(), p, dir, []string{multi, single})
	if res.err != nil {
		t.Fatal(res.err)
	}
	// Several top-level entries → a folder named after the archive; a single
	// top-level folder → that folder.
	if got := dirNames(t, dir); got != "multi multi.zip proj single.tar.gz" {
		t.Fatalf("extracted: %q (%s)", got, res.desc)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "multi/b/c.txt")); string(got) != "c" {
		t.Errorf("content: %q", got)
	}
	if p.files.Load() != 3 || p.totalFiles.Load() != 3 || p.bytes.Load() != 3 {
		t.Errorf("progress: %d/%d files, %d bytes", p.files.Load(), p.totalFiles.Load(), p.bytes.Load())
	}

	// Again: the names are taken, so it uses "(2)" and says so; nothing is overwritten.
	mkfile(t, filepath.Join(dir, "multi/a.txt.keep"), "")
	res = extractJob(context.Background(), &progress{}, dir, []string{multi, single})
	if res.err != nil || !strings.Contains(res.desc, "multi (2)") || !strings.Contains(res.desc, "proj (2)") {
		t.Fatalf("second extract: %v %q", res.err, res.desc)
	}
	if !exists(filepath.Join(dir, "multi/a.txt.keep")) {
		t.Error("existing folder was touched")
	}

	// Cancelled: nothing is left behind, not even the hidden temp folder.
	before := dirNames(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res = extractJob(ctx, &progress{}, dir, []string{multi})
	if !strings.HasPrefix(res.desc, "Stopped") || dirNames(t, dir) != before {
		t.Fatalf("cancel: %q, dir now %q", res.desc, dirNames(t, dir))
	}
}

func TestExtractRefusesPathTraversal(t *testing.T) {
	needBsdtar(t)
	root := t.TempDir()
	dir := filepath.Join(root, "dl")
	os.Mkdir(dir, 0o755)
	evil := filepath.Join(dir, "evil.tar.gz")
	writeTestTarGz(t, evil, map[string]string{"../escaped.txt": "x", "ok.txt": "fine"})
	extractJob(context.Background(), &progress{}, dir, []string{evil})
	if exists(filepath.Join(root, "escaped.txt")) {
		t.Fatal("a ../ member was written outside the destination")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".console-places-extract-*")); len(leftovers) > 0 {
		t.Fatalf("temp folder left behind: %v", leftovers)
	}
}

func TestBrowseArchiveInModel(t *testing.T) {
	needBsdtar(t)
	t.Setenv("CONSOLE_PLACES_IMAGES", "blocks")
	dir := t.TempDir()
	writeTestZip(t, filepath.Join(dir, "pack.zip"), map[string]string{"inner/note.txt": "hello", "top.txt": "t"})
	cfg := defaultConfig()
	cfg.Theme = "tokyo-night"
	m := newModel(dir, cfg, "", newGfxOut(os.Stdout))
	m.width, m.height = 120, 40
	m.reload("pack.zip")

	key := func(k tea.KeyMsg) {
		t.Helper()
		var cmd tea.Cmd
		m, cmd = m.handleKey(k)
		if cmd != nil {
			if msg, ok := cmd().(arcOpenedMsg); ok {
				next, _ := m.Update(msg)
				m = next.(model)
			}
		}
	}
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	left := tea.KeyMsg{Type: tea.KeyLeft}
	key(enter)
	if m.arc == nil || dirNamesOf(m.entries) != "inner top.txt" {
		t.Fatalf("not inside the archive: %+v", m.entries)
	}
	key(enter) // into inner/
	if m.arc.dir != "inner" || dirNamesOf(m.entries) != "note.txt" {
		t.Fatalf("inner: %q %v", m.arc.dir, m.entries)
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}) // trash: refused, read-only
	if !strings.Contains(m.msg, "read-only") || m.arc == nil {
		t.Fatalf("trash inside an archive: %q", m.msg)
	}
	key(left)
	key(left) // out of the archive, back on it
	if m.arc != nil || m.curName() != "pack.zip" {
		t.Fatalf("left: arc=%v cur=%q", m.arc != nil, m.curName())
	}
	if !exists(filepath.Join(dir, "pack.zip")) {
		t.Fatal("archive changed")
	}
}

func dirNamesOf(es []entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.name)
	}
	return strings.Join(out, " ")
}
