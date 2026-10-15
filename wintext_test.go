package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/charmbracelet/x/ansi"
)

func utf16le(s string, bom bool) []byte {
	var b bytes.Buffer
	if bom {
		b.Write([]byte{0xff, 0xfe})
	}
	for _, u := range utf16.Encode([]rune(s)) {
		binary.Write(&b, binary.LittleEndian, u)
	}
	return b.Bytes()
}

func TestUTF16Text(t *testing.T) {
	reg := "Windows Registry Editor Version 5.00\r\n\r\n[HKEY_CURRENT_USER\\Software\\Tidé]\r\n\"Name\"=\"value\"\r\n"
	for _, bom := range []bool{true, false} {
		got, ok := utf16Text(utf16le(reg, bom))
		if !ok || got != reg {
			t.Errorf("bom %v: %v %q", bom, ok, got)
		}
	}
	// Cut in the middle of a character by the read limit.
	if got, ok := utf16Text(utf16le(reg, true)[:41]); !ok || !strings.HasPrefix(reg, got) {
		t.Errorf("cut: %v %q", ok, got)
	}
	for _, bin := range [][]byte{{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0}, []byte("plain ascii text here"), {0, 0, 0, 0, 0, 0, 0, 0, 0, 0}} {
		if _, ok := utf16Text(bin); ok {
			t.Errorf("not UTF-16: %q", bin)
		}
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "settings.reg"), utf16le(reg, true), 0o644)
	m := testModel(t, dir)
	m.refreshPreview()
	if pv := ansi.Strip(strings.Join(m.pv.lines, "\n")); !strings.Contains(pv, "HKEY_CURRENT_USER") || !strings.Contains(m.pv.meta, "UTF-16") {
		t.Fatalf("preview %q: %s", m.pv.meta, pv)
	}
}

// buildLnk makes a shortcut: a LinkInfo with a local path or a network
// share, then Unicode strings.
func buildLnk(local, share, suffix, args, startIn, comment string) []byte {
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	flags := uint32(lnkHasLinkInfo | lnkIsUnicode)
	if comment != "" {
		flags |= lnkHasName
	}
	if startIn != "" {
		flags |= lnkHasWorkingDir
	}
	if args != "" {
		flags |= lnkHasArguments
	}
	le(uint32(0x4c))
	b.Write(lnkCLSID)
	le(flags)
	b.Write(make([]byte, 0x4c-24))
	// LinkInfo: 0x1c header, then the strings.
	var li bytes.Buffer
	lw := func(v any) { binary.Write(&li, binary.LittleEndian, v) }
	tail := []byte{}
	var liFlags, base, cnr uint32
	headerSize := uint32(0x1c)
	if local != "" {
		liFlags = 1
		base = headerSize
		tail = append(append(tail, local...), 0)
	} else {
		liFlags = 2
		cnr = headerSize
		netName := append([]byte(share), 0)
		c := new(bytes.Buffer)
		binary.Write(c, binary.LittleEndian, uint32(0x14+len(netName))) // size
		binary.Write(c, binary.LittleEndian, uint32(0))                 // flags
		binary.Write(c, binary.LittleEndian, uint32(0x14))              // NetNameOffset
		binary.Write(c, binary.LittleEndian, uint32(0))                 // DeviceNameOffset
		binary.Write(c, binary.LittleEndian, uint32(0))                 // provider
		c.Write(netName)
		tail = append(tail, c.Bytes()...)
	}
	suffixAt := headerSize + uint32(len(tail))
	tail = append(append(tail, suffix...), 0)
	lw(uint32(headerSize) + uint32(len(tail)))
	lw(headerSize)
	lw(liFlags)
	lw(uint32(0)) // VolumeIDOffset
	lw(base)
	lw(cnr)
	lw(suffixAt)
	li.Write(tail)
	b.Write(li.Bytes())
	for _, s := range []string{comment, startIn, args} {
		if s == "" {
			continue
		}
		u := utf16.Encode([]rune(s))
		le(uint16(len(u)))
		for _, c := range u {
			le(c)
		}
	}
	return b.Bytes()
}

func TestParseLnk(t *testing.T) {
	l, err := parseLnk(buildLnk(`C:\Program Files\Steam\steam.exe`, "", "", "-silent", `C:\Program Files\Steam`, "Play games"))
	if err != nil || l.target != `C:\Program Files\Steam\steam.exe` || l.args != "-silent" || l.startIn != `C:\Program Files\Steam` || l.comment != "Play games" {
		t.Fatalf("local: %+v %v", l, err)
	}
	l, err = parseLnk(buildLnk("", `\\NAS\photos`, `2026\beach.jpg`, "", "", ""))
	if err != nil || l.target != `\\NAS\photos\2026\beach.jpg` {
		t.Fatalf("network: %+v %v", l, err)
	}
	if _, err := parseLnk([]byte("not a shortcut at all, just text that is long enough to pass the size check......")); err == nil {
		t.Fatal("text parsed as a shortcut")
	}
	if _, err := parseLnk(buildLnk(`C:\x`, "", "", "", "", "")[:0x50]); err == nil {
		t.Fatal("cut shortcut parsed")
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Steam.lnk"), buildLnk(`C:\Program Files\Steam\steam.exe`, "", "", "", "", ""), 0o644)
	m := testModel(t, dir)
	m.refreshPreview()
	if pv := ansi.Strip(strings.Join(m.pv.lines, "\n")); !strings.Contains(pv, `Target     C:\Program Files\Steam\steam.exe`) {
		t.Fatalf("preview: %s", pv)
	}
}
