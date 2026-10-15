package main

// Drives in places: every filesystem on a disk that isn't part of the running
// system, mounted or not, read from lsblk. Mounting, unlocking (LUKS),
// unmounting and powering off go through udisks (udisksctl), which needs no
// root: the desktop's polkit agent asks for a password where one is needed.
// udisksctl runs with no terminal, so it never prompts inside the TUI.

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// placesTimeout bounds each lookup made while places opens, so a stuck
// gvfs or udev can't freeze the UI.
const placesTimeout = 2 * time.Second

type blockDev struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Parent      string   `json:"pkname"`
	Type        string   `json:"type"`
	FSType      string   `json:"fstype"`
	Label       string   `json:"label"`
	Size        int64    `json:"size"`
	Mountpoints []string `json:"mountpoints"`
	Removable   bool     `json:"rm"`
	Hotplug     bool     `json:"hotplug"`
	PartType    string   `json:"parttype"`
}

// blockDevices lists every block device, flat, with its parent's name.
var blockDevices = func() ([]blockDev, error) {
	ctx, cancel := context.WithTimeout(context.Background(), placesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-l", "-b", "-o",
		"NAME,PATH,PKNAME,TYPE,FSTYPE,LABEL,SIZE,MOUNTPOINTS,RM,HOTPLUG,PARTTYPE").Output()
	if err != nil {
		return nil, err
	}
	return parseLsblk(out)
}

func parseLsblk(b []byte) ([]blockDev, error) {
	var v struct {
		Devs []blockDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	for i := range v.Devs {
		var mps []string
		for _, mp := range v.Devs[i].Mountpoints {
			if mp != "" {
				mps = append(mps, mp)
			}
		}
		v.Devs[i].Mountpoints = mps
	}
	return v.Devs, nil
}

// Partition types that are never data: EFI system, BIOS boot, Microsoft
// reserved, Windows recovery.
var skipPartTypes = map[string]bool{
	"c12a7328-f81f-11d2-ba4b-00a0c93ec93b": true,
	"21686148-6449-6e6f-744e-656564454649": true,
	"e3c9e316-0b5c-4db8-817d-f92df00215ae": true,
	"de94bba4-06d1-4d40-a16a-bfd50179d6ac": true,
	"0xef":                                 true,
}

// Filesystems that hold no files to browse.
var skipFS = map[string]bool{
	"swap": true, "LVM2_member": true, "linux_raid_member": true, "zfs_member": true,
	"squashfs": true, "BitLocker": true,
}

// systemMount says whether a mountpoint is part of the running system, so its
// filesystem isn't offered as a drive (Computer covers it).
func systemMount(mp string) bool {
	if mp == "/" || mp == "[SWAP]" {
		return true
	}
	for _, p := range []string{"/boot", "/efi", "/home", "/usr", "/var", "/opt", "/srv", "/root", "/nix", "/snap", "/etc", "/gnu"} {
		if mp == p || strings.HasPrefix(mp, p+"/") {
			return true
		}
	}
	return false
}

// drive is a filesystem shown in places.
type drive struct {
	dev     string // what udisks mounts: the partition or the unlocked mapper
	name    string
	mount   string // where it is mounted, "" if it isn't
	locked  bool   // an encrypted volume that must be unlocked first
	size    int64
	fstype  string
	luks    string // the encrypted container this lives in, to lock after unmounting
	disk    string // the whole disk, to power off a removable one
	removes bool   // removable or hot-plugged: power off after unmounting
}

// drives picks the user-facing filesystems out of the block devices.
func drives(devs []blockDev) []drive {
	byName := map[string]blockDev{}
	children := map[string]int{}
	for _, d := range devs {
		byName[d.Name] = d
		if d.Parent != "" {
			children[d.Parent]++
		}
	}
	// diskOf walks up to the whole disk; luksOf finds an encrypted container.
	diskOf := func(d blockDev) blockDev {
		for i := 0; i < 8 && d.Parent != ""; i++ {
			p, ok := byName[d.Parent]
			if !ok {
				break
			}
			d = p
		}
		return d
	}
	var out []drive
	for _, d := range devs {
		if d.FSType == "" || skipFS[d.FSType] || skipPartTypes[strings.ToLower(d.PartType)] || d.Type == "loop" {
			continue
		}
		system := false
		for _, mp := range d.Mountpoints {
			if systemMount(mp) {
				system = true
			}
		}
		if system {
			continue
		}
		disk := diskOf(d)
		dr := drive{dev: d.Path, size: d.Size, fstype: d.FSType, disk: disk.Path,
			removes: disk.Removable || disk.Hotplug || d.Removable || d.Hotplug}
		if d.FSType == "crypto_LUKS" {
			if children[d.Name] > 0 {
				continue // unlocked: its filesystem is listed itself
			}
			dr.locked = true
		}
		if p, ok := byName[d.Parent]; ok && p.FSType == "crypto_LUKS" {
			dr.luks = p.Path
		}
		if len(d.Mountpoints) > 0 {
			dr.mount = d.Mountpoints[0]
		}
		switch {
		case d.Label != "":
			dr.name = d.Label
		case dr.locked:
			dr.name = humanSize(d.Size) + " encrypted volume"
		default:
			dr.name = humanSize(d.Size) + " volume"
		}
		out = append(out, dr)
	}
	return out
}

// info is the drive's suffix in places.
func (d drive) info() string {
	switch {
	case d.locked:
		return "locked · " + humanSize(d.size)
	case d.mount == "":
		return humanSize(d.size) + " · " + d.fstype
	}
	var st syscall.Statfs_t
	if syscall.Statfs(d.mount, &st) == nil && st.Blocks > 0 {
		return humanSize(int64(st.Bavail)*st.Bsize) + " free of " + humanSize(int64(st.Blocks)*st.Bsize)
	}
	return tildePath(d.mount)
}

// udisks runs udisksctl and turns its errors into a sentence.
var udisks = func(stdin string, args ...string) (string, error) {
	if _, err := exec.LookPath("udisksctl"); err != nil {
		return "", missing("udisks", "needs udisks (udisksctl) to mount drives")
	}
	c := exec.Command("udisksctl", args...) // stdin isn't a terminal: no text prompt
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		return "", errors.New(udisksError(string(out), err))
	}
	return string(out), nil
}

func udisksError(out string, err error) string {
	msg := strings.TrimSpace(out)
	if msg == "" {
		return err.Error()
	}
	switch {
	case strings.Contains(msg, "NotAuthorized"):
		return "not authorized (is a polkit agent running to ask for your password?)"
	case strings.Contains(msg, "target is busy") || strings.Contains(msg, "DeviceBusy"):
		return "it's in use: close programs with files open there"
	case strings.Contains(msg, "Failed to activate device") || strings.Contains(msg, "passphrase"):
		return "wrong passphrase"
	}
	// "Error mounting /dev/x: GDBus.Error:org.freedesktop.UDisks2.Error.Failed: reason"
	if i := strings.LastIndex(msg, "Error."); i >= 0 {
		if _, rest, ok := strings.Cut(msg[i:], ": "); ok {
			msg = rest
		}
	}
	return strings.TrimSpace(strings.SplitN(msg, "\n", 2)[0])
}

// findDrive reads the drives again and returns the one for dev.
func findDrive(dev string) (drive, bool) {
	devs, err := blockDevices()
	if err != nil {
		return drive{}, false
	}
	for _, d := range drives(devs) {
		if d.dev == dev {
			return d, true
		}
	}
	return drive{}, false
}

// placeMsg reports a mount, unmount or connection; open is a folder to go to.
type placeMsg struct {
	open  string
	text  string
	err   bool
	cause error  // what went wrong, when it can be installed
	busy  string // a network location that wouldn't disconnect: the next try forces it
}

// mountDrive mounts dev (unlocking it first with passphrase when it is
// encrypted) and opens it.
func mountDrive(dr drive, passphrase string) tea.Cmd {
	return func() tea.Msg {
		dev := dr.dev
		if dr.locked {
			if _, err := udisks(passphrase, "unlock", "-b", dev, "--key-file", "/dev/stdin"); err != nil {
				return placeMsg{text: "unlock " + dr.name + ": " + err.Error(), err: true, cause: err}
			}
			devs, _ := blockDevices()
			name := filepath.Base(dev)
			for _, d := range devs {
				if d.Parent == name {
					dev = d.Path
					if len(d.Mountpoints) > 0 { // the system mounted it already
						return placeMsg{open: d.Mountpoints[0], text: "unlocked " + dr.name}
					}
				}
			}
		}
		if _, err := udisks("", "mount", "-b", dev); err != nil {
			return placeMsg{text: "mount " + dr.name + ": " + err.Error(), err: true, cause: err}
		}
		if d, ok := findDrive(dev); ok && d.mount != "" {
			return placeMsg{open: d.mount, text: "mounted " + dr.name}
		}
		return placeMsg{text: "mounted " + dr.name}
	}
}

// unmountDrive unmounts, locks an encrypted container, and powers off a
// removable disk once nothing else on it is mounted.
func unmountDrive(dr drive) tea.Cmd {
	return func() tea.Msg {
		if _, err := udisks("", "unmount", "-b", dr.dev); err != nil {
			return placeMsg{text: "unmount " + dr.name + ": " + err.Error(), err: true, cause: err}
		}
		if dr.luks != "" {
			if _, err := udisks("", "lock", "-b", dr.luks); err != nil {
				return placeMsg{text: "unmounted " + dr.name + ", but locking failed: " + err.Error(), err: true}
			}
		}
		if dr.removes && dr.disk != "" && !diskInUse(dr.disk) {
			if _, err := udisks("", "power-off", "-b", dr.disk); err == nil {
				return placeMsg{text: dr.name + " can be removed safely"}
			}
		}
		return placeMsg{text: "unmounted " + dr.name}
	}
}

// diskInUse says whether anything on the disk is still mounted.
func diskInUse(disk string) bool {
	devs, err := blockDevices()
	if err != nil {
		return true
	}
	byName := map[string]blockDev{}
	for _, d := range devs {
		byName[d.Name] = d
	}
	for _, d := range devs {
		if len(d.Mountpoints) == 0 {
			continue
		}
		for p, i := d, 0; i < 8; i++ {
			if p.Path == disk {
				return true
			}
			next, ok := byName[p.Parent]
			if !ok {
				break
			}
			p = next
		}
	}
	return false
}
