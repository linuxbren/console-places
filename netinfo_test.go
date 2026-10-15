package main

import (
	"strings"
	"testing"
)

func TestDisplayPath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	for in, want := range map[string]string{
		"/run/user/1000/gvfs/sftp:host=pc.local,user=me/Applications/Claude.app": "pc.local: /Applications/Claude.app",
		"/run/user/1000/gvfs/sftp:host=pc.local":                                 "pc.local: /",
		"/run/user/1000/gvfs/smb-share:server=nas,share=photos/2026":             "nas/photos: /2026",
		"/run/user/1000/gvfs": "/run/user/1000/gvfs",
		"/srv/data":           "/srv/data",
	} {
		if got := displayPath(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestNetInfoLines(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	origA, origS := lookupAddr, remoteSpace
	t.Cleanup(func() { lookupAddr, remoteSpace = origA, origS })
	lookupAddr = func(h string) string {
		if h == "pc.local" {
			return "192.168.1.42"
		}
		return ""
	}
	remoteSpace = func(place) (int64, int64, bool) { return 500 << 30, 120 << 30, true }
	p := place{name: "pc.local", kind: placeNet, path: "/run/user/1000/gvfs/sftp:host=pc.local,user=me",
		net: netMount{name: "pc.local", uri: "sftp://pc.local/", path: "/run/user/1000/gvfs/sftp:host=pc.local,user=me"}}
	got := strings.Join(netInfoLines(p), "\n")
	for _, s := range []string{"Computer   pc.local", "Address    192.168.1.42", "Protocol   SSH (SFTP)", "Signed in  as me", "Space      120.0 GiB free of 500.0 GiB", "d disconnects"} {
		if !strings.Contains(got, s) {
			t.Errorf("lacks %q:\n%s", s, got)
		}
	}
}
