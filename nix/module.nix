{ config, lib, pkgs, ... }:
let
  cfg = config.services.p;
  runtime = cfg.settings.runtime or {};
  policies = lib.optional (runtime ? project_policy) runtime.project_policy
    ++ lib.attrValues (runtime.project_policies or {});
  state = "/var/lib/${cfg.stateDirectory}";
  host = pkgs.writeText "p-host.json" (builtins.toJSON
    (cfg.settings // { schema = "p.host/v1"; state_dir = state; }));
  prepare = pkgs.writeShellScript "p-prepare-config" ''
    set -euo pipefail
    umask 077
    pending=$(${pkgs.coreutils}/bin/mktemp ${lib.escapeShellArg "${state}/host.XXXXXXXX"})
    trap '${pkgs.coreutils}/bin/rm -f -- "$pending"' EXIT
    ${pkgs.coreutils}/bin/cat ${host} > "$pending"
    ${pkgs.coreutils}/bin/chmod 0600 "$pending"
    ${pkgs.coreutils}/bin/mv -T -- "$pending" ${lib.escapeShellArg "${state}/host.json"}
    ${lib.optionalString cfg.bundledActivation ''
      activation=${lib.escapeShellArg "${state}/activation.json"}
      if ! test -e "$activation" && ! test -L "$activation"; then
        pending=$(${pkgs.coreutils}/bin/mktemp ${lib.escapeShellArg "${state}/activation.XXXXXXXX"})
        ${cfg.package}/bin/p plugins defaults ${lib.escapeShellArg "${state}/events.ndjson"} \
          ${cfg.package}/share/p/plugins > "$pending"
        ${pkgs.coreutils}/bin/chmod 0600 "$pending"
        ${pkgs.coreutils}/bin/mv -T -- "$pending" "$activation"
      fi
    ''}
  '';
in {
  options.services.p = {
    enable = lib.mkEnableOption "P's local CLI-first control plane";
    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ../dev/package.nix {};
      description = "P CLI package; the flake module selects its locked package output.";
    };
    user = lib.mkOption { type = lib.types.str; default = "p"; };
    group = lib.mkOption { type = lib.types.str; default = "p"; };
    createUser = lib.mkOption {
      type = lib.types.bool; default = true;
      description = "Create a persistent system account; disable for an existing confined account.";
    };
    stateDirectory = lib.mkOption {
      type = lib.types.str; default = "p";
      description = "Private persistent directory name below /var/lib.";
    };
    settings = lib.mkOption {
      type = lib.types.attrs; default = {};
      description = "Trusted p.host/v1 settings, excluding forced schema/state_dir. Never put credentials in Nix settings.";
    };
    bundledActivation = lib.mkOption {
      type = lib.types.bool; default = false;
      description = "Approve generating the bundled six-role selection once at STATE/activation.json; existing selections are preserved.";
    };
  };
  config = lib.mkIf cfg.enable {
    assertions = [
      { assertion = pkgs.stdenv.hostPlatform.system == "x86_64-linux";
        message = "P MVP supports NixOS x86_64-linux only."; }
      { assertion = config.virtualisation.incus.enable;
        message = "P requires owner-provisioned local Incus."; }
      { assertion = cfg.user != "root" && cfg.group != "incus-admin"
          && config.users.users.${cfg.user}.group != "incus-admin"
          && !lib.elem "incus-admin" config.users.users.${cfg.user}.extraGroups
          && config.users.users.${cfg.user}.uid != 0;
        message = "P must use a non-root account without administrative Incus membership."; }
      { assertion = builtins.match "[a-zA-Z0-9][a-zA-Z0-9_-]*" cfg.stateDirectory != null;
        message = "P stateDirectory must be a single safe directory name."; }
      { assertion = cfg.settings ? git && cfg.settings ? runtime;
        message = "P installation requires explicit trusted Git and confined runtime settings."; }
      { assertion = (runtime.public_egress or null) == null
          && lib.all (policy: (policy.network or "none") == "none") policies;
        message = "P's hardened service supports network:none only; public-egress requires the owner-run daemon and its scoped privileged proofs."; }
    ];
    users.groups = lib.mkIf cfg.createUser { ${cfg.group} = {}; };
    users.users.${cfg.user} = {
      extraGroups = [ "incus" ];
    } // lib.optionalAttrs cfg.createUser {
      isSystemUser = true;
      group = cfg.group;
      home = state;
    };
    environment.systemPackages = [ cfg.package ];
    systemd.services.p = {
      description = "P local control plane";
      wantedBy = [ "multi-user.target" ];
      requires = [ "incus-user.socket" ];
      after = [ "incus.service" "incus-user.socket" ];
      path = [ pkgs.git pkgs.openssh pkgs.bash pkgs.coreutils ];
      serviceConfig = {
        User = cfg.user;
        Group = cfg.group;
        StateDirectory = cfg.stateDirectory;
        StateDirectoryMode = "0700";
        UMask = "0077";
        ExecStartPre = prepare;
        ExecStart = "${cfg.package}/bin/p daemon ${state}/host.json";
        Restart = "on-failure";
        RestartSec = 2;
        TimeoutStopSec = 30;
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        RestrictSUIDSGID = true;
        CapabilityBoundingSet = "";
        ReadWritePaths = [ state ] ++ lib.optional ((cfg.settings.runtime or {}) ? endpoint_prefix)
          cfg.settings.runtime.endpoint_prefix;
      };
    };
  };
}
