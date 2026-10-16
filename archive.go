package main

// Archives: browse one like a read-only folder, extract archives into the
// current folder, and compress a selection. Reading goes through bsdtar
// (libarchive: zip, tar with any compression, 7z, most rar, iso, deb, rpm…)
// with 7z as a fallback for what bsdtar can't open.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	archiveListTimeout = 60 * time.Second
	arcPreviewMaxBytes = 64 << 20 // members bigger than this aren't extracted to preview
)

// archiveSuffixes are the names console-places treats as archives, longest first
// so stripping them gives the right stem ("x.tar.gz" → "x").
var archiveSuffixes = []string{
	".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst", ".tar.lz4", ".tar.lz", ".tar.Z",
	".tgz", ".tbz", ".tbz2", ".txz", ".tzst",
	".tar", ".zip", ".7z", ".rar", ".jar", ".war", ".apk", ".cbz", ".cbr", ".whl", ".epub",
	".deb", ".rpm", ".iso", ".cpio", ".xpi",
}

func isArchiveName(name string) bool { _, ok := archiveStem(name); return ok }

// archiveStem strips a known archive suffix: "photos.tar.gz" → "photos".
func archiveStem(name string) (string, bool) {
	lower := strings.ToLower(name)
	for _, s := range archiveSuffixes {
		if strings.HasSuffix(lower, strings.ToLower(s)) && len(name) > len(s) {
			return name[:len(name)-len(s)], true
		}
	}
	return name, false
}

// arcMember is one entry of an archive listing.
type arcMember struct {
	raw   string // name as stored, for extraction
	path  string // cleaned: no "./" prefix or trailing "/"
	isDir bool
	size  int64
	mod   time.Time
	mode  os.FileMode
}

// listArchive lists an archive's members with bsdtar, or 7z if bsdtar can't
// read it, and says which tool worked ("bsdtar" or "7z") so extraction uses
// the same one.
func listArchive(ctx context.Context, file string) ([]arcMember, string, error) {
	ctx, cancel := context.WithTimeout(ctx, archiveListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bsdtar", "-tvf", file)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return parseBsdtarList(string(out)), "bsdtar", nil
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if _, lerr := exec.LookPath("7z"); lerr == nil {
		if out, zerr := exec.CommandContext(ctx, "7z", "l", "-slt", "-ba", file).Output(); zerr == nil {
			return parse7zList(string(out)), "7z", nil
		}
	}
	msg := strings.TrimSpace(strings.TrimPrefix(firstLine(stderr.String()), "bsdtar: "))
	if msg == "" {
		msg = err.Error()
	}
	return nil, "", fmt.Errorf("can't read %s: %s", filepath.Base(file), msg)
}

func firstLine(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }

// bsdtar -tv lines look like ls -l:
//
//	drwxr-xr-x  0 user  group      0 Sep 28 21:41 folder/
//	-rw-r--r--  0 user  group   1234 Sep 28  2024 folder/a b.txt
//	lrwxrwxrwx  0 user  group      0 Sep 28 21:41 link -> target
var bsdtarLine = regexp.MustCompile(`^([-a-zA-Z?][-rwxsStTl?.+]{9})\S*\s+\d+\s+\S+\s+\S+\s+(\d+)\s+([A-Z][a-z]{2}\s+\d+\s+(?:\d{1,2}:\d{2}|\d{4}))\s(.*)$`)

func parseBsdtarList(out string) []arcMember {
	var ms []arcMember
	for _, line := range strings.Split(out, "\n") {
		g := bsdtarLine.FindStringSubmatch(line)
		if g == nil {
			continue
		}
		name := g[4]
		switch g[1][0] {
		case 'l':
			if i := strings.Index(name, " -> "); i >= 0 {
				name = name[:i]
			}
		case 'h':
			if i := strings.Index(name, " link to "); i >= 0 {
				name = name[:i]
			}
		}
		size, _ := strconv.ParseInt(g[2], 10, 64)
		m := arcMember{raw: name, isDir: g[1][0] == 'd' || strings.HasSuffix(name, "/"), size: size, mode: parseModeString(g[1])}
		m.mod = parseListTime(g[3])
		ms = append(ms, m.clean())
	}
	return ms
}

func parseListTime(s string) time.Time {
	s = strings.Join(strings.Fields(s), " ")
	if t, err := time.Parse("Jan 2 2006", s); err == nil {
		return t
	}
	if t, err := time.Parse("Jan 2 15:04", s); err == nil {
		now := time.Now()
		t = t.AddDate(now.Year(), 0, 0)
		if t.After(now.Add(24 * time.Hour)) {
			t = t.AddDate(-1, 0, 0) // ls shows times only for the last six months
		}
		return t
	}
	return time.Time{}
}

func parseModeString(s string) os.FileMode {
	var m os.FileMode
	for i, c := range s[1:10] {
		if c != '-' {
			m |= 1 << (8 - i)
		}
	}
	switch s[0] {
	case 'd':
		m |= os.ModeDir
	case 'l':
		m |= os.ModeSymlink
	}
	return m
}

// parse7zList reads `7z l -slt -ba`: blocks of "Key = value" lines.
func parse7zList(out string) []arcMember {
	var ms []arcMember
	var cur map[string]string
	flush := func() {
		if cur == nil || cur["Path"] == "" {
			return
		}
		size, _ := strconv.ParseInt(cur["Size"], 10, 64)
		m := arcMember{raw: cur["Path"], isDir: cur["Folder"] == "+" || strings.HasPrefix(cur["Attributes"], "D"), size: size}
		m.mod, _ = time.Parse("2006-01-02 15:04:05", strings.SplitN(cur["Modified"], ".", 2)[0])
		m.mode = 0o644
		if m.isDir {
			m.mode = os.ModeDir | 0o755
		}
		ms = append(ms, m.clean())
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			cur = nil
			continue
		}
		k, v, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		if cur == nil {
			cur = map[string]string{}
		}
		cur[k] = v
	}
	flush()
	return ms
}

func (m arcMember) clean() arcMember {
	p := strings.TrimPrefix(filepath.ToSlash(m.raw), "./")
	p = strings.Trim(path.Clean("/"+p), "/")
	m.path = p
	return m
}

// ---- browsing ---------------------------------------------------------------

// arcView is an archive opened as a read-only folder tree.
type arcView struct {
	file    string // absolute path of the archive
	engine  string // "bsdtar" or "7z"
	dir     string // current folder inside it ("" is the top)
	members map[string]arcMember
	kids    map[string][]entry // folder → its entries

	mu      sync.Mutex
	tmp     string            // where members are extracted for previews
	cached  map[string]string // member path → extracted file
	cursors map[string]string // folder → last selected name
}

type arcOpenedMsg struct {
	file string
	view *arcView
	err  error
}

func openArchiveCmd(file string) tea.Cmd {
	return func() tea.Msg {
		ms, engine, err := listArchive(context.Background(), file)
		if err != nil {
			return arcOpenedMsg{file: file, err: err}
		}
		v := newArcView(file, ms)
		v.engine = engine
		return arcOpenedMsg{file: file, view: v}
	}
}

func newArcView(file string, ms []arcMember) *arcView {
	v := &arcView{file: file, engine: "bsdtar", members: map[string]arcMember{}, kids: map[string][]entry{},
		cached: map[string]string{}, cursors: map[string]string{}}
	for _, m := range ms {
		if m.path != "" {
			v.members[m.path] = m // a later duplicate (appended tar members) wins, as on extraction
		}
	}
	// Archives needn't list every folder: add the missing parents.
	for p := range v.members {
		for d := parentOf(p); d != ""; d = parentOf(d) {
			if _, ok := v.members[d]; ok {
				break
			}
			v.members[d] = arcMember{path: d, isDir: true, mode: os.ModeDir | 0o755}
		}
	}
	for p, m := range v.members {
		v.kids[parentOf(p)] = append(v.kids[parentOf(p)], memberEntry(m))
	}
	return v
}

func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

func memberEntry(m arcMember) entry {
	return entry{name: path.Base(m.path), isDir: m.isDir, size: m.size, mod: m.mod, mode: m.mode}
}

// list returns the entries of folder dir inside the archive.
func (v *arcView) list(dir string, hidden bool) []entry {
	var out []entry
	for _, e := range v.kids[dir] {
		if hidden || !e.hidden() {
			out = append(out, e)
		}
	}
	return out
}

// label is how the location reads in the pane title: "foo.zip ▸ docs/api".
func (v *arcView) label() string {
	if v.dir == "" {
		return filepath.Base(v.file)
	}
	return filepath.Base(v.file) + " ▸ " + v.dir
}

func (v *arcView) memberPath(name string) string {
	if v.dir == "" {
		return name
	}
	return v.dir + "/" + name
}

// close removes the files extracted for previews.
func (v *arcView) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.tmp != "" {
		_ = os.RemoveAll(v.tmp)
		v.tmp = ""
	}
}

// extractForPreview writes one member to a private temp folder and returns
// that folder, so the normal preview code can show it.
func (v *arcView) extractForPreview(m arcMember) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p, ok := v.cached[m.path]; ok {
		return p, nil
	}
	if v.tmp == "" {
		t, err := os.MkdirTemp("", "console-places-archive-")
		if err != nil {
			return "", err
		}
		v.tmp = t
	}
	dir, err := os.MkdirTemp(v.tmp, "m")
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Join(dir, path.Base(m.path)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bsdtar", "-xOf", v.file, bsdtarPattern(m.raw))
	if v.engine == "7z" {
		cmd = exec.CommandContext(ctx, "7z", "e", "-so", v.file, m.raw)
	}
	cmd.Stdout = &limitWriter{w: f, n: arcPreviewMaxBytes}
	if err := cmd.Run(); err != nil && ctx.Err() != nil {
		return "", err
	}
	v.cached[m.path] = dir
	return dir, nil
}

// bsdtarPattern escapes a member name so bsdtar matches it literally.
func bsdtarPattern(name string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(name)
}

type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil // discard the rest, quietly
	}
	q := p
	if int64(len(q)) > l.n {
		q = q[:l.n]
	}
	n, err := l.w.Write(q)
	l.n -= int64(n)
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// arcPreview renders the preview for entry e of the archive's current folder.
func arcPreview(v *arcView, dir string, e entry, o previewOpts) preview {
	full := e.name
	if dir != "" {
		full = dir + "/" + e.name
	}
	p := preview{title: e.name}
	if e.isDir {
		kids := v.list(full, o.hidden)
		p.meta = plural(len(kids), "item")
		if len(kids) == 0 {
			p.lines = []string{"  (empty)"}
		}
		for _, k := range kids {
			name := k.name
			if k.isDir {
				name += "/"
			}
			p.lines = append(p.lines, " "+sanitize(name))
		}
		return p
	}
	m, ok := v.members[full]
	if !ok {
		return p
	}
	if m.size > arcPreviewMaxBytes {
		p.meta = humanSize(m.size)
		p.lines = []string{"  " + humanSize(m.size) + ": too big to preview inside the archive", "  X extracts the archive"}
		return p
	}
	tmp, err := v.extractForPreview(m)
	if err != nil {
		p.lines = []string{"  can't read from the archive: " + errText(err)}
		return p
	}
	return buildPreview(tmp, e, o)
}

// ---- extracting -------------------------------------------------------------

// freeName returns name, or "name (2)", "name (3)"… if taken in dir.
func freeName(dir, name string) string {
	if !exists(filepath.Join(dir, name)) {
		return name
	}
	stem, ext := name, ""
	if s, e := splitExt(name); e != "" && !strings.HasPrefix(name, ".") {
		stem, ext = s, e
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s (%d)%s", stem, i, ext); !exists(filepath.Join(dir, n)) {
			return n
		}
	}
}

// extractJob extracts each archive into dir: into a folder named after the
// archive, or as its single top-level folder if it has exactly one. Names
// that are taken get " (2)" rather than being overwritten. Everything is
// unpacked into a hidden temporary folder first, so cancelling or a failure
// removes it all and leaves dir as it was.
func extractJob(ctx context.Context, p *progress, dir string, archives []string) opResult {
	p.setVerb("Extracting")
	p.counting.Store(true)
	sizes := make([]map[string]int64, len(archives))
	engines := make([]string, len(archives))
	for i, a := range archives {
		ms, engine, err := listArchive(ctx, a)
		engines[i] = engine
		if err != nil {
			p.counting.Store(false)
			if ctx.Err() != nil {
				return opResult{desc: "Stopped extracting; nothing was left behind"}
			}
			return opResult{err: err}
		}
		sizes[i] = map[string]int64{}
		for _, m := range ms {
			if !m.isDir {
				p.totalFiles.Add(1)
				p.totalBytes.Add(m.size)
				sizes[i][m.path] = m.size
			}
		}
	}
	p.counting.Store(false)

	var made, notes []string
	removeMade := func() {
		for _, n := range made {
			_ = os.RemoveAll(filepath.Join(dir, n))
		}
	}
	for i, a := range archives {
		tmp, err := os.MkdirTemp(dir, ".console-places-extract-")
		if err != nil {
			return opResult{err: err}
		}
		err = runExtract(ctx, a, tmp, engines[i], sizes[i], p)
		if err != nil {
			_ = os.RemoveAll(tmp)
			if ctx.Err() != nil {
				removeMade()
				return opResult{desc: "Stopped extracting; nothing was left behind"}
			}
			return opResult{err: fmt.Errorf("extracting %s: %w", filepath.Base(a), err), desc: extractedDesc(made, notes), undo: trashUndo(dir, made)}
		}
		des, _ := os.ReadDir(tmp)
		stem, _ := archiveStem(filepath.Base(a))
		src, want := tmp, stem
		if len(des) == 1 && des[0].IsDir() {
			src, want = filepath.Join(tmp, des[0].Name()), des[0].Name()
		}
		if len(des) == 0 {
			_ = os.RemoveAll(tmp)
			notes = append(notes, filepath.Base(a)+" is empty")
			continue
		}
		name := freeName(dir, want)
		if err := os.Rename(src, filepath.Join(dir, name)); err != nil {
			_ = os.RemoveAll(tmp)
			removeMade()
			return opResult{err: err}
		}
		_ = os.RemoveAll(tmp) // empty now, or the temp shell around a single folder
		if name != want {
			notes = append(notes, fmt.Sprintf("%s was taken, so it's in %s", want, name))
		}
		made = append(made, name)
	}
	res := opResult{desc: extractedDesc(made, notes), undo: trashUndo(dir, made)}
	if len(made) > 0 {
		res.focus = made[0]
	}
	return res
}

func extractedDesc(made, notes []string) string {
	var s string
	switch len(made) {
	case 0:
		s = "Nothing extracted"
	case 1:
		s = "Extracted into " + made[0]
	default:
		s = "Extracted " + plural(len(made), "archive")
	}
	if len(notes) > 0 {
		s += " (" + strings.Join(notes, "; ") + ")"
	}
	return s
}

func trashUndo(dir string, made []string) func() error {
	if len(made) == 0 {
		return nil
	}
	return func() error {
		for _, n := range made {
			if _, err := trashPath(filepath.Join(dir, n)); err != nil {
				return err
			}
		}
		return nil
	}
}

// runExtract unpacks archive into dest, counting files (and their listed
// sizes) as bsdtar reports them. bsdtar refuses absolute paths and ".." in
// member names unless told otherwise, so nothing lands outside dest.
func runExtract(ctx context.Context, archive, dest, engine string, sizes map[string]int64, p *progress) error {
	if engine == "7z" {
		cmd := exec.CommandContext(ctx, "7z", "x", "-y", "-o"+dest, archive)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s", firstLine(string(out)))
		}
		p.files.Add(int64(len(sizes)))
		return nil
	}
	cmd := exec.CommandContext(ctx, "bsdtar", "-xvf", archive, "-C", dest)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var last string
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "x "); ok {
			m := arcMember{raw: name}.clean()
			if size, ok := sizes[m.path]; ok {
				p.files.Add(1)
				p.bytes.Add(size)
			}
		} else {
			last = strings.TrimPrefix(line, "bsdtar: ")
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if last != "" {
			return fmt.Errorf("%s", last)
		}
		return err
	}
	return nil
}

// ---- the model side of browsing ---------------------------------------------

// enterArchive shows v's top folder in the file list.
func (m *model) enterArchive(v *arcView) {
	if e, ok := m.current(); ok {
		m.cursors[m.cwd] = e.name
	}
	m.arc = v
	m.offset, m.cursor, m.filter = 0, 0, ""
	m.selected = map[string]bool{}
	m.reload("")
	m.setMsg("inside "+filepath.Base(v.file)+" (read-only) · ← leaves · X extracts", false)
}

// leaveArchive stops browsing an archive, removing its preview files.
func (m *model) leaveArchive() {
	if m.arc == nil {
		return
	}
	m.arc.close()
	m.arc = nil
	m.filter = ""
	m.selected = map[string]bool{}
}

// archiveUp goes to the parent folder inside the archive, or out of it.
func (m *model) archiveUp() {
	v := m.arc
	if v.dir == "" {
		name := filepath.Base(v.file)
		m.leaveArchive()
		m.reload(name)
		return
	}
	v.cursors[v.dir] = m.curName()
	from := path.Base(v.dir)
	v.dir = parentOf(v.dir)
	m.offset, m.cursor, m.filter = 0, 0, ""
	m.reload(from)
}

func (m *model) archiveInto(name string) {
	v := m.arc
	v.cursors[v.dir] = name
	v.dir = v.memberPath(name)
	m.offset, m.cursor, m.filter = 0, 0, ""
	m.selected = map[string]bool{}
	m.reload("")
}

// reloadArchive fills the panes from the archive instead of the disk.
func (m *model) reloadArchive(focus string) {
	v := m.arc
	m.all = v.list(v.dir, m.cfg.ShowHidden)
	sortEntries(m.all, m.cfg.Sort, m.cfg.SortDesc)
	if focus == "" {
		focus = v.cursors[v.dir]
	}
	m.applyView(focus)
	m.parentCursor = 0
	var base string
	if v.dir == "" {
		m.parent, _ = readDir(m.cwd, m.cfg.ShowHidden)
		base = filepath.Base(v.file)
	} else {
		m.parent = v.list(parentOf(v.dir), m.cfg.ShowHidden)
		base = path.Base(v.dir)
	}
	sortEntries(m.parent, m.cfg.Sort, m.cfg.SortDesc)
	for i, e := range m.parent {
		if e.name == base {
			m.parentCursor = i
			break
		}
	}
}
