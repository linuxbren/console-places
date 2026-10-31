# Grok Bot handoff: Console Places

This folder is a handoff from Brenden's previous Grok Bot to his new one. It is written to you, the
assistant picking this up. It covers only his Console Places work and his X posting rules. Personal
details and automations were deliberately left out; ask him directly for anything else you need.

Snapshot taken 2026-10-31. Verify anything time-sensitive (stars, releases, posts) before acting on it.

## Who

- **Brenden.** GitHub: [linuxbren](https://github.com/linuxbren). X: [@RookiOS72](https://x.com/RookiOS72).
  Site: https://linuxbren.github.io
- When you offer choices, let him pick several. He reads an answer like "a b" as "both a and b".
  He dislikes either-or questions.

## The project: Console Places

- Repo: https://github.com/linuxbren/console-places. Formerly **Tidefiles**;
  `linuxbren/tidefiles` redirects here.
- A keyboard-first terminal file manager for [Omarchy](https://omarchy.org) (Arch-based; he sometimes
  spells it "Omarki"). An alternative to yazi.
- Latest release: **v0.12.0** (2026-10-28).
- Verified features:
  - Follows the live Omarchy theme (`omarchy-theme-color`, or
    `~/.local/state/omarchy/current/theme/colors.toml`).
  - Image, PDF and archive previews. Code highlighting.
  - Fuzzy find. Search inside files.
  - Tabs, trash, bulk rename.
  - Works outside Omarchy with built-in themes (plain Arch, Ubuntu, any Linux).
- Install options:
  - Release tarballs, plus `.deb` / `.rpm` / `.apk` packages, for amd64 and arm64.
  - Arch PKGBUILD in `contrib/arch` (pkgver 0.12.0).
  - `go install github.com/linuxbren/console-places@latest`
  - Release binaries update themselves.
- Not packaged anywhere public yet:
  - **AUR:** no package. AUR isn't accepting new accounts, so this is blocked for now.
  - **Homebrew:** no formula. A tested personal tap is ready but on hold (see below).
  - **Repology:** not tracked. Repology picks a project up automatically once a repo carries it.
- **Does not build on macOS.** It uses Linux-only calls (`unix.Renameat2`, `TCGETS`/`TCSETS`,
  `Stat_t.Atim`, etc.) in `bulkrename.go`, `drives.go`, `gfx.go`, `modal.go` and `netinfo.go`.
- Stargazers include BlancoKat (Oct 4) and hilather (Oct 28). Check the current list with:
  `gh api -H "Accept: application/vnd.github.star+json" repos/linuxbren/console-places/stargazers`
- Competitors seen on X: Flea (@thisisgm), Strata, Douby Commander (@doubji), Xermius,
  Midnight Commander.

## X posting rules

1. Only post replies he explicitly approves.
2. Show each draft next to the original post it replies to.
3. Check for duplicates before posting (has @RookiOS72 already replied there?).
4. The 🖥 emoji is for release posts only.
5. Attach the demo clip [`demo/console-places-demo.mp4`](demo/console-places-demo.mp4) to Console
   Places posts and replies, so the GitHub link shows inline rather than as a link card.
6. Never delete earlier replies.
7. After posting, check the real post URL through the X API. A browser agent once reported the
   wrong URL.

### What to bring him

- Only bring him posts where a real user has a problem that Console Places can solve. Running on
  plain Arch counts.
- Skip promotional, tips, showcase and announcement posts, and posts praising another file manager.
- Skip threads he has already replied in ("If we're already in that thread, let's leave it alone").
- Don't jump into someone else's project thread to promote Console Places. He considers that
  hijacking.
- Skip these posts permanently:
  - **link28574653.** Never bring it up again.
  - osdotsystem
  - mahdi
  - bndnsmth
  - mxLr / ureken2 (he's already in GM's thread)
  - howtopam (promotional)

### Replies already posted by @RookiOS72

Leave these as they are.

| To | Link | Notes |
| --- | --- | --- |
| Matt Brewer | https://x.com/RookiOS72/status/2115548299675779357 | 2026-10-28 |
| Matt Brewer (follow-up) | https://x.com/RookiOS72/status/2115615148169728008 | 2026-10-29 |
| Zeke (Nautilus) | https://x.com/RookiOS72/status/2115614678319800824 | 2026-10-29 |
| Nota_Rilmann | https://x.com/RookiOS72/status/2115888138727911470 | 2026-10-29 |
| Thord (@tdh) | https://x.com/RookiOS72/status/2115888280311095615 | 2026-10-29 |
| Cristian (@cvatca) | https://x.com/RookiOS72/status/2115901889900614024 | 2026-10-29 |
| Sebastian (@codeface_io) | https://x.com/RookiOS72/status/2115919631412244980 | 2026-10-29, with video |
| Thord follow-up | https://x.com/RookiOS72/status/2115967433731437025 | 2026-10-30, with video: "Thanks, Thord! If you give it a spin, I'd love to hear what you think." |

### Unposted drafts

See [`x-drafts/unposted-drafts.md`](x-drafts/unposted-drafts.md). It has the Omarchy plugin
explainer, which shouldn't be posted until the plugin repo exists. It also notes that an
alternate Sebastian reply existed but its text wasn't saved.

## Open items / where we left off

### (a) Omarchy plugin — waiting on his OK

- Drafts are in [`omarchy-plugin-draft/`](omarchy-plugin-draft/). It's a bar button that launches
  or focuses console-places, with plugin id `linuxbren.console-places`. It passed local validation
  and the security baseline. It has **not been tested on a real Omarchy machine.**
- Next steps, only after he says yes:
  1. Create `linuxbren/omarchy-console-places` and push the contents of `omarchy-console-places/`.
  2. Show him the marketplace submission issue
     ([`submission-issue-DRAFT.md`](omarchy-plugin-draft/submission-issue-DRAFT.md)) before
     filing it.
- Submission works like this:
  - It's an issue on `omacom/omarchy-plugin-marketplace` using the `submit-plugin.yml` form.
  - A security scan runs. Then a maintainer adds the `approved-and-verified` label.
  - The median wait is about 1.5 hours.
- Users install it with `omarchy plugin add <git URL>`. The short `publisher/name` form isn't live
  yet. Once it is, the registry will resolve the name to the repo.

### (b) Homebrew tap — built, on hold

- The tap is in [`homebrew-tap/`](homebrew-tap/): the formula `Formula/console-places.rb`, a README,
  and an autobump workflow. Its `.github/` folder only runs once copied into a tap repo.
- Everything passed: install, `brew test`, `brew audit --strict --new --online`, and `brew style`.
- It's a formula, not a cask, and it's Linux-only (`depends_on :linux`).
- It builds with `main.version=0.12.0` (no leading `v`). That keeps the self-updater off in brew
  installs. The side effect is that pressing U in the app calls it a "development build".
- He said to **hold off** on publishing `linuxbren/homebrew-tap`. If he changes his mind:
  - Create the repo and push.
  - Turn on "Allow GitHub Actions to create and approve pull requests" so autobump can open PRs.
  - Users would run `brew install linuxbren/tap/console-places`.
- Optional cleanup: make `detectInstall` in `update.go` treat `/Cellar/` paths as a package
  install. The formula could then use the normal `v` version.
- The main Homebrew repo isn't an option. Console Places doesn't meet its notability requirements.

### (c) Maybe later

- A reply from LukeTestsAI was about local LLMs, not Console Places. Leave it unless he asks.

## Other files here

- [`demo/console-places-demo.mp4`](demo/console-places-demo.mp4): 16.5 s, 1080p demo clip for X posts.
- [`demo/rec/`](demo/rec/): the scripts that recorded it. They use puppeteer and xterm.js in Chrome
  and a pty bridge, and capture with ffmpeg x11grab. Paths inside point at the old box
  (`/workspace/...`, a demo home folder), so adjust them before re-recording.
