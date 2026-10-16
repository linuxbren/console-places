package main

// Letting discovery answers through the firewall. Browsing asks the local
// network who shares files; Windows PCs answer WS-Discovery from UDP port
// 3702 and older machines answer NetBIOS from port 137. Those answers aren't
// replies the firewall recognises (the question went to a multicast or
// broadcast address), so ufw's default "deny incoming" drops them. mDNS
// passes (ufw allows it by default). console-places explains this and, if asked,
// adds two narrow ufw rules: UDP from the local network, from those source
// ports only. Nothing changes without a yes. The same place (browse
// results, f) offers to remove the rules again once they are there, finding
// them by their comment so a rule the user added themselves is never touched.

import (
	"encoding/hex"
	"net"
	"os"
	"slices"
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

// fwComment labels the rules console-places adds; ufw stores it hex-encoded.
// Rules added as tidefiles carry fwOldComment.
const (
	fwComment    = "console-places discovery"
	fwOldComment = "tidefiles discovery"
)

// fwRule is one discovery rule console-places added.
type fwRule struct{ src, port string }

// ourRules lists the discovery rules console-places added, on any network.
func ourRules() []fwRule {
	rules, err := os.ReadFile(ufwRules)
	if err != nil {
		return nil
	}
	tags := []string{"comment=" + hex.EncodeToString([]byte(fwComment)), "comment=" + hex.EncodeToString([]byte(fwOldComment))}
	var out []fwRule
	for _, l := range strings.Split(string(rules), "\n") {
		t, ok := strings.CutPrefix(l, "### tuple ### ")
		if !ok {
			continue
		}
		// allow udp any 0.0.0.0/0 <sport> <src> in comment=…
		f := strings.Fields(t)
		if len(f) >= 8 && f[0] == "allow" && f[1] == "udp" && f[2] == "any" && f[6] == "in" && slices.Contains(tags, f[7]) &&
			slices.Contains(discoveryPorts, f[4]) {
			out = append(out, fwRule{src: f[5], port: f[4]})
		}
	}
	return out
}

// ufwDeleteArgs remove rules, one ufw command each.
func ufwDeleteArgs(rs []fwRule) [][]string {
	var out [][]string
	for _, r := range rs {
		out = append(out, []string{"ufw", "delete", "allow", "proto", "udp", "from", r.src, "port", r.port})
	}
	return out
}

// ufwArgs are the rules to add, one ufw command each.
func ufwArgs(lan string) [][]string {
	var out [][]string
	for _, p := range discoveryPorts {
		out = append(out, []string{"ufw", "allow", "proto", "udp", "from", lan, "port", p, "comment", fwComment})
	}
	return out
}

// openFirewallAsk explains the firewall and asks before changing it.
func (m *model) openFirewallAsk() {
	lan := localLAN()
	lines := []string{
		"Your firewall (ufw) is dropping the answers computers send",
		"when console-places asks who shares files: Windows PCs answer",
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
	lines = append(lines, "", "Only UDP from your own network, from those two ports,",
		"labelled \""+fwComment+"\". To undo it later:",
		"Browse network again, then f removes them.")
	m.openConfirm("allowfw", "firewall", lines, []string{lan})
}

// openFirewallRemove asks before removing the rules console-places added.
func (m *model) openFirewallRemove() {
	lines := []string{
		"console-places added firewall (ufw) rules that let Windows PCs",
		"answer when you browse the network. Remove them?",
		"",
		"Browsing will still find Macs, Linux and NAS boxes, and",
		"connecting by address always works.",
		"",
		"This runs, as root:",
	}
	for _, a := range ufwDeleteArgs(ourRules()) {
		lines = append(lines, "  "+strings.Join(a, " "))
	}
	m.openConfirm("removefw", "firewall", lines, nil)
}

type firewallMsg struct {
	err     error
	text    string
	removed bool
}

// allowDiscovery adds the rules and browses again.
func (m *model) allowDiscovery(lan string) tea.Cmd {
	return m.runUfw("letting discovery answers from "+lan+" through ufw", ufwArgs(lan), false)
}

// removeDiscovery removes the rules console-places added.
func (m *model) removeDiscovery() tea.Cmd {
	rs := ourRules()
	if len(rs) == 0 {
		m.setMsg("firewall: no rules from console-places to remove", false)
		return nil
	}
	return m.runUfw("removing the discovery rules console-places added to ufw", ufwDeleteArgs(rs), true)
}

// runUfw runs ufw commands as root (pkexec or sudo, terminal handed over).
func (m *model) runUfw(why string, cmds [][]string, removed bool) tea.Cmd {
	errFile, done, err := privErrFile("ufw")
	if err != nil {
		m.setMsg("firewall: "+err.Error(), true)
		return nil
	}
	var script []string
	for _, a := range cmds {
		q := make([]string, len(a))
		for i, s := range a {
			q[i] = "'" + s + "'"
		}
		script = append(script, strings.Join(q, " "))
	}
	c, err := privileged(why, errFile, "sh", "-c", strings.Join(script, " && "))
	if err != nil {
		done()
		m.setMsg(err.Error(), true)
		return nil
	}
	return runPrivileged(c, func(err error) tea.Msg {
		b, _ := os.ReadFile(errFile)
		done()
		return firewallMsg{err: err, text: strings.TrimSpace(string(b)), removed: removed}
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
	if msg.removed {
		m.setMsg("firewall: removed the discovery rules console-places added", false)
		return nil
	}
	m.setMsg("firewall: discovery answers allowed from your network", false)
	return m.openBrowse()
}
