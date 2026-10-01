{
  lib,
  pkgs,
  modulesPath,
  ...
}:
{
  # A runtime feasibility fixture, not the production P base-image contract.
  imports = [ "${modulesPath}/../maintainers/scripts/incus/incus-container-image.nix" ];
  system.stateVersion = "26.05";
  networking.hostName = "p-lab-container";
  documentation.enable = lib.mkForce false;
  documentation.nixos.enable = lib.mkForce false;
  system.installer.channel.enable = false;
  services.openssh.enable = lib.mkForce false;
  users.users.root.initialHashedPassword = lib.mkForce "!";
  users.users.p = {
    isNormalUser = true;
    uid = 1000;
    shell = pkgs.bashInteractive;
  };
  environment.systemPackages = with pkgs; [
    git
    openssh
    tmux
    iproute2
  ];
  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    # Match the production container posture: isolation is supplied by Incus.
    sandbox = false;
    allowed-users = [ "p" ];
    trusted-users = [ "root" ];
    substituters = lib.mkForce [ ];
  };
  systemd.tmpfiles.rules = [ "d /workspace 0755 p users -" ];
}
