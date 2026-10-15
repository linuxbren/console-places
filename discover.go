package main

// Browsing the network for computers that share files, the way Nautilus'
// "Network" view does, without needing gvfs' optional discovery backends:
//
//   - mDNS / DNS-SD (avahi-browse): Macs, Linux machines and NAS boxes that
//     announce SMB, SFTP, NFS, AFP, WebDAV or FTP.
//   - WS-Discovery: Windows 10/11 (and wsdd on Samba servers) answer a probe.
//   - NetBIOS (nmblookup): older Windows machines and NAS boxes.
//   - Tailscale (tailscale.go): the tailnet's computers, when it's running.
//
// WS-Discovery and NetBIOS replies come back to a multicast or broadcast
// question, which a firewall that drops unsolicited incoming traffic (ufw's
// default) discards; mDNS passes. Picking a computer lists its SMB shares
// (smbclient, as a guest) or connects straight away.

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const discoverTime = 3 * time.Second

// netHost is a computer found on the network and how to reach its files.
type netHost struct {
	name string // as announced, or the address
	addr string
	via  string // how it was found: mdns, wsd, netbios, tailscale
	svcs []netService
}

type netService struct {
	scheme string // smb, sftp, nfs, afp, dav, davs, ftp
	uri    string // where to connect; for smb, the server (shares are listed)
}

// mdnsTypes maps DNS-SD service types to gvfs schemes.
var mdnsTypes = map[string]string{
	"_smb._tcp": "smb", "_sftp-ssh._tcp": "sftp", "_ssh._tcp": "sftp", "_nfs._tcp": "nfs",
	"_afpovertcp._tcp": "afp", "_webdav._tcp": "dav", "_webdavs._tcp": "davs", "_ftp._tcp": "ftp",
}

var avahiEscape = regexp.MustCompile(`\\(\d{3})`)

// unescapeAvahi turns avahi-browse's \032-style escapes back into text.
func unescapeAvahi(s string) string {
	return avahiEscape.ReplaceAllStringFunc(s, func(e string) string {
		n, _ := strconv.Atoi(e[1:])
		return string(rune(n))
	})
}

// browseMDNS asks avahi for every file-sharing service type at once.
var browseMDNS = func(ctx context.Context) []netHost {
	if _, err := exec.LookPath("avahi-browse"); err != nil {
		return nil
	}
	var mu sync.Mutex
	var out strings.Builder
	var wg sync.WaitGroup
	for typ := range mdnsTypes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := exec.CommandContext(ctx, "avahi-browse", "-p", "-r", "-t", "-k", typ).Output()
			mu.Lock()
			out.Write(b)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return parseAvahi(out.String())
}

// parseAvahi reads avahi-browse -p -r output:
// =;iface;IPv4;name;_smb._tcp;local;host.local;192.168.1.5;445;"txt"…
func parseAvahi(text string) []netHost {
	hosts := map[string]*netHost{}
	var order []string
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ";")
		if len(f) < 9 || f[0] != "=" || f[2] != "IPv4" {
			continue
		}
		scheme := mdnsTypes[f[4]]
		if scheme == "" {
			continue
		}
		name, host, addr, port := unescapeAvahi(f[3]), strings.TrimSuffix(f[6], "."), f[7], f[8]
		h := hosts[host]
		if h == nil {
			h = &netHost{name: name, addr: addr, via: "mdns"}
			hosts[host] = h
			order = append(order, host)
		}
		target := host
		if def := map[string]string{"smb": "445", "sftp": "22", "nfs": "2049", "afp": "548", "dav": "80", "davs": "443", "ftp": "21"}[scheme]; port != def {
			target += ":" + port
		}
		path := "/"
		if len(f) > 9 {
			for _, kv := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(f[9], -1) {
				if p, ok := strings.CutPrefix(kv[1], "path="); ok && strings.HasPrefix(p, "/") {
					path = p
				}
			}
		}
		svc := netService{scheme: scheme, uri: scheme + "://" + target + path}
		if scheme == "smb" {
			h.name = name // the SMB name is the one people know
		}
		if !hasScheme(h.svcs, scheme) {
			h.svcs = append(h.svcs, svc)
		}
	}
	res := make([]netHost, 0, len(order))
	for _, k := range order {
		res = append(res, *hosts[k])
	}
	return res
}

func hasScheme(svcs []netService, scheme string) bool {
	for _, s := range svcs {
		if s.scheme == scheme {
			return true
		}
	}
	return false
}

// browseWSD sends a WS-Discovery probe and collects who answers.
var browseWSD = func(ctx context.Context) []netHost {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil
	}
	defer conn.Close()
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	probe := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:wsd="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:wsdp="http://schemas.xmlsoap.org/ws/2006/02/devprof"><soap:Header><wsa:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</wsa:To><wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</wsa:Action><wsa:MessageID>urn:uuid:%x-%x-%x-%x-%x</wsa:MessageID></soap:Header><soap:Body><wsd:Probe><wsd:Types>wsdp:Device</wsd:Types></wsd:Probe></soap:Body></soap:Envelope>`,
		id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}
	if _, err := conn.WriteToUDP([]byte(probe), dst); err != nil {
		return nil
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetReadDeadline(deadline)
	seen := map[string]bool{}
	var res []netHost
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		// Computers (not printers or media players) say "pub:Computer".
		if ip := from.IP.String(); !seen[ip] && strings.Contains(string(buf[:n]), "Computer") {
			seen[ip] = true
			res = append(res, netHost{name: ip, addr: ip, via: "wsd", svcs: []netService{{"smb", "smb://" + ip + "/"}}})
		}
	}
	return res
}

var nbHit = regexp.MustCompile(`^(\d+\.\d+\.\d+\.\d+) \*<00>`)

// browseNetBIOS asks for every NetBIOS name on the local network.
var browseNetBIOS = func(ctx context.Context) []netHost {
	if _, err := exec.LookPath("nmblookup"); err != nil {
		return nil
	}
	b, _ := exec.CommandContext(ctx, "nmblookup", "*").Output()
	var res []netHost
	for _, l := range strings.Split(string(b), "\n") {
		if mt := nbHit.FindStringSubmatch(l); mt != nil {
			res = append(res, netHost{name: mt[1], addr: mt[1], via: "netbios", svcs: []netService{{"smb", "smb://" + mt[1] + "/"}}})
		}
	}
	return res
}

// netbiosName asks a machine for its own name (a unicast question, which
// firewalls let the answer to through).
var netbiosName = func(ctx context.Context, ip string) string {
	if _, err := exec.LookPath("nmblookup"); err != nil {
		return ""
	}
	b, _ := exec.CommandContext(ctx, "nmblookup", "-A", ip).Output()
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[1] == "<00>" && !strings.Contains(l, "<GROUP>") {
			return f[0]
		}
	}
	return ""
}

type discoverMsg struct{ hosts []netHost }

// discover runs every kind of browsing at once and merges what was found.
func discover() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), discoverTime)
		defer cancel()
		var mdns, wsd, nb, ts []netHost
		var wg sync.WaitGroup
		wg.Add(4)
		go func() { defer wg.Done(); mdns = browseMDNS(ctx) }()
		go func() { defer wg.Done(); wsd = browseWSD(ctx) }()
		go func() { defer wg.Done(); nb = browseNetBIOS(ctx) }()
		go func() { defer wg.Done(); ts = browseTailscale(ctx) }()
		wg.Wait()
		return discoverMsg{append(mergeHosts(mdns, append(wsd, nb...)), ts...)}
	}
}

// mergeHosts adds the address-only finds (WS-Discovery, NetBIOS) to the
// mDNS ones, naming them where possible, and sorts by name.
func mergeHosts(named, bare []netHost) []netHost {
	byAddr := map[string]int{}
	out := append([]netHost(nil), named...)
	for i, h := range out {
		byAddr[h.addr] = i
	}
	var fresh []int
	for _, h := range bare {
		if i, ok := byAddr[h.addr]; ok {
			if !hasScheme(out[i].svcs, "smb") {
				out[i].svcs = append(out[i].svcs, h.svcs...)
			}
			continue
		}
		byAddr[h.addr] = len(out)
		fresh = append(fresh, len(out))
		out = append(out, h)
	}
	if len(fresh) > 0 { // name the machines found by address
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for _, i := range fresh {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if n := netbiosName(ctx, out[i].addr); n != "" {
					out[i].name = n
				}
			}()
		}
		wg.Wait()
	}
	sort.SliceStable(out, func(a, b int) bool { return strings.ToLower(out[a].name) < strings.ToLower(out[b].name) })
	return out
}

// ---- SMB shares ------------------------------------------------------------

type sharesMsg struct {
	server string // smb://host/
	shares []string
	err    string
	need   string // a requirement that's missing
}

// listShares asks an SMB server for its shares as a guest.
var listShares = func(server string) tea.Cmd {
	return func() tea.Msg {
		host := strings.TrimSuffix(strings.TrimPrefix(server, "smb://"), "/")
		if _, err := exec.LookPath("smbclient"); err != nil {
			return sharesMsg{server: server, err: "smbclient isn't installed", need: "smbclient"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		args := []string{"-g", "-N", "-L", "//" + host}
		if h, port, err := net.SplitHostPort(host); err == nil {
			args = []string{"-g", "-N", "-p", port, "-L", "//" + h}
		}
		b, err := exec.CommandContext(ctx, "smbclient", args...).CombinedOutput()
		shares := parseShares(string(b))
		if len(shares) == 0 {
			why := "no shares are visible to a guest"
			if err != nil {
				for _, l := range strings.Split(string(b), "\n") {
					if strings.Contains(l, "NT_STATUS_") {
						why = strings.TrimSpace(l)
					}
				}
			}
			return sharesMsg{server: server, err: why}
		}
		return sharesMsg{server: server, shares: shares}
	}
}

// parseShares reads smbclient -g output ("Disk|photos|Family photos"),
// leaving out admin shares (ending in $).
func parseShares(text string) []string {
	var res []string
	for _, l := range strings.Split(text, "\n") {
		f := strings.Split(l, "|")
		if len(f) >= 2 && f[0] == "Disk" && !strings.HasSuffix(f[1], "$") {
			res = append(res, f[1])
		}
	}
	return res
}

// ---- the model side ------------------------------------------------------

// openBrowse starts looking for computers.
func (m *model) openBrowse() tea.Cmd {
	m.modal = &modal{kind: mInfo, purpose: "browsing", title: "network",
		lines: []string{"Looking for computers that share files…", "", "mDNS, WS-Discovery and NetBIOS (a firewall here", "can hide Windows PCs)"}}
	return discover()
}

func (m *model) onDiscover(msg discoverMsg) {
	if m.modal == nil || m.modal.purpose != "browsing" {
		return // closed meanwhile
	}
	var offers []string
	if discoveryBlocked(localLAN()) {
		offers = append(offers, "Your firewall hides Windows PCs: f explains (and can fix it).")
	}
	if len(msg.hosts) > 0 {
		offers = append(offers, "Not listed? c connects by address.")
	}
	if _, err := exec.LookPath("avahi-browse"); err != nil {
		offers = append(offers, "avahi isn't installed (it finds Macs, NAS): i installs it.")
	}
	if len(msg.hosts) == 0 {
		lines := []string{
			"No computers announced shared files.",
			"",
			"Windows PCs answer only with network discovery on. You can",
			"always connect by address: c, then smb://pc-name/share.",
		}
		if len(offers) > 0 {
			lines = append(append(lines, ""), offers...)
		}
		m.modal = &modal{kind: mInfo, purpose: "browsed", title: "network", lines: lines}
		return
	}
	var items []menuItem
	for _, h := range msg.hosts {
		for _, s := range h.svcs {
			label := h.name
			if h.name != h.addr {
				label += "  " + h.addr
			}
			suffix := s.scheme
			if h.via == "tailscale" {
				suffix += " · tailscale"
			}
			items = append(items, menuItem{label: label, suffix: suffix, value: s.uri})
		}
	}
	m.modal = &modal{kind: mMenu, purpose: "browse", title: "network · " + plural(len(msg.hosts), "computer"), items: items, lines: offers}
}

// pickService connects to a service picked while browsing; for SMB it lists
// the server's shares first.
func (m *model) pickService(uri string) tea.Cmd {
	if strings.HasPrefix(uri, "smb://") && strings.Count(strings.TrimPrefix(uri, "smb://"), "/") <= 1 && strings.HasSuffix(uri, "/") {
		m.modal = &modal{kind: mInfo, purpose: "browsing", title: "network", lines: []string{"Asking " + strings.TrimSuffix(strings.TrimPrefix(uri, "smb://"), "/") + " for its shares…"}}
		return listShares(uri)
	}
	return m.connect(uri, false)
}

// browseOffer handles f (firewall) and i (install avahi) in browse results.
func (m *model) browseOffer(key string) bool {
	switch key {
	case "c":
		m.openConnect("", "")
		return true
	case "f":
		if discoveryBlocked(localLAN()) {
			m.openFirewallAsk()
			return true
		}
	case "i":
		if _, err := exec.LookPath("avahi-browse"); err != nil {
			return m.offerInstall(missing("avahi", "avahi-browse isn't installed"))
		}
	}
	return false
}

func (m *model) onShares(msg sharesMsg) {
	if m.modal == nil || m.modal.purpose != "browsing" {
		return
	}
	if msg.need != "" && m.offerInstall(missing(msg.need, msg.err)) {
		return
	}
	if msg.err != "" {
		// Shares a guest can't list are still reachable by name.
		m.openConnect(msg.server, "Couldn't list the shares ("+msg.err+"). Type the share's name after "+msg.server)
		return
	}
	items := make([]menuItem, len(msg.shares))
	for i, s := range msg.shares {
		items[i] = menuItem{label: s, value: msg.server + s}
	}
	m.modal = &modal{kind: mMenu, purpose: "browse", title: "shares on " + strings.TrimSuffix(strings.TrimPrefix(msg.server, "smb://"), "/"), items: items}
}
