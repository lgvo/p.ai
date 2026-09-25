{
  lib,
  pkgs,
  modulesPath,
  ...
}:
let
  kit = pkgs.callPackage ./package.nix { };
  # NixOS makes /etc/systemd/system a link to an immutable generated unit
  # tree. These fixed links let trusted assembly place selected, digest-checked
  # asset bytes in the private instance root while retaining the public unit
  # paths and a stateless boot dependency.
  hostUnitLinks = pkgs.runCommand "p-host-unit-links" { } ''
    mkdir -p $out/etc/systemd/system
    ln -s /etc/p/assets/p-session.target $out/etc/systemd/system/p-session.target
    ln -s /etc/p/assets/p-interactive.service $out/etc/systemd/system/p-interactive.service
  '';
in
{
  imports = [ "${modulesPath}/../maintainers/scripts/incus/incus-container-image.nix" ];

  assertions = [
    {
      assertion = pkgs.codex.version == "0.151.0";
      message = "The bundled Codex adapter requires Codex 0.151.0.";
    }
  ];

  system.stateVersion = "26.05";
  networking.hostName = "p-session";
  # Public sessions receive a root-installed static IPv4 address at service
  # start. No guest DHCP, router advertisement, or IPv6 escape path is used.
  networking.useDHCP = false;
  networking.enableIPv6 = false;
  # The root pre-start installs the per-session, root-owned resolver file from
  # the immutable public-network config. NixOS resolvconf would otherwise
  # place a dynamically owned target behind /etc/resolv.conf.
  networking.resolvconf.enable = false;
  documentation.enable = lib.mkForce false;
  documentation.nixos.enable = lib.mkForce false;
  system.installer.channel.enable = false;
  services.openssh.enable = lib.mkForce false;
  services.journald.settings.Journal.Storage = "persistent";
  # The Incus file endpoint formats SFTP directory entries with os/user
  # lookups. During a read-only frozen workspace inspection, a guest nscd
  # process cannot answer those lookups. This image has fixed local users;
  # keep passwd/group/shadow local and preserve ordinary files/DNS hosts.
  services.nscd.enable = false;
  system.nssModules = lib.mkForce [ ];
  system.nssDatabases.passwd = lib.mkForce [ "files" ];
  system.nssDatabases.group = lib.mkForce [ "files" ];
  system.nssDatabases.shadow = lib.mkForce [ "files" ];
  system.nssDatabases.hosts = lib.mkForce [ "files" "dns" ];
  users.users.root.initialHashedPassword = lib.mkForce "!";

  users.groups.p.gid = 1000;
  users.users.p = {
    isNormalUser = true;
    uid = 1000;
    group = "p";
    home = "/home/p";
    homeMode = "0700";
    createHome = true;
    shell = pkgs.bashInteractive;
  };

  systemd.packages = [ hostUnitLinks ];
  systemd.targets.multi-user.wants = [ "p-session.target" ];

  environment.systemPackages = with pkgs; [
    bashInteractive
    cacert
    coreutils
    codex
    git
    iproute2
    openssh
    python3
    tmux
  ];
  # The source-git asset calls this fixed path; NixOS packages otherwise live
  # under /run/current-system/sw/bin and immutable store paths.
  # Incus attaches sockets at /opt/p/endpoints. Only its parent is managed
  # here: prepare-endpoints binds it at /run/p after activation mounts /run.
  systemd.tmpfiles.rules = [
    "d /workspace 0755 p p -"
    "d /etc/p 0755 root root -"
    "d /etc/p/assets 0755 root root -"
    "d /opt/p 0755 root root -"
    "d /usr/libexec/p 0755 root root -"
    "L+ /usr/libexec/p/runtime-kit - - - - ${kit}/bin/p-runtime-kit"
    "L+ /usr/libexec/p/attach - - - - /etc/p/assets/p-attach"
    "L+ /usr/libexec/p/git-ssh - - - - /etc/p/assets/p-git-ssh"
    "L+ /usr/libexec/p/codex-adapter - - - - /etc/p/assets/p-codex-adapter"
    "L+ /usr/libexec/p/tmux - - - - ${pkgs.tmux}/bin/tmux"
    "L+ /usr/libexec/p/systemctl - - - - ${pkgs.systemd}/bin/systemctl"
    "L+ /usr/libexec/p/journalctl - - - - ${pkgs.systemd}/bin/journalctl"
    "L+ /usr/bin/ssh - - - - ${pkgs.openssh}/bin/ssh"
    "L+ /opt/p/tmux.conf - - - - ${./tmux.conf}"
  ];

  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    # Incus supplies the required isolation boundary. Keep container nesting
    # disabled; Nix does not add a second namespace sandbox inside this root.
    sandbox = false;
    allowed-users = [ "p" ];
    trusted-users = [ "root" ];
    substituters = lib.mkForce [ ];
  };
  nix.distributedBuilds = false;
}
