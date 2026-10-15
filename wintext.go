package main

// Windows files that would otherwise preview as "binary file":
//
//   - UTF-16 text: Windows writes many .reg, .inf, .log, .txt and .csv
//     files (and macOS many .strings files) as UTF-16, whose zero bytes
//     made them look binary. They're decoded and shown as text.
//   - .lnk shortcuts (MS-SHLLINK): the target (a local path or a
//     \\server\share one), its arguments, the folder it starts in and its
//     comment.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"unicode/utf16"
)

// utf16Text decodes b if it is UTF-16 text: with a byte-order mark, or
// without one when it is plainly ASCII-range text with every other byte
// zero. ok is false for anything else.
func utf16Text(b []byte) (string, bool) {
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}):
		order, b = binary.LittleEndian, b[2:]
	case bytes.HasPrefix(b, []byte{0xfe, 0xff}):
		order, b = binary.BigEndian, b[2:]
	default:
		order = guessUTF16(b)
		if order == nil {
			return "", false
		}
	}
	b = b[:len(b)&^1] // a read limit may have cut the last character in half
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	s := string(utf16.Decode(u))
	if strings.ContainsRune(s, 0) {
		return "", false
	}
	return s, true
}

// guessUTF16 recognises UTF-16 without a byte-order mark by its zero bytes:
// in the first 512 bytes, nearly every high byte is zero and no low one is.
func guessUTF16(b []byte) binary.ByteOrder {
	n := min(len(b), 512) &^ 1
	if n < 8 {
		return nil
	}
	var zeroEven, zeroOdd int
	for i := 0; i < n; i += 2 {
		if b[i] == 0 {
			zeroEven++
		}
		if b[i+1] == 0 {
			zeroOdd++
		}
	}
	pairs := n / 2
	switch {
	case zeroOdd*10 >= pairs*9 && zeroEven == 0:
		return binary.LittleEndian
	case zeroEven*10 >= pairs*9 && zeroOdd == 0:
		return binary.BigEndian
	}
	return nil
}

// lnkInfo is what a Windows shortcut points at.
type lnkInfo struct {
	target, args, startIn, comment, icon string
}

var lnkCLSID = []byte{0x01, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}

// Link flags (MS-SHLLINK 2.1.1).
const (
	lnkHasIDList = 1 << iota
	lnkHasLinkInfo
	lnkHasName
	lnkHasRelPath
	lnkHasWorkingDir
	lnkHasArguments
	lnkHasIconLocation
	lnkIsUnicode
)

func lnkPreview(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	l, err := parseLnk(b)
	if err != nil {
		return nil, err
	}
	lines := []string{"  Windows shortcut", ""}
	row := func(k, v string) {
		if v != "" {
			lines = append(lines, "  "+padRight(k, 10)+" "+v)
		}
	}
	row("Target", l.target)
	row("Arguments", l.args)
	row("Start in", l.startIn)
	row("Comment", l.comment)
	row("Icon", l.icon)
	return lines, nil
}

func padRight(s string, n int) string { return s + strings.Repeat(" ", max(0, n-len(s))) }

var errLnk = errors.New("not a Windows shortcut, or a damaged one")

func parseLnk(b []byte) (lnkInfo, error) {
	var l lnkInfo
	if len(b) < 0x4c || binary.LittleEndian.Uint32(b) != 0x4c || !bytes.Equal(b[4:20], lnkCLSID) {
		return l, errLnk
	}
	flags := binary.LittleEndian.Uint32(b[20:])
	at := 0x4c
	if flags&lnkHasIDList != 0 {
		if at+2 > len(b) {
			return l, errLnk
		}
		at += 2 + int(binary.LittleEndian.Uint16(b[at:]))
	}
	if flags&lnkHasLinkInfo != 0 {
		if at+4 > len(b) {
			return l, errLnk
		}
		size := int(binary.LittleEndian.Uint32(b[at:]))
		if size < 0x1c || at+size > len(b) {
			return l, errLnk
		}
		l.target = linkInfoTarget(b[at : at+size])
		at += size
	}
	// StringData, in this order, each one present only if its flag is set.
	str := func(flag uint32) (string, bool) {
		if flags&flag == 0 {
			return "", true
		}
		if at+2 > len(b) {
			return "", false
		}
		n := int(binary.LittleEndian.Uint16(b[at:]))
		at += 2
		if flags&lnkIsUnicode != 0 {
			if at+2*n > len(b) {
				return "", false
			}
			u := make([]uint16, n)
			for i := range u {
				u[i] = binary.LittleEndian.Uint16(b[at+2*i:])
			}
			at += 2 * n
			return string(utf16.Decode(u)), true
		}
		if at+n > len(b) {
			return "", false
		}
		s := latin1(b[at : at+n])
		at += n
		return s, true
	}
	var rel string
	var ok bool
	for _, f := range []struct {
		flag uint32
		dst  *string
	}{{lnkHasName, &l.comment}, {lnkHasRelPath, &rel}, {lnkHasWorkingDir, &l.startIn}, {lnkHasArguments, &l.args}, {lnkHasIconLocation, &l.icon}} {
		if *f.dst, ok = str(f.flag); !ok {
			return l, errLnk
		}
	}
	if l.target == "" {
		l.target = rel
	}
	if l.target == "" && l.args == "" {
		return l, errLnk
	}
	return l, nil
}

// linkInfoTarget is the target in a LinkInfo structure: the local path, or
// the network share's name, with the common suffix after it.
func linkInfoTarget(li []byte) string {
	u32 := func(off int) int {
		if off+4 > len(li) {
			return 0
		}
		return int(binary.LittleEndian.Uint32(li[off:]))
	}
	cstr := func(off int) string {
		if off <= 0 || off >= len(li) {
			return ""
		}
		end := bytes.IndexByte(li[off:], 0)
		if end < 0 {
			return ""
		}
		return latin1(li[off : off+end])
	}
	wstr := func(off int) string {
		if off <= 0 || off >= len(li) {
			return ""
		}
		var u []uint16
		for i := off; i+1 < len(li); i += 2 {
			c := binary.LittleEndian.Uint16(li[i:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		return string(utf16.Decode(u))
	}
	headerSize, flags := u32(4), u32(8)
	base, suffix := cstr(u32(16)), cstr(u32(24))
	if headerSize >= 0x24 { // the Unicode versions, when there are any
		if s := wstr(u32(28)); s != "" {
			base = s
		}
		if s := wstr(u32(32)); s != "" {
			suffix = s
		}
	}
	if flags&1 != 0 && base != "" { // VolumeIDAndLocalBasePath
		return base + suffix
	}
	if flags&2 != 0 { // CommonNetworkRelativeLinkAndPathSuffix
		cnr := u32(20)
		if cnr > 0 && cnr+12 <= len(li) {
			net := cstr(cnr + int(binary.LittleEndian.Uint32(li[cnr+8:])))
			if net != "" {
				if suffix != "" && !strings.HasSuffix(net, `\`) {
					net += `\`
				}
				return net + suffix
			}
		}
	}
	return base + suffix
}

// latin1 decodes a Windows "ANSI" string well enough for paths: bytes
// below 0x80 are ASCII; the rest are taken as Latin-1.
func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}
