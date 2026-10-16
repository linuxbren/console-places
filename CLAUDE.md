# tidefiles

Keyboard-first terminal file manager (Go, Bubble Tea, lipgloss). **Linux only**: never add macOS or
Windows code, builds or docs. Built on Omarchy (Hyprland + foot) and must also work off Omarchy, on X11
and in a plain console. UI components come from `github.com/allisonhere/tideui`, kept as a normal
go.mod dependency (not vendored) so upstream fixes arrive; Dependabot watches it.

Detailed project log, decisions and history: `~/Documents/obsidian/Projects/tidefiles.md`. Read it
first; Conductor (another Claude session) also edits it.

## Layout

One `main` package, one file per concern:

- `model.go` state + `Update`; `handleKey` maps key → action, `guard()` applies busy/read-only checks,
  `perform()` runs actions (shared with the help palette). `view.go` rendering.
- `keys.go` the single keymap table: drives dispatch, help, the palette and the hint strip. New
  actions go here; `keys_test.go` rejects duplicate keys. One key = one meaning everywhere; a capital
  is a variant of its lowercase (d/D, f/F), with a few accepted exceptions.
- `modal.go` modals; `palette.go` (`?`), `settings.go` (`S`), `perms.go` (`P`), `find.go` (`f`),
  `grep.go` (`F`).
- `preview.go` (+ `markdown.go`, `image.go`, `gfx.go`, `syntax.go`) previews; heavy kinds build off
  the UI goroutine. `anim.go` animated GIFs: frames composited per disposal rule, encoded in the
  background, stepped by `animTickMsg` (the `*animation` pointer is the generation id; a tick for
  one no longer in `m.pv` stops it); sixel/kitty frames go out via `gfxOut.flush` since the text
  frame doesn't change. `video.go`: videos play in the preview (still first, then after a moment or
  `alt+p`): ffmpeg decodes muted RGBA at preview size (`-re`) into a pipe, a goroutine encodes frames
  (shared `frameEncoder`); the `*videoRun` pointer is the generation id; `syncVideo` after every
  update kills ffmpeg when the preview changes, hides or is covered (pause = kill, resume = restart
  at the position). `psel.go`: mouse drag selection inside the preview (tidefiles keeps the
  mouse, since terminals capture it per window); release copies the file's own text via `segments`.
- Document previews (kind `kindDoc`, built off the UI goroutine): `officedoc.go` (docx/xlsx/pptx,
  ODF and RTF as text, `.msi` via `file`), `peinfo.go` (`.exe`/`.dll` version resource, .NET, and
  a Mac `.app` folder's Info.plist), `plist.go` (binary plists), `wintext.go` (UTF-16 text, `.lnk`).
  `termwidth.go`: `View()` goes through `fitFrame`, so no line is wider than the terminal (a
  terminal that measures a character wider than we do would otherwise scroll the screen).
- `ops.go` / `fsops.go` file operations; `jobs.go` background jobs with progress + cancel (paste,
  extract, compress); `archive.go` / `compress.go`; `trash.go` (freedesktop trash, pinned Trash row,
  new window); `tabs.go`; `bulkrename.go` (`r` with several selected: template or find & replace, live
  preview with problems, two-pass rename via temp names with RENAME_NOREPLACE and rollback, one undo);
  `dusize.go` (properties' folder sizes: counted off the UI goroutine, size + on disk, hard links once,
  refreshed by `sizeTickMsg`, cancelled when the dialog goes);
  `places.go` (`b`: sections Places / Bookmarks / Drives / Network; `B` toggles a bookmark);
  `listplaces.go` (opt-in setting: places rows below every folder's files, the Trash row generalised;
  drives/network from a background scan, never read while drawing); `clipboard.go` (Wayland ext-data-control owner, X11 xclip/xsel,
  wl-copy fallback); `config.go` (`~/.config/tidefiles/config.json`); `internal/omarchy` theme reading.
- Drives and network (all listed in places): `drives.go` lsblk → drives that aren't the running
  system, mounted/unlocked/unmounted via `udisksctl` (no tty, so polkit asks through the desktop
  agent). `network.go` connects with `gio mount` (gvfs, like Nautilus), relaying each prompt gio
  flushes over a pipe ("…: ") into a modal; the `*connection` pointer is the generation id.
  `keyring.go` saves a password (when ticked) to the Secret Service over D-Bus with gvfs' own
  NetworkPassword attributes, since gio never saves; gvfs finds it next time. `discover.go` browses
  via avahi-browse, a WS-Discovery probe and nmblookup (ufw drops WSD/NetBIOS replies; mDNS passes),
  smbclient lists shares. `rootmount.go` opt-in kernel cifs/nfs mounts via pkexec/sudo with the
  terminal handed over (`tea.ExecProcess`); the cifs password goes in a 0600 file deleted after.
  `tailscale.go` lists the tailnet's online computers from `tailscale status --json` (Tailscale… in
  Network, and its own heading in Browse network…). `netinfo.go` fills a connected location's
  preview (address, protocol, user, space) off the UI goroutine. `firewall.go` offers (with a yes) two ufw rules letting WSD/NetBIOS answers in from the LAN, and removes them again (only rules
  carrying its comment);
  `install.go` turns a `missingError` into an offer to install the package (per package manager).
  Root actions share `privileged()` + `runPrivileged` (pkexec/sudo, terminal handed over).
  Tests stub every system lookup in `TestMain` (lsblk, gvfs, mounts, keyring, network, ufw, pm).
- `update.go` daily release check + self-update (`U`; Settings → Updates). Only a release binary in a
  user-writable folder replaces itself (checksum-verified, `--version` sanity run, atomic rename,
  then a restart prompt that re-execs with `TIDEFILES_RESTARTED`); packages / Nix / `go install`
  get a hint; dev builds never check. It reads the releases list, and the restart prompt lists the
  bold `## New` headlines of every release since the running version (RELEASING.md). Tests use
  `TIDEFILES_UPDATE_URL` (TestMain points it nowhere).

## Build and test

```sh
go build ./... && go vet ./... && go test -race -count=1 ./...
```

- `gofmt` must be clean (CI checks it). Go 1.27; keep `go.mod`'s `go` line at or below Arch's Go.
- Tests must never touch the user's real config or trash: `TestMain` points `TIDEFILES_CONFIG` and
  `XDG_DATA_HOME` at temp dirs. Model tests use `testModel()` (sets `TIDEFILES_IMAGES=blocks`).
  External tools are faked with scripts on `PATH` (`fakeTool`); tests skip if bsdtar/rg are missing.
- Hands-on checks: drive the binary in a private tmux (`tmux -L <name>`) with `TIDEFILES_CONFIG` set
  to a scratch file (quitting saves tabs into the config). Never open windows or type into the user's
  desktop; for real-terminal screenshots see the vault note (headless Hyprland output).
- After `go.mod`/`go.sum` changes, update `vendorHash` in `flake.nix` (CI's nix job fails otherwise).

## Rules

- Commit only when asked; never push, tag or release without Brenden's explicit go (a go relayed
  by Conductor is confirmed with Brenden first). v0.10.0 was released 2026-10-15. Push with
  `GH_TOKEN=$(gh auth token --user linuxbren)`; never `gh auth switch`.
- Every release is a normal GitHub release, never a pre-release. Steps are in `RELEASING.md`
  (GoReleaser builds tarballs, deb/rpm/apk, checksums and the AUR tidefiles-bin PKGBUILD; then bump
  the flake version and `contrib/arch/PKGBUILD`).
- Packaging channels: release assets, `contrib/arch` (makepkg), Nix flake, `go install`. AUR waits
  for registration to reopen; Cloudsmith apt/dnf is next; Homebrew skipped.

## Keep docs and the code graph current

With every change: update README (features, keys, install) and RELEASING.md if the process changed,
and add notable decisions to the vault note. After committing, reindex the codebase-memory graph
(`index_repository` on this repo) and record architecture decisions with `manage_adr`.
