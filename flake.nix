{
  description = "flywheel — control a Bluetooth smart trainer (FTMS) from the terminal";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-linux" "aarch64-linux" "x86_64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (s: f nixpkgs.legacyPackages.${s});
    in {
      devShells = forAllSystems (pkgs: {
        # cgo is required: on macOS the bluetooth package binds CoreBluetooth,
        # and stdenv's clang + Apple SDK cover that.
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.gotools
          ];
        };
      });
    };
}
