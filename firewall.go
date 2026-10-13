package main

// Letting discovery answers through the firewall. Browsing asks the local
// network who shares files; Windows PCs answer WS-Discovery from UDP port
// 3702 and older machines answer NetBIOS from port 137. Those answers aren't
// replies the firewall recognises (the question went to a multicast or
// broadcast address), so ufw's default "deny incoming" drops them. mDNS
// passes (ufw allows it by default). tidefiles explains this and, if asked,
// adds two narrow ufw rules: UDP from the local network, from those source
// ports only. Nothing changes without a yes.

import (
	"fmt"
	"net"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// discoveryPorts are the source ports discovery answers come from.
var discoveryPorts = []string{"3702", "137"}

var (
	ufwConf  = "/etc/ufw/ufw.conf"
	ufwRules = "/etc/ufw/user.rules"
	netRoute = "/proc/net/route"
)

// localLAN is the IPv4 network of the default route's interface, e.g.
// 192.168.1.0/24 ("" if there is none, or it isn't a private network).
var localLAN = func() string {
	b, err := os.ReadFile(netRoute)
	if err != nil {
		return ""
	}
	iface := ""
	for _, l := range strings.Split(string(b), "\n")[1:] {
		if f := strings.Fields(l); len(f) > 2 && f[1] == "00000000" {
			iface = f[0]
			break
		}
	}
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
			_, lan, _ := net.ParseCIDR(n.String())
			return lan.String()
		}
	}
	return ""
}

// discoveryBlocked says whether ufw is on without rules letting discovery
// answers in from lan.
func discoveryBlocked(lan string) bool {
	conf, err := os.ReadFile(ufwConf)
	if err != nil || lan == "" || !strings.Contains(string(conf), "ENABLED=yes") {
		return false
	}
	rules, err := os.ReadFile(ufwRules)
	if err != nil {
		return true // can't tell: offer it; ufw skips a rule it already has
	}
	allowed := map[string]bool{}
	for _, l := range strings.Split(string(rules), "\n") {
		t, ok := strings.CutPrefix(l, "### tuple ### ")
		if !ok {
			continue
		}
		// allow udp <dport> <dst> <sport> <src> in
		f := strings.Fields(t)
		if len(f) >= 7 && f[0] == "allow" && (f[1] == "udp" || f[1] == "any") && f[6] == "in" &&
			(f[5] == "0.0.0.0/0" || f[5] == lan) && (f[2] == "any" || f[2] == f[4]) {
			allowed[f[4]] = true
			if f[4] == "any" && f[2] == "any" {
				return false // everything from there is allowed
			}
		}
	}
	for _, p := range discoveryPorts {
		if !allowed[p] {
			return true
		}
	}
	return false
}

// ufwArgs are the rules to add, one ufw command each.
func ufwArgs(lan string) [][]string {
	var out [][]string
	for _, p := range discoveryPorts {
		out = append(out, []string{"ufw", "allow", "proto", "udp", "from", lan, "port", p, "comment", "tidefiles discovery"})
	}
	return out
}

// openFirewallAsk explains the firewall and asks before changing it.
func (m *model) openFirewallAsk() {
	lan := localLAN()
	lines := []string{
		"Your firewall (ufw) is dropping the answers computers send",
		"when tidefiles asks who shares files: Windows PCs answer",
		"from UDP port 3702 (WS-Discovery), older ones from 137",
		"(NetBIOS). Macs, Linux and NAS boxes (mDNS) already get",
		"through, and connecting by address always works.",
		"",
		"Allow those answers from your network " + lan + "?",
		"This runs, as root:",
	}
	for _, a := range ufwArgs(lan) {
		lines = append(lines, "  "+strings.Join(a[:8], " "))
	}
	lines = append(lines, "", "Only UDP from your own network, from those two ports",
		"(labelled \"tidefiles discovery\"). To undo later, list them",
		"with  sudo ufw status numbered  and remove each with",
		"sudo ufw delete <number>.")
	m.openConfirm("allowfw", "firewall", lines, []string{lan})
}

type firewallMsg struct {
	err  error
	text string
}

// allowDiscovery adds the rules (through pkexec or sudo, terminal handed
// over) and browses again.
func (m *model) allowDiscovery(lan string) tea.Cmd {
	errFile := fmt.Sprintf("%s/tidefiles-ufw-%d", os.TempDir(), os.Getpid())
	_ = os.WriteFile(errFile, nil, 0o600)
	var script []string
	for _, a := range ufwArgs(lan) {
		q := make([]string, len(a))
		for i, s := range a {
			q[i] = "'" + s + "'"
		}
		script = append(script, strings.Join(q, " "))
	}
	c, err := privileged("letting discovery answers from "+lan+" through ufw", errFile, "sh", "-c", strings.Join(script, " && "))
	if err != nil {
		os.Remove(errFile)
		m.setMsg(err.Error(), true)
		return nil
	}
	return runPrivileged(c, func(err error) tea.Msg {
		b, _ := os.ReadFile(errFile)
		os.Remove(errFile)
		return firewallMsg{err: err, text: strings.TrimSpace(string(b))}
	})
}

func (m *model) onFirewall(msg firewallMsg) tea.Cmd {
	if msg.err != nil {
		why := lastLine(msg.text)
		if why == "" {
			why = "not authorized"
		}
		m.setMsg("firewall unchanged: "+why, true)
		return nil
	}
	m.setMsg("firewall: discovery answers allowed from your network", false)
	return m.openBrowse()
}
