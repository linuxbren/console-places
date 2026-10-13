package main

// System mounts: an opt-in alternative to gvfs for power users. The kernel's
// own SMB (cifs) or NFS client mounts the share as root, at a folder like
// /mnt/photos: faster than gvfs-fuse, visible to every program and session,
// and the same mount an fstab line would make. It needs root, so the mount
// runs through pkexec (the desktop's password dialog, or a text prompt in
// this terminal when there is no agent) or sudo, with the terminal handed
// over while it asks. An SMB password goes to mount.cifs in a credentials
// file only the user can read, deleted as soon as the mount is done.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// rootMount is a system mount being set up, one question at a time.
type rootMount struct {
	uri      string
	fstype   string // cifs or nfs
	source   string // //host/share or host:/export
	port     string
	mp       string // where to mount it
	user     string
	domain   string
	password string
}

// sudoTool is how tidefiles becomes root: pkexec, else sudo.
var sudoTool = func() (string, error) {
	for _, t := range []string{"pkexec", "sudo"} {
		if p, err := exec.LookPath(t); err == nil {
			return p, nil
		}
	}
	return "", errors.New("a system mount needs pkexec (polkit) or sudo")
}

// hasMountHelper says whether the kernel client's mount helper is installed.
var hasMountHelper = func(fstype string) bool {
	for _, dir := range []string{"/usr/bin", "/usr/sbin", "/sbin", "/bin"} {
		if _, err := os.Stat(filepath.Join(dir, "mount."+fstype)); err == nil {
			return true
		}
	}
	return false
}

// newRootMount checks an address for a system mount.
func newRootMount(addr string) (*rootMount, error) {
	uri, err := normalizeServer(addr)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(uri)
	path := strings.Trim(u.Path, "/")
	rm := &rootMount{uri: uri, port: u.Port()}
	switch u.Scheme {
	case "smb":
		share, _, _ := strings.Cut(path, "/")
		if share == "" {
			return nil, errors.New("add the share: smb://" + u.Host + "/share")
		}
		rm.fstype, rm.source = "cifs", "//"+u.Hostname()+"/"+path
		if u.User != nil {
			rm.user = u.User.Username()
			if d, usr, ok := strings.Cut(rm.user, ";"); ok {
				rm.domain, rm.user = d, usr
			}
		}
	case "nfs":
		if path == "" {
			return nil, errors.New("add the export: nfs://" + u.Host + "/export")
		}
		rm.fstype, rm.source = "nfs", u.Hostname()+":/"+path
	default:
		return nil, errors.New("system mounts are for smb:// and nfs:// (use a normal connection for " + u.Scheme + ")")
	}
	if !hasMountHelper(rm.fstype) {
		pkg := map[string]string{"cifs": "cifs-utils", "nfs": "nfs-utils (nfs-common on Debian/Ubuntu)"}[rm.fstype]
		return nil, missing(rm.fstype, "a system "+strings.ToUpper(u.Scheme)+" mount needs "+pkg)
	}
	name := filepath.Base(path)
	if name == "" || name == "." || name == "/" {
		name = u.Hostname()
	}
	rm.mp = "/mnt/" + strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' {
			return '-'
		}
		return r
	}, name)
	return rm, nil
}

// checkMountpoint refuses folders that mounting over would hide or break.
func checkMountpoint(mp string) error {
	if !filepath.IsAbs(mp) {
		return errors.New("the folder must be a full path, like /mnt/photos")
	}
	mp = filepath.Clean(mp)
	if systemMount(mp) || mp == "/mnt" || mp == "/media" {
		return errors.New(mp + " is a system folder")
	}
	if es, err := os.ReadDir(mp); err == nil && len(es) > 0 {
		return errors.New(mp + " isn't empty: mounting there would hide its files")
	}
	return nil
}

// options are the mount options; the credentials file holds the secrets.
func (rm *rootMount) options(creds string) string {
	if rm.fstype == "nfs" {
		if rm.port != "" {
			return "port=" + rm.port
		}
		return "defaults"
	}
	opts := []string{fmt.Sprintf("uid=%d", os.Getuid()), fmt.Sprintf("gid=%d", os.Getgid()), "iocharset=utf8"}
	if rm.user == "" {
		opts = append(opts, "guest")
	} else {
		opts = append(opts, "credentials="+creds)
	}
	if rm.port != "" {
		opts = append(opts, "port="+rm.port)
	}
	return strings.Join(opts, ",")
}

// runPrivileged runs a command with the terminal handed over (a variable so
// tests can run it directly).
var runPrivileged = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	return tea.ExecProcess(c, done)
}

// privScript says what is about to happen, then runs the rest of its
// arguments as root through the tool in $2. Only the command's own errors go
// to the file in $3 (the screen is redrawn afterwards); the password prompt
// stays on the terminal.
const privScript = `printf '\ntidefiles: %s (as root)\n\n' "$1"
tool=$2 err=$3
shift 3
exec "$tool" /bin/sh -c '"$@" 2>"$0"' "$err" "$@"
`

// privileged builds the command that runs args as root.
func privileged(say, errFile string, args ...string) (*exec.Cmd, error) {
	tool, err := sudoTool()
	if err != nil {
		return nil, err
	}
	return exec.Command("sh", append([]string{"-c", privScript, "sh", say, tool, errFile}, args...)...), nil
}

type rootDoneMsg struct {
	rm   *rootMount // nil when unmounting
	mp   string
	err  error
	text string // what the mount said on failure
}

// mountAsRoot mounts the share; the result arrives as rootDoneMsg.
func (m *model) mountAsRoot(rm *rootMount) tea.Cmd {
	dir, err := os.MkdirTemp(os.Getenv("XDG_RUNTIME_DIR"), "tidefiles-mount-")
	if err != nil {
		dir, err = os.MkdirTemp("", "tidefiles-mount-")
	}
	if err != nil {
		m.setMsg("system mount: "+err.Error(), true)
		return nil
	}
	creds, errFile := filepath.Join(dir, "credentials"), filepath.Join(dir, "error")
	if rm.fstype == "cifs" && rm.user != "" {
		body := "username=" + rm.user + "\npassword=" + rm.password + "\n"
		if rm.domain != "" {
			body += "domain=" + rm.domain + "\n"
		}
		if err := os.WriteFile(creds, []byte(body), 0o600); err != nil {
			os.RemoveAll(dir)
			m.setMsg("system mount: "+err.Error(), true)
			return nil
		}
	}
	rm.password = ""
	_ = os.WriteFile(errFile, nil, 0o600)
	c, err := privileged("mounting "+rm.source+" at "+rm.mp, errFile,
		"sh", "-c", `mkdir -p -- "$1" && mount -t "$2" -o "$3" -- "$4" "$1"`, "sh", rm.mp, rm.fstype, rm.options(creds), rm.source)
	if err != nil {
		os.RemoveAll(dir)
		m.setMsg(err.Error(), true)
		return nil
	}
	return runPrivileged(c, func(err error) tea.Msg {
		b, _ := os.ReadFile(errFile)
		os.RemoveAll(dir) // the credentials are read at mount time only
		return rootDoneMsg{rm: rm, mp: rm.mp, err: err, text: strings.TrimSpace(string(b))}
	})
}

// unmountAsRoot unmounts a system mount.
func (m *model) unmountAsRoot(nm netMount) tea.Cmd {
	m.leaveMount(nm.path)
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("tidefiles-umount-%d", os.Getpid()))
	_ = os.WriteFile(errFile, nil, 0o600)
	c, err := privileged("unmounting "+nm.path, errFile, "umount", "--", nm.path)
	if err != nil {
		os.Remove(errFile)
		m.setMsg(err.Error(), true)
		return nil
	}
	return runPrivileged(c, func(err error) tea.Msg {
		b, _ := os.ReadFile(errFile)
		os.Remove(errFile)
		return rootDoneMsg{mp: nm.path, err: err, text: strings.TrimSpace(string(b))}
	})
}

func (m *model) onRootDone(msg rootDoneMsg) {
	if msg.err != nil {
		why := msg.text
		if why == "" {
			why = msg.err.Error()
		}
		var ee *exec.ExitError
		if errors.As(msg.err, &ee) && msg.text == "" && (ee.ExitCode() == 126 || ee.ExitCode() == 127) {
			why = "not authorized" // pkexec: dismissed, or no way to ask
		}
		verb := "unmount " + msg.mp
		if msg.rm != nil {
			verb = "system mount of " + msg.rm.source
		}
		m.setMsg(verb+": "+lastLine(why), true)
		return
	}
	if msg.rm == nil {
		m.setMsg("unmounted "+msg.mp, false)
		m.reload(m.curName())
		return
	}
	m.chdir(msg.mp, "")
	m.setMsg("mounted "+msg.rm.source+" at "+msg.mp+" (system mount · u in places unmounts)", false)
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}

// ---- the questions ---------------------------------------------------------

// startRootMount begins a system mount of what was typed in Connect.
func (m *model) startRootMount(addr string) {
	rm, err := newRootMount(addr)
	if m.offerInstall(err) {
		return
	}
	if err != nil {
		m.openInput("rootaddr", "system mount (root)", err.Error(), addr, nil)
		return
	}
	m.rm = rm
	m.openInput("rootmp", "system mount (root)", "Mount "+rm.source+" at this folder (created if missing):", rm.mp, nil)
}

// rootAnswer takes the answer to one question and asks the next.
func (m *model) rootAnswer(purpose, val string) tea.Cmd {
	rm := m.rm
	if rm == nil {
		return nil
	}
	title := "system mount (root)"
	switch purpose {
	case "rootmp":
		val = filepath.Clean(strings.TrimSpace(val))
		if err := checkMountpoint(val); err != nil {
			m.openInput("rootmp", title, err.Error(), val, nil)
			return nil
		}
		rm.mp = val
		if rm.fstype == "cifs" {
			def := rm.user
			if def == "" {
				def = os.Getenv("USER")
			}
			m.openInput("rootuser", title, "User on "+rm.source+" (DOMAIN\\user works; empty for guest):", def, nil)
			return nil
		}
	case "rootuser":
		rm.user = strings.TrimSpace(val)
		if d, u, ok := strings.Cut(rm.user, `\`); ok {
			rm.domain, rm.user = d, u
		}
		if rm.user != "" {
			m.modal = &modal{kind: mInput, purpose: "rootpass", title: title, label: "Password for " + rm.user + " on " + rm.source + ":", secret: true}
			return nil
		}
	case "rootpass":
		rm.password = val
	}
	m.rm = nil
	return m.mountAsRoot(rm)
}
