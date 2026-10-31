# linuxbren/homebrew-tap

Homebrew formulae for [linuxbren](https://github.com/linuxbren)'s projects.

## Console Places

[Console Places](https://github.com/linuxbren/console-places) is a keyboard-first
terminal file manager with previews, tabs and archive browsing.

```sh
brew install linuxbren/tap/console-places
```

Or `brew tap linuxbren/tap` and then `brew install console-places`.

**Linux only.** Console Places uses Linux-specific system calls, so this formula
works with Homebrew on Linux and does not install on macOS.

Homebrew builds it from source (Go is pulled in as a build dependency). The
Homebrew build turns off Console Places' built-in self-updater; update with:

```sh
brew upgrade console-places
```

## Updates to this tap

A daily GitHub Action (`.github/workflows/autobump.yml`) checks for new
Console Places releases and opens a pull request bumping the formula.
