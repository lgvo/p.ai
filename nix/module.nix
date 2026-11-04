{ config, lib, pkgs, ... }:
let
  cfg = config.services.p;
  runtime = cfg.settings.runtime or {};
  policies = lib.optional (runtime ? project_policy) runtime.project_policy
    ++ lib.attrValues (runtime.project_policies or {});
  state = "/var/lib/${cfg.stateDirectory}";
  driver = runtime.driver or {};
  gitSettings = cfg.settings.git or {};
  sourceDriver = gitSettings.driver or {};
  # The package's own p-source-git inherits no PATH, so its config names the
  # git and ssh it runs, by default the system's.
  packagedSourceDriver = sourceDriver // {
    config = { git = "${pkgs.git}/bin/git"; ssh = "${pkgs.openssh}/bin/ssh"; }
      // (sourceDriver.config or {});
  };
  hostSettings = cfg.settings // { schema = "p.host/v1"; state_dir = state; }
    // lib.optionalAttrs (cfg.settings ? git && !(sourceDriver ? path)) {
      git = gitSettings // { driver = packagedSourceDriver; };
    };
  # A driver without a path is the package's own executable, p-runtime-incus
  # or p-source-git, pinned by the SHA-256 computed when host.json is built.
  packaged = lib.optional (cfg.settings ? runtime && !(driver ? path))
      { block = "runtime"; executable = "p-runtime-incus"; }
    ++ lib.optional (cfg.settings ? git && !(sourceDriver ? path))
      { block = "git"; executable = "p-source-git"; };
  pin = d: ''
    executable=${cfg.package}/bin/${d.executable}
    digest=$(sha256sum "$executable" | cut -d' ' -f1)
    jq -c --arg path "$executable" --arg sha256 "$digest" \
      '.${d.block}.driver += {path: $path, sha256: $sha256}' host.json > pinned.json
    mv pinned.json host.json
  '';
  # The bundled file-log and Codex adapter, selected by their packages'
  # paths and digests in the generated host configuration, unless settings
  # select their own; the file-log writes STATE/events.ndjson.
  bundled = ''
    ${cfg.package}/bin/p plugins defaults ${lib.escapeShellArg "${state}/events.ndjson"} \
      ${cfg.package}/share/p/plugins > defaults.json
    jq -c --slurpfile d defaults.json '
      (if has("events") then . else .events = $d[0].events end)
      | (if .runtime | has("agent_adapter") then . else .runtime.agent_adapter = $d[0].runtime.agent_adapter end)' \
      host.json > selected.json
    mv selected.json host.json
  '';
  host = if packaged != [ ] || cfg.bundledActivation
    then pkgs.runCommand "p-host.json" {
      nativeBuildInputs = [ pkgs.jq ];
      settings = builtins.toJSON hostSettings;
      passAsFile = [ "settings" ];
    } ''
      cp "$settingsPath" host.json
      ${lib.concatMapStrings pin packaged}
      ${lib.optionalString cfg.bundledActivation bundled}
      cp host.json "$out"
    ''
    else pkgs.writeText "p-host.json" (builtins.toJSON hostSettings);
  prepare = pkgs.writeShellScript "p-prepare-config" ''
    set -euo pipefail
    umask 077
    pending=$(${pkgs.coreutils}/bin/mktemp ${lib.escapeShellArg "${state}/host.XXXXXXXX"})
    trap '${pkgs.coreutils}/bin/rm -f -- "$pending"' EXIT
    ${pkgs.coreutils}/bin/cat ${host} > "$pending"
    ${pkgs.coreutils}/bin/chmod 0600 "$pending"
    ${pkgs.coreutils}/bin/mv -T -- "$pending" ${lib.escapeShellArg "${state}/host.json"}
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
      description = "Trusted p.host/v1 settings, excluding forced schema/state_dir. A runtime.driver or git.driver without path and sha256 runs the package's p-runtime-incus or p-source-git, pinned by its digest; p-source-git's config names the system's git and ssh unless it sets them. Never put credentials in Nix settings.";
    };
    bundledActivation = lib.mkOption {
      type = lib.types.bool; default = false;
      description = "Approve selecting the package's bundled file-log event handler, writing STATE/events.ndjson, and Codex agent adapter in the generated host configuration, by path and digest; settings that select their own events or runtime.agent_adapter keep it.";
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
      { assertion = (driver ? path) == (driver ? sha256);
        message = "P's runtime.driver names path and sha256 together, or neither to run the package's p-runtime-incus."; }
      { assertion = (sourceDriver ? path) == (sourceDriver ? sha256);
        message = "P's git.driver names path and sha256 together, or neither to run the package's p-source-git."; }
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
    environment.variables.P_SOCKET = lib.mkDefault "${state}/control.sock";
    systemd.services.p = {
      description = "P local control plane";
      wantedBy = [ "multi-user.target" ];
      requires = [ "incus-user.socket" ];
      after = [ "incus.service" "incus-user.socket" ];
      path = [ pkgs.bash pkgs.coreutils ];
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
