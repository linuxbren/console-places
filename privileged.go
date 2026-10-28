package main

// Running commands as root: installing a missing package and adding firewall
// rules go through pkexec (the desktop's password dialog, or a text prompt in
// this terminal when there is no agent) or sudo, with the terminal handed
// over while it asks.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// sudoTool is how console-places becomes root: pkexec, else sudo.
var sudoTool = func() (string, error) {
	for _, t := range []string{"pkexec", "sudo"} {
		if p, err := exec.LookPath(t); err == nil {
			return p, nil
		}
	}
	return "", errors.New("this needs pkexec (polkit) or sudo")
}

// runPrivileged runs a command with the terminal handed over (a variable so
// tests can run it directly).
var runPrivileged = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	return tea.ExecProcess(c, done)
}

// privScript says what is about to happen, then runs the rest of its
// arguments as root through the tool in $2. Only the command's own errors go
// to the file in $3 (the screen is redrawn afterwards); the password prompt
// stays on the terminal.
const privScript = `printf '\nconsole-places: %s (as root)\n\n' "$1"
tool=$2 err=$3
shift 3
exec "$tool" /bin/sh -c '"$@" 2>"$0"' "$err" "$@"
`

// privErrFile makes the file a root command's errors go to, inside a private
// folder. Not straight in /tmp: with fs.protected_regular (systemd's default)
// even root may not open a file someone else owns in a sticky folder, so
// `2>file` would fail and the command never run. done removes the folder.
func privErrFile(name string) (file string, done func(), err error) {
	dir, err := os.MkdirTemp(os.Getenv("XDG_RUNTIME_DIR"), "console-places-"+name+"-")
	if err != nil {
		dir, err = os.MkdirTemp("", "console-places-"+name+"-")
	}
	if err != nil {
		return "", nil, err
	}
	file = filepath.Join(dir, "error")
	_ = os.WriteFile(file, nil, 0o600)
	return file, func() { os.RemoveAll(dir) }, nil
}

// privileged builds the command that runs args as root.
func privileged(say, errFile string, args ...string) (*exec.Cmd, error) {
	tool, err := sudoTool()
	if err != nil {
		return nil, err
	}
	return exec.Command("sh", append([]string{"-c", privScript, "sh", say, tool, errFile}, args...)...), nil
}

// lastLine is the last line of a command's output, for an error message.
func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}
