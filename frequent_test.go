package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFrecencyWeights(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	at := func(ago time.Duration) frecent { return frecent{Rank: 8, Last: now.Add(-ago).Unix()} }
	for _, c := range []struct {
		ago  time.Duration
		want float64
	}{{time.Minute, 32}, {3 * time.Hour, 16}, {3 * 24 * time.Hour, 4}, {30 * 24 * time.Hour, 2}} {
		if got := at(c.ago).frecency(now); got != c.want {
			t.Errorf("%v ago: %v, want %v", c.ago, got, c.want)
		}
	}
}

func TestFrequentRanking(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	l := frequentList{}
	for i := 0; i < 5; i++ {
		l.visit("/often/last-month", now.Add(-30*24*time.Hour))
	}
	l.visit("/once/just-now", now)
	l.visit("/twice/today", now.Add(-3*time.Hour))
	l.visit("/twice/today", now.Add(-3*time.Hour))
	// 1×4 = 4 and 2×2 = 4 tie, the more recent first; 5/4 = 1.25 last.
	want := []string{"/once/just-now", "/twice/today", "/often/last-month"}
	if got := l.ranked(now); !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked %q, want %q", got, want)
	}
	if l["/often/last-month"].Rank != 5 {
		t.Fatalf("rank %v, want 5", l["/often/last-month"].Rank)
	}
}

func TestFrequentAging(t *testing.T) {
	l := frequentList{"/a": {Rank: frequentAging, Last: 1}, "/b": {Rank: 1.05, Last: 1}}
	l.age()
	if _, ok := l["/b"]; ok {
		t.Fatal("a folder aged below one visit is kept")
	}
	if r := l["/a"].Rank; r >= frequentAging || r < 0.8*frequentAging {
		t.Fatalf("aged rank %v", r)
	}
}

func TestFrequentPrune(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	l := frequentList{}
	for i := 0; i < 10; i++ {
		l[filepath.Join("/d", string(rune('a'+i)))] = frecent{Rank: float64(i + 1), Last: now.Unix()}
	}
	l["/gone"] = frecent{Rank: 100, Last: now.Unix()}
	l.prune(now, 3, func(p string) bool { return p != "/gone" })
	want := []string{"/d/j", "/d/i", "/d/h"}
	if got := l.ranked(now); !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %q, want %q", got, want)
	}
	// visit keeps at most frequentKeep.
	for i := 0; i < frequentKeep+20; i++ {
		l.visit(filepath.Join("/v", time.Duration(i).String()), now)
	}
	if len(l) != frequentKeep {
		t.Fatalf("%d remembered, want %d", len(l), frequentKeep)
	}
}

func TestParseZoxide(t *testing.T) {
	got := parseZoxide("  12.5 /home/me/src\n   4.0 /tmp/a b\n\nnot a line\n   1.0 relative\n")
	if want := []string{"/home/me/src", "/tmp/a b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

func frequentNames(m model) []string {
	var out []string
	for _, p := range m.modal.places {
		if p.section == secFrequent {
			out = append(out, p.name)
		}
	}
	return out
}

func TestFrequentInPlaces(t *testing.T) {
	os.Remove(frequentPath())
	t.Cleanup(func() { os.Remove(frequentPath()) })
	root := t.TempDir()
	for _, d := range []string{"src", "notes", "gone"} {
		os.Mkdir(filepath.Join(root, d), 0o755)
	}
	m := testModel(t, root)
	for _, d := range []string{"src", "notes", "src", "gone", "src", "notes"} {
		m.chdir(filepath.Join(root, d), "")
		m.chdir(root, "")
	}
	os.Remove(filepath.Join(root, "gone")) // gone folders drop out
	// root was returned to six times, src three, notes two.
	m.openPlaces()
	if got, want := frequentNames(m), []string{filepath.Base(root), "src", "notes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("frequent %q, want %q", got, want)
	}
	at := func(name string) int {
		for i, p := range m.modal.places {
			if p.section == secFrequent && p.name == name {
				return i
			}
		}
		t.Fatalf("no frequent %s", name)
		return -1
	}

	m.modal.sel = at("src") // B pins it: a bookmark now, so no longer under Frequent
	m, _, _ = m.handlePlacesKey(m.modal, "B")
	if !m.isBookmarked(filepath.Join(root, "src")) {
		t.Fatal("B didn't bookmark")
	}
	if got := frequentNames(m); !reflect.DeepEqual(got, []string{filepath.Base(root), "notes"}) {
		t.Fatalf("after pin %q", got)
	}

	m.modal.sel = at("notes") // d forgets it
	m, _, _ = m.handlePlacesKey(m.modal, "d")
	if got := frequentNames(m); !reflect.DeepEqual(got, []string{filepath.Base(root)}) {
		t.Fatalf("after remove %q", got)
	}
	if _, ok := loadFrequent()[filepath.Join(root, "notes")]; ok {
		t.Fatal("d didn't remove it from frequent.json")
	}

	m.cfg.Frequent = frequentOff // off: no tracking, no section
	m.chdir(filepath.Join(root, "src"), "")
	m.chdir(filepath.Join(root, "notes"), "")
	if _, ok := loadFrequent()[filepath.Join(root, "notes")]; ok {
		t.Fatal("tracked while off")
	}
	m.openPlaces()
	if got := frequentNames(m); got != nil {
		t.Fatalf("frequent shown while off: %q", got)
	}
}

func TestFrequentZoxideMissing(t *testing.T) {
	os.Remove(frequentPath())
	t.Cleanup(func() { os.Remove(frequentPath()) })
	root := t.TempDir()
	m := testModel(t, t.TempDir())
	m.cfg.Frequent = frequentZoxide // zoxideBin is "" in tests: our own list instead
	m.chdir(root, "")
	m.openPlaces()
	if got := frequentNames(m); !reflect.DeepEqual(got, []string{filepath.Base(root)}) {
		t.Fatalf("frequent %q", got)
	}
}
