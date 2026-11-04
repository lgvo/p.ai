{
  description = "Interactive P lab and disposable Incus validation VMs";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      image = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [ ./image.nix ];
      };
      mkVM =
        automated:
        nixpkgs.lib.nixosSystem {
          inherit system;
          specialArgs = {
            inherit image automated;
            demo = false;
            demoPublic = false;
            pPackage = null;
            productTest = null;
            runtimeImage = null;
            selectedSteps = [ ];
          };
          modules = [ ./machine.nix ];
        };
      interactive = (mkVM false).config.system.build.vm;
      smoke = (mkVM true).config.system.build.vm;
      runner = pkgs.writeShellApplication {
        name = "p-incus-lab";
        runtimeInputs = [ pkgs.coreutils ];
        text = ''
          state_dir="$(realpath -m "''${P_VM_STATE_DIR:-.cache/p-vm}")"
          mkdir -p "$state_dir"
          export NIX_DISK_IMAGE="$state_dir/disk.qcow2"
          echo "VM disk: $NIX_DISK_IMAGE"
          exec ${pkgs.bash}/bin/bash ${./run-console.sh} ${interactive}/bin/run-p-vm-vm "$@"
        '';
      };
      # Resolve production sources from the checkout at launch time. Keeping
      # the small infrastructure flake separate avoids copying persistent VM
      # disks, caches, or unrelated checkout files into its source closure.
      productRunner = pkgs.writeShellApplication {
        name = "p-vm";
        runtimeInputs = [
          pkgs.nix
          pkgs.bash
          pkgs.git
          pkgs.coreutils
          pkgs.util-linux
          pkgs.iproute2
          pkgs.gawk
        ];
        text = ''
          repo="''${P_VM_REPO_DIR:-$PWD}"
          if ! [ -x "$repo/dev/demo-vm" ]; then
            echo "Run from the P repository root, or set P_VM_REPO_DIR to its absolute path." >&2
            exit 2
          fi
          exec "$repo/dev/demo-vm" "$@"
        '';
      };
      smokeRunner = pkgs.writeShellApplication {
        name = "p-vm-smoke-test";
        runtimeInputs = [
          pkgs.coreutils
          pkgs.gnugrep
        ];
        text = ''
          log_dir="$(realpath -m "''${P_VM_LOG_DIR:-.cache/p-vm}")"
          mkdir -p "$log_dir"
          work_dir="$(mktemp -d -t p-vm-smoke.XXXXXXXX)"
          trap 'rm -rf -- "$work_dir"' EXIT
          export NIX_DISK_IMAGE="$work_dir/disk.qcow2"
          log_file="$log_dir/smoke-$(date -u +%Y%m%dT%H%M%SZ)-$$.log"
          echo "Booting a fresh VM; console log: $log_file"
          status=0
          timeout --foreground --kill-after=30s "''${P_VM_TIMEOUT:-900}" \
            ${smoke}/bin/run-p-vm-vm >"$log_file" 2>&1 || status=$?
          cat "$log_file"
          if [ "$status" -ne 0 ] || ! grep -q '^P_VM_SMOKE_PASS' "$log_file"; then
            echo "VM smoke test failed (runner status $status); see $log_file" >&2
            exit 1
          fi
          echo "VM smoke test passed; fresh VM disk removed."
        '';
      };
    in
    {
      packages.${system} = {
        default = productRunner;
        incus-lab = runner;
        vm = interactive;
        smoke-vm = smoke;
        image = image.config.system.build.squashfs;
      };
      apps.${system} = {
        default = {
          type = "app";
          program = "${productRunner}/bin/p-vm";
        };
        incus-lab = {
          type = "app";
          program = "${runner}/bin/p-incus-lab";
        };
        smoke = {
          type = "app";
          program = "${smokeRunner}/bin/p-vm-smoke-test";
        };
      };
      devShells.${system}.default = pkgs.mkShellNoCC {
        packages = [
          pkgs.go
          pkgs.git
          pkgs.nixfmt
          pkgs.shellcheck
        ];
      };
      formatter.${system} = pkgs.nixfmt;
    };
}
