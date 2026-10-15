package main

// Binary property lists (bplist00): most macOS preference files, many app
// Info.plist files and .webloc links. XML plists are already text; binary
// ones are decoded here into an indented key: value tree. The format
// (CoreFoundation's CFBinaryPList.c): objects with a one-byte marker, an
// offset table, and a 32-byte trailer at the end that says where the table
// and the top object are.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	plistMaxBytes = 8 << 20 // a bigger binary plist isn't decoded
	plistMaxDepth = 32
	plistMaxLines = 2000
)

var bplistMagic = []byte("bplist00")

type bplist struct {
	b               []byte
	offsets         []uint64
	refSize         int
	lines           []string
	plistInFlight   map[uint64]bool // objects being printed: a cycle stops here
	truncatedOutput bool
}

// plistPreview decodes the binary plist at path into lines.
func plistPreview(path string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > plistMaxBytes {
		return nil, fmt.Errorf("binary plist too large to preview (%s)", humanSize(fi.Size()))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeBplist(b)
}

func decodeBplist(b []byte) ([]string, error) {
	if len(b) < 40 || !bytes.HasPrefix(b, bplistMagic) {
		return nil, errors.New("not a binary plist")
	}
	t := b[len(b)-32:]
	offSize, refSize := int(t[6]), int(t[7])
	num := binary.BigEndian.Uint64(t[8:])
	top := binary.BigEndian.Uint64(t[16:])
	tableAt := binary.BigEndian.Uint64(t[24:])
	if offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8 || num == 0 || top >= num ||
		tableAt >= uint64(len(b)) || num > uint64(len(b)) || tableAt+num*uint64(offSize) > uint64(len(b)-32) {
		return nil, errors.New("damaged binary plist")
	}
	p := &bplist{b: b, refSize: refSize, plistInFlight: map[uint64]bool{}}
	for i := uint64(0); i < num; i++ {
		at := tableAt + i*uint64(offSize)
		p.offsets = append(p.offsets, uintN(b[at:at+uint64(offSize)]))
	}
	if err := p.print(top, "", 0); err != nil {
		return nil, err
	}
	if p.truncatedOutput {
		p.lines = append(p.lines, "  …")
	}
	return p.lines, nil
}

func uintN(b []byte) uint64 {
	var n uint64
	for _, c := range b {
		n = n<<8 | uint64(c)
	}
	return n
}

func (p *bplist) emit(s string) {
	if len(p.lines) >= plistMaxLines {
		p.truncatedOutput = true
		return
	}
	p.lines = append(p.lines, s)
}

// object returns the marker byte, the count from its low nibble (or the int
// object after it) and where the object's data starts.
func (p *bplist) object(ref uint64) (marker byte, count uint64, data uint64, err error) {
	if ref >= uint64(len(p.offsets)) {
		return 0, 0, 0, errors.New("damaged binary plist")
	}
	at := p.offsets[ref]
	if at >= uint64(len(p.b)) {
		return 0, 0, 0, errors.New("damaged binary plist")
	}
	marker = p.b[at]
	count, data = uint64(marker&0x0f), at+1
	if t := marker >> 4; count == 0x0f && (t == 0x4 || t == 0x5 || t == 0x6 || t == 0xa || t == 0xc || t == 0xd) {
		if data >= uint64(len(p.b)) || p.b[data]>>4 != 1 {
			return 0, 0, 0, errors.New("damaged binary plist")
		}
		n := uint64(1) << (p.b[data] & 0x0f)
		if data+1+n > uint64(len(p.b)) {
			return 0, 0, 0, errors.New("damaged binary plist")
		}
		count = uintN(p.b[data+1 : data+1+n])
		data += 1 + n
	}
	return marker, count, data, nil
}

func (p *bplist) bytes(at, n uint64) ([]byte, error) {
	if at > uint64(len(p.b)) || n > uint64(len(p.b))-at {
		return nil, errors.New("damaged binary plist")
	}
	return p.b[at : at+n], nil
}

func (p *bplist) ref(at uint64, i uint64) (uint64, error) {
	b, err := p.bytes(at+i*uint64(p.refSize), uint64(p.refSize))
	if err != nil {
		return 0, err
	}
	return uintN(b), nil
}

// scalar renders a non-container object on one line ("" if it is one).
func (p *bplist) scalar(ref uint64) (string, bool, error) {
	marker, count, data, err := p.object(ref)
	if err != nil {
		return "", false, err
	}
	switch marker >> 4 {
	case 0x0:
		switch marker {
		case 0x08:
			return "false", true, nil
		case 0x09:
			return "true", true, nil
		}
		return "null", true, nil
	case 0x1:
		b, err := p.bytes(data, 1<<(marker&0x0f))
		if err != nil {
			return "", false, err
		}
		if len(b) == 8 {
			return strconv.FormatInt(int64(binary.BigEndian.Uint64(b)), 10), true, nil
		}
		return strconv.FormatUint(uintN(b), 10), true, nil
	case 0x2:
		b, err := p.bytes(data, 1<<(marker&0x0f))
		if err != nil {
			return "", false, err
		}
		f := 0.0
		switch len(b) {
		case 4:
			f = float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
		case 8:
			f = math.Float64frombits(binary.BigEndian.Uint64(b))
		}
		return strconv.FormatFloat(f, 'g', -1, 64), true, nil
	case 0x3:
		b, err := p.bytes(data, 8)
		if err != nil {
			return "", false, err
		}
		secs := math.Float64frombits(binary.BigEndian.Uint64(b))
		at := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(secs * float64(time.Second)))
		return at.Local().Format("2006-01-02 15:04:05"), true, nil
	case 0x4:
		b, err := p.bytes(data, count)
		if err != nil {
			return "", false, err
		}
		if bytes.HasPrefix(b, bplistMagic) { // a plist inside: say so, don't recurse
			return fmt.Sprintf("<plist, %s>", humanSize(int64(len(b)))), true, nil
		}
		return fmt.Sprintf("<%s of data>", humanSize(int64(len(b)))), true, nil
	case 0x5:
		b, err := p.bytes(data, count)
		if err != nil {
			return "", false, err
		}
		return quoteIfNeeded(string(b)), true, nil
	case 0x6:
		b, err := p.bytes(data, count*2)
		if err != nil {
			return "", false, err
		}
		u := make([]uint16, count)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		return quoteIfNeeded(string(utf16.Decode(u))), true, nil
	case 0x8:
		b, err := p.bytes(data, uint64(marker&0x0f)+1)
		if err != nil {
			return "", false, err
		}
		return fmt.Sprintf("UID %d", uintN(b)), true, nil
	case 0xa, 0xc, 0xd:
		return "", false, nil
	}
	return fmt.Sprintf("<object 0x%02x>", marker), true, nil
}

// quoteIfNeeded keeps a string on one line.
func quoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, "\n\r\t") || strings.TrimSpace(s) != s {
		return strconv.Quote(s)
	}
	return s
}

// print writes object ref after prefix ("key: " or "- "), indented by depth.
func (p *bplist) print(ref uint64, prefix string, depth int) error {
	indent := "  " + strings.Repeat("  ", depth)
	if s, ok, err := p.scalar(ref); err != nil {
		return err
	} else if ok {
		p.emit(indent + prefix + s)
		return nil
	}
	if depth >= plistMaxDepth || p.plistInFlight[ref] {
		p.emit(indent + prefix + "…")
		return nil
	}
	p.plistInFlight[ref] = true
	defer delete(p.plistInFlight, ref)
	marker, count, data, err := p.object(ref)
	if err != nil {
		return err
	}
	if count == 0 {
		empty := "[]"
		if marker>>4 == 0xd {
			empty = "{}"
		}
		p.emit(indent + prefix + empty)
		return nil
	}
	if prefix != "" {
		p.emit(indent + strings.TrimSuffix(prefix, " "))
		depth++
	}
	for i := uint64(0); i < count && !p.truncatedOutput; i++ {
		switch marker >> 4 {
		case 0xa, 0xc: // array, set
			r, err := p.ref(data, i)
			if err != nil {
				return err
			}
			if err := p.print(r, "- ", depth); err != nil {
				return err
			}
		case 0xd: // dict: count keys, then count values
			kr, err := p.ref(data, i)
			if err != nil {
				return err
			}
			vr, err := p.ref(data, count+i)
			if err != nil {
				return err
			}
			key, _, err := p.scalar(kr)
			if err != nil {
				return err
			}
			if err := p.print(vr, key+": ", depth); err != nil {
				return err
			}
		}
	}
	return nil
}
