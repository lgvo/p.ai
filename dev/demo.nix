{ repositoryBundle, public ? false, notes ? false, outerHostIPv4Text ? "", outerHostLANIPv4Text ? "" }:
let
  lab = builtins.getFlake "path:${toString ./vm}";
  nixpkgs = lab.inputs.nixpkgs;
  system = "x86_64-linux";
  pkgs = nixpkgs.legacyPackages.${system};
  lib = nixpkgs.lib;
  outerHostIPv4 = lib.splitString "\n" outerHostIPv4Text;
  outerHostLANIPv4 = if outerHostLANIPv4Text == "" then [] else lib.splitString "\n" outerHostLANIPv4Text;
  validIPv4 = address:
    let parts = lib.splitString "." address;
    in builtins.length parts == 4 && builtins.all
      (part: builtins.match "(0|[1-9][0-9]{0,2})" part != null && lib.toInt part <= 255) parts;
  validPrefix = prefix:
    let parts = lib.splitString "/" prefix;
    in (builtins.length parts == 1 && validIPv4 prefix)
      || (builtins.length parts == 2 && validIPv4 (builtins.head parts)
        && builtins.match "(0|[1-9][0-9]?)" (builtins.elemAt parts 1) != null
        && lib.toInt (builtins.elemAt parts 1) <= 32);
  pPackage = pkgs.callPackage ./package.nix { };
  originFixture = pPackage.overrideAttrs {
    subPackages = [ "tests/integration/cmd/origin-fixture" ];
    doCheck = false;
  };
  image = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [ ./vm/image.nix ];
  };
  runtimeImage = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [ ../runtime/image.nix ] ++ lib.optional notes ./notes-runtime.nix;
  };
  machine = nixpkgs.lib.nixosSystem {
    inherit system;
    specialArgs = {
      inherit image runtimeImage pPackage;
      demoRepositoryBundle = builtins.path {
        path = builtins.toPath repositoryBundle;
        name = "p-lab-repository.bundle";
      };
      automated = false;
      demo = true;
      demoPublic = public;
      demoNotes = notes;
      demoOriginFixture = originFixture;
      productTest = null;
      selectedSteps = [];
      dnsOverHTTPSModule = ../runtime/dns-over-https.nix;
      outerHostIPv4 = if public then assert builtins.all validIPv4 outerHostIPv4; outerHostIPv4 else [];
      outerHostLANIPv4 = assert builtins.all validPrefix outerHostLANIPv4; outerHostLANIPv4;
    };
    modules = [ ./vm/machine.nix ./vm/demo.nix ];
  };
  vm = machine.config.system.build.vm;
  runner = pkgs.writeShellApplication {
    name = "p-demo-vm";
    runtimeInputs = [ pkgs.coreutils pkgs.util-linux ];
    text = ''
      if [ "$#" -ne 0 ]; then
        echo "Usage: ./dev/demo-vm (configure the disk directory with P_DEMO_STATE_DIR)" >&2
        exit 2
      fi
      if ! [ -t 0 ] || ! [ -t 1 ]; then
        echo "Run ./dev/demo-vm in an interactive terminal." >&2
        exit 2
      fi
      state_dir="$(realpath -m "''${P_DEMO_STATE_DIR:-.cache/p-vm/${if notes then (if public then "demo-notes-public" else "demo-notes") else if public then "demo-public" else "demo"}}")"
      mkdir -p -- "$state_dir"
      exec 9>"$state_dir/vm.lock"
      if ! flock -n 9; then
        echo "A P demo VM is already using $state_dir." >&2
        exit 1
      fi
      export NIX_DISK_IMAGE="$state_dir/disk.qcow2"
      echo "P demo disk: $NIX_DISK_IMAGE"
      echo "The guest opens a shell. Use p api for the API or p tui for the TUI."
      echo "Detach from a session with Ctrl+B, d. Shut down with p-demo-poweroff."
      echo "QEMU emergency exit: Ctrl+A, X. The demo disk is retained."
      ${vm}/bin/run-p-vm-vm
    '';
  };
in
{
  inherit runner vm machine pPackage;
}
