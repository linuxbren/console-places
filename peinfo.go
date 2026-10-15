package main

// Windows programs (.exe .dll .sys .scr .cpl .ocx) preview as the details
// Windows shows under Properties → Details: description, product, company,
// versions and copyright, from the version resource (VS_VERSIONINFO), plus
// the CPU and whether it opens a window or a console. debug/pe reads the
// headers; the resource tree and the version block are walked here.

import (
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

var peExts = setOf(".exe", ".dll", ".sys", ".scr", ".cpl", ".ocx", ".efi")

const rtVersion = 16 // RT_VERSION

func peLines(path string) ([]string, string, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	kind := "Windows program"
	if f.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		kind = "Windows library (DLL)"
	}
	if strings.HasPrefix(peSubsystem(f), "UEFI") {
		kind = "UEFI program"
	}
	var lines []string
	row := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			lines = append(lines, "  "+padRight(k, 13)+" "+v)
		}
	}
	info := map[string]string{}
	fixed := ""
	if s := f.Section(".rsrc"); s != nil {
		if data, err := s.Data(); err == nil {
			if v, err := findResource(data, s.VirtualAddress, rtVersion); err == nil {
				info, fixed = parseVersionInfo(v)
			}
		}
	}
	row("Description", info["FileDescription"])
	row("Product", info["ProductName"])
	row("Company", info["CompanyName"])
	version := info["ProductVersion"]
	if version == "" {
		version = fixed
	}
	row("Version", version)
	if fv := info["FileVersion"]; fv != "" && fv != version {
		row("File version", fv)
	}
	row("Copyright", info["LegalCopyright"])
	row("Original name", info["OriginalFilename"])
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	row("Type", kind)
	if isDotNet(f) {
		row("Runs on", ".NET")
	} else {
		row("CPU", peMachine(f.Machine))
	}
	if f.Characteristics&pe.IMAGE_FILE_DLL == 0 {
		row("Runs as", peSubsystem(f))
	}
	return lines, kind, nil
}

// isDotNet says whether f is a .NET assembly (it has a CLR header).
func isDotNet(f *pe.File) bool {
	const clr = 14 // IMAGE_DIRECTORY_ENTRY_COM_DESCRIPTOR
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		return h.NumberOfRvaAndSizes > clr && h.DataDirectory[clr].VirtualAddress != 0
	case *pe.OptionalHeader32:
		return h.NumberOfRvaAndSizes > clr && h.DataDirectory[clr].VirtualAddress != 0
	}
	return false
}

func peMachine(m uint16) string {
	switch m {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		return "x86-64 (64-bit)"
	case pe.IMAGE_FILE_MACHINE_I386:
		return "x86 (32-bit)"
	case pe.IMAGE_FILE_MACHINE_ARM64:
		return "ARM64"
	case pe.IMAGE_FILE_MACHINE_ARMNT:
		return "ARM (32-bit)"
	}
	return fmt.Sprintf("machine 0x%04x", m)
}

func peSubsystem(f *pe.File) string {
	var sub uint16
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		sub = h.Subsystem
	case *pe.OptionalHeader32:
		sub = h.Subsystem
	}
	switch sub {
	case pe.IMAGE_SUBSYSTEM_WINDOWS_GUI:
		return "a window (GUI)"
	case pe.IMAGE_SUBSYSTEM_WINDOWS_CUI:
		return "a console program"
	case pe.IMAGE_SUBSYSTEM_NATIVE:
		return "a driver (native)"
	case pe.IMAGE_SUBSYSTEM_EFI_APPLICATION, pe.IMAGE_SUBSYSTEM_EFI_BOOT_SERVICE_DRIVER, pe.IMAGE_SUBSYSTEM_EFI_RUNTIME_DRIVER:
		return "UEFI firmware"
	}
	return ""
}

var errNoResource = errors.New("no such resource")

// findResource returns the first resource of type typ in a .rsrc section
// (data, loaded at virtual address va): type → name → language.
func findResource(data []byte, va uint32, typ uint32) ([]byte, error) {
	u16 := func(at uint32) (uint32, bool) {
		if int(at)+2 > len(data) {
			return 0, false
		}
		return uint32(binary.LittleEndian.Uint16(data[at:])), true
	}
	u32 := func(at uint32) (uint32, bool) {
		if int(at)+4 > len(data) {
			return 0, false
		}
		return binary.LittleEndian.Uint32(data[at:]), true
	}
	// entry finds, in the directory at dir, the entry with id (or the first
	// one if any), returning its offset field.
	entry := func(dir uint32, id uint32, any bool) (uint32, bool) {
		named, ok1 := u16(dir + 12)
		ids, ok2 := u16(dir + 14)
		if !ok1 || !ok2 {
			return 0, false
		}
		for i := uint32(0); i < named+ids && i < 4096; i++ {
			at := dir + 16 + 8*i
			name, ok := u32(at)
			off, ok2 := u32(at + 4)
			if !ok || !ok2 {
				return 0, false
			}
			if any || (name&0x80000000 == 0 && name == id) {
				return off, true
			}
		}
		return 0, false
	}
	off, ok := entry(0, typ, false)
	for depth := 0; ok && off&0x80000000 != 0 && depth < 3; depth++ { // into name, then language
		off, ok = entry(off&0x7fffffff, 0, true)
	}
	if !ok || off&0x80000000 != 0 {
		return nil, errNoResource
	}
	rva, ok1 := u32(off)
	size, ok2 := u32(off + 4)
	if !ok1 || !ok2 || rva < va || int(rva-va)+int(size) > len(data) {
		return nil, errNoResource
	}
	return data[rva-va : rva-va+size], nil
}

// verBlock is one node of a VS_VERSIONINFO tree.
type verBlock struct {
	key      string
	value    []byte
	text     bool
	children []verBlock
}

// parseVerBlock reads the block at the start of b (wLength, wValueLength,
// wType, the key, then the value and child blocks, each 32-bit aligned).
func parseVerBlock(b []byte, depth int) (verBlock, int, bool) {
	var v verBlock
	if len(b) < 6 || depth > 8 {
		return v, 0, false
	}
	length := int(binary.LittleEndian.Uint16(b))
	valueLen := int(binary.LittleEndian.Uint16(b[2:]))
	v.text = binary.LittleEndian.Uint16(b[4:]) == 1
	if length < 6 || length > len(b) {
		return v, 0, false
	}
	b = b[:length]
	at := 6
	var key []uint16
	for at+2 <= len(b) {
		c := binary.LittleEndian.Uint16(b[at:])
		at += 2
		if c == 0 {
			break
		}
		key = append(key, c)
	}
	v.key = string(utf16.Decode(key))
	at = (at + 3) &^ 3
	n := valueLen
	if v.text {
		n *= 2 // in WCHARs
	}
	if at+n > len(b) {
		n = max(0, len(b)-at)
	}
	v.value = b[min(at, len(b)) : min(at, len(b))+n]
	at = (at + n + 3) &^ 3
	for at+6 <= len(b) {
		c, used, ok := parseVerBlock(b[at:], depth+1)
		if !ok {
			break
		}
		v.children = append(v.children, c)
		at = (at + used + 3) &^ 3
	}
	return v, length, true
}

func utf16z(b []byte) string {
	var u []uint16
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// parseVersionInfo returns the version strings (from the first string
// table) and the fixed file version ("1.2.3.4").
func parseVersionInfo(b []byte) (map[string]string, string) {
	info := map[string]string{}
	root, _, ok := parseVerBlock(b, 0)
	if !ok || root.key != "VS_VERSION_INFO" {
		return info, ""
	}
	fixed := ""
	if len(root.value) >= 16 && binary.LittleEndian.Uint32(root.value) == 0xfeef04bd {
		ms, ls := binary.LittleEndian.Uint32(root.value[8:]), binary.LittleEndian.Uint32(root.value[12:])
		fixed = fmt.Sprintf("%d.%d.%d.%d", ms>>16, ms&0xffff, ls>>16, ls&0xffff)
	}
	for _, c := range root.children {
		if c.key != "StringFileInfo" || len(c.children) == 0 {
			continue
		}
		for _, s := range c.children[0].children {
			info[s.key] = utf16z(s.value)
		}
		break
	}
	return info, fixed
}

// appLines sums up a Mac app (a .app folder) from its Info.plist: name,
// version, identifier, the macOS it needs; nil if it has none.
func appLines(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, "Contents", "Info.plist"))
	if err != nil || len(b) > plistMaxBytes {
		return nil
	}
	v := plistTop(b)
	if len(v) == 0 {
		return nil
	}
	var lines []string
	row := func(k, val string) {
		if val = strings.TrimSpace(val); val != "" {
			lines = append(lines, "  "+padRight(k, 10)+" "+val)
		}
	}
	name := v["CFBundleDisplayName"]
	if name == "" {
		name = v["CFBundleName"]
	}
	row("App", name)
	version := v["CFBundleShortVersionString"]
	if b := v["CFBundleVersion"]; b != "" && b != version {
		if version == "" {
			version = b
		} else {
			version += " (build " + b + ")"
		}
	}
	row("Version", version)
	row("ID", v["CFBundleIdentifier"])
	if minOS := v["LSMinimumSystemVersion"]; minOS != "" {
		row("Needs", "macOS "+minOS+" or later")
	}
	row("Copyright", v["NSHumanReadableCopyright"])
	if len(lines) == 0 {
		return nil
	}
	return append(append([]string{"  Mac app", ""}, lines...), "", "  ── Contents ──")
}
