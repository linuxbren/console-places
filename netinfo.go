package main

// What tidefiles shows about a connected network location: the preview of
// its row in the file list lists the computer, its address, the protocol,
// who you're signed in as and the space left (from gio and getent, off the
// UI goroutine, since a slow server or name lookup must not stall it), and
// a folder inside it is titled "computer: /path" instead of gvfs' own
// folder name (/run/user/1000/gvfs/sftp:host=…,user=…/path).

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"
)

const netInfoTimeout = 3 * time.Second

// protocolNames says what each kind of network location is, in plain words.
var protocolNames = map[string]string{
	"smb":  "Windows / Samba share (SMB)",
	"sftp": "SSH (SFTP)",
	"ssh":  "SSH (SFTP)",
	"nfs":  "NFS",
	"ftp":  "FTP",
	"ftps": "FTP over TLS",
	"dav":  "WebDAV",
	"davs": "WebDAV over TLS",
	"afp":  "Apple File Protocol (AFP)",
}

type netInfoMsg struct {
	key   string // the preview it belongs to
	lines []string
}

func netInfoCmd(key string, p place) tea.Cmd {
	return func() tea.Msg { return netInfoMsg{key: key, lines: netInfoLines(p)} }
}

// netInfoLines describes a connected location (it runs gio and getent).
func netInfoLines(p place) []string {
	u, _ := url.Parse(p.net.uri)
	var host, user, scheme string
	if u != nil {
		host, scheme = u.Hostname(), u.Scheme
		if u.User != nil {
			user = u.User.Username()
		}
	}
	if gv := gvfsParts(p.path); gv != nil {
		if user == "" {
			user = gv["user"]
		}
		if host == "" {
			host = gv["host"] + gv["server"]
		}
	}
	lines := []string{"  " + p.net.uri, ""}
	row := func(k, v string) { lines = append(lines, fmt.Sprintf("  %-10s %s", k, v)) }
	if host != "" {
		row("Computer", host)
		if addr := lookupAddr(host); addr != "" {
			row("Address", addr)
		}
	}
	if name := protocolNames[scheme]; name != "" {
		row("Protocol", name)
	} else if scheme != "" {
		row("Protocol", scheme)
	}
	if user != "" {
		row("Signed in", "as "+user)
	}
	if size, free, ok := remoteSpace(p); ok {
		row("Space", humanSize(free)+" free of "+humanSize(size))
	}
	if p.path != "" {
		row("Folder", tildePath(p.path))
	}
	return append(lines, "", "  enter opens it · d disconnects")
}

// lookupAddr is host's IP address ("" if host is one, or it can't be
// found). getent goes through the system's resolver, so name.local (mDNS)
// works too.
var lookupAddr = func(host string) string {
	if net.ParseIP(host) != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), netInfoTimeout)
	defer cancel()
	// IPv4 first (the address people recognise), else whatever there is.
	for _, db := range []string{"ahostsv4", "hosts"} {
		out, err := exec.CommandContext(ctx, "getent", db, host).Output()
		if err != nil {
			continue
		}
		var addrs []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.Fields(l)
			if len(f) == 0 || slices.Contains(addrs, f[0]) {
				continue
			}
			if ip := net.ParseIP(f[0]); ip != nil && !ip.IsLinkLocalUnicast() {
				addrs = append(addrs, f[0])
			}
		}
		if len(addrs) > 0 {
			return strings.Join(addrs[:min(2, len(addrs))], ", ")
		}
	}
	return ""
}

// remoteSpace is the size and free space of the server's filesystem.
var remoteSpace = func(p place) (size, free int64, ok bool) {
	if p.net.system && p.path != "" {
		var st unix.Statfs_t
		if unix.Statfs(p.path, &st) != nil || st.Blocks == 0 {
			return 0, 0, false
		}
		return int64(st.Blocks) * st.Bsize, int64(st.Bavail) * st.Bsize, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), netInfoTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gio", "info", "-f", "-a", "filesystem::size,filesystem::free", p.net.uri).Output()
	if err != nil {
		return 0, 0, false
	}
	for _, l := range strings.Split(string(out), "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(l), ": ")
		n, err := strconv.ParseInt(v, 10, 64)
		if !found || err != nil {
			continue
		}
		switch k {
		case "filesystem::size":
			size = n
		case "filesystem::free":
			free = n
		}
	}
	return size, free, size > 0
}

// gvfsRoot is where gvfs-fuse shows connected locations.
func gvfsRoot() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return filepath.Join(dir, "gvfs")
}

// gvfsParts splits a gvfs-fuse folder name ("sftp:host=pc,user=me" or
// "smb-share:server=nas,share=photos") into its fields, with "scheme";
// nil if path isn't inside one.
func gvfsParts(path string) map[string]string {
	rest, ok := strings.CutPrefix(path, gvfsRoot()+"/")
	if !ok {
		return nil
	}
	top, _, _ := strings.Cut(rest, "/")
	scheme, fields, ok := strings.Cut(top, ":")
	if !ok {
		return nil
	}
	out := map[string]string{"scheme": scheme}
	for _, f := range strings.Split(fields, ",") {
		if k, v, ok := strings.Cut(f, "="); ok {
			if uv, err := url.PathUnescape(v); err == nil {
				v = uv
			}
			out[k] = v
		}
	}
	return out
}

// displayPath is how a folder is titled: "~/…" at home, and "computer:
// /path" inside a network location gvfs connected.
func displayPath(path string) string {
	gv := gvfsParts(path)
	if gv == nil {
		return tildePath(path)
	}
	name := gv["host"] + gv["server"]
	if name == "" {
		return tildePath(path)
	}
	if s := gv["share"]; s != "" {
		name += "/" + s
	}
	rest := strings.TrimPrefix(path, gvfsRoot()+"/")
	_, sub, _ := strings.Cut(rest, "/")
	return name + ": /" + sub
}
