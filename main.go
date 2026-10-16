// console-places is a keyboard-first terminal file manager:
// three panes, a word-wrapped preview, an always-visible shortcut strip, and a
// theme that follows the active Omarchy desktop theme.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if os.Getenv(clipServeEnv) != "" { // re-exec'd to own the clipboard
		os.Exit(serveClipboard())
	}
	restarted := envVar("RESTARTED") != "" // restarted into an update (by an old copy too)
	os.Unsetenv(restartEnv)
	os.Unsetenv("TIDEFILES_RESTARTED")
	themeFlag := flag.String("theme", "", `theme: "omarchy" (follow the desktop) or a tideui palette name (default: saved choice)`)
	cwdFile := flag.String("cwd-file", "", "write the final directory to this file on exit (for shell cd-on-quit)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(versionString())
		return
	}

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		if st, serr := os.Stat(abs); serr != nil || !st.IsDir() {
			err = fmt.Errorf("%s is not a directory", dir)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "console-places:", err)
		os.Exit(1)
	}

	cfg := loadConfig()
	if *themeFlag != "" {
		cfg.Theme = *themeFlag
	}
	out := newGfxOut(os.Stdout)
	// Save the window title and put it back on exit, so a shell left in this
	// window isn't mistaken for console-places.
	fmt.Fprint(os.Stdout, "\x1b[22;0t")
	m := newModel(abs, cfg, *cwdFile, out)
	if len(cfg.Tabs) > 0 && (restarted || restoreTabsAtStart(cfg, flag.NArg() > 0)) {
		// Last session's tabs: on the one that was active, or with the folder
		// named on the command line active.
		m.restoreTabs(cfg.Tabs, cfg.ActiveTab, flag.NArg() > 0 && !restarted)
	}
	if restarted {
		msg := "updated to " + appName + " " + versionNumber()
		if m.upd.renamed {
			msg += " · tidefiles is now console-places; both commands start it"
		}
		m.setMsg(msg, false)
	}
	final, err := tea.NewProgram(m, tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	fmt.Fprint(os.Stdout, "\x1b[23;0t")
	if err != nil {
		fmt.Fprintln(os.Stderr, "console-places:", err)
		os.Exit(1)
	}
	if fm, ok := final.(model); ok && fm.restart {
		restartInto(fm.cwd, *themeFlag, *cwdFile)
	}
}
