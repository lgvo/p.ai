let
  lab = builtins.getFlake "path:${toString ../../dev/vm}";
  pkgs = lab.inputs.nixpkgs.legacyPackages.x86_64-linux;
  lib = lab.inputs.nixpkgs.lib;
  make = extra: (lib.nixosSystem {
    system = "x86_64-linux";
    modules = [ ../../nix/module.nix {
      system.stateVersion = "26.05";
      boot.isContainer = true;
      virtualisation.incus.enable = true;
      networking.nftables.enable = true;
      services.p = {
        enable = true;
        settings = { git = {}; runtime = {}; };
      };
    } extra ];
  }).config;
  valid = make {};
  invalid = extra: builtins.any (a: !a.assertion) (make extra).assertions;
in assert !builtins.any (a: !a.assertion) valid.assertions;
   assert invalid { services.p.user = lib.mkForce "root"; services.p.createUser = false; };
   assert invalid { virtualisation.incus.enable = lib.mkForce false; };
   assert invalid { services.p.stateDirectory = "../unsafe"; };
   assert invalid { users.users.p.extraGroups = [ "incus-admin" ]; };
   assert invalid { services.p.settings = lib.mkForce { git = {}; runtime.public_egress = {}; }; };
   assert invalid { services.p.settings = lib.mkForce { git = {}; runtime.project_policy.network = "public-egress"; }; };
   assert invalid { services.p.settings = lib.mkForce { git = {}; runtime.project_policies.example.network = "public-egress"; }; };
   { user = valid.systemd.services.p.serviceConfig.User;
     config = valid.systemd.services.p.serviceConfig.ExecStart;
     stateMode = valid.systemd.services.p.serviceConfig.StateDirectoryMode;
     sandbox = valid.systemd.services.p.serviceConfig.ProtectSystem;
     noPrivileges = valid.systemd.services.p.serviceConfig.NoNewPrivileges; }
