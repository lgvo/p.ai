{
  description = "P CLI-first control plane for NixOS and local Incus";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      image = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [ ./runtime/image.nix ];
      };
    in {
      packages.${system} = {
        default = pkgs.callPackage ./dev/package.nix {};
        runtime-image = image.config.system.build.squashfs;
        runtime-metadata = image.config.system.build.metadata;
        runtime-fingerprint = pkgs.runCommand "p-runtime-image-fingerprint" {} ''
          cat ${image.config.system.build.metadata}/tarball/*.tar.xz \
            ${image.config.system.build.squashfs}/*.squashfs | sha256sum | cut -d' ' -f1 > "$out"
        '';
      };
      nixosModules.default = { lib, ... }: {
        imports = [ ./nix/module.nix ];
        services.p.package = lib.mkDefault self.packages.${system}.default;
      };
      apps.${system}.default = {
        type = "app";
        program = "${self.packages.${system}.default}/bin/p";
      };
    };
}
