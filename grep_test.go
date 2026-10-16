package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// grepFixture builds a small git repo exercising every rule both engines
// must follow.
func grepFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"main.go":           "package main\n\nfunc Needle() {}\n// needle in a comment\n",
		"docs/notes.md":     "first line\nsecond NEEDLE line\n",
		"ignored/skip.txt":  "needle but gitignored\n",
		".hidden/secret.go": "needle in a hidden dir\n",
		".env":              "TOKEN=needle\n",
		"bin.dat":           "needle\x00binary\n",
		"tabs.txt":          "\t\tindented needle here\n",
		".gitignore":        "ignored/\n",
	}
	for p, c := range files {
		mkfile(t, filepath.Join(root, p), c)
	}
	big := strings.Repeat("x", grepMaxFileSize) + "\nneedle\n"
	mkfile(t, filepath.Join(root, "big.log"), big)
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	return root
}

type grepEngine = func(context.Context, string, grepQuery, int, chan<- grepBatchMsg)

func runGrep(t *testing.T, engine grepEngine, root, query string, hidden bool) []grepHit {
	t.Helper()
	hits, err := runGrepQ(engine, root, grepQuery{text: query, hidden: hidden})
	if err != "" {
		t.Fatalf("search error: %s", err)
	}
	return hits
}

// runGrepQ runs a search to completion and returns its sorted hits and error.
func runGrepQ(engine grepEngine, root string, q grepQuery) ([]grepHit, string) {
	ch := make(chan grepBatchMsg, 4)
	go engine(context.Background(), root, q, 1, ch)
	var hits []grepHit
	errMsg := ""
	for msg := range ch {
		hits = append(hits, msg.hits...)
		if msg.err != "" {
			errMsg = msg.err
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})
	return hits, errMsg
}

func hitKeys(hits []grepHit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.path+":"+strconv.Itoa(h.line))
	}
	return out
}

func engines(t *testing.T) map[string]grepEngine {
	e := map[string]grepEngine{"built-in": grepFiles}
	if _, err := exec.LookPath("rg"); err == nil {
		e["ripgrep"] = ripgrep
	} else {
		t.Log("rg not installed: testing the built-in engine only")
	}
	return e
}

func TestGrepEnginesAgree(t *testing.T) {
	root := grepFixture(t)
	for name, engine := range engines(t) {
		t.Run(name, func(t *testing.T) {
			got := strings.Join(hitKeys(runGrep(t, engine, root, "needle", false)), " ")
			// smart case: lower-case query matches Needle and NEEDLE; skips
			// .gitignored, hidden, binary and oversized files.
			if want := "docs/notes.md:2 main.go:3 main.go:4 tabs.txt:1"; got != want {
				t.Errorf("hidden off:\n got  %s\n want %s", got, want)
			}
			got = strings.Join(hitKeys(runGrep(t, engine, root, "needle", true)), " ")
			if want := ".env:1 .hidden/secret.go:1 docs/notes.md:2 main.go:3 main.go:4 tabs.txt:1"; got != want {
				t.Errorf("hidden on:\n got  %s\n want %s", got, want)
			}
			got = strings.Join(hitKeys(runGrep(t, engine, root, "NEEDLE", false)), " ")
			if want := "docs/notes.md:2"; got != want {
				t.Errorf("capitals are case-sensitive:\n got  %s\n want %s", got, want)
			}
			hits := runGrep(t, engine, root, "Needle", false)
			if len(hits) != 1 || hits[0].text != "func Needle() {}" || len(hits[0].spans) != 1 || hits[0].spans[0] != [2]int{5, 11} {
				t.Errorf("text and spans: %+v", hits)
			}
		})
	}
}

func TestGrepWithoutGit(t *testing.T) {
	root := t.TempDir()
	mkfile(t, filepath.Join(root, "a/b.txt"), "one needle\n")
	mkfile(t, filepath.Join(root, ".h/c.txt"), "needle\n")
	got := strings.Join(hitKeys(runGrep(t, grepFiles, root, "needle", false)), " ")
	if got != "a/b.txt:1" {
		t.Errorf("walk fallback: %s", got)
	}
}

func TestGrepNestedRepoGitignore(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	mkfile(t, filepath.Join(repo, "keep.txt"), "needle\n")
	mkfile(t, filepath.Join(repo, "gen/out.json"), "needle\n")
	mkfile(t, filepath.Join(repo, ".gitignore"), "gen/\n")
	mkfile(t, filepath.Join(root, "loose.txt"), "needle\n")
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	for name, engine := range engines(t) {
		got := strings.Join(hitKeys(runGrep(t, engine, root, "needle", false)), " ")
		if want := "loose.txt:1 proj/keep.txt:1"; got != want {
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
}

func TestGrepPerFileCapAndTotalCap(t *testing.T) {
	root := t.TempDir()
	mkfile(t, filepath.Join(root, "many.txt"), strings.Repeat("hit\n", grepMaxPerFile+50))
	for name, engine := range engines(t) {
		if n := len(runGrep(t, engine, root, "hit", false)); n != grepMaxPerFile {
			t.Errorf("%s: per-file cap gave %d hits", name, n)
		}
	}
	for i := 0; i < 15; i++ {
		mkfile(t, filepath.Join(root, "f"+string(rune('a'+i))+".txt"), strings.Repeat("hit\n", grepMaxPerFile))
	}
	for name, engine := range engines(t) {
		if n := len(runGrep(t, engine, root, "hit", false)); n != grepMaxResults {
			t.Errorf("%s: total cap gave %d hits", name, n)
		}
	}
}

func TestParseRgMatch(t *testing.T) {
	line := `{"type":"match","data":{"path":{"text":"./src/a.go"},"lines":{"text":"x := foo(foo)\n"},"line_number":7,"submatches":[{"match":{"text":"foo"},"start":5,"end":8},{"match":{"text":"foo"},"start":9,"end":12}]}}`
	h, ok := parseRgMatch([]byte(line))
	if !ok || h.path != "src/a.go" || h.line != 7 || h.text != "x := foo(foo)" || len(h.spans) != 2 || h.spans[1] != [2]int{9, 12} {
		t.Fatalf("%+v %v", h, ok)
	}
	if _, ok := parseRgMatch([]byte(`{"type":"begin","data":{}}`)); ok {
		t.Error("only match events are hits")
	}
	if _, ok := parseRgMatch([]byte(`{"type":"match","data":{"path":{"bytes":"/w=="},"lines":{"text":"x"}}}`)); ok {
		t.Error("non-UTF-8 paths are skipped")
	}
}

func TestSnippetKeepsMatchVisible(t *testing.T) {
	text := "\t\t" + strings.Repeat("filler ", 20) + "TARGET end"
	i := strings.Index(text, "TARGET")
	h := grepHit{text: text, spans: [][2]int{{i, i + 6}}}
	out := snippet(h, 30, func(s ...string) string { return "[" + strings.Join(s, "") + "]" })
	if !strings.HasPrefix(out, "…") || !strings.Contains(out, "[T][A][R][G][E][T]") {
		t.Fatalf("match not visible: %q", out)
	}
	if plain := strings.NewReplacer("[", "", "]", "").Replace(out); len([]rune(plain)) > 30 {
		t.Fatalf("too wide: %q", plain)
	}
	ctrl := snippet(grepHit{text: "a\x1b[31mb"}, 20, func(s ...string) string { return strings.Join(s, "") })
	if strings.ContainsRune(ctrl, 0x1b) {
		t.Fatalf("control characters leaked: %q", ctrl)
	}
}

func TestFindAllCase(t *testing.T) {
	if got := findAll("Foo foo FOO", "foo", false); len(got) != 3 || got[2] != [2]int{8, 11} {
		t.Errorf("insensitive: %v", got)
	}
	if got := findAll("Foo foo FOO", "Foo", true); len(got) != 1 || got[0] != [2]int{0, 3} {
		t.Errorf("sensitive: %v", got)
	}
}

func TestPreviewJumpsToLine(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 5000; i++ {
		b.WriteString("line " + strings.Repeat("x", i%7) + "\n")
	}
	mkfile(t, filepath.Join(dir, "big.txt"), b.String())
	fi, _ := os.Stat(filepath.Join(dir, "big.txt"))
	e := entry{name: "big.txt", size: fi.Size()}
	o := previewOpts{width: 80, rows: 20, wrap: true, line: 4321}
	p := buildPreview(dir, e, o)
	if p.hlTo <= p.hlFrom {
		t.Fatalf("no highlight for line 4321 (meta %q)", p.meta)
	}
	if got := p.lines[p.hlFrom]; got != "line "+strings.Repeat("x", 4321%7) {
		t.Fatalf("highlighted %q", got)
	}
	if !strings.HasPrefix(p.lines[0], "… from line") || !strings.Contains(p.meta, "line 4321") {
		t.Fatalf("window not marked: %q / %q", p.lines[0], p.meta)
	}
	o.line = 3
	p = buildPreview(dir, e, o)
	if p.hlFrom != 2 || p.hlTo != 3 {
		t.Fatalf("near the top: %d..%d", p.hlFrom, p.hlTo)
	}
}

func TestMarkdownJumpShowsSource(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "r.md"), "# Title\n\nsome **text**\n")
	p := buildPreview(dir, entry{name: "r.md", size: 25}, previewOpts{width: 60, rows: 10, wrap: true, line: 3, syn: testSyntax})
	if p.hlTo <= p.hlFrom || !strings.Contains(p.lines[p.hlFrom], "**text**") {
		t.Fatalf("source line 3 not highlighted: %v %d..%d", p.lines, p.hlFrom, p.hlTo)
	}
}

func TestGrepRegex(t *testing.T) {
	root := t.TempDir()
	mkfile(t, filepath.Join(root, "a.go"), "func Alpha() {}\nfunc beta(x int) {}\nvar gamma = 3\nliteral (.*) here\n")
	for name, engine := range engines(t) {
		t.Run(name, func(t *testing.T) {
			keys := func(q grepQuery) string {
				hits, err := runGrepQ(engine, root, q)
				if err != "" {
					t.Fatalf("%+v: %s", q, err)
				}
				return strings.Join(hitKeys(hits), " ")
			}
			if got := keys(grepQuery{text: `^func \w+\(`, regex: true}); got != "a.go:1 a.go:2" {
				t.Errorf("regex: %s", got)
			}
			// \w has a capital-free escape; \W must not switch on case sensitivity.
			if got := keys(grepQuery{text: `ALPHA\W`, regex: true}); got != "" {
				t.Errorf("capitals in a regex are case-sensitive: %s", got)
			}
			if got := keys(grepQuery{text: `alpha\W`, regex: true}); got != "a.go:1" {
				t.Errorf("\\W alone keeps it case-insensitive: %s", got)
			}
			if got := keys(grepQuery{text: `(.*)`}); got != "a.go:4" {
				t.Errorf("literal mode treats regex characters literally: %s", got)
			}
			hits, _ := runGrepQ(engine, root, grepQuery{text: `gam+a`, regex: true})
			if len(hits) != 1 || len(hits[0].spans) != 1 || hits[0].spans[0] != [2]int{4, 9} {
				t.Errorf("regex spans: %+v", hits)
			}
			if _, err := runGrepQ(engine, root, grepQuery{text: `func (`, regex: true}); err == "" {
				t.Error("an invalid regex should report an error")
			}
		})
	}
}

func TestRipgrepArgsMode(t *testing.T) {
	lit := strings.Join(ripgrepArgs(grepQuery{text: "x"}), " ")
	re := strings.Join(ripgrepArgs(grepQuery{text: "x", regex: true, hidden: true}), " ")
	if !strings.Contains(lit, "--fixed-strings") || strings.Contains(lit, "--hidden") {
		t.Errorf("literal args: %s", lit)
	}
	if strings.Contains(re, "--fixed-strings") || !strings.Contains(re, "--hidden") || !strings.HasSuffix(re, "-- x .") {
		t.Errorf("regex args: %s", re)
	}
}

func TestJumpHighlightClearsWhenLeavingFile(t *testing.T) {
	t.Setenv("CONSOLE_PLACES_IMAGES", "blocks") // no terminal probing in tests
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "a.txt"), "one\ntwo\nthree\n")
	mkfile(t, filepath.Join(dir, "b.txt"), "other\n")
	cfg := defaultConfig()
	cfg.Theme = "tokyo-night"
	m := newModel(dir, cfg, "", newGfxOut(os.Stdout))
	m.width, m.height = 120, 40
	m.jump = jumpTarget{path: filepath.Join(dir, "a.txt"), line: 2}
	m.chdir(dir, "a.txt")
	m.refreshPreview()
	if m.pv.hlTo <= m.pv.hlFrom || m.pv.lines[m.pv.hlFrom] != "two" {
		t.Fatalf("jump not highlighted: %v %d..%d", m.pv.lines, m.pv.hlFrom, m.pv.hlTo)
	}
	m.move(1) // to b.txt
	m.refreshPreview()
	if m.jump.path != "" {
		t.Fatalf("jump kept after leaving the file: %+v", m.jump)
	}
	m.move(-1) // back to a.txt: no highlight any more
	m.refreshPreview()
	if m.pv.hlTo > m.pv.hlFrom {
		t.Fatalf("highlight came back: %d..%d", m.pv.hlFrom, m.pv.hlTo)
	}
}

func TestGrepResultsSortedWhenDone(t *testing.T) {
	g := &grepState{run: 7, files: map[string]bool{}, cancel: func() {}}
	m := model{modal: &modal{kind: mGrep, grep: g}}
	m.handleGrepBatch(grepBatchMsg{run: 7, hits: []grepHit{{path: "b.go", line: 9}, {path: "a.go", line: 30}}})
	m.modal.sel, g.moved = 1, true // the user moved to a.go:30 while results stream in
	m.handleGrepBatch(grepBatchMsg{run: 7, hits: []grepHit{{path: "a.go", line: 4}, {path: "b.go", line: 2}}, done: true})
	var got []string
	for _, h := range g.hits {
		got = append(got, h.path+":"+strconv.Itoa(h.line))
	}
	if strings.Join(got, " ") != "a.go:4 a.go:30 b.go:2 b.go:9" {
		t.Fatalf("not sorted: %v", got)
	}
	if h := g.hits[m.modal.sel]; h.path != "a.go" || h.line != 30 {
		t.Fatalf("selection moved off a.go:30 to %s:%d", h.path, h.line)
	}

	// If the user hasn't moved, the sorted list starts at the top.
	g2 := &grepState{run: 8, files: map[string]bool{}, cancel: func() {}}
	m2 := model{modal: &modal{kind: mGrep, grep: g2}}
	m2.handleGrepBatch(grepBatchMsg{run: 8, hits: []grepHit{{path: "z.go", line: 1}}})
	m2.handleGrepBatch(grepBatchMsg{run: 8, hits: []grepHit{{path: "a.go", line: 1}}, done: true})
	if m2.modal.sel != 0 || g2.hits[0].path != "a.go" {
		t.Fatalf("untouched selection should be the first sorted hit: sel %d, %+v", m2.modal.sel, g2.hits)
	}
}
