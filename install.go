package main

// Offering to install what a feature needs. Mounting, connecting and
// browsing use system tools that a static binary can't carry (kernel mount
// helpers, gvfs, udisks…); the packages list them as optional dependencies.
// When one is missing, tidefiles says what it is for and offers to install
// it with the system's package manager, as root, after a yes. The package
// manager runs in this terminal, so it shows what it will do and asks too.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// requirement is something a feature needs, in each distro's package names.
type requirement struct {
	what  string            // the feature, for the message
	pkgs  map[string]string // package manager -> packages
	after string            // what to do once installed
}

var requirements = map[string]requirement{
	"nfs": {what: "System NFS mounts use the kernel's NFS client, whose mount helper (mount.nfs) isn't installed.",
		pkgs: map[string]string{"pacman": "nfs-utils", "apt-get": "nfs-common", "dnf": "nfs-utils", "zypper": "nfs-client", "apk": "nfs-utils"}},
	"cifs": {what: "System SMB mounts use the kernel's SMB client, whose mount helper (mount.cifs) isn't installed.",
		pkgs: map[string]string{"pacman": "cifs-utils", "apt-get": "cifs-utils", "dnf": "cifs-utils", "zypper": "cifs-utils", "apk": "cifs-utils"}},
	"gvfs": {what: "Connecting to servers goes through gvfs (as in Nautilus), which isn't installed.",
		pkgs:  map[string]string{"pacman": "gvfs gvfs-smb gvfs-nfs", "apt-get": "gvfs gvfs-fuse gvfs-backends", "dnf": "gvfs gvfs-fuse gvfs-smb gvfs-nfs", "zypper": "gvfs gvfs-fuse gvfs-backends", "apk": "gvfs gvfs-fuse gvfs-smb gvfs-nfs"},
		after: "if connecting still fails, log out and back in so gvfs starts"},
	"gvfs-smb": {what: "Windows, Mac and Samba shares (smb://) need gvfs' SMB support, which isn't installed.",
		pkgs:  map[string]string{"pacman": "gvfs-smb", "apt-get": "gvfs-backends", "dnf": "gvfs-smb", "zypper": "gvfs-backends", "apk": "gvfs-smb"},
		after: "if it still fails, log out and back in so gvfs picks it up"},
	"gvfs-nfs": {what: "NFS shares (nfs://) need gvfs' NFS support, which isn't installed.",
		pkgs:  map[string]string{"pacman": "gvfs-nfs", "apt-get": "gvfs-backends", "dnf": "gvfs-nfs", "zypper": "gvfs-backends", "apk": "gvfs-nfs"},
		after: "if it still fails, log out and back in so gvfs picks it up"},
	"udisks": {what: "Mounting drives without root goes through udisks (udisksctl), which isn't installed.",
		pkgs: map[string]string{"pacman": "udisks2", "apt-get": "udisks2", "dnf": "udisks2", "zypper": "udisks2", "apk": "udisks2"}},
	"smbclient": {what: "Listing a server's shares uses smbclient, which isn't installed.",
		pkgs: map[string]string{"pacman": "smbclient", "apt-get": "smbclient", "dnf": "samba-client", "zypper": "samba-client", "apk": "samba-client"}},
	"avahi": {what: "Finding Macs, Linux machines and NAS boxes uses avahi (mDNS), whose avahi-browse isn't installed.",
		pkgs:  map[string]string{"pacman": "avahi", "apt-get": "avahi-utils", "dnf": "avahi-tools", "zypper": "avahi-utils", "apk": "avahi-tools"},
		after: "the avahi-daemon service must be running too"},
}

// missingError is an error caused by something that can be installed.
type missingError struct {
	need string // a key of requirements
	msg  string
}

func (e *missingError) Error() string { return e.msg }

func missing(need, msg string) error { return &missingError{need, msg} }

// packageManager is the system's package manager, "" if none is known.
var packageManager = func() string {
	for _, pm := range []string{"pacman", "apt-get", "dnf", "zypper", "apk"} {
		if _, err := exec.LookPath(pm); err == nil {
			return pm
		}
	}
	return ""
}

func installArgs(pm, pkgs string) []string {
	cmd := map[string][]string{
		"pacman": {"pacman", "-S", "--needed"}, "apt-get": {"apt-get", "install"},
		"dnf": {"dnf", "install"}, "zypper": {"zypper", "install"}, "apk": {"apk", "add"},
	}[pm]
	return append(append([]string(nil), cmd...), strings.Fields(pkgs)...)
}

// offerInstall explains what is missing and asks to install it; without a
// known package manager it just says what to install.
func (m *model) offerInstall(err error) bool {
	var me *missingError
	if !errors.As(err, &me) {
		return false
	}
	req, ok := requirements[me.need]
	if !ok {
		return false
	}
	pm := packageManager()
	if pm == "" {
		m.setMsg(me.msg, true)
		return true
	}
	lines := []string{}
	lines = append(lines, wrapWords(req.what, 58)...)
	lines = append(lines, "", "Install it now? This runs, as root:", "  "+strings.Join(installArgs(pm, req.pkgs[pm]), " "),
		"", "The package manager shows what it will do and asks first.")
	m.openConfirm("install", "install "+req.pkgs[pm], lines, []string{me.need})
	return true
}

type installMsg struct {
	need string
	err  error
	text string
}

// install runs the package manager as root with the terminal handed over.
func (m *model) install(need string) tea.Cmd {
	req, pm := requirements[need], packageManager()
	errFile := fmt.Sprintf("%s/tidefiles-install-%d", os.TempDir(), os.Getpid())
	_ = os.WriteFile(errFile, nil, 0o600)
	c, err := privileged("installing "+req.pkgs[pm], errFile, installArgs(pm, req.pkgs[pm])...)
	if err != nil {
		os.Remove(errFile)
		m.setMsg(err.Error(), true)
		return nil
	}
	return runPrivileged(c, func(err error) tea.Msg {
		b, _ := os.ReadFile(errFile)
		os.Remove(errFile)
		return installMsg{need: need, err: err, text: strings.TrimSpace(string(b))}
	})
}

func (m *model) onInstall(msg installMsg) {
	req, pm := requirements[msg.need], packageManager()
	if msg.err != nil {
		why := lastLine(msg.text)
		if why == "" {
			why = "cancelled or not authorized"
		}
		m.setMsg("didn't install "+req.pkgs[pm]+": "+why, true)
		return
	}
	text := "installed " + req.pkgs[pm] + " · try again"
	if req.after != "" {
		text += " (" + req.after + ")"
	}
	m.setMsg(text, false)
}

// wrapWords breaks text into lines of at most width runes.
func wrapWords(text string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && len([]rune(line))+1+len([]rune(w)) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	return append(lines, line)
}
