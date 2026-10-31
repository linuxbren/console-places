class ConsolePlaces < Formula
  desc "Keyboard-first terminal file manager with previews, tabs and archive browsing"
  homepage "https://github.com/linuxbren/console-places"
  url "https://github.com/linuxbren/console-places/archive/refs/tags/v0.12.0.tar.gz"
  sha256 "a980c1627427f4faf5b6bd946f792c3b9ffcc36e9792c2a55535e5169ee9d542"
  license "MIT"
  head "https://github.com/linuxbren/console-places.git", branch: "main"

  # Upstream is Linux only: v0.12.0 uses Linux-specific syscalls
  # (renameat2, TCGETS/TCSETS, Pdeathsig, Linux Stat_t fields) and does not
  # compile for darwin.
  depends_on "go" => :build
  depends_on :linux

  def install
    # The version is set WITHOUT the leading "v" on purpose. With a "vX.Y.Z"
    # version, console-places treats a binary in a user-writable folder (the
    # Homebrew Cellar is user-owned) as a release install and replaces itself
    # with GitHub release downloads, behind Homebrew's back. A version that is
    # not "vX.Y.Z" makes it skip the update checks, so `brew upgrade` stays in
    # charge of updates.
    ldflags = "-X main.version=#{version}"
    ENV["CGO_ENABLED"] = "0"
    system "go", "build", *std_go_args(ldflags:)

    pkgshare.install "contrib/omarchy-super-c.lua"
  end

  def caveats
    <<~EOS
      Optional helpers console-places uses when present: ripgrep (faster
      search), libarchive (bsdtar, archives), imagemagick, poppler,
      ffmpegthumbnailer and ffmpeg (previews), plus wl-clipboard or xclip,
      udisks2 and gvfs from your distro for clipboard, drives and network places.

      Self-update is off for Homebrew installs; update with:
        brew upgrade console-places

      Omarchy Super+C binding: #{opt_pkgshare}/omarchy-super-c.lua
    EOS
  end

  test do
    assert_equal "console-places #{version}", shell_output("#{bin}/console-places --version").strip
    assert_match "is not a directory",
                 shell_output("#{bin}/console-places #{testpath}/missing 2>&1", 1)
  end
end
