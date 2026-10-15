package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/charmbracelet/x/ansi"
)

// verNode builds a VS_VERSIONINFO-style block.
func verNode(key string, value []byte, text bool, children ...[]byte) []byte {
	var b bytes.Buffer
	b.Write(make([]byte, 6))
	for _, u := range utf16.Encode([]rune(key)) {
		binary.Write(&b, binary.LittleEndian, u)
	}
	b.Write([]byte{0, 0})
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
	b.Write(value)
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
	for _, c := range children {
		b.Write(c)
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out, uint16(len(out)))
	vl := len(value)
	if text {
		vl /= 2
		binary.LittleEndian.PutUint16(out[4:], 1)
	}
	binary.LittleEndian.PutUint16(out[2:], uint16(vl))
	return out
}

func wz(s string) []byte {
	var b bytes.Buffer
	for _, u := range utf16.Encode([]rune(s)) {
		binary.Write(&b, binary.LittleEndian, u)
	}
	b.Write([]byte{0, 0})
	return b.Bytes()
}

func TestParseVersionInfo(t *testing.T) {
	fixed := make([]byte, 52)
	binary.LittleEndian.PutUint32(fixed, 0xfeef04bd)
	binary.LittleEndian.PutUint32(fixed[8:], 2<<16|5)
	binary.LittleEndian.PutUint32(fixed[12:], 1<<16|30)
	str := func(k, v string) []byte { return verNode(k, wz(v), true) }
	vi := verNode("VS_VERSION_INFO", fixed, false,
		verNode("StringFileInfo", nil, true,
			verNode("040904b0", nil, true, str("FileDescription", "Steam Client"), str("CompanyName", "Valve Corporation"), str("ProductVersion", "2.10.91.91"))),
		verNode("VarFileInfo", nil, true))
	info, fv := parseVersionInfo(vi)
	if fv != "2.5.1.30" || info["FileDescription"] != "Steam Client" || info["CompanyName"] != "Valve Corporation" || info["ProductVersion"] != "2.10.91.91" {
		t.Fatalf("%q %v", fv, info)
	}
	if info, _ := parseVersionInfo(vi[:20]); len(info) != 0 {
		t.Fatal("a cut block parsed")
	}

	// A resource section: type 16 → name 1 → language 0x409 → the data.
	var r bytes.Buffer
	dir := func(entries ...[2]uint32) {
		r.Write(make([]byte, 12))
		binary.Write(&r, binary.LittleEndian, uint16(0))
		binary.Write(&r, binary.LittleEndian, uint16(len(entries)))
		for _, e := range entries {
			binary.Write(&r, binary.LittleEndian, e[0])
			binary.Write(&r, binary.LittleEndian, e[1])
		}
	}
	const va = 0x5000
	dir([2]uint32{3, 0x80000000 | 32}, [2]uint32{16, 0x80000000 | 32}) // icons, then RT_VERSION; 32 bytes
	dir([2]uint32{1, 0x80000000 | 56})                                 // at 32
	dir([2]uint32{0x409, 80})                                          // at 56
	binary.Write(&r, binary.LittleEndian, uint32(va+96))               // data entry at 80: RVA
	binary.Write(&r, binary.LittleEndian, uint32(len(vi)))
	r.Write(make([]byte, 8))
	r.Write(vi) // at 96
	got, err := findResource(r.Bytes(), va, rtVersion)
	if err != nil || !bytes.Equal(got, vi) {
		t.Fatalf("findResource: %v", err)
	}
	if _, err := findResource(r.Bytes(), va, 24); err == nil {
		t.Fatal("a missing type found")
	}
}

func TestPlistTopAndApp(t *testing.T) {
	xmlPlist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleName</key><string>Claude</string>
<key>CFBundleShortVersionString</key><string>1.2.3</string>
<key>CFBundleVersion</key><string>456</string>
<key>CFBundleIdentifier</key><string>com.anthropic.claudefordesktop</string>
<key>LSMinimumSystemVersion</key><string>12.0</string>
<key>CFBundleDocumentTypes</key><array><dict><key>CFBundleName</key><string>nested</string></dict></array>
<key>LSUIElement</key><true/>
</dict></plist>`
	v := plistTop([]byte(xmlPlist))
	if v["CFBundleName"] != "Claude" || v["CFBundleVersion"] != "456" || v["LSUIElement"] != "true" {
		t.Fatalf("xml: %v", v)
	}
	b, _ := hex.DecodeString(bplistFixture)
	if v := plistTop(b); v["CFBundleName"] != "Claude" || v["Count"] != "42" || v["Ext"] != "" {
		t.Fatalf("binary: %v", v)
	}

	root := t.TempDir()
	app := filepath.Join(root, "Claude.app")
	os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(xmlPlist), 0o644)
	m := testModel(t, root)
	m.refreshPreview()
	pv := ansi.Strip(strings.Join(m.pv.lines, "\n"))
	for _, want := range []string{"App        Claude", "Version    1.2.3 (build 456)", "ID         com.anthropic.claudefordesktop", "Needs      macOS 12.0 or later", "Contents"} {
		if !strings.Contains(pv, want) {
			t.Errorf("lacks %q:\n%s", want, pv)
		}
	}
	if m.pv.meta != "Mac app" {
		t.Errorf("meta %q", m.pv.meta)
	}
}
