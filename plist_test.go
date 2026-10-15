package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Made with Python's plistlib (FMT_BINARY): strings, UTF-16, ints, a real,
// a bool, data, a date, nested arrays and dicts, an empty dict.
const bplistFixture = "62706c6973743030dc0102030405060708090a0b0c0d0e0f1018191a1b1c1d1e1f5c434642756e646c654e616d655f101a434642756e646c6553686f727456657273696f6e537472696e6755436f756e7454446f637355456d70747957456e61626c65645449636f6e5f10164c534d696e696d756d53797374656d56657273696f6e584c6f6e675465787455526174696f545768656e6700dc006e00ef0063006f0064006556436c6175646555312e322e33102aa21117d21213141653457874544e616d65a11553706e6759504e4720696d61676555706c61696ed00944000102035431322e305f10147878787878787878787878787878787878787878233ff80000000000003341c8407be00000006d006800e9006c006c006f002020140020007700f60072006c006400080021002e004b00510056005c006400690082008b0091009600a500ac00b200b400b700bc00c000c500c700cb00d500db00dc00dd00e200e700fe01070110000000000000020100000000000000200000000000000000000000000000012b"

func TestDecodeBplist(t *testing.T) {
	b, _ := hex.DecodeString(bplistFixture)
	lines, err := decodeBplist(b)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"  CFBundleName: Claude",
		"  CFBundleShortVersionString: 1.2.3",
		"  Count: 42",
		"  Ratio: 1.5",
		"  Enabled: true",
		"  Icon: <4 B of data>",
		"  Empty: {}",
		"  Ünïcode: héllo — wörld",
		"  LongText: xxxxxxxxxxxxxxxxxxxx",
		"  Docs:\n    -\n      Ext:\n        - png\n      Name: PNG image\n    - plain",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "  When: 2026-10-15") {
		t.Errorf("date:\n%s", got)
	}
	for _, bad := range [][]byte{nil, []byte("bplist00"), b[:len(b)-5], append([]byte("bplist00"), make([]byte, 40)...)} {
		if _, err := decodeBplist(bad); err == nil {
			t.Errorf("damaged plist %d bytes decoded", len(bad))
		}
	}
}
