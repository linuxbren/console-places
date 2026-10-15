package main

// Tailscale: when tailscaled is running, Browse network… also lists the
// tailnet's online computers, from `tailscale status --json` (nothing is
// probed). Each is offered over SFTP and SMB at its MagicDNS name (its
// Tailscale address when MagicDNS is off); phones are left out, since they
// don't serve files. They're listed after the local network's computers,
// marked "tailscale".

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"sort"
	"strings"
)

type tsPeer struct {
	HostName     string
	DNSName      string
	OS           string
	Online       bool
	TailscaleIPs []string
}

type tsStatus struct {
	BackendState   string
	CurrentTailnet *struct{ MagicDNSEnabled bool }
	Peer           map[string]tsPeer
}

// browseTailscale lists the tailnet's computers (nil without tailscale).
var browseTailscale = func(ctx context.Context) []netHost {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil
	}
	return parseTailscale(out)
}

func parseTailscale(b []byte) []netHost {
	var st tsStatus
	if json.Unmarshal(b, &st) != nil || st.BackendState != "Running" {
		return nil
	}
	magic := st.CurrentTailnet == nil || st.CurrentTailnet.MagicDNSEnabled
	var out []netHost
	for _, p := range st.Peer {
		sys := strings.ToLower(p.OS)
		if !p.Online || sys == "ios" || sys == "android" || len(p.TailscaleIPs) == 0 {
			continue
		}
		addr := p.TailscaleIPs[0]
		for _, ip := range p.TailscaleIPs {
			if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
				addr = ip
				break
			}
		}
		host := addr
		if dns := strings.TrimSuffix(p.DNSName, "."); magic && dns != "" {
			host = dns
		}
		name := p.HostName
		if name == "" {
			name, _, _ = strings.Cut(host, ".")
		}
		schemes := []string{"sftp", "smb"}
		switch sys {
		case "windows":
			schemes = []string{"smb"}
		case "macos":
			schemes = []string{"smb", "sftp"}
		}
		h := netHost{name: name, addr: addr, via: "tailscale"}
		for _, s := range schemes {
			h.svcs = append(h.svcs, netService{scheme: s, uri: s + "://" + host + "/"})
		}
		out = append(out, h)
	}
	sort.Slice(out, func(a, b int) bool { return strings.ToLower(out[a].name) < strings.ToLower(out[b].name) })
	return out
}
