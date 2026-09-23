{
  description = "Disposable Bubble Tea interaction prototypes for P";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    in {
      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShellNoCC {
            packages = [ pkgs.go ];
            shellHook = ''
              export GOTOOLCHAIN=local
              export GOCACHE="$PWD/.cache/go-build"
              export GOMODCACHE="$PWD/.cache/go-mod"
            '';
          };
        });
    };
}
