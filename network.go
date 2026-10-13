package main

// Network locations in places. Connecting goes through gvfs (`gio mount`), as
// in Nautilus: SMB reaches Windows, macOS and Samba shares, SFTP any machine
// with ssh, NFS Unix exports. gvfs mounts are user mounts (no root) and are
// shared with Nautilus, including passwords saved in the keyring. gio asks
// for anything else it needs (user, domain, password, a host key question) on
// stdin; tidefiles relays each prompt into a modal and sends the answer back.
// Passwords pass straight through and are never stored; the server address
// is remembered in places.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const maxServers = 10

// guestSchemes can log in anonymously (gio mount -a).
var guestSchemes = map[string]bool{"smb": true, "ftp": true, "ftps": true, "dav": true, "davs": true}

// netSchemes are the gvfs backends tidefiles connects to.
var netSchemes = map[string]bool{"smb": true, "sftp": true, "nfs": true, "ftp": true, "ftps": true, "dav": true, "davs": true, "afp": true}

// normalizeServer turns what someone types into a gvfs location:
// smb://host/share, \\host\share, user@host:/path (ssh), nfs://host/export…
func normalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("type a server address")
	}
	if strings.HasPrefix(s, `\\`) || strings.HasPrefix(s, "//") { // Windows UNC path
		s = "smb://" + strings.ReplaceAll(strings.TrimLeft(s, `\/`), `\`, "/")
	}
	if !strings.Contains(s, "://") {
		host, path, ok := strings.Cut(s, ":")
		if !ok || host == "" || strings.ContainsAny(host, "/ ") {
			return "", errors.New("start with smb:// (Windows, Mac, Samba), sftp:// (ssh) or nfs://")
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		s = "sftp://" + host + path
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", errors.New("not a server address: " + s)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "ssh":
		u.Scheme = "sftp"
	case "cifs":
		u.Scheme = "smb"
	case "webdav":
		u.Scheme = "dav"
	case "webdavs":
		u.Scheme = "davs"
	}
	if !netSchemes[u.Scheme] {
		return "", errors.New(u.Scheme + ":// isn't supported: use smb, sftp, nfs, ftp or dav")
	}
	return u.String(), nil
}

// serverName is how a location is shown: "share on host", "user@host".
func serverName(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	host := u.Host
	if u.User != nil {
		host = u.User.Username() + "@" + host
	}
	if p := strings.Trim(u.Path, "/"); p != "" {
		return p + " on " + host
	}
	return host
}

// netMount is a connected network location.
type netMount struct {
	name, uri, path string
	system          bool // mounted by the system (fstab, sshfs), not gvfs
}

var mountLine = regexp.MustCompile(`^\s*Mount\(\d+\): (.*) -> (\S+)$`)

// gvfsMounts lists the network locations gvfs has mounted, with the local
// (gvfs-fuse) folder each one is reachable at.
var gvfsMounts = func() []netMount {
	if _, err := exec.LookPath("gio"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), placesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gio", "mount", "-l").Output()
	if err != nil {
		return nil
	}
	var res []netMount
	for _, l := range strings.Split(string(out), "\n") {
		mt := mountLine.FindStringSubmatch(l)
		if mt == nil {
			continue
		}
		scheme, _, _ := strings.Cut(mt[2], "://")
		if !netSchemes[scheme] {
			continue
		}
		res = append(res, netMount{name: mt[1], uri: mt[2], path: gioLocalPath(mt[2])})
	}
	return res
}

// gioLocalPath is where gvfs-fuse shows a location, "" if nowhere.
var gioLocalPath = func(uri string) string {
	ctx, cancel := context.WithTimeout(context.Background(), placesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gio", "info", uri).Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(l, "local path: "); ok {
			return strings.TrimSpace(p)
		}
	}
	return ""
}

// netFS are the filesystems of network mounts the system made.
var netFS = map[string]bool{"cifs": true, "smb3": true, "nfs": true, "nfs4": true, "fuse.sshfs": true, "fuse.rclone": true, "davfs": true, "9p": true}

// systemNetMounts lists network filesystems mounted outside gvfs.
func systemNetMounts(mounts string) []netMount {
	var res []netMount
	for _, l := range strings.Split(mounts, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || !netFS[f[2]] {
			continue
		}
		unesc := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`)
		res = append(res, netMount{name: unesc.Replace(f[0]), uri: unesc.Replace(f[0]), path: unesc.Replace(f[1]), system: true})
	}
	return res
}

func readMounts() string {
	b, _ := os.ReadFile("/proc/self/mounts")
	return string(b)
}

// rememberServer puts uri first in the saved servers.
func (m *model) rememberServer(uri string) {
	list := []string{uri}
	for _, s := range m.cfg.Servers {
		if s != uri && len(list) < maxServers {
			list = append(list, s)
		}
	}
	m.cfg.Servers = list
	m.cfg.save()
}

func (m *model) forgetServer(uri string) {
	var keep []string
	for _, s := range m.cfg.Servers {
		if s != uri {
			keep = append(keep, s)
		}
	}
	m.cfg.Servers = keep
	m.cfg.save()
}

// ---- connecting ----------------------------------------------------------

// connection is one running `gio mount`. Its pointer identifies it, so
// events from a cancelled one are ignored.
type connection struct {
	uri     string
	guest   bool
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	events  chan connEvent
	secrets int          // passwords sent so far: a second ask means the first was wrong
	stderr  bytes.Buffer // gio's errors, read once it has exited

	asking       string // the label of the question being answered
	user, domain string // what was typed at those prompts
	password     string // kept only to save it once connected
	remember     bool   // save the password in the keyring once connected
}

func (c *connection) scheme() string { s, _, _ := strings.Cut(c.uri, "://"); return s }

type connEvent struct {
	prompt string // text up to a question gio waits on
	done   bool
	err    error
	out    string // everything gio said, for the error
}

type connectMsg struct {
	c  *connection
	ev connEvent
}

// startConnection runs gio mount for uri and starts relaying its output.
var startConnection = func(uri string, guest bool) (*connection, error) {
	if _, err := exec.LookPath("gio"); err != nil {
		return nil, missing("gvfs", "connecting needs gvfs (the gio command): install gvfs, plus gvfs-smb for Windows and Mac shares")
	}
	args := []string{"mount"}
	if guest {
		args = append(args, "-a")
	}
	c := &connection{uri: uri, guest: guest, events: make(chan connEvent, 4)}
	c.cmd = exec.Command("gio", append(args, uri)...)
	in, err := c.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Questions come on stdout; stderr has the errors, and warnings (gvfs
	// logs some) that must not end up in a question.
	r, w := io.Pipe()
	c.cmd.Stdout, c.cmd.Stderr = w, &c.stderr
	c.stdin = in
	if err := c.cmd.Start(); err != nil {
		return nil, err
	}
	errc := make(chan error, 1)
	go func() {
		err := c.cmd.Wait() // returns once gio's output has been copied out
		w.Close()
		errc <- err
	}()
	go c.relay(r, errc)
	return c, nil
}

// relay reads gio's output and sends each question it stops on. gio flushes
// a prompt ("Password: ") and then blocks reading stdin, so output ending in
// ": " is a question.
func (c *connection) relay(r io.Reader, errc <-chan error) {
	br := bufio.NewReader(r)
	var all, cur strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			all.Write(buf[:n])
			cur.Write(buf[:n])
			if strings.HasSuffix(cur.String(), ": ") {
				c.events <- connEvent{prompt: cur.String()}
				cur.Reset()
			}
		}
		if err != nil {
			break
		}
	}
	err := <-errc // Wait is done, so stderr is complete
	c.events <- connEvent{done: true, err: err, out: all.String() + "\n" + c.stderr.String()}
}

// wait delivers the connection's next event.
func (c *connection) wait() tea.Cmd {
	return func() tea.Msg { return connectMsg{c, <-c.events} }
}

func (c *connection) answer(s string) {
	_, _ = io.WriteString(c.stdin, s+"\n")
}

func (c *connection) cancel() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// gioPrompt is one question gio asked.
type gioPrompt struct {
	context []string // the lines before it ("Enter password for …")
	label   string   // "User", "Domain", "Password", "Choice"
	def     string   // a default shown in brackets
	choices []string // numbered answers for a question
}

var (
	promptLine = regexp.MustCompile(`^(.*?)(?: \[(.*)\])?: $`)
	choiceLine = regexp.MustCompile(`^\[(\d+)\] (.*)$`)
)

func parsePrompt(text string) gioPrompt {
	lines := strings.Split(text, "\n")
	var p gioPrompt
	last := lines[len(lines)-1]
	if mt := promptLine.FindStringSubmatch(last); mt != nil {
		p.label, p.def = mt[1], mt[2]
	} else {
		p.label = strings.TrimSuffix(last, ": ")
	}
	for _, l := range lines[:len(lines)-1] {
		l = strings.TrimSpace(l)
		switch mt := choiceLine.FindStringSubmatch(l); {
		case l == "" || l == "Authentication Required":
		case mt != nil:
			p.choices = append(p.choices, mt[2])
		default:
			p.context = append(p.context, l)
		}
	}
	return p
}

func (p gioPrompt) secret() bool {
	l := strings.ToLower(p.label)
	return strings.Contains(l, "password") || strings.Contains(l, "passphrase")
}

// connectError turns gio's last words into a sentence.
func connectError(uri, out string, err error) error {
	text := connectText(uri, out, err)
	scheme, _, _ := strings.Cut(uri, "://")
	if need := "gvfs-" + scheme; strings.Contains(text, "needs "+need) && requirements[need].what != "" {
		return missing(need, text)
	}
	return errors.New(text)
}

func connectText(uri, out string, err error) string {
	msg := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if i := strings.Index(l, "gio: "); i >= 0 { // may follow an unanswered "Password: "
			msg = l[i:]
		}
	}
	if msg == "" {
		if err != nil {
			return "couldn't connect: " + err.Error()
		}
		return "couldn't connect"
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "gio:"))
	// "smb://host/share/: Failed to mount Windows share: Connection refused"
	if strings.HasPrefix(msg, strings.TrimSuffix(uri, "/")) {
		if _, rest, ok := strings.Cut(msg, ": "); ok {
			msg = rest
		}
	}
	scheme, _, _ := strings.Cut(uri, "://")
	if strings.Contains(msg, "not supported") || strings.Contains(msg, "not mountable") || strings.Contains(msg, "doesn’t implement") || strings.Contains(msg, "doesn't implement") {
		pkg := map[string]string{"smb": "gvfs-smb", "nfs": "gvfs-nfs", "afp": "gvfs-afp"}[scheme]
		if pkg != "" {
			return scheme + ":// needs " + pkg + " (gvfs-backends on Debian/Ubuntu)"
		}
	}
	return msg
}

// ---- the model side ------------------------------------------------------

// connect starts connecting to what was typed or picked.
func (m *model) connect(addr string, guest bool) tea.Cmd {
	uri, err := normalizeServer(addr)
	if err != nil {
		m.openConnect(addr, err.Error())
		return nil
	}
	for _, nm := range gvfsMounts() { // already connected: just open it
		if strings.TrimSuffix(nm.uri, "/") == strings.TrimSuffix(uri, "/") && nm.path != "" {
			m.rememberServer(uri)
			m.chdir(nm.path, "")
			return nil
		}
	}
	if m.conn != nil {
		m.conn.cancel()
	}
	c, err := startConnection(uri, guest)
	if err != nil {
		m.conn = nil
		if !m.offerInstall(err) {
			m.setMsg(err.Error(), true)
		}
		return nil
	}
	m.conn = c
	m.modal = &modal{kind: mInfo, purpose: "connecting", title: "connect", lines: []string{"Connecting to " + uri + " …"}}
	return c.wait()
}

// openConnect asks for a server address.
func (m *model) openConnect(initial, problem string) {
	label := "Server address: smb://host/share · sftp://user@host/path · nfs://host/export"
	if problem != "" {
		label = problem
	}
	m.openInput("connect", "connect to server", label, initial, nil)
}

func (m *model) cancelConnect() {
	if m.conn != nil {
		m.conn.cancel()
		m.conn = nil
		m.setMsg("connection cancelled", false)
	}
}

// onConnect handles a question or the end of a connection.
func (m *model) onConnect(msg connectMsg) tea.Cmd {
	c := msg.c
	if c != m.conn {
		if !msg.ev.done {
			c.cancel() // a stale question: nobody will answer it
		}
		return nil
	}
	if msg.ev.done {
		m.conn = nil
		if m.modal != nil && strings.HasPrefix(m.modal.purpose, "connect") {
			m.modal = nil
		}
		if msg.ev.err != nil {
			if err := connectError(c.uri, msg.ev.out, msg.ev.err); !m.offerInstall(err) {
				m.setMsg(serverName(c.uri)+": "+err.Error(), true)
			}
			return nil
		}
		m.rememberServer(c.uri)
		var save tea.Cmd
		if c.remember && c.password != "" {
			if entry, ok := keyringEntry(c.uri, c.user, c.domain); ok {
				name, pw := serverName(c.uri), c.password
				save = func() tea.Msg {
					if err := saveSecret(entry, pw); err != nil {
						return statusMsg{"connected to " + name + ", but the password wasn't saved: " + err.Error(), true}
					}
					return statusMsg{"connected to " + name + " · password saved in the keyring", false}
				}
			}
		}
		c.password = ""
		path := gioLocalPath(c.uri)
		if path == "" {
			m.setMsg("connected to "+serverName(c.uri)+", but gvfs-fuse isn't running, so it has no folder to open", true)
			return save
		}
		m.chdir(path, "")
		m.setMsg("connected to "+serverName(c.uri), false)
		return save
	}
	p := parsePrompt(msg.ev.prompt)
	c.asking = p.label
	title := "connect to " + serverName(c.uri)
	if len(p.choices) > 0 {
		items := make([]menuItem, len(p.choices))
		for i, ch := range p.choices {
			items[i] = menuItem{label: ch, value: strconv.Itoa(i + 1)}
		}
		m.modal = &modal{kind: mMenu, purpose: "connectq", title: title, lines: p.context, items: items}
		return nil
	}
	lines := append([]string(nil), p.context...)
	if p.secret() && c.secrets > 0 {
		lines = append([]string{"That didn't work. Try again:"}, lines...)
	}
	lines = append(lines, p.label+":")
	m.modal = &modal{kind: mInput, purpose: "connectp", title: title, label: strings.Join(lines, "\n"),
		in: newInput(p.def), secret: p.secret(), guest: !c.guest && guestSchemes[c.scheme()] && (p.label == "User" || p.secret())}
	return nil
}

// answerConnect sends an answer and waits for what comes next.
func (m *model) answerConnect(s string, secret bool) tea.Cmd {
	c := m.conn
	if c == nil {
		return nil
	}
	switch {
	case secret:
		c.secrets++
		c.password = s
	case c.asking == "User":
		c.user = s
	case c.asking == "Domain":
		c.domain = s
	}
	c.answer(s)
	m.modal = &modal{kind: mInfo, purpose: "connecting", title: "connect", lines: []string{"Connecting to " + c.uri + " …"}}
	return c.wait()
}

// disconnect unmounts a network location, after moving tabs out of it.
func (m *model) disconnect(nm netMount) tea.Cmd {
	m.leaveMount(nm.path)
	return func() tea.Msg {
		var out []byte
		var err error
		switch {
		case !nm.system:
			out, err = exec.Command("gio", "mount", "-u", nm.uri).CombinedOutput()
		case hasFuseMount(nm.path):
			out, err = fuseUnmount(nm.path)
		default:
			out, err = exec.Command("umount", nm.path).CombinedOutput()
		}
		if err != nil {
			text := strings.TrimSpace(string(out))
			if text == "" {
				text = err.Error()
			}
			if i := strings.LastIndex(text, ": "); i >= 0 && strings.HasPrefix(text, "gio:") {
				text = text[i+2:]
			}
			return placeMsg{text: "disconnect " + nm.name + ": " + text, err: true}
		}
		return placeMsg{text: "disconnected " + nm.name}
	}
}

func hasFuseMount(path string) bool {
	for _, l := range strings.Split(readMounts(), "\n") {
		if f := strings.Fields(l); len(f) >= 3 && f[1] == path && strings.HasPrefix(f[2], "fuse") {
			return true
		}
	}
	return false
}

func fuseUnmount(path string) ([]byte, error) {
	for _, bin := range []string{"fusermount3", "fusermount"} {
		if _, err := exec.LookPath(bin); err == nil {
			return exec.Command(bin, "-u", path).CombinedOutput()
		}
	}
	return exec.Command("umount", path).CombinedOutput()
}
