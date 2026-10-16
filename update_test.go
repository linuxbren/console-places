package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.7.1", "v0.7.0", true},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.7.0", "v0.7.0", false},
		{"v0.6.9", "v0.7.0", false},
		{"v0.8.0", "(devel)", false},
		{"v0.8.0", "v0.7.0-0.20261006041954-5f76a2df6047", false},
		{"nightly", "v0.7.0", false},
	} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestDetectInstall(t *testing.T) {
	writable := t.TempDir()
	// A folder this user can't write (like a root-owned /usr/local/bin);
	// root can write anywhere, so there it counts as writable.
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	lockedKind := installPackage
	if os.Geteuid() == 0 {
		lockedKind = installSelf
	}
	for _, c := range []struct {
		name, exe, ld, bi string
		want              installKind
	}{
		{"release in ~/.local/bin", filepath.Join(writable, "console-places"), "v0.7.0", "", installSelf},
		{"package in /usr/bin", "/usr/bin/console-places", "v0.7.0", "", installPackage},
		{"folder the user can't write", filepath.Join(locked, "console-places"), "v0.7.0", "", lockedKind},
		{"nix", "/nix/store/abc-console-places-0.7.0/bin/console-places", "v0.7.0", "", installNix},
		{"go install", filepath.Join(writable, "console-places"), "", "v0.7.0", installGo},
		{"local build", filepath.Join(writable, "console-places"), "", "v0.7.1-0.20261006041954-5f76a2df6047", installDev},
		{"go run", filepath.Join(writable, "console-places"), "", "(devel)", installDev},
	} {
		got, hint := detectInstall(c.exe, c.ld, c.bi)
		if got != c.want {
			t.Errorf("%s: kind %v, want %v", c.name, got, c.want)
		}
		if (got == installPackage || got == installNix || got == installGo) && hint == "" {
			t.Errorf("%s: no hint for updating", c.name)
		}
	}
}

// fakeRelease serves GitHub's release list: tag, whose archive holds a
// console-places that prints tag (tamper corrupts the archive after its
// checksum), plus older releases with notes, a draft and a pre-release.
func fakeRelease(t *testing.T, tag string, tamper bool) {
	t.Helper()
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	bin := []byte("#!/bin/sh\necho 'console-places " + tag + "'\n")
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {"console-places", bin}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		tw.Write(f.body)
	}
	tw.Close()
	gz.Close()
	name := "console-places_linux_" + runtime.GOARCH + ".tar.gz"
	sum := sha256.Sum256(tgz.Bytes())
	archive := tgz.Bytes()
	if tamper {
		archive = append([]byte{}, archive...)
		archive[len(archive)/2] ^= 0xff
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v100.0.0", "draft": true, "body": "## New\n- **Draft thing**"},
				{"tag_name": "v100.0.1", "prerelease": true, "body": "## New\n- **Pre-release thing**"},
				{"tag_name": tag, "body": "## New\n\n- **Fresh thing** in the preview\n- **Second thing**: details\n\n## Changed\n\n- **Not news**", "assets": []map[string]string{
					{"name": name, "browser_download_url": srv.URL + "/" + name},
					{"name": "checksums.txt", "browser_download_url": srv.URL + "/checksums.txt"},
				}},
				{"tag_name": "v0.7.5", "body": "## New\n- **Middle thing**\n- plain bullet, no headline"},
				{"tag_name": "v0.7.0", "body": "## New\n- **Already have this**"},
			})
		case "/" + name:
			w.Write(archive)
		case "/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n%s  other.deb\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CONSOLE_PLACES_UPDATE_URL", srv.URL+"/releases")
}

// oldBinary writes a stand-in console-places v0.7.0 and makes it the running
// version.
func oldBinary(t *testing.T) string {
	t.Helper()
	old := version
	version = "v0.7.0"
	t.Cleanup(func() { version = old })
	exe := filepath.Join(t.TempDir(), "console-places")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'console-places v0.7.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestInstallRelease(t *testing.T) {
	fakeRelease(t, "v9.9.9", false)
	exe := oldBinary(t)
	rel, _, err := fetchRelease("v0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := installRelease(rel, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "v9.9.9") {
		t.Fatalf("binary not replaced: %s", b)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".console-places-update-*")); len(left) > 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestInstallReleaseRefusesABadDownload(t *testing.T) {
	fakeRelease(t, "v9.9.9", true)
	exe := oldBinary(t)
	rel, _, err := fetchRelease("v0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := installRelease(rel, exe); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want a checksum error, got %v", err)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "v0.7.0") {
		t.Fatal("the old binary must be left alone")
	}
}

func TestAutomaticUpdateAsksToRestart(t *testing.T) {
	fakeRelease(t, "v99.0.0", false)
	m := testModel(t, t.TempDir())
	m.upd = updateState{kind: installSelf, exe: oldBinary(t)}

	msg := checkUpdate(false)()
	m2, cmd := m.Update(msg)
	m = m2.(model)
	if cmd == nil || !m.upd.busy {
		t.Fatal("automatic mode should start installing")
	}
	if time.Since(time.Unix(m.cfg.UpdateChecked, 0)) > time.Minute {
		t.Fatal("the check time should be recorded")
	}
	m2, _ = m.Update(cmd())
	m = m2.(model)
	if m.modal == nil || m.modal.purpose != "restart" || m.upd.ready != "v99.0.0" {
		t.Fatalf("want the restart question, got modal %+v, upd %+v", m.modal, m.upd)
	}
	prompt := strings.Join(m.modal.lines, "\n")
	for _, want := range []string{"New since v0.7.0:", "  • Fresh thing\n  • Second thing\n  • Middle thing\n"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("restart prompt lacks %q:\n%s", want, prompt)
		}
	}
	for _, not := range []string{"Draft", "Pre-release", "Not news", "Already have", "plain bullet"} {
		if strings.Contains(prompt, not) {
			t.Errorf("restart prompt has %q:\n%s", not, prompt)
		}
	}
	if b, _ := os.ReadFile(m.upd.exe); !strings.Contains(string(b), "v99.0.0") {
		t.Fatal("binary not replaced")
	}

	no := send(m, "n")
	if no.restart || no.modal != nil {
		t.Fatal("n should keep working")
	}
	if again := send(no, "U"); again.modal == nil || again.modal.purpose != "restart" {
		t.Fatal("U should offer the restart again")
	}
	if yes := send(m, "y"); !yes.restart {
		t.Fatal("y should restart")
	}
}

func TestNotifyAndPackagedInstallsDontReplaceThemselves(t *testing.T) {
	fakeRelease(t, "v99.0.0", false)
	m := testModel(t, t.TempDir())
	m.cfg.Updates = updatesNotify
	m.upd = updateState{kind: installSelf, exe: oldBinary(t)}
	m2, cmd := m.Update(checkUpdate(false)())
	m = m2.(model)
	if cmd != nil || !strings.Contains(m.msg, "U installs it") {
		t.Fatalf("notify mode: cmd %v, msg %q", cmd != nil, m.msg)
	}

	p := testModel(t, t.TempDir())
	p.upd = updateState{kind: installPackage, hint: "install the new package"}
	p2, cmd := p.Update(checkUpdate(false)())
	p = p2.(model)
	if cmd != nil || !strings.Contains(p.msg, "install the new package") {
		t.Fatalf("packaged: cmd %v, msg %q", cmd != nil, p.msg)
	}
}

func TestUpdateDue(t *testing.T) {
	m := testModel(t, t.TempDir())
	m.upd.kind = installSelf
	now := time.Now()
	m.cfg.UpdateChecked = now.Add(-25 * time.Hour).Unix()
	if !m.updateDue(now) {
		t.Fatal("a day old: due")
	}
	m.cfg.UpdateChecked = now.Add(-time.Hour).Unix()
	if m.updateDue(now) {
		t.Fatal("an hour old: not due")
	}
	m.cfg.UpdateChecked = 0
	m.cfg.Updates = updatesOff
	if m.updateDue(now) {
		t.Fatal("off: never due")
	}
	m.cfg.Updates = updatesAuto
	m.upd.kind = installDev
	if m.updateDue(now) {
		t.Fatal("development builds never check")
	}
}

// send delivers a key through Update, as the program does (modals included).
func send(m model, key string) model {
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return m2.(model)
}

func TestHeadlines(t *testing.T) {
	notes := "## New\n\n- **Animated GIFs** in the preview\n* **Copy text (`Y`)**: drag\n- Copy with `Y` (replaced later)\n- **\x1b[31mEvil\x1b[0m**\n\n## Install\n\n- **Static binaries**\n"
	got := headlines(notes)
	want := []string{"Animated GIFs", "Copy text (Y)", "[31mEvil[0m"}
	if !slices.Equal(got, want) {
		t.Errorf("headlines = %q, want %q", got, want)
	}
}

func TestNewsIsCappedAndNotesWhenSkippingReleases(t *testing.T) {
	u := updateState{since: "v0.7.0", news: []string{"a", "b", "c", "d", "e", "f"}}
	lines := u.newsLines()
	if len(lines) != 1+maxNews+2 || !strings.Contains(lines[maxNews+1], "and 2 more") {
		t.Errorf("news lines %q", lines)
	}
	if note := u.newsNote(); note != " (a, +5 more)" {
		t.Errorf("note %q", note)
	}
	if (updateState{}).newsLines() != nil || (updateState{}).newsNote() != "" {
		t.Error("no news should add nothing")
	}
}

func TestNotifyOnlySaysWhatsNew(t *testing.T) {
	fakeRelease(t, "v99.0.0", false)
	m := testModel(t, t.TempDir())
	m.cfg.Updates = updatesNotify
	m.upd = updateState{kind: installSelf, exe: oldBinary(t)}
	m2, _ := m.Update(checkUpdate(false)())
	if msg := m2.(model).msg; !strings.Contains(msg, "v99.0.0 is available (Fresh thing, +2 more) · U installs it") {
		t.Errorf("notify message %q", msg)
	}
}
