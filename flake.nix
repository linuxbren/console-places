{
  description = "tidefiles: keyboard-first terminal file manager";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      # Bump with each release (RELEASING.md).
      version = "0.9.0";
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        # go.mod asks for Go 1.27, newer than nixpkgs' default go.
        tidefiles = pkgs.buildGo127Module {
          pname = "tidefiles";
          inherit version;
          src = self;
          # Hash of the Go module dependencies: update when go.mod/go.sum change
          # (set it to pkgs.lib.fakeHash, run `nix build`, copy the hash it reports).
          vendorHash = "sha256-CQylu8/dEq32l7gtsPyym9GjqQ2mvSGF03RhezCHKG8=";
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X main.version=v${version}" ];
          meta = {
            description = "Keyboard-first terminal file manager with previews, tabs and archive browsing";
            homepage = "https://github.com/linuxbren/tidefiles";
            license = pkgs.lib.licenses.mit;
            mainProgram = "tidefiles";
            platforms = pkgs.lib.platforms.linux;
          };
        };
        default = tidefiles;
      });
    };
}
