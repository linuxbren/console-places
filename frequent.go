package main

// Frequent folders: every folder you go to is counted, with a frecency score
// (visits, weighted by how recently: zoxide's scheme), and the best ones are
// listed in places under Frequent. Settings → Frequent folders turns it off,
// or hands the ranking to zoxide when it is installed, so the shell's `z` and
// console-places share one list. Without zoxide that setting quietly uses
// console-places' own list. The list is frequent.json next to config.json.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	frequentOn     = ""       // console-places' own list (the default)
	frequentZoxide = "zoxide" // zoxide's, when installed
	frequentOff    = "off"

	secFrequent   = "Frequent"
	frequentShown = 10   // folders listed in places
	frequentKeep  = 200  // folders remembered
	frequentAging = 2000 // total rank above which every rank decays (as zoxide's _ZO_MAXAGE)
)

// frecent is one remembered folder.
type frecent struct {
	Rank float64 `json:"rank"` // visits, aged
	Last int64   `json:"last"` // unix time of the last visit
}

// frecency scores rank by how recent the last visit was, as zoxide does.
func (f frecent) frecency(now time.Time) float64 {
	switch age := now.Sub(time.Unix(f.Last, 0)); {
	case age < time.Hour:
		return f.Rank * 4
	case age < 24*time.Hour:
		return f.Rank * 2
	case age < 7*24*time.Hour:
		return f.Rank / 2
	}
	return f.Rank / 4
}

// frequentList maps folder paths to their visits.
type frequentList map[string]frecent

// visit counts a visit to dir, then ages and prunes the list.
func (l frequentList) visit(dir string, now time.Time) {
	f := l[dir]
	f.Rank++
	f.Last = now.Unix()
	l[dir] = f
	l.age()
	l.prune(now, frequentKeep, func(string) bool { return true })
}

// age scales every rank down once their total passes frequentAging, so old
// habits fade; folders left below one visit are dropped.
func (l frequentList) age() {
	var total float64
	for _, f := range l {
		total += f.Rank
	}
	if total <= frequentAging {
		return
	}
	k := 0.9 * frequentAging / total
	for p, f := range l {
		if f.Rank *= k; f.Rank < 1 {
			delete(l, p)
		} else {
			l[p] = f
		}
	}
}

// prune drops folders that no longer exist, then keeps the best keep.
func (l frequentList) prune(now time.Time, keep int, exists func(string) bool) {
	for p := range l {
		if !exists(p) {
			delete(l, p)
		}
	}
	if len(l) <= keep {
		return
	}
	for _, p := range l.ranked(now)[keep:] {
		delete(l, p)
	}
}

// ranked lists the folders best first (ties: the more recent, then by name).
func (l frequentList) ranked(now time.Time) []string {
	out := make([]string, 0, len(l))
	for p := range l {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := l[out[i]], l[out[j]]
		if sa, sb := a.frecency(now), b.frecency(now); sa != sb {
			return sa > sb
		}
		if a.Last != b.Last {
			return a.Last > b.Last
		}
		return out[i] < out[j]
	})
	return out
}

func frequentPath() string {
	p := configPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "frequent.json")
}

func loadFrequent() frequentList {
	l := frequentList{}
	if p := frequentPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &l)
		}
	}
	return l
}

func (l frequentList) save() {
	p := frequentPath()
	if p == "" || os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	if b, err := json.Marshal(l); err == nil {
		tmp := p + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, p)
		}
	}
}

func isDirPath(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// zoxideBin is zoxide's path, "" when it isn't installed (a variable for tests).
var zoxideBin = func() string {
	p, _ := exec.LookPath("zoxide")
	return p
}

func (m model) useZoxide() bool { return m.cfg.Frequent == frequentZoxide && zoxideBin() != "" }

// recordVisit counts a visit to dir (navigate calls it).
func (m *model) recordVisit(dir string) {
	if m.cfg.Frequent == frequentOff || dir == "" || dir == "/" || dir == trashFilesDir() {
		return
	}
	if home, _ := os.UserHomeDir(); dir == home {
		return // like zoxide: home is always a place already
	}
	if m.useZoxide() {
		if cmd := exec.Command(zoxideBin(), "add", "--", dir); cmd.Start() == nil {
			go cmd.Wait() //nolint:errcheck // reap it; navigation doesn't wait for zoxide
			return
		} // zoxide failed to start: our own list instead

	}
	l := loadFrequent() // re-read: other windows and tabs record too
	l.visit(dir, time.Now())
	l.save()
}

// frequentFolders are the best few folders, existing ones only, skipping skip.
func (m model) frequentFolders(skip func(string) bool) []string {
	if m.cfg.Frequent == frequentOff {
		return nil
	}
	var ranked []string
	if m.useZoxide() {
		ranked = zoxideRanked()
	}
	if ranked == nil {
		l := loadFrequent()
		n := len(l)
		l.prune(time.Now(), frequentKeep, isDirPath)
		if len(l) != n {
			l.save()
		}
		ranked = l.ranked(time.Now())
	}
	var out []string
	for _, p := range ranked {
		if len(out) == frequentShown {
			break
		}
		if !skip(p) && isDirPath(p) {
			out = append(out, p)
		}
	}
	return out
}

// zoxideRanked reads zoxide's ranking (best first); nil when it can't.
func zoxideRanked() []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, zoxideBin(), "query", "--list", "--score").Output()
	if err != nil {
		return nil
	}
	return parseZoxide(string(out))
}

// parseZoxide reads `zoxide query --list --score` lines: "  12.5 /path".
func parseZoxide(s string) []string {
	ranked := []string{}
	for _, l := range strings.Split(s, "\n") {
		score, path, ok := strings.Cut(strings.TrimSpace(l), " ")
		if _, err := strconv.ParseFloat(score, 64); !ok || err != nil {
			continue
		}
		if path = strings.TrimSpace(path); filepath.IsAbs(path) {
			ranked = append(ranked, path)
		}
	}
	return ranked
}

// forgetFrequent removes dir from the frequent list (zoxide's too, when used).
func (m *model) forgetFrequent(dir string) {
	if m.useZoxide() {
		_ = exec.Command(zoxideBin(), "remove", "--", dir).Run()
	}
	l := loadFrequent()
	delete(l, dir)
	l.save()
}

// pinFrequent bookmarks a frequent folder.
func (m *model) pinFrequent(dir string) {
	if !m.isBookmarked(dir) {
		m.cfg.Bookmarks = append(m.cfg.Bookmarks, dir)
		m.cfg.save()
	}
	m.setMsg("bookmarked "+tildePath(dir), false)
}

// openFrequent opens places on the first frequent folder.
func (m *model) openFrequent() {
	m.openPlaces()
	for i, p := range m.modal.places {
		if p.frequent {
			m.modal.sel = i
			return
		}
	}
	if m.cfg.Frequent == frequentOff {
		m.setMsg("frequent folders are off · Settings (S) turns them on", false)
	} else {
		m.setMsg("no frequent folders yet: they appear as you open folders", false)
	}
}
