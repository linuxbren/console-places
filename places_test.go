package main

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// lsblkFixture: the system disk (LUKS root, /boot, EFI), a second internal
// disk with an NTFS partition, a locked LUKS partition and Windows' reserved
// partition, and a USB stick mounted by udisks.
const lsblkFixture = `{"blockdevices": [
 {"name":"zram0","path":"/dev/zram0","pkname":null,"type":"disk","fstype":"swap","label":"zram0","size":100,"mountpoints":["[SWAP]"],"rm":false,"hotplug":false,"parttype":null},
 {"name":"nvme1n1","path":"/dev/nvme1n1","pkname":null,"type":"disk","fstype":null,"label":null,"size":1000,"mountpoints":[null],"rm":false,"hotplug":false,"parttype":null},
 {"name":"nvme1n1p1","path":"/dev/nvme1n1p1","pkname":"nvme1n1","type":"part","fstype":"vfat","label":null,"size":2,"mountpoints":["/boot"],"rm":false,"hotplug":false,"parttype":"c12a7328-f81f-11d2-ba4b-00a0c93ec93b"},
 {"name":"nvme1n1p2","path":"/dev/nvme1n1p2","pkname":"nvme1n1","type":"part","fstype":"crypto_LUKS","label":null,"size":998,"mountpoints":[null],"rm":false,"hotplug":false,"parttype":"4f68bce3-e8cd-4db1-96e7-fbcaf984b709"},
 {"name":"root","path":"/dev/mapper/root","pkname":"nvme1n1p2","type":"crypt","fstype":"btrfs","label":null,"size":998,"mountpoints":["/home","/"],"rm":false,"hotplug":false,"parttype":null},
 {"name":"nvme0n1","path":"/dev/nvme0n1","pkname":null,"type":"disk","fstype":null,"label":null,"size":1000,"mountpoints":[null],"rm":false,"hotplug":false,"parttype":null},
 {"name":"nvme0n1p1","path":"/dev/nvme0n1p1","pkname":"nvme0n1","type":"part","fstype":null,"label":null,"size":16,"mountpoints":[null],"rm":false,"hotplug":false,"parttype":"e3c9e316-0b5c-4db8-817d-f92df00215ae"},
 {"name":"nvme0n1p2","path":"/dev/nvme0n1p2","pkname":"nvme0n1","type":"part","fstype":"ntfs","label":"New Volume","size":819099336704,"mountpoints":[null],"rm":false,"hotplug":false,"parttype":"ebd0a0a2-b9e5-4433-87c0-68b6b72699c7"},
 {"name":"nvme0n1p5","path":"/dev/nvme0n1p5","pkname":"nvme0n1","type":"part","fstype":"crypto_LUKS","label":null,"size":176792011264,"mountpoints":[null],"rm":false,"hotplug":false,"parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4"},
 {"name":"sdb","path":"/dev/sdb","pkname":null,"type":"disk","fstype":null,"label":null,"size":32,"mountpoints":[null],"rm":true,"hotplug":true,"parttype":null},
 {"name":"sdb1","path":"/dev/sdb1","pkname":"sdb","type":"part","fstype":"exfat","label":"STICK","size":32,"mountpoints":["/run/media/me/STICK"],"rm":true,"hotplug":true,"parttype":"0x7"}
]}`

func TestDrivesFromLsblk(t *testing.T) {
	devs, err := parseLsblk([]byte(lsblkFixture))
	if err != nil {
		t.Fatal(err)
	}
	ds := drives(devs)
	var got []string
	for _, d := range ds {
		got = append(got, d.name+"|"+d.dev+"|"+d.mount)
	}
	want := []string{
		"New Volume|/dev/nvme0n1p2|",
		"164.7 GiB encrypted volume|/dev/nvme0n1p5|",
		"STICK|/dev/sdb1|/run/media/me/STICK",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("drives:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !ds[1].locked || ds[0].locked || !ds[2].removes || ds[0].removes || ds[2].disk != "/dev/sdb" {
		t.Fatalf("flags: %+v", ds)
	}
	if got := ds[0].info(); got != "762.8 GiB · ntfs" {
		t.Errorf("info: %q", got)
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		`\\nas\photos`:             "smb://nas/photos",
		`\\nas\photos\2024`:        "smb://nas/photos/2024",
		"//mac.local/Shared":       "smb://mac.local/Shared",
		"me@box:/srv/data":         "sftp://me@box/srv/data",
		"box:notes":                "sftp://box/notes",
		"ssh://me@box":             "sftp://me@box",
		"SMB://host/share":         "smb://host/share",
		"nfs://nas/export/media":   "nfs://nas/export/media",
		"davs://cloud.example/dav": "davs://cloud.example/dav",
	} {
		if got, err := normalizeServer(in); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "nas", "http://example.com", "smb://"} {
		if got, err := normalizeServer(bad); err == nil {
			t.Errorf("%q should be refused, got %q", bad, got)
		}
	}
	if got := serverName("smb://me@nas/photos/"); got != "photos on me@nas" {
		t.Errorf("serverName: %q", got)
	}
}

func TestParsePrompt(t *testing.T) {
	p := parsePrompt("Authentication Required\nEnter user and password for share “photos” on “nas”:\nUser [brenden]: ")
	if p.label != "User" || p.def != "brenden" || len(p.context) != 1 || p.secret() {
		t.Fatalf("user: %+v", p)
	}
	if p := parsePrompt("Password: "); !p.secret() || p.def != "" {
		t.Fatalf("password: %+v", p)
	}
	q := parsePrompt("The identity of the remote computer (box) is unknown.\n[1] Log In Anyway\n[2] Cancel Login\nChoice: ")
	if q.label != "Choice" || len(q.choices) != 2 || q.choices[0] != "Log In Anyway" || len(q.context) != 1 {
		t.Fatalf("question: %+v", q)
	}
	if got := connectError("smb://h/s", "gio: smb://h/s/: Failed to mount Windows share: Connection refused\n", nil).Error(); got != "Failed to mount Windows share: Connection refused" {
		t.Errorf("error: %q", got)
	}
	if got := connectError("smb://h/s", "gio: smb://h/s/: Location is not mountable\n", nil).Error(); !strings.Contains(got, "gvfs-smb") {
		t.Errorf("missing backend: %q", got)
	}
}

// fakeGio is a gio that asks for a user and a password (accepting "s3cret"),
// logging its arguments and answers.
func fakeGio(t *testing.T) string {
	log := filepath.Join(t.TempDir(), "gio.log")
	fakeTool(t, "gio", `echo "$@" >> `+log+`
[ "$1" = mount ] || exit 0
echo "Authentication Required"
echo "Enter user and password for share “photos” on “nas”:"
printf 'User [me]: '
read u; echo "user=$u" >> `+log+`
for i in 1 2 3; do
  printf 'Password: '
  IFS= read -r p || exit 2
  echo "pass=$p" >> `+log+`
  [ "$p" = " s3cret" ] && exit 0
done
echo "gio: smb://nas/photos/: Failed to mount Windows share: Permission denied" >&2
exit 2
`)
	return log
}

// drive runs cmd and feeds its message to the model, as Bubble Tea would.
func drive1(t *testing.T, m model, cmd tea.Cmd) (model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command to run")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		next, c := m.Update(msg)
		return next.(model), c
	case <-time.After(5 * time.Second):
		t.Fatal("command hung")
	}
	return m, nil
}

// mkey presses a key in the open modal.
func mkey(t *testing.T, m model, k string) model {
	t.Helper()
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	}
	m, _ = m.handleModalKey(msg)
	return m
}

func typeText(m model, s string) model {
	for _, r := range s {
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			k = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		m, _ = m.handleModalKey(k)
	}
	return m
}

func TestConnectRelaysPrompts(t *testing.T) {
	log := fakeGio(t)
	share := t.TempDir()
	mkfile(t, filepath.Join(share, "beach.jpg"), "x")
	gioLocalPath = func(uri string) string { return share }
	t.Cleanup(func() { gioLocalPath = func(string) string { return "" } })

	m := testModel(t, t.TempDir())
	cmd := m.connect(`\\nas\photos`, false)
	if m.modal == nil || m.modal.purpose != "connecting" {
		t.Fatalf("connecting modal: %+v", m.modal)
	}
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || m.modal.purpose != "connectp" || m.modal.secret || m.modal.in.value() != "me" ||
		!strings.Contains(m.modal.label, "share “photos” on “nas”") || !m.modal.guest {
		t.Fatalf("user prompt: %+v", m.modal)
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter}) // the default user
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || !m.modal.secret {
		t.Fatalf("password prompt: %+v", m.modal)
	}
	m = typeText(m, "wrong")
	if body := ansi.Strip(m.View()); strings.Contains(body, "wrong") || !strings.Contains(body, "•••••") {
		t.Fatalf("the password should show as dots:\n%s", body)
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || !strings.HasPrefix(m.modal.label, "That didn't work") {
		t.Fatalf("retry: %+v", m.modal)
	}
	m = typeText(m, " s3cret") // a leading space is part of the password
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if m.modal != nil || m.cwd != share || m.msgErr || !strings.Contains(m.msg, "connected to photos on nas") {
		t.Fatalf("connected: cwd %s modal %+v msg %q", m.cwd, m.modal, m.msg)
	}
	if len(m.cfg.Servers) != 1 || m.cfg.Servers[0] != "smb://nas/photos" || m.conn != nil {
		t.Fatalf("saved: %v", m.cfg.Servers)
	}
	b, _ := os.ReadFile(log)
	if got := string(b); !strings.Contains(got, "mount smb://nas/photos\nuser=me\npass=wrong\npass= s3cret") {
		t.Fatalf("gio saw:\n%s", got)
	}
}

func TestConnectFailsAndCancels(t *testing.T) {
	fakeGio(t)
	m := testModel(t, t.TempDir())
	cmd := m.connect("smb://nas/photos", false)
	m, _ = drive1(t, m, cmd)
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	for i := 0; i < 3; i++ { // three wrong passwords
		m, _ = drive1(t, m, cmd)
		m = typeText(m, "no")
		m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	m, _ = drive1(t, m, cmd)
	if m.modal != nil || !m.msgErr || m.msg != "photos on nas: Failed to mount Windows share: Permission denied" || len(m.cfg.Servers) != 0 {
		t.Fatalf("failure: modal %+v msg %q servers %v", m.modal, m.msg, m.cfg.Servers)
	}

	// esc at a prompt stops gio; nothing it says afterwards counts.
	cmd = m.connect("smb://nas/photos", false)
	m, _ = drive1(t, m, cmd)
	c := m.conn
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.modal != nil || m.conn != nil || m.msg != "connection cancelled" {
		t.Fatalf("cancel: %+v %q", m.modal, m.msg)
	}
	m, _ = drive1(t, m, c.wait())
	if m.msg != "connection cancelled" {
		t.Fatalf("a cancelled connection reported: %q", m.msg)
	}
}

func TestConnectAsGuestAndQuestions(t *testing.T) {
	log := filepath.Join(t.TempDir(), "gio.log")
	fakeTool(t, "gio", `echo "$@" >> `+log+`
if [ "$2" = -a ]; then exit 0; fi
echo "The identity of the remote computer (box) is unknown."
echo "[1] Log In Anyway"
echo "[2] Cancel Login"
printf 'Choice: '
read c; echo "choice=$c" >> `+log+`
printf 'Password: '
read p
exit 2
`)
	m := testModel(t, t.TempDir())
	cmd := m.connect("ftp://box/", false)
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || m.modal.kind != mMenu || m.modal.purpose != "connectq" || len(m.modal.items) != 2 ||
		!strings.Contains(ansi.Strip(m.View()), "identity of the remote computer") {
		t.Fatalf("question: %+v", m.modal)
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || !m.modal.secret {
		t.Fatalf("password after the question: %+v", m.modal)
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	m, _ = drive1(t, m, cmd)
	b, _ := os.ReadFile(log)
	if got := string(b); !strings.Contains(got, "choice=1") || !strings.Contains(got, "mount -a ftp://box/") {
		t.Fatalf("gio saw:\n%s", got)
	}
	if !strings.Contains(m.msg, "connected to box") && !strings.Contains(m.msg, "no folder to open") {
		t.Fatalf("guest connection: %q", m.msg)
	}
}

func TestPlacesSections(t *testing.T) {
	m, home := homeModel(t)
	stick := t.TempDir()
	devs, _ := parseLsblk([]byte(strings.ReplaceAll(lsblkFixture, "/run/media/me/STICK", stick)))
	blockDevices = func() ([]blockDev, error) { return devs, nil }
	share := t.TempDir()
	gvfsMounts = func() []netMount {
		return []netMount{{name: "photos on nas", uri: "smb://nas/photos/", path: share}}
	}
	t.Cleanup(func() {
		blockDevices = func() ([]blockDev, error) { return nil, nil }
		gvfsMounts = func() []netMount { return nil }
	})
	os.Mkdir(filepath.Join(home, "proj"), 0o755)
	m.cfg.Bookmarks = []string{filepath.Join(home, "proj"), filepath.Join(home, "gone")}
	m.cfg.Servers = []string{"smb://nas/photos", "sftp://me@box/srv"}

	m = press(t, m, "b")
	body := ansi.Strip(m.View())
	for _, want := range []string{"Bookmarks", "proj", "missing · ~/gone", "Drives", "Computer", "New Volume", "762.8 GiB · ntfs",
		"locked", "STICK", "Network", "photos on nas", "srv on me@box", "Connect to server…"} {
		if !strings.Contains(body, want) {
			t.Errorf("places is missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "photos on nas") != 1 {
		t.Errorf("a connected server is listed once:\n%s", body)
	}
	var order []string
	for _, p := range m.modal.places {
		if len(order) == 0 || order[len(order)-1] != p.section {
			order = append(order, p.section)
		}
	}
	if strings.Join(order, ",") != ",Bookmarks,Drives,Network" {
		t.Fatalf("sections: %v", order)
	}

	at := func(name string) int {
		for i, p := range m.modal.places {
			if p.name == name {
				return i
			}
		}
		t.Fatalf("no place %q", name)
		return -1
	}
	m.modal.sel = at("srv on me@box")
	m = mkey(t, m, "d") // forget a saved server
	if len(m.cfg.Servers) != 1 || m.modal == nil || m.modal.purpose != "places" {
		t.Fatalf("forget: %v", m.cfg.Servers)
	}
	m.modal.sel = at("gone")
	m = mkey(t, m, "d")
	if len(m.cfg.Bookmarks) != 1 {
		t.Fatalf("remove bookmark: %v", m.cfg.Bookmarks)
	}
	m.modal.sel = at("STICK")
	m = mkey(t, m, "enter")
	if m.cwd != stick {
		t.Fatalf("open a mounted drive: %s", m.cwd)
	}
	m = press(t, m, "b")
	m.modal.sel = at("Connect to server…")
	m = mkey(t, m, "enter")
	if m.modal == nil || m.modal.purpose != "connect" {
		t.Fatalf("connect row: %+v", m.modal)
	}
	m = typeText(m, "nas")
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modal == nil || m.modal.purpose != "connect" || !strings.Contains(m.modal.label, "smb://") || m.modal.in.value() != "nas" {
		t.Fatalf("a bare host is explained, keeping what was typed: %+v", m.modal)
	}
	m.modal = nil
	m.cfg.Servers = nil
	m.connect("smb://nas/photos", false) // already connected: just opens it
	if m.cwd != share || m.conn != nil || len(m.cfg.Servers) != 1 {
		t.Fatalf("open a connected location: %s", m.cwd)
	}
}

func TestBookmarkToggleAndStar(t *testing.T) {
	m, home := homeModel(t)
	os.Mkdir(filepath.Join(home, "proj"), 0o755)
	m.chdir(filepath.Join(home, "proj"), "")
	m = press(t, m, "B")
	if !m.isBookmarked(filepath.Join(home, "proj")) {
		t.Fatal("B bookmarks")
	}
	m = press(t, m, "left")
	if !strings.Contains(ansi.Strip(m.View()), "proj/") || !strings.Contains(ansi.Strip(m.View()), "★") {
		t.Fatalf("bookmarked folders show a star:\n%s", ansi.Strip(m.View()))
	}
	m = press(t, m, "enter", "B")
	if m.isBookmarked(filepath.Join(home, "proj")) || !strings.Contains(m.msg, "removed bookmark") {
		t.Fatalf("B again removes it: %v %q", m.cfg.Bookmarks, m.msg)
	}
}

// fakeUdisks logs udisksctl calls (and stdin) and plays a drive at mnt.
func fakeUdisks(t *testing.T) string {
	log := filepath.Join(t.TempDir(), "udisks.log")
	fakeTool(t, "udisksctl", `echo "$@" >> `+log+`
case "$1" in
unlock) printf 'key=' >> `+log+`; cat >> `+log+`; echo >> `+log+`; echo "Unlocked $3 as /dev/dm-9." ;;
unmount) [ -f `+log+`.busy ] && { echo "Error unmounting $3: GDBus.Error:org.freedesktop.UDisks2.Error.DeviceBusy: target is busy" >&2; exit 1; } ;;
esac
exit 0
`)
	return log
}

func TestMountUnlockUnmount(t *testing.T) {
	log := fakeUdisks(t)
	m, home := homeModel(t)
	mnt := t.TempDir()
	mounted := map[string]string{}
	unlocked := false
	blockDevices = func() ([]blockDev, error) {
		devs := []blockDev{
			{Name: "sdb", Path: "/dev/sdb", Type: "disk", Removable: true},
			{Name: "sdb1", Path: "/dev/sdb1", Parent: "sdb", Type: "part", FSType: "crypto_LUKS", Size: 1 << 30},
		}
		if unlocked {
			mp := mounted["/dev/dm-9"]
			var mps []string
			if mp != "" {
				mps = []string{mp}
			}
			devs = append(devs, blockDev{Name: "luks-1", Path: "/dev/dm-9", Parent: "sdb1", Type: "crypt", FSType: "ext4", Label: "Backup", Mountpoints: mps})
		}
		return devs, nil
	}
	t.Cleanup(func() { blockDevices = func() ([]blockDev, error) { return nil, nil } })

	m = press(t, m, "b")
	sel := -1
	for i, p := range m.modal.places {
		if p.kind == placeDrive {
			sel = i
		}
	}
	m.modal.sel = sel
	m = mkey(t, m, "enter")
	if m.modal == nil || m.modal.purpose != "unlock" || !m.modal.secret {
		t.Fatalf("locked drive asks for the passphrase: %+v", m.modal)
	}
	m = typeText(m, "open sesame")
	unlocked = true
	mounted["/dev/dm-9"] = mnt // udisksctl mount, as the fake can't change our stub
	m, cmd := m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if m.cwd != mnt || m.msgErr {
		t.Fatalf("opened %s, msg %q", m.cwd, m.msg)
	}
	b, _ := os.ReadFile(log)
	if got := string(b); !strings.Contains(got, "unlock -b /dev/sdb1 --key-file /dev/stdin\nkey=open sesame\n") {
		t.Fatalf("udisksctl saw:\n%s", got)
	}

	// u unmounts, locks and powers off; the tab inside goes home first.
	m = press(t, m, "b")
	for i, p := range m.modal.places {
		if p.name == "Backup" {
			m.modal.sel = i
		}
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if m.cwd != home {
		t.Fatalf("the tab should leave the drive: %s", m.cwd)
	}
	delete(mounted, "/dev/dm-9")
	m, _ = drive1(t, m, cmd)
	b, _ = os.ReadFile(log)
	if got := string(b); !strings.Contains(got, "unmount -b /dev/dm-9\nlock -b /dev/sdb1\npower-off -b /dev/sdb\n") || m.msg != "Backup can be removed safely" {
		t.Fatalf("unmount: %q\n%s", m.msg, got)
	}

	mkfile(t, log+".busy", "")
	mounted["/dev/dm-9"] = mnt
	dr, _ := findDrive("/dev/dm-9")
	m, _ = drive1(t, m, unmountDrive(dr))
	if !m.msgErr || !strings.Contains(m.msg, "in use") {
		t.Fatalf("busy: %q", m.msg)
	}
}

func TestUdisksErrors(t *testing.T) {
	for out, want := range map[string]string{
		"Error mounting /dev/sdb1: GDBus.Error:org.freedesktop.UDisks2.Error.NotAuthorizedCanObtain: Not authorized to perform operation": "not authorized",
		"Error mounting /dev/sdb1: GDBus.Error:org.freedesktop.UDisks2.Error.Failed: Error mounting: wrong fs type":                       "Error mounting: wrong fs type",
		"Error unlocking /dev/sdb1: GDBus.Error:org.freedesktop.UDisks2.Error.Failed: Error unlocking: Failed to activate device":         "wrong passphrase",
	} {
		if got := udisksError(out, nil); !strings.HasPrefix(got, want) {
			t.Errorf("%s:\n got %q", out, got)
		}
	}
}

func TestDisconnectNetwork(t *testing.T) {
	log := filepath.Join(t.TempDir(), "gio.log")
	fakeTool(t, "gio", `echo "$@" >> `+log+"\n")
	m, home := homeModel(t)
	share := t.TempDir()
	gvfsMounts = func() []netMount { return []netMount{{name: "photos on nas", uri: "smb://nas/photos/", path: share}} }
	t.Cleanup(func() { gvfsMounts = func() []netMount { return nil } })
	m.chdir(share, "")
	m.newTab()
	m = press(t, m, "b")
	for i, p := range m.modal.places {
		if p.kind == placeNet {
			m.modal.sel = i
		}
	}
	m, cmd := m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if m.cwd != home || m.tabs[0].cwd != home {
		t.Fatalf("tabs should leave the share: %s %s", m.cwd, m.tabs[0].cwd)
	}
	m, _ = drive1(t, m, cmd)
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "mount -u smb://nas/photos/") || m.msg != "disconnected photos on nas" {
		t.Fatalf("disconnect: %q %s", m.msg, b)
	}
}

// A busy location is tried again, then named with what uses it; the next
// disconnect forces it.
func TestDisconnectBusy(t *testing.T) {
	log := filepath.Join(t.TempDir(), "gio.log")
	fakeTool(t, "gio", `echo "$@" >> `+log+`
case "$*" in *-f*) exit 0 ;; esac
echo "gio: smb://nas/photos/: File system is busy" >&2; exit 2
`)
	orig := busyRetry
	busyRetry = 0
	t.Cleanup(func() { busyRetry = orig })
	share := t.TempDir()
	m := testModel(t, t.TempDir())
	nm := netMount{name: "photos on nas", uri: "smb://nas/photos/", path: share}
	m, _ = drive1(t, m, m.disconnect(nm))
	b, _ := os.ReadFile(log)
	if strings.Count(string(b), "mount -u smb://nas/photos/") != 2 || !strings.Contains(m.msg, "photos on nas is in use") ||
		!strings.Contains(m.msg, "disconnect again to force it") || m.forceDisc != nm.uri {
		t.Fatalf("busy: %q %q\n%s", m.msg, m.forceDisc, b)
	}
	m, _ = drive1(t, m, m.disconnect(nm))
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "mount -u -f smb://nas/photos/") || m.msg != "disconnected photos on nas" || m.forceDisc != "" {
		t.Fatalf("forced: %q\n%s", m.msg, b)
	}
}

func TestMountUsers(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := mountUsers(dir); len(got) != 1 || got[0] != "tidefiles" {
		t.Fatalf("users: %v", got)
	}
	if got := mountUsers(t.TempDir()); len(got) != 0 {
		t.Fatalf("unused: %v", got)
	}
}

func TestSystemNetMounts(t *testing.T) {
	got := systemNetMounts("//nas/photos /mnt/photos cifs rw 0 0\nme@box:/srv /home/me/box fuse.sshfs rw 0 0\ngvfsd-fuse /run/user/1000/gvfs fuse.gvfsd-fuse rw 0 0\nnas:/export /mnt/my\\040nfs nfs4 rw 0 0\n")
	if len(got) != 3 || got[0].path != "/mnt/photos" || got[1].name != "me@box:/srv" || got[2].path != "/mnt/my nfs" || !got[0].system {
		t.Fatalf("%+v", got)
	}
}

func TestKeyringEntryMatchesGvfs(t *testing.T) {
	for _, c := range []struct{ uri, user, domain, want string }{
		{"smb://nas/photos", "me", "WORKGROUP", "domain=WORKGROUP protocol=smb server=nas user=me"},
		{"smb://HOME;me@nas:4450/photos", "", "", "domain=HOME port=4450 protocol=smb server=nas user=me"},
		{"sftp://me@box/srv", "", "", "authtype=password protocol=sftp server=box user=me"},
		{"sftp://box:22/", "", "", "authtype=password protocol=sftp server=box"},
		{"ftp://tester@127.0.0.1:2121/", "", "", "port=2121 protocol=ftp server=127.0.0.1 user=tester"},
	} {
		e, ok := keyringEntry(c.uri, c.user, c.domain)
		var got []string
		for _, k := range []string{"authtype", "domain", "port", "protocol", "server", "user"} {
			if v, has := e.attrs[k]; has {
				got = append(got, k+"="+v)
			}
		}
		if !ok || strings.Join(got, " ") != c.want || e.attrs["xdg:schema"] != "org.gnome.keyring.NetworkPassword" {
			t.Errorf("%s: %v", c.uri, e.attrs)
		}
	}
	if _, ok := keyringEntry("davs://cloud/dav", "me", ""); ok {
		t.Error("WebDAV passwords can't be saved where gvfs finds them")
	}
}

func TestConnectRemembersPassword(t *testing.T) {
	fakeGio(t)
	var saved netSecret
	var savedPw string
	orig := saveSecret
	saveSecret = func(s netSecret, pw string) error { saved, savedPw = s, pw; return nil }
	t.Cleanup(func() { saveSecret = orig })
	m := testModel(t, t.TempDir())
	cmd := m.connect("smb://nas/photos", false)
	m, _ = drive1(t, m, cmd)
	m.modal.in = newInput("alice")
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if !strings.Contains(ansi.Strip(m.View()), "☐ Remember the password") {
		t.Fatalf("remember box:\n%s", ansi.Strip(m.View()))
	}
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !strings.Contains(ansi.Strip(m.View()), "☑ Remember the password") {
		t.Fatal("ctrl+s ticks it")
	}
	m = typeText(m, " s3cret")
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd = drive1(t, m, cmd)
	m, _ = drive1(t, m, cmd)
	if savedPw != " s3cret" || saved.attrs["user"] != "alice" || saved.attrs["server"] != "nas" || !strings.Contains(m.msg, "password saved") {
		t.Fatalf("saved %q %v, msg %q", savedPw, saved.attrs, m.msg)
	}
	if m.conn != nil {
		t.Fatal("connection kept after it finished")
	}
}

const avahiFixture = `+;wlan0;IPv4;nas;_smb._tcp;local
=;wlan0;IPv4;Family\032NAS;_smb._tcp;local;nas.local;192.168.1.5;445;"model=Xserve"
=;wlan0;IPv6;Family\032NAS;_smb._tcp;local;nas.local;fe80::1;445;
=;wlan0;IPv4;nas;_nfs._tcp;local;nas.local;192.168.1.5;2049;"path=/export/media"
=;wlan0;IPv4;box;_sftp-ssh._tcp;local;box.local;192.168.1.7;2222;
=;wlan0;IPv4;box;_ssh._tcp;local;box.local;192.168.1.7;2222;
=;wlan0;IPv4;Printer;_ipp._tcp;local;printer.local;192.168.1.9;631;
`

func TestParseAvahiAndShares(t *testing.T) {
	hosts := parseAvahi(avahiFixture)
	if len(hosts) != 2 {
		t.Fatalf("hosts: %+v", hosts)
	}
	nas, box := hosts[0], hosts[1]
	if nas.name != "Family NAS" || nas.addr != "192.168.1.5" || len(nas.svcs) != 2 ||
		nas.svcs[0].uri != "smb://nas.local/" || nas.svcs[1].uri != "nfs://nas.local/export/media" {
		t.Fatalf("nas: %+v", nas)
	}
	if len(box.svcs) != 1 || box.svcs[0].uri != "sftp://box.local:2222/" {
		t.Fatalf("box: %+v", box)
	}
	got := parseShares("Disk|photos|Family photos\nDisk|ADMIN$|Remote Admin\nIPC|IPC$|IPC Service\nDisk|backup|\n")
	if strings.Join(got, ",") != "photos,backup" {
		t.Fatalf("shares: %v", got)
	}
}

func TestMergeHostsNamesWindowsMachines(t *testing.T) {
	orig := netbiosName
	netbiosName = func(_ context.Context, ip string) string {
		return map[string]string{"192.168.1.179": "GAMING-PC"}[ip]
	}
	t.Cleanup(func() { netbiosName = orig })
	named := parseAvahi(avahiFixture)
	bare := []netHost{
		{name: "192.168.1.179", addr: "192.168.1.179", via: "wsd", svcs: []netService{{"smb", "smb://192.168.1.179/"}}},
		{name: "192.168.1.5", addr: "192.168.1.5", via: "netbios", svcs: []netService{{"smb", "smb://192.168.1.5/"}}},
	}
	hosts := mergeHosts(named, bare)
	var names []string
	for _, h := range hosts {
		names = append(names, h.name)
	}
	if strings.Join(names, ",") != "box,Family NAS,GAMING-PC" || len(hosts[1].svcs) != 2 {
		t.Fatalf("merged: %+v", hosts)
	}
}

func TestBrowseNetworkFlow(t *testing.T) {
	origM, origL := browseMDNS, listShares
	browseMDNS = func(context.Context) []netHost { return parseAvahi(avahiFixture) }
	listShares = func(server string) tea.Cmd {
		return func() tea.Msg { return sharesMsg{server: server, shares: []string{"photos", "backup"}} }
	}
	t.Cleanup(func() { browseMDNS, listShares = origM, origL })
	fakeGio(t)

	m := testModel(t, t.TempDir())
	m = press(t, m, "b")
	for i, p := range m.modal.places {
		if p.kind == placeBrowse {
			m.modal.sel = i
		}
	}
	m, cmd := m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modal == nil || m.modal.purpose != "browsing" {
		t.Fatalf("browsing: %+v", m.modal)
	}
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || m.modal.purpose != "browse" || len(m.modal.items) != 3 || m.modal.title != "network · 2 computers" {
		t.Fatalf("found: %+v", m.modal)
	}
	if body := ansi.Strip(m.View()); !strings.Contains(body, "Family NAS  192.168.1.5") || !strings.Contains(body, "nfs") {
		t.Fatalf("list:\n%s", body)
	}
	m.modal.sel = 1 // Family NAS · smb
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || m.modal.title != "shares on nas.local" || len(m.modal.items) != 2 {
		t.Fatalf("shares: %+v", m.modal)
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.conn == nil || m.conn.uri != "smb://nas.local/photos" {
		t.Fatalf("picking a share connects: %+v", m.conn)
	}
	m.cancelConnect()

	// Nothing found: say why, and how to connect by address.
	browseMDNS = func(context.Context) []netHost { return nil }
	cmd = m.openBrowse()
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || !strings.Contains(strings.Join(m.modal.lines, " "), "connect by address") {
		t.Fatalf("nothing found: %+v", m.modal)
	}

	// Shares a guest can't list: ask for the name.
	listShares = func(server string) tea.Cmd {
		return func() tea.Msg { return sharesMsg{server: server, err: "NT_STATUS_ACCESS_DENIED"} }
	}
	cmd = m.pickService("smb://pc/")
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || m.modal.purpose != "connect" || m.modal.in.value() != "smb://pc/" || !strings.Contains(m.modal.label, "ACCESS_DENIED") {
		t.Fatalf("unlisted shares: %+v", m.modal)
	}
}

// fakeRoot makes "root" commands run as the user: pkexec runs its arguments,
// mount and umount log theirs (and the SMB credentials file).
func fakeRoot(t *testing.T) string {
	log := filepath.Join(t.TempDir(), "root.log")
	fakeTool(t, "pkexec", `[ -f `+log+`.deny ] && exit 126
exec "$@"
`)
	fakeTool(t, "mount", `echo "mount $*" >> `+log+`
case "$4" in *credentials=*) f=${4#*credentials=}; f=${f%%,*}; cat "$f" >> `+log+`; echo "creds=$f" >> `+log+`;; esac
[ -f `+log+`.fail ] && { echo "mount error(13): Permission denied" >&2; exit 32; }
exit 0
`)
	fakeTool(t, "umount", `echo "umount $*" >> `+log+"\n")
	origH, origR := hasMountHelper, runPrivileged
	hasMountHelper = func(string) bool { return true }
	runPrivileged = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
		return func() tea.Msg { return done(c.Run()) }
	}
	t.Cleanup(func() { hasMountHelper, runPrivileged = origH, origR })
	return log
}

func TestSystemMountSMB(t *testing.T) {
	log := fakeRoot(t)
	m := testModel(t, t.TempDir())
	mp := filepath.Join(t.TempDir(), "photos")
	m.openConnect("", "")
	m = typeText(m, `\\nas\photos`)
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.modal == nil || m.modal.purpose != "rootmp" || m.modal.in.value() != "/mnt/photos" || !strings.Contains(m.modal.label, "//nas/photos") {
		t.Fatalf("mountpoint question: %+v", m.modal)
	}
	m.modal.in = newInput("/home")
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modal == nil || m.modal.purpose != "rootmp" || !strings.Contains(m.modal.label, "system folder") {
		t.Fatalf("system folders are refused: %+v", m.modal)
	}
	m.modal.in = newInput(mp)
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modal == nil || m.modal.purpose != "rootuser" {
		t.Fatalf("user question: %+v", m.modal)
	}
	m.modal.in = newInput(`HOME\alice`)
	m, _ = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modal == nil || m.modal.purpose != "rootpass" || !m.modal.secret {
		t.Fatalf("password question: %+v", m.modal)
	}
	m = typeText(m, "pa ss")
	m, cmd := m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if m.cwd != mp || m.msgErr || !strings.Contains(m.msg, "mounted //nas/photos at "+mp) {
		t.Fatalf("mounted: cwd %s msg %q", m.cwd, m.msg)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	if !strings.Contains(got, "mount -t cifs -o uid=") || !strings.Contains(got, "-- //nas/photos "+mp) ||
		!strings.Contains(got, "username=alice\npassword=pa ss\ndomain=HOME\n") || strings.Contains(strings.SplitN(got, "\n", 2)[0], "pa ss") {
		t.Fatalf("mount saw:\n%s", got)
	}
	creds := got[strings.Index(got, "creds=")+6:]
	if _, err := os.Stat(strings.TrimSpace(creds)); err == nil {
		t.Fatal("the credentials file should be gone after mounting")
	}
}

func TestSystemMountNFSAndErrors(t *testing.T) {
	log := fakeRoot(t)
	m := testModel(t, t.TempDir())
	if _, err := newRootMount("smb://nas"); err == nil || !strings.Contains(err.Error(), "share") {
		t.Errorf("smb without a share: %v", err)
	}
	if _, err := newRootMount("sftp://box/srv"); err == nil {
		t.Error("sftp can't be a system mount here")
	}
	hasMountHelper = func(string) bool { return false }
	if _, err := newRootMount("nfs://nas/export"); err == nil || !strings.Contains(err.Error(), "nfs-utils") {
		t.Errorf("missing helper: %v", err)
	}
	hasMountHelper = func(string) bool { return true }
	full := t.TempDir()
	mkfile(t, filepath.Join(full, "x"), "")
	if err := checkMountpoint(full); err == nil || !strings.Contains(err.Error(), "isn't empty") {
		t.Errorf("non-empty folder: %v", err)
	}

	mp := filepath.Join(t.TempDir(), "media")
	m.startRootMount("nfs://nas/export/media")
	m.modal.in = newInput(mp)
	m, cmd := m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter}) // NFS asks nothing more
	m, _ = drive1(t, m, cmd)
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "mount -t nfs -o defaults -- nas:/export/media "+mp) || m.cwd != mp {
		t.Fatalf("nfs: %q\n%s", m.msg, b)
	}

	mkfile(t, log+".fail", "")
	m.startRootMount("nfs://nas/export/other")
	m.modal.in = newInput(filepath.Join(t.TempDir(), "other"))
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if !m.msgErr || m.msg != "system mount of nas:/export/other: mount error(13): Permission denied" {
		t.Fatalf("failure: %q", m.msg)
	}
	mkfile(t, log+".deny", "")
	m.startRootMount("nfs://nas/export/other")
	m.modal.in = newInput(filepath.Join(t.TempDir(), "other"))
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if !strings.HasSuffix(m.msg, "not authorized") {
		t.Fatalf("dismissed: %q", m.msg)
	}
}

func TestUnmountSystemMount(t *testing.T) {
	log := fakeRoot(t)
	m, home := homeModel(t)
	share := t.TempDir()
	mountsText = func() string { return "//nas/photos " + share + " cifs rw 0 0\n" }
	t.Cleanup(func() { mountsText = func() string { return "" } })
	m.chdir(share, "")
	m = press(t, m, "b")
	for i, p := range m.modal.places {
		if p.kind == placeNet {
			m.modal.sel = i
		}
	}
	m, cmd := m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if m.cwd != home {
		t.Fatalf("leave the mount first: %s", m.cwd)
	}
	m, _ = drive1(t, m, cmd)
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "umount -- "+share) || m.msg != "unmounted "+share {
		t.Fatalf("umount: %q\n%s", m.msg, b)
	}
}

func TestFirewallDetection(t *testing.T) {
	dir := t.TempDir()
	origC, origR := ufwConf, ufwRules
	ufwConf, ufwRules = filepath.Join(dir, "ufw.conf"), filepath.Join(dir, "user.rules")
	t.Cleanup(func() { ufwConf, ufwRules = origC, origR })
	lan := "192.168.1.0/24"
	if discoveryBlocked(lan) {
		t.Fatal("no ufw: nothing blocked")
	}
	mkfile(t, ufwConf, "ENABLED=yes\n")
	mkfile(t, ufwRules, "### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 in\n### tuple ### allow udp 3702 0.0.0.0/0 any 0.0.0.0/0 in\n")
	if !discoveryBlocked(lan) {
		t.Fatal("a destination-port rule doesn't let replies in")
	}
	mkfile(t, ufwRules, "### tuple ### allow udp any 0.0.0.0/0 3702 192.168.1.0/24 in comment=7469\n### tuple ### allow udp any 0.0.0.0/0 137 192.168.1.0/24 in\n")
	if discoveryBlocked(lan) {
		t.Fatal("both source-port rules: nothing blocked")
	}
	mkfile(t, ufwRules, "### tuple ### allow any any 0.0.0.0/0 any 192.168.1.0/24 in\n")
	if discoveryBlocked(lan) {
		t.Fatal("everything from the LAN allowed: nothing blocked")
	}
	mkfile(t, ufwConf, "ENABLED=no\n")
	mkfile(t, ufwRules, "")
	if discoveryBlocked(lan) {
		t.Fatal("ufw off: nothing blocked")
	}
}

func TestFirewallDialog(t *testing.T) {
	log := fakeRoot(t)
	fakeTool(t, "ufw", `echo "ufw $*" >> `+log+"\n")
	dir := t.TempDir()
	origC, origR, origL := ufwConf, ufwRules, localLAN
	ufwConf, ufwRules = filepath.Join(dir, "ufw.conf"), filepath.Join(dir, "user.rules")
	localLAN = func() string { return "192.168.1.0/24" }
	t.Cleanup(func() { ufwConf, ufwRules, localLAN = origC, origR, origL })
	mkfile(t, ufwConf, "ENABLED=yes\n")
	mkfile(t, ufwRules, "")

	m := testModel(t, t.TempDir())
	cmd := m.openBrowse()
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || !strings.Contains(strings.Join(m.modal.lines, " "), "f explains") {
		t.Fatalf("offer: %+v", m.modal)
	}
	m = mkey(t, m, "f")
	body := strings.Join(m.modal.lines, "\n")
	if m.modal.purpose != "allowfw" || !strings.Contains(body, "ufw allow proto udp from 192.168.1.0/24 port 3702") ||
		!strings.Contains(body, "f removes them") || !strings.Contains(body, "Windows PCs answer") {
		t.Fatalf("explanation:\n%s", body)
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.modal != nil || cmd != nil {
		t.Fatal("no changes nothing")
	}
	m = press(t, m, "b") // ask again, say yes
	cmd = m.openBrowse()
	m, _ = drive1(t, m, cmd)
	m = mkey(t, m, "f")
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m, cmd = drive1(t, m, cmd)
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "ufw allow proto udp from 192.168.1.0/24 port 3702 comment tidefiles discovery\nufw allow proto udp from 192.168.1.0/24 port 137 comment tidefiles discovery") {
		t.Fatalf("ufw saw:\n%s", b)
	}
	if m.modal == nil || m.modal.purpose != "browsing" || cmd == nil {
		t.Fatalf("browses again afterwards: %+v", m.modal)
	}
}

func TestFirewallRemove(t *testing.T) {
	log := fakeRoot(t)
	fakeTool(t, "ufw", `echo "ufw $*" >> `+log+"\n")
	dir := t.TempDir()
	origC, origR, origL := ufwConf, ufwRules, localLAN
	ufwConf, ufwRules = filepath.Join(dir, "ufw.conf"), filepath.Join(dir, "user.rules")
	localLAN = func() string { return "192.168.1.0/24" }
	t.Cleanup(func() { ufwConf, ufwRules, localLAN = origC, origR, origL })
	mkfile(t, ufwConf, "ENABLED=yes\n")
	ours := "comment=" + hex.EncodeToString([]byte(fwComment))
	// Ours from two networks, and the user's own rule for the same port.
	mkfile(t, ufwRules, "### tuple ### allow udp any 0.0.0.0/0 3702 192.168.1.0/24 in "+ours+"\n"+
		"### tuple ### allow udp any 0.0.0.0/0 137 192.168.1.0/24 in "+ours+"\n"+
		"### tuple ### allow udp any 0.0.0.0/0 137 10.0.0.0/8 in "+ours+"\n"+
		"### tuple ### allow udp any 0.0.0.0/0 137 172.16.0.0/12 in\n")

	m := testModel(t, t.TempDir())
	cmd := m.openBrowse()
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || !strings.Contains(strings.Join(m.modal.lines, " "), "f undoes it") {
		t.Fatalf("offer: %+v", m.modal)
	}
	m = mkey(t, m, "f")
	body := strings.Join(m.modal.lines, "\n")
	if m.modal.purpose != "removefw" || !strings.Contains(body, "ufw delete allow proto udp from 10.0.0.0/8 port 137") ||
		strings.Contains(body, "172.16") {
		t.Fatalf("remove dialog:\n%s", body)
	}
	for _, l := range m.modal.lines {
		if n := len([]rune(l)); n > 62 {
			t.Errorf("%d wide: %q", n, l)
		}
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m, _ = drive1(t, m, cmd)
	b, _ := os.ReadFile(log)
	want := "ufw delete allow proto udp from 192.168.1.0/24 port 3702\n" +
		"ufw delete allow proto udp from 192.168.1.0/24 port 137\n" +
		"ufw delete allow proto udp from 10.0.0.0/8 port 137\n"
	if !strings.HasSuffix(string(b), want) || m.msg != "firewall: removed the discovery rules tidefiles added" {
		t.Fatalf("ufw saw (%q):\n%s", m.msg, b)
	}
}

func TestInstallDialog(t *testing.T) {
	log := fakeRoot(t)
	fakeTool(t, "pacman", `echo "pacman $*" >> `+log+"\n")
	orig := packageManager
	packageManager = func() string { return "pacman" }
	t.Cleanup(func() { packageManager = orig })
	hasMountHelper = func(string) bool { return false }

	m := testModel(t, t.TempDir())
	m.startRootMount("nfs://nas/export")
	body := strings.Join(m.modal.lines, "\n")
	if m.modal == nil || m.modal.purpose != "install" || !strings.Contains(body, "mount.nfs") || !strings.Contains(body, "pacman -S --needed nfs-utils") {
		t.Fatalf("install offer: %+v", m.modal)
	}
	m, cmd := m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m, _ = drive1(t, m, cmd)
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "pacman -S --needed nfs-utils") || m.msg != "installed nfs-utils · try again" {
		t.Fatalf("install: %q\n%s", m.msg, b)
	}

	// A gvfs backend that's missing, reported by gio.
	err := connectError("smb://h/s", "gio: smb://h/s/: Location is not mountable\n", nil)
	if !m.offerInstall(err) || !strings.Contains(strings.Join(m.modal.lines, " "), "pacman -S --needed gvfs-smb") {
		t.Fatalf("gvfs-smb: %v %+v", err, m.modal)
	}
	// No known package manager: just say what's missing.
	packageManager = func() string { return "" }
	m.modal = nil
	if !m.offerInstall(missing("udisks", "needs udisks")) || m.modal != nil || m.msg != "needs udisks" {
		t.Fatalf("no package manager: %+v %q", m.modal, m.msg)
	}
}

func TestGioWarningsStayOutOfPrompts(t *testing.T) {
	fakeTool(t, "gio", `echo "(gio mount:1): GVFS-RemoteVolumeMonitor-WARNING **: not supported" >&2
echo "Enter password for “demo” on “box”:"
printf 'Password: '
read p
echo "gio: sftp://box/: Permission denied" >&2
exit 2
`)
	m := testModel(t, t.TempDir())
	cmd := m.connect("sftp://box/", false)
	m, _ = drive1(t, m, cmd)
	if m.modal == nil || strings.Contains(m.modal.label, "WARNING") || !strings.Contains(m.modal.label, "Enter password") {
		t.Fatalf("prompt: %+v", m.modal)
	}
	m, cmd = m.handleModalKey(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drive1(t, m, cmd)
	if m.msg != "box: Permission denied" {
		t.Fatalf("the error still comes from stderr: %q", m.msg)
	}
}

func TestBrowseResultsConnectByAddress(t *testing.T) {
	orig := browseMDNS
	browseMDNS = func(context.Context) []netHost { return parseAvahi(avahiFixture) }
	t.Cleanup(func() { browseMDNS = orig })
	m := testModel(t, t.TempDir())
	cmd := m.openBrowse()
	m, _ = drive1(t, m, cmd)
	if !strings.Contains(strings.Join(m.modal.lines, " "), "c connects by address") {
		t.Fatalf("hint: %v", m.modal.lines)
	}
	m = mkey(t, m, "c")
	if m.modal == nil || m.modal.purpose != "connect" {
		t.Fatalf("c: %+v", m.modal)
	}
	browseMDNS = func(context.Context) []netHost { return nil }
	cmd = m.openBrowse()
	m, _ = drive1(t, m, cmd)
	m = mkey(t, m, "c") // the "nothing found" text says c too
	if m.modal == nil || m.modal.purpose != "connect" {
		t.Fatalf("c when nothing was found: %+v", m.modal)
	}
}

func TestFirewallDialogFits(t *testing.T) {
	orig := localLAN
	localLAN = func() string { return "192.168.100.0/24" }
	t.Cleanup(func() { localLAN = orig })
	m := testModel(t, t.TempDir())
	m.openFirewallAsk()
	for _, l := range m.modal.lines {
		if n := len([]rune(l)); n > 62 { // the modal's text width
			t.Errorf("%d wide: %q", n, l)
		}
	}
}
