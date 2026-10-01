{ config, lib, pkgs, demoPublic, demoPublicEgress, demoRepositoryBundle, demoNotes ? false, demoOriginFixture ? null, ... }:
let
  socket = "/var/lib/p-demo/control.sock";
  browser = pkgs.writeShellApplication {
    name = "p-demo";
    runtimeInputs = [ config.services.p.package pkgs.coreutils pkgs.jq ];
    text = ''
      # A serial getty can initially report 0x0; P needs a usable frame.
      if [ -t 0 ] && [ -t 1 ]; then
        read -r p_demo_rows p_demo_cols < <(stty size)
        if (( p_demo_rows == 0 || p_demo_cols == 0 )); then
          stty rows 24 cols 80
        fi
      fi
      deadline=$((SECONDS+600))
      echo "Waiting for the P demo daemon..."
      until timeout 5 p api system.health | jq -e '.result.control_state == "ready"' >/dev/null 2>&1; do
        if ((SECONDS >= deadline)); then
          echo "P did not become ready. Check systemctl status p-vm-prepare p and use su - (password p-vm) for journalctl -u p." >&2
          exit 1
        fi
        sleep 1
      done
      exec p tui
    '';
  };
  api = pkgs.writeShellApplication {
    name = "p-demo-api";
    runtimeInputs = [ config.services.p.package ];
    text = ''
      exec p api "$@"
    '';
  };
  shutdown = pkgs.writeShellApplication {
    name = "p-demo-poweroff";
    text = ''
      exec /run/wrappers/bin/sudo -n ${pkgs.systemd}/bin/systemctl poweroff
    '';
  };
  cfg = config.services.p;
  preparePlugins = pkgs.writeShellScript "p-demo-prepare-plugins" ''
    set -euo pipefail
    umask 077
    state=/var/lib/p-demo
    catalog="$state/bundled/${builtins.baseNameOf (toString cfg.package.bundledPlugins)}"
    mkdir -p "$state/bundled"
    if ! test -d "$catalog"; then
      pending=$(mktemp -d "$state/bundled/.pending.XXXXXXXX")
      trap 'rm -rf -- "$pending"' EXIT
      cp -R ${cfg.package}/share/p/plugins/. "$pending/"
      mv -T -- "$pending" "$catalog"
      trap - EXIT
    fi
    pending=$(mktemp "$state/activation.XXXXXXXX")
    trap 'rm -f -- "$pending"' EXIT
    ${cfg.package}/bin/p plugins defaults "$state/events.ndjson" "$catalog" > "$pending"
    # Keep user-managed activation intact. Only our last untouched default
    # selection follows the current checkout's bundled packages.
    if ! test -e "$state/activation.json" && ! test -L "$state/activation.json" \
       || ${pkgs.diffutils}/bin/cmp -s "$state/activation.json" "$state/bundled-activation.json"; then
      cp "$pending" "$state/bundled-activation.json"
      mv -T -- "$pending" "$state/activation.json"
    else
      mv -T -- "$pending" "$state/bundled-activation.json"
    fi
  '';
  publicHost = pkgs.writeText "p-demo-public-host.json" (builtins.toJSON (cfg.settings // {
    schema = "p.host/v1";
    state_dir = "/var/lib/p-demo";
    runtime = cfg.settings.runtime // {
      public_egress = demoPublicEgress;
      project_policy = cfg.settings.runtime.project_policy // { network = "public-egress"; };
    };
  }));
in
{
  environment.systemPackages = [ browser api shutdown ] ++ lib.optional (demoNotes && demoOriginFixture != null) demoOriginFixture;
  environment.variables.P_SOCKET = socket;
  environment.etc = lib.optionalAttrs demoNotes {
    "p-notes-example".source = ../../examples/notes;
    "p-notes-persistence-check.sh".source = ../../tests/integration/notes-lab-persistence.sh;
    "p-notes-origin-fixture.sh".source = ../../tests/integration/notes-origin-fixture.sh;
  };
  # Expose the guest owner's trusted SSH configuration to the notes daemon
  # while hiding the rest of its home. Fixture keys live in private daemon
  # state, which is already available to this unit. Create the empty directory
  # before the bind so a fresh lab needs no restart after fixture setup.
  systemd.tmpfiles.rules = lib.optional demoNotes "d /home/pdev/.ssh 0700 pdev users -";
  services.p.bundledActivation = lib.mkForce false;
  # The public mode follows the owner-run daemon contract. Its two read-only
  # root proofs require sudo; the hardened services.p unit forbids privilege
  # transitions and is used unchanged in the default offline mode.
  systemd.services.p = lib.mkMerge [
    { serviceConfig.ExecStartPre = lib.mkBefore [ preparePlugins ]; }
    (lib.mkIf demoNotes {
      after = [ "systemd-tmpfiles-setup.service" ];
      serviceConfig = {
        ProtectHome = lib.mkForce "tmpfs";
        BindReadOnlyPaths = [ "/home/pdev/.ssh" ];
      };
    })
    (lib.mkIf demoPublic {
      description = "P public lab daemon (confined Incus owner)";
      wantedBy = [ "multi-user.target" ];
      path = [ pkgs.git pkgs.openssh pkgs.bash pkgs.coreutils ];
      serviceConfig = {
        User = "pdev"; Group = "users";
        StateDirectory = "p-demo"; StateDirectoryMode = "0700";
        UMask = "0077";
        ExecStart = "${cfg.package}/bin/p daemon /var/lib/p-demo/host.json";
        Restart = "on-failure"; RestartSec = 2; TimeoutStopSec = 30;
      };
      preStart = ''
        set -euo pipefail
        umask 077
        cp ${publicHost} /var/lib/p-demo/host.json
        chmod 0600 /var/lib/p-demo/host.json
      '';
    })
  ];
  systemd.services.p-lab-repository = {
    description = "Seed the local-only P repository and main session";
    wantedBy = [ "multi-user.target" ];
    requires = [ "p.service" ];
    after = [ "p.service" ];
    path = [ cfg.package pkgs.incus pkgs.git pkgs.coreutils pkgs.jq ];
    environment = {
      P_LAB_REPOSITORY_BUNDLE = toString demoRepositoryBundle;
      P_LAB_REPOSITORY_LOADER = "${./load-repository.sh}";
    };
    serviceConfig = {
      Type = "oneshot";
      User = "pdev";
      Group = "users";
      UMask = "0077";
      TimeoutStartSec = 700;
      RemainAfterExit = true;
      ExecStart = "${pkgs.bash}/bin/bash ${./seed-repository.sh}";
    };
  };
  security.sudo.extraRules = [{
    users = [ "pdev" ];
    commands = [{
      command = "${pkgs.systemd}/bin/systemctl poweroff";
      options = [ "NOPASSWD" ];
    }];
  }];
  programs.bash.loginShellInit = ''
    if [[ $USER == pdev && -t 0 && -t 1 ]]; then
      # Give ordinary p tui a usable initial frame on a serial getty.
      read -r p_demo_rows p_demo_cols < <(stty size)
      if (( p_demo_rows == 0 || p_demo_cols == 0 )); then
        stty rows 24 cols 80
      fi
      echo "P lab shell: p api calls the API; p tui opens the TUI; p-demo-poweroff shuts down."
      echo "The P instance is configured. The daemon starts in the background."
      echo "Project p-ai/main is seeded from committed source on first boot. Check systemctl status p-lab-repository."
      echo "${if demoPublic then "Public DNS/HTTP(S) enabled; host/LAN/private destinations blocked." else "Sessions have no network access."}"
      ${lib.optionalString demoNotes ''echo "Three-service notes sample: /etc/p-notes-example. New sessions have PostgreSQL and Python/Psycopg tools."''}
    fi
  '';
}
