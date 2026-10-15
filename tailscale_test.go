package main

import (
	"context"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

const tsFixture = `{"BackendState":"Running","CurrentTailnet":{"MagicDNSEnabled":true},"Peer":{
"a":{"HostName":"studio","DNSName":"studio.tail1234.ts.net.","OS":"macOS","Online":true,"TailscaleIPs":["fd7a:115c:a1e0::1","100.64.0.2"]},
"b":{"HostName":"phone","DNSName":"phone.tail1234.ts.net.","OS":"iOS","Online":true,"TailscaleIPs":["100.64.0.3"]},
"c":{"HostName":"box","DNSName":"box.tail1234.ts.net.","OS":"linux","Online":false,"TailscaleIPs":["100.64.0.4"]},
"d":{"HostName":"Gaming-PC","DNSName":"gaming-pc.tail1234.ts.net.","OS":"windows","Online":true,"TailscaleIPs":["100.64.0.5"]}}}`

func TestParseTailscale(t *testing.T) {
	var got []string
	for _, h := range parseTailscale([]byte(tsFixture)) {
		for _, s := range h.svcs {
			got = append(got, h.name+" "+h.addr+" "+s.uri)
		}
	}
	want := "Gaming-PC 100.64.0.5 smb://gaming-pc.tail1234.ts.net/|" +
		"studio 100.64.0.2 smb://studio.tail1234.ts.net/|studio 100.64.0.2 sftp://studio.tail1234.ts.net/"
	if strings.Join(got, "|") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
	if parseTailscale([]byte(`{"BackendState":"Stopped"}`)) != nil {
		t.Fatal("tailscale down: nothing")
	}
	// MagicDNS off: the Tailscale address.
	off := strings.Replace(tsFixture, `"MagicDNSEnabled":true`, `"MagicDNSEnabled":false`, 1)
	if h := parseTailscale([]byte(off)); h[0].svcs[0].uri != "smb://100.64.0.5/" {
		t.Fatalf("MagicDNS off: %+v", h[0])
	}
}

func TestBrowseListsTailscale(t *testing.T) {
	orig := browseTailscale
	browseTailscale = func(context.Context) []netHost { return parseTailscale([]byte(tsFixture)) }
	t.Cleanup(func() { browseTailscale = orig })
	m := testModel(t, t.TempDir())
	m, _ = drive1(t, m, m.openBrowse())
	if m.modal == nil || m.modal.purpose != "browse" {
		t.Fatalf("browse: %+v", m.modal)
	}
	var rows []string
	for _, it := range m.modal.items {
		rows = append(rows, it.section+": "+it.label+" ["+it.suffix+"]")
	}
	if got := strings.Join(rows, "|"); !strings.Contains(got, "Tailscale: studio  100.64.0.2 [sftp]") || strings.Contains(got, "phone") {
		t.Fatalf("rows: %s", got)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, " Tailscale") {
		t.Fatalf("no Tailscale heading:\n%s", v)
	}
}
