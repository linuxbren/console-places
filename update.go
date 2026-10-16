package main

// Updates. Once a day, in the background, console-places asks GitHub for the
// latest release. A release binary in a folder the user can write (the
// README's ~/.local/bin install) updates itself: it downloads the release
// archive, checks it against the release's checksums.txt, makes sure the new
// binary runs, swaps it in, then asks to restart into it. Installs owned by
// something else (a distro package, Nix, go install) are never touched; they
// get a note with the command that updates them. A local development build
// never checks. Settings → Updates: automatic (default), notify only, off; U
// checks, installs or restarts by hand whatever the setting. The restart
// prompt (and the notify-only message) say what is new: the bold headlines of
// the "## New" section of every release since the running version.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

// Values of config.Updates.
const (
	updatesAuto   = ""       // install new releases, then ask to restart
	updatesNotify = "notify" // say a new release exists; U installs it
	updatesOff    = "off"    // never check (U still does, on request)
)

const (
	updateEvery    = 24 * time.Hour
	updateRepo     = "linuxbren/console-places"
	releasesPage   = "github.com/" + updateRepo + "/releases"
	maxArchiveSize = 64 << 20
	maxNews        = 4  // headlines in the restart prompt
	maxNewsWidth   = 56 // cells per headline
	// restartEnv tells a restarted console-places to bring its tabs back whatever
	// the "Tabs at startup" setting, and to say which version it now is.
	restartEnv = "CONSOLE_PLACES_RESTARTED"
)

// updateURL is the GitHub API address of the recent releases (a list, so
// the news covers every version skipped); CONSOLE_PLACES_UPDATE_URL replaces it
// (tests point it at a local server, which may also answer with one release).
func updateURL() string {
	if u := envVar("UPDATE_URL"); u != "" {
		return u
	}
	return "https://api.github.com/repos/" + updateRepo + "/releases?per_page=30"
}

type release struct {
	Tag        string `json:"tag_name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r *release) asset(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

// installKind is how this copy of console-places was installed, which decides
// whether it may replace itself.
type installKind int

const (
	installDev     installKind = iota // local build: no release version, never checks
	installSelf                       // release binary in a writable folder: updates itself
	installPackage                    // distro package or a root-owned folder
	installNix
	installGo // go install …@vX.Y.Z
)

// updateState is what the model knows about updates.
type updateState struct {
	kind    installKind
	hint    string   // for kinds that don't update themselves: how to update
	exe     string   // the running binary, symlinks resolved
	busy    bool     // a check or an install is running
	avail   *release // a newer release not yet installed
	ready   string   // a version installed and waiting for a restart
	news    []string // what is new in avail / ready, newest release first
	since   string   // the version the news starts from
	renamed bool     // the binary was just renamed from tidefiles (rename.go)
}

type updateCheckedMsg struct {
	rel    *release
	news   []string
	err    error
	manual bool // U, not the daily check: report "already the latest" too
}

type updateInstalledMsg struct {
	version string
	err     error
}

var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// currentVersion is the version this binary reports (see versionString).
func currentVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.Main.Version
	}
	return ""
}

// detectInstall works out how console-places at exe was installed. ldVersion is
// main.version (set by every release route: GoReleaser, the Arch PKGBUILD,
// the Nix flake); biVersion is the module version Go embeds.
func detectInstall(exe, ldVersion, biVersion string) (installKind, string) {
	switch {
	case strings.HasPrefix(exe, "/nix/store/"):
		return installNix, "nix profile upgrade console-places  (or update your flake input)"
	case ldVersion == "":
		if releaseVersion.MatchString(biVersion) {
			return installGo, "go install github.com/" + updateRepo + "@latest"
		}
		return installDev, ""
	case !releaseVersion.MatchString(ldVersion):
		return installDev, ""
	}
	// A package's binary is never replaced, even by root, who can write there.
	if strings.HasPrefix(exe, "/usr/bin/") {
		m, _ := filepath.Glob("/var/lib/pacman/local/console-places-*")
		if old, _ := filepath.Glob("/var/lib/pacman/local/tidefiles-*"); len(old) > 0 {
			m = append(m, old...)
		}
		if len(m) > 0 {
			return installPackage, "rebuild the console-places package (makepkg, or your AUR helper)"
		}
		return installPackage, "install the new package from " + releasesPage
	}
	if unix.Access(filepath.Dir(exe), unix.W_OK) == nil {
		return installSelf, ""
	}
	return installPackage, "download it again from " + releasesPage + " (" + filepath.Dir(exe) + " needs root)"
}

func newUpdateState() updateState {
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
	}
	bi := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		bi = info.Main.Version
	}
	kind, hint := detectInstall(exe, version, bi)
	st := updateState{kind: kind, hint: hint, exe: exe}
	if kind == installSelf {
		if p := migrateBinary(exe); p != "" {
			st.exe, st.renamed = p, true
		}
	}
	return st
}

// newerVersion says whether release version a (vX.Y.Z) is newer than b.
func newerVersion(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var p [3]int
	if !releaseVersion.MatchString(v) {
		return p, false
	}
	for i, s := range strings.Split(v[1:], ".") {
		p[i], _ = strconv.Atoi(s)
	}
	return p, true
}

// updateDue says whether the daily check should run now.
func (m model) updateDue(now time.Time) bool {
	if m.cfg.Updates == updatesOff || m.upd.kind == installDev {
		return false
	}
	last := time.Unix(m.cfg.UpdateChecked, 0)
	return now.Sub(last) >= updateEvery || now.Before(last)
}

func checkUpdate(manual bool) tea.Cmd {
	cur := currentVersion()
	return func() tea.Msg {
		rel, news, err := fetchRelease(cur)
		return updateCheckedMsg{rel: rel, news: news, err: err, manual: manual}
	}
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

func httpGet(url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "console-places/"+currentVersion())
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", path.Base(url), resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(body)) > limit {
		err = fmt.Errorf("%s is too large", path.Base(url))
	}
	return body, err
}

// fetchRelease returns the newest release and what is new in it and every
// other release since version cur.
func fetchRelease(cur string) (*release, []string, error) {
	body, err := httpGet(updateURL(), 4<<20)
	if err != nil {
		return nil, nil, err
	}
	var rels []release
	if err := json.Unmarshal(body, &rels); err != nil {
		var one release
		if json.Unmarshal(body, &one) != nil {
			return nil, nil, fmt.Errorf("reading the releases: %w", err)
		}
		rels = []release{one}
	}
	var latest *release
	for i := range rels {
		r := &rels[i]
		if r.Draft || r.Prerelease || !releaseVersion.MatchString(r.Tag) {
			continue
		}
		if latest == nil || newerVersion(r.Tag, latest.Tag) {
			latest = r
		}
	}
	if latest == nil {
		return nil, nil, errors.New("no release found")
	}
	return latest, releaseNews(rels, cur, latest.Tag), nil
}

// releaseNews lists the headlines of the releases after from up to to,
// newest first, without repeats.
func releaseNews(rels []release, from, to string) []string {
	var in []release
	for _, r := range rels {
		if !r.Draft && !r.Prerelease && newerVersion(r.Tag, from) && !newerVersion(r.Tag, to) {
			in = append(in, r)
		}
	}
	sort.Slice(in, func(i, j int) bool { return newerVersion(in[i].Tag, in[j].Tag) })
	var news []string
	seen := map[string]bool{}
	for _, r := range in {
		for _, h := range headlines(r.Body) {
			if !seen[strings.ToLower(h)] {
				seen[strings.ToLower(h)] = true
				news = append(news, h)
			}
		}
	}
	return news
}

// headlines picks the bold lead-ins of the bullets under a release's
// "## New" heading ("- **Animated GIFs** in the preview…" → "Animated
// GIFs"). Release notes are written that way on purpose (RELEASING.md).
func headlines(body string) []string {
	var out []string
	inNew := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			inNew = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(t, "#")), "new")
			continue
		}
		if !inNew || !(strings.HasPrefix(t, "- **") || strings.HasPrefix(t, "* **")) {
			continue
		}
		h, _, ok := strings.Cut(t[4:], "**")
		h = strings.TrimSpace(strings.TrimRight(strings.ReplaceAll(sanitize(h), "`", ""), ":"))
		if ok && h != "" {
			out = append(out, h)
		}
	}
	return out
}

// newsLines is what is new, for the restart prompt.
func (u updateState) newsLines() []string {
	if len(u.news) == 0 {
		return nil
	}
	lines := []string{"New since " + u.since + ":"}
	for i, h := range u.news {
		if i == maxNews {
			lines = append(lines, fmt.Sprintf("  • and %d more: %s", len(u.news)-maxNews, releasesPage))
			break
		}
		lines = append(lines, "  • "+ansi.Truncate(h, maxNewsWidth, "…"))
	}
	return append(lines, "")
}

// newsNote is the first headline, for one-line messages.
func (u updateState) newsNote() string {
	switch len(u.news) {
	case 0:
		return ""
	case 1:
		return " (" + ansi.Truncate(u.news[0], maxNewsWidth, "…") + ")"
	}
	return fmt.Sprintf(" (%s, +%d more)", ansi.Truncate(u.news[0], maxNewsWidth, "…"), len(u.news)-1)
}

func installUpdate(rel *release, exe string) tea.Cmd {
	return func() tea.Msg {
		return updateInstalledMsg{version: rel.Tag, err: installRelease(rel, exe)}
	}
}

// installRelease replaces the binary at exe with rel's. The new binary is
// written beside the old one and renamed over it only once its checksum
// matches and it runs and reports rel's version, so a failure at any step
// leaves the old one in place. The running process keeps its old copy.
func installRelease(rel *release, exe string) error {
	name := "console-places_linux_" + runtime.GOARCH + ".tar.gz"
	archiveURL, sumsURL := rel.asset(name), rel.asset("checksums.txt")
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("%s has no %s for this machine", rel.Tag, name)
	}
	sums, err := httpGet(sumsURL, 1<<20)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt does not list %s", name)
	}
	archive, err := httpGet(archiveURL, maxArchiveSize)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return errors.New("the download does not match its checksum; nothing was changed")
	}
	bin, err := binaryFromArchive(archive)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(exe), ".console-places-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	_, err = tmp.Write(bin)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp.Name(), "--version").Output()
	if got := strings.TrimSpace(string(out)); err != nil || got != "console-places "+rel.Tag {
		return fmt.Errorf("the new binary did not start correctly (%q); nothing was changed", got)
	}
	return os.Rename(tmp.Name(), exe)
}

func binaryFromArchive(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("the release archive has no console-places binary")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == "console-places" {
			return io.ReadAll(io.LimitReader(tr, maxArchiveSize))
		}
	}
}

// onUpdateChecked handles the answer to a check.
func (m model) onUpdateChecked(msg updateCheckedMsg) (model, tea.Cmd) {
	m.upd.busy = false
	m.cfg.UpdateChecked = time.Now().Unix()
	m.cfg.save()
	cur := currentVersion()
	switch {
	case msg.err != nil:
		if msg.manual {
			m.setMsg("update check failed: "+msg.err.Error(), true)
		}
		return m, nil
	case !newerVersion(msg.rel.Tag, cur):
		if msg.manual {
			m.setMsg("console-places "+cur+" is the latest version", false)
		}
		return m, nil
	}
	m.upd.avail, m.upd.news, m.upd.since = msg.rel, msg.news, cur
	switch {
	case m.upd.kind != installSelf:
		m.setMsg("console-places "+msg.rel.Tag+" is available"+m.upd.newsNote()+" · update with: "+m.upd.hint, false)
	case m.cfg.Updates == updatesAuto:
		m.upd.busy = true
		return m, installUpdate(msg.rel, m.upd.exe)
	default:
		m.setMsg("console-places "+msg.rel.Tag+" is available"+m.upd.newsNote()+" · U installs it", false)
	}
	return m, nil
}

// onUpdateInstalled handles the end of an install: on success, ask to
// restart (or, if something else is open, say how to).
func (m model) onUpdateInstalled(msg updateInstalledMsg) (model, tea.Cmd) {
	m.upd.busy = false
	if msg.err != nil {
		m.setMsg("update to "+msg.version+" failed: "+msg.err.Error()+" · U retries", true)
		return m, nil
	}
	m.upd.avail, m.upd.ready = nil, msg.version
	if m.modal == nil {
		m.openRestart()
	} else {
		m.setMsg("console-places "+msg.version+" is installed · U restarts into it", false)
	}
	return m, nil
}

func (m *model) openRestart() {
	lines := append([]string{"console-places " + m.upd.ready + " is installed.", ""}, m.upd.newsLines()...)
	m.openConfirm("restart", "update installed", append(lines,
		"Restart into it now? Your tabs come back.",
		"",
		"n keeps working; the new version starts next time.",
	), nil)
}

// updateAction is U: restart into an installed update, install an
// available one, or check now.
func (m model) updateAction() (model, tea.Cmd) {
	switch {
	case m.upd.ready != "":
		m.openRestart()
	case m.upd.busy:
		m.setMsg("already checking for updates…", false)
	case m.upd.avail != nil && m.upd.kind == installSelf:
		m.upd.busy = true
		m.setMsg("downloading console-places "+m.upd.avail.Tag+"…", false)
		return m, installUpdate(m.upd.avail, m.upd.exe)
	case m.upd.avail != nil:
		m.setMsg("console-places "+m.upd.avail.Tag+" is available"+m.upd.newsNote()+" · update with: "+m.upd.hint, false)
	case m.upd.kind == installDev:
		m.setMsg("this is a development build ("+currentVersion()+"); release builds update themselves", false)
	default:
		m.upd.busy = true
		m.setMsg("checking for updates…", false)
		return m, checkUpdate(true)
	}
	return m, nil
}

// restartInto replaces this process with the (updated) binary, opening dir.
// It runs after the UI has closed; if it fails, the user just starts
// console-places again.
func restartInto(dir, theme, cwdFile string) {
	exe, err := os.Executable() // Go drops the " (deleted)" of a replaced binary
	if err == nil {
		args := []string{exe}
		if theme != "" {
			args = append(args, "--theme", theme)
		}
		if cwdFile != "" {
			args = append(args, "--cwd-file", cwdFile)
		}
		args = append(args, "--", dir)
		err = syscall.Exec(exe, args, append(os.Environ(), restartEnv+"=1"))
	}
	fmt.Fprintln(os.Stderr, "console-places: the update is installed, but restarting failed ("+err.Error()+"); start console-places again to use it")
}
