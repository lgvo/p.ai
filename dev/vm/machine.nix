{
  config,
  lib,
  pkgs,
  modulesPath,
  image,
  automated,
  pPackage ? null,
  productTest ? null,
  selectedSteps ? [ ],
  runtimeImage ? null,
  outerHostIPv4 ? [ ],
  outerHostLANIPv4 ? [ ],
  specialArgs,
  ...
}:
let
  dnsOverHTTPSModule = specialArgs.dnsOverHTTPSModule or null;
  smokeScript = pkgs.writeShellApplication {
    name = "p-vm-smoke";
    runtimeInputs = [
      config.virtualisation.incus.clientPackage
      pkgs.jq
      pkgs.coreutils
      pkgs.gnugrep
    ];
    text = builtins.readFile ./smoke.sh;
  };
  productMarker = if selectedSteps == [ ] then "P_PRODUCT_INTEGRATION_PASS"
    else "P_PRODUCT_INTEGRATION_SELECTED_PASS ${lib.concatStringsSep "," selectedSteps}";
  publicEgressFixture = automated && productTest != null
    && (selectedSteps == [ ] || lib.elem "37-public-egress.sh" selectedSteps);
  publicEgressDeniedCIDRs = [
    "0.0.0.0/8" "10.0.0.0/8" "100.64.0.0/10" "127.0.0.0/8"
    "169.254.0.0/16" "172.16.0.0/12" "192.0.0.0/24"
    "192.0.2.0/24" "192.88.99.0/24" "192.168.0.0/16"
    "198.18.0.0/15" "198.51.100.0/24" "203.0.113.0/24"
    "224.0.0.0/3"
  ];
  publicEgressDeniedText = lib.concatStringsSep "," publicEgressDeniedCIDRs;
  publicNetworkProof = pkgs.writeShellScriptBin "p-public-network-proof" ''
    set -euo pipefail
    test "$#" -eq 0
    exec </dev/null
    network_response=$(${pkgs.coreutils}/bin/mktemp --tmpdir=/run p-public-network-proof.XXXXXX)
    acl_response=$(${pkgs.coreutils}/bin/mktemp --tmpdir=/run p-public-acl-proof.XXXXXX)
    trap '${pkgs.coreutils}/bin/rm -f -- "$network_response" "$acl_response"' EXIT
    ${pkgs.coreutils}/bin/env -i HOME=/var/empty \
      ${pkgs.curl}/bin/curl -q --fail --silent --show-error --max-time 8 \
        --max-filesize 65536 \
        --unix-socket /var/lib/incus/unix.socket \
        'http://incus/1.0/networks/p-public-v1?project=default' > "$network_response"
    ${pkgs.coreutils}/bin/env -i HOME=/var/empty \
      ${pkgs.curl}/bin/curl -q --fail --silent --show-error --max-time 8 \
        --max-filesize 65536 \
        --unix-socket /var/lib/incus/unix.socket \
        'http://incus/1.0/network-acls/p-public-v1-acl?project=default' > "$acl_response"
    test "$(${pkgs.coreutils}/bin/stat -c %s "$network_response")" -le 65536
    test "$(${pkgs.coreutils}/bin/stat -c %s "$acl_response")" -le 65536
    ${pkgs.coreutils}/bin/env -i HOME=/var/empty \
      ${pkgs.python3}/bin/python3 -I -S - "$network_response" "$acl_response" <<'PY'
import json
import sys

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result

def metadata(path):
    with open(path, "rb") as source:
        payload = json.load(source, object_pairs_hook=unique_object)
    if not isinstance(payload, dict) or payload.get("type") != "sync" or payload.get("status_code") != 200:
        raise SystemExit("unexpected Incus response")
    result = payload.get("metadata")
    if not isinstance(result, dict):
        raise SystemExit("missing Incus metadata")
    return result

network = metadata(sys.argv[1])
acl = metadata(sys.argv[2])
expected_config = {
    "ipv4.address": "10.233.0.1/24",
    "ipv4.dhcp": "false",
    "ipv4.nat": "true",
    "ipv4.routing": "true",
    "ipv4.firewall": "true",
    "ipv6.address": "none",
    "dns.mode": "none",
    "security.acls": "p-public-v1-acl",
    "security.acls.default.ingress.action": "reject",
    "security.acls.default.egress.action": "reject",
}
if any((
    network.get("project") != "default",
    network.get("name") != "p-public-v1",
    network.get("type") != "bridge",
    network.get("managed") is not True,
    network.get("status") != "Created",
    network.get("config") != expected_config,
)):
    raise SystemExit("unexpected Incus public network")
expected_rules = [
    {"action":"drop","state":"enabled","destination":"${publicEgressDeniedText}"},
    {"action":"allow","state":"enabled","destination":"1.1.1.1","protocol":"udp","destination_port":"53"},
    {"action":"allow","state":"enabled","destination":"1.1.1.1","protocol":"tcp","destination_port":"53"},
    {"action":"allow","state":"enabled","destination":"9.9.9.9","protocol":"udp","destination_port":"53"},
    {"action":"allow","state":"enabled","destination":"9.9.9.9","protocol":"tcp","destination_port":"53"},
    {"action":"allow","state":"enabled","protocol":"tcp","destination_port":"80,443"},
]
rules = acl.get("egress")
if not isinstance(rules, list) or any(not isinstance(rule, dict) for rule in rules):
    raise SystemExit("unexpected Incus ACL rules")
rule_keys = {"action", "source", "destination", "protocol", "source_port",
    "destination_port", "icmp_type", "icmp_code", "description", "state"}
if any(set(rule) - rule_keys for rule in rules):
    raise SystemExit("unknown Incus ACL rule field")
rules = [{key: value for key, value in rule.items() if value not in (None, "", [], {})}
    for rule in rules]
def canonical(rule):
    return json.dumps(rule, sort_keys=True, separators=(",", ":"))
if any((
    acl.get("project") != "default",
    acl.get("name") != "p-public-v1-acl",
    acl.get("config") != {},
    acl.get("ingress") != [],
    list(map(canonical, rules)) != list(map(canonical, expected_rules)),
)):
    raise SystemExit("unexpected Incus public ACL")
output = {
    "network": {key: network[key] for key in
        ("project", "name", "type", "managed", "status", "config")},
    "acl": {"name": acl["name"], "config": acl["config"],
        "ingress": acl["ingress"], "egress": rules},
}
print(json.dumps(output, separators=(",", ":")))
PY
  '';
in
{
  imports = [ "${modulesPath}/virtualisation/qemu-vm.nix" ]
    ++ lib.optional (dnsOverHTTPSModule != null) dnsOverHTTPSModule;
  system.stateVersion = "26.05";
  assertions = lib.optional publicEgressFixture {
    assertion = outerHostIPv4 != [ ] && dnsOverHTTPSModule != null;
    message = "Public-egress validation requires captured outer host IPv4 addresses; use dev/test-vm.";
  };
  networking.hostName = "p-vm";
  networking.nftables.enable = true;
  networking.enableIPv6 = lib.mkIf publicEgressFixture false;
  networking.nameservers = lib.mkIf publicEgressFixture [ "127.0.0.1" ];
  services = {
    openssh.enable = false;
    getty.autologinUser = lib.mkIf (!automated) "pdev";
    getty.helpLine = "P infrastructure lab. Run p-vm-smoke. Console root password: p-vm.";
  } // lib.optionalAttrs (dnsOverHTTPSModule != null) {
    p-dns-over-https.enable = publicEgressFixture;
  };
  networking.dhcpcd.extraConfig = lib.mkIf publicEgressFixture (lib.mkAfter ''
    nooption domain_name_servers
  '');
  security.apparmor.enable = true;
  boot.kernelModules = [ "btrfs" ];
  documentation.enable = false;
  documentation.nixos.enable = false;

  virtualisation = {
    # One serial VM can exercise two bounded 4 GiB builders concurrently,
    # with room for session containers and the Incus/NixOS host services.
    memorySize = 12288;
    cores = 2;
    diskSize = 24576;
    graphics = false;
    # Boot from a closure image; do not expose the host store, checkout or home.
    useNixStoreImage = true;
    mountHostNixStore = false;
    writableStore = true;
    writableStoreUseTmpfs = false;
    sharedDirectories = lib.mkForce { };
    # Public tests need an actual outside route. Guest nftables constrains
    # this one selection; smoke and all other selections retain SLIRP isolation.
    restrictNetwork = !publicEgressFixture;
    incus = {
      enable = true;
      package = pkgs.incus;
      preseed = {
        storage_pools = [
          {
            name = "default";
            driver = "dir";
          }
          {
            name = "builders";
            driver = "btrfs";
            config.size = "16GiB";
          }
        ];
        # incus-user expects this bridge. Existing selected gates remain NIC-free.
        networks = [
          {
            name = "incusbr0";
            type = "bridge";
            config = {
              "ipv4.address" = "none";
              "ipv6.address" = "none";
            };
          }
        ] ++ lib.optionals publicEgressFixture [
          {
            name = "p-public-v1";
            type = "bridge";
            config = {
              "ipv4.address" = "10.233.0.1/24";
              "ipv4.dhcp" = "false";
              "ipv4.nat" = "true";
              "ipv4.routing" = "true";
              "ipv4.firewall" = "true";
              "ipv6.address" = "none";
              "dns.mode" = "none";
            };
          }
        ];
        projects = [
          {
            name = "user-1000";
            description = "Confined P development account";
            config = {
              "features.images" = "true";
              "features.profiles" = "true";
              "features.networks" = "false";
              "restricted" = "true";
              "restricted.containers.privilege" = "isolated";
              "restricted.containers.nesting" = "block";
              "restricted.containers.lowlevel" = "block";
              "restricted.devices.disk" = "allow";
              "restricted.devices.disk.paths" = "/var/lib/p-vm/endpoints,/var/lib/p-vm/grants";
              "restricted.devices.nic" = if publicEgressFixture then "managed" else "block";
              "restricted.devices.gpu" = "block";
              "restricted.storage-pools.access" = "default,builders";
              "limits.containers" = "4";
              "limits.virtual-machines" = "0";
            } // lib.optionalAttrs publicEgressFixture {
              "restricted.networks.access" = "p-public-v1";
            };
          }
        ];
        profiles = [
          {
            name = "default";
            project = "user-1000";
            config = {
              "security.idmap.isolated" = "true";
              "limits.cpu" = "1";
              "limits.memory" = "768MiB";
            };
            devices.root = {
              type = "disk";
              path = "/";
              pool = "default";
            };
          }
        ];
      };
    };
  };

  users.users.pdev = {
    isNormalUser = true;
    uid = 1000;
    extraGroups = [ "incus" ];
    shell = pkgs.bashInteractive;
  };
  security.sudo.extraRules = lib.optionals publicEgressFixture [
    {
      users = [ "pdev" ];
      commands = [
        {
          command = "${pkgs.nftables}/bin/nft -j list table inet p_egress_p_public_v1";
          options = [ "NOPASSWD" ];
        }
        {
          command = "${pkgs.nftables}/bin/nft -j list table inet p_vm_outer";
          options = [ "NOPASSWD" ];
        }
        {
          command = "${pkgs.nftables}/bin/nft list counter ip p_vm_dnat_probe forward_hits";
          options = [ "NOPASSWD" ];
        }
        {
          command = "${pkgs.nftables}/bin/nft list counter ip p_vm_dnat_probe forward_any_hits";
          options = [ "NOPASSWD" ];
        }
        {
          command = "${pkgs.nftables}/bin/nft list counter ip p_vm_dnat_probe input_any_hits";
          options = [ "NOPASSWD" ];
        }
        {
          command = "${pkgs.nftables}/bin/nft list counter ip p_vm_dnat_probe prerouting_hits";
          options = [ "NOPASSWD" ];
        }
        {
          command = "${publicNetworkProof}/bin/p-public-network-proof \"\"";
          options = [ "NOPASSWD" ];
        }
      ];
    }
  ];
  networking.nftables.tables = lib.mkIf publicEgressFixture {
    p_vm_outer = {
      family = "inet";
      content = ''
        chain output {
          type filter hook output priority -10; policy drop;
          oifname "lo" accept
          # Only bootstrap DHCP is exempt from destination denial. QEMU's
          # private gateway/DNS aliases remain unavailable to applications.
          oifname "eth0" ip saddr { 0.0.0.0, 10.0.2.15 } ip daddr { 10.0.2.2, 255.255.255.255 } udp sport 68 udp dport 67 accept
          meta nfproto ipv6 drop
          ${lib.concatMapStringsSep "\n" (address: ''ip daddr ${address} counter drop'') outerHostIPv4}
          ${lib.concatMapStringsSep "\n" (prefix: ''ip daddr ${prefix} counter drop'') outerHostLANIPv4}
          ${lib.concatMapStringsSep "\n" (cidr: ''ip daddr ${cidr} counter drop'') publicEgressDeniedCIDRs}
          # Retain existing pinned DNS allowances; the resolver itself uses DoH.
          ip daddr { 1.1.1.1, 9.9.9.9 } udp dport 53 counter accept
          ip daddr { 1.1.1.1, 9.9.9.9 } tcp dport 53 counter accept
          tcp dport { 80, 443 } counter accept
        }
        chain input {
          type filter hook input priority -10; policy drop;
          iifname "lo" accept
          ct state established,related accept
          iifname "eth0" ip saddr 10.0.2.2 udp sport 67 udp dport 68 accept
        }
        chain forward {
          type filter hook forward priority -10; policy accept;
          iifname "p-public-v1" meta nfproto ipv6 drop
          ${lib.concatMapStringsSep "\n" (address: ''iifname "p-public-v1" ip daddr ${address} counter drop'') outerHostIPv4}
          ${lib.concatMapStringsSep "\n" (prefix: ''iifname "p-public-v1" ip daddr ${prefix} counter drop'') outerHostLANIPv4}
          ${lib.concatMapStringsSep "\n" (cidr: ''iifname "p-public-v1" ip daddr ${cidr} counter drop'') publicEgressDeniedCIDRs}
          iifname "p-public-v1" ip daddr { 1.1.1.1, 9.9.9.9 } udp dport 53 counter accept
          iifname "p-public-v1" ip daddr { 1.1.1.1, 9.9.9.9 } tcp dport 53 counter accept
          iifname "p-public-v1" tcp dport { 80, 443 } counter accept
          iifname "p-public-v1" drop
          oifname "p-public-v1" ct state established,related accept
          oifname "p-public-v1" drop
        }
      '';
    };
    # Disposable integration probe only. The separate production table below
    # still drops every forwarded packet whose destination was rewritten.
    p_vm_dnat_probe = {
      family = "ip";
      content = ''
        counter prerouting_hits { }
        counter forward_hits { }
        counter forward_any_hits { }
        counter input_any_hits { }
        chain prerouting {
          type nat hook prerouting priority dstnat; policy accept;
          iifname "p-public-v1" ip daddr 8.8.4.4 tcp dport 443 counter name prerouting_hits dnat to 8.8.4.10:443
        }
        chain forward {
          type filter hook forward priority -1; policy accept;
          iifname "p-public-v1" counter name forward_any_hits
          iifname "p-public-v1" ct status dnat counter name forward_hits
        }
        chain input {
          type filter hook input priority -1; policy accept;
          iifname "p-public-v1" counter name input_any_hits
        }
      '';
    };
    p_egress_p_public_v1 = {
      family = "inet";
      content = ''
        chain input {
          type filter hook input priority 0; policy accept;
          iifname "p-public-v1" drop
        }
        chain forward {
          type filter hook forward priority 0; policy accept;
          iifname "p-public-v1" ct status dnat counter drop
          ${lib.concatMapStringsSep "\n" (cidr: ''iifname "p-public-v1" ip daddr ${cidr} drop'') publicEgressDeniedCIDRs}
          iifname "p-public-v1" meta nfproto ipv6 drop
        }
      '';
    };
  };
  # Deliberate disposable-console credential; no SSH or forwarded ports.
  users.users.root.initialPassword = "p-vm";
  environment.systemPackages =
    (with pkgs; [
      smokeScript
      git
      go
      jq
      tmux
      btrfs-progs
    ])
    ++ lib.optional publicEgressFixture pkgs.python3
    ++ lib.optional (pPackage != null) pPackage
    ++ lib.optional (productTest != null) productTest;
  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];
  systemd.tmpfiles.rules = [
    "d /var/lib/p-vm/endpoints 0755 root root -"
    "d /var/lib/p-vm/endpoints/pdev 0700 pdev users -"
    "d /var/lib/p-vm/grants 0755 root root -"
    "d /var/lib/p-vm/grants/pdev 0700 pdev users -"
  ];

  systemd.services.p-dns-over-https = lib.mkIf publicEgressFixture {
    serviceConfig.StandardOutput = "journal+console";
    serviceConfig.StandardError = "journal+console";
  };

  systemd.services.p-vm-prepare = {
    description = "Prepare the confined Incus lab and pinned fixture image";
    wantedBy = [ "multi-user.target" ];
    requires = [
      "incus-preseed.service"
      "incus-user.socket"
    ];
    after = [
      "incus-preseed.service"
      "incus-user.socket"
    ];
    before = [ "getty.target" ];
    path = [
      config.virtualisation.incus.clientPackage
      pkgs.util-linux
      pkgs.iproute2
      pkgs.jq
    ];
    unitConfig.OnFailure = lib.mkIf automated "p-vm-failed.service";
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      TimeoutStartSec = 600;
      StandardOutput = lib.mkIf automated "journal+console";
      StandardError = lib.mkIf automated "journal+console";
    };
    script = ''
      set -eu
      # The project/profile exist before first contact; incus-user only enrolls
      # the confined principal instead of supplying its broader default policy.
      runuser -u pdev -- incus list
      ${lib.optionalString publicEgressFixture ''
        # A disposable public-looking host address tests the INPUT boundary.
        ip address add 8.8.8.8/32 dev lo
        # A separately routed public-looking listener lets the DNAT test
        # reach FORWARD without making private/sibling traffic ACL-eligible.
        ip netns add p-vm-dnat-target
        ip link add p-vm-dnat-host type veth peer name p-vm-dnat-peer
        ip link set p-vm-dnat-peer netns p-vm-dnat-target
        ip address add 8.8.4.9/30 dev p-vm-dnat-host
        ip link set p-vm-dnat-host up
        ip netns exec p-vm-dnat-target ip address add 8.8.4.10/30 dev p-vm-dnat-peer
        ip netns exec p-vm-dnat-target ip link set lo up
        ip netns exec p-vm-dnat-target ip link set p-vm-dnat-peer up
        ip netns exec p-vm-dnat-target ip route add default via 8.8.4.9
      ''}
      ${lib.optionalString publicEgressFixture ''
        incus network acl create p-public-v1-acl
        incus network acl rule add p-public-v1-acl egress \
          action=drop state=enabled destination=${publicEgressDeniedText}
        for dns in 1.1.1.1 9.9.9.9; do
          incus network acl rule add p-public-v1-acl egress \
            action=allow state=enabled destination="$dns" protocol=udp destination_port=53
          incus network acl rule add p-public-v1-acl egress \
            action=allow state=enabled destination="$dns" protocol=tcp destination_port=53
        done
        incus network acl rule add p-public-v1-acl egress \
          action=allow state=enabled protocol=tcp destination_port=80,443
        incus network set p-public-v1 \
          security.acls=p-public-v1-acl \
          security.acls.default.ingress.action=reject \
          security.acls.default.egress.action=reject
        echo 'root Incus public bridge configuration after provisioning:'
        incus network show p-public-v1
        echo 'root Incus public ACL after provisioning:'
        incus network acl show p-public-v1-acl
      ''}
      # Incus split-image identity is SHA-256(metadata || rootfs).
      fingerprint=$(cat ${image.config.system.build.metadata}/tarball/*.tar.xz \
        ${image.config.system.build.squashfs}/*.squashfs | sha256sum)
      fingerprint=''${fingerprint%% *}
      ensure_image() {
        inventory=$(incus image list --project user-1000 --format json)
        if ! jq -e --arg f "$fingerprint" 'any(.[]; .fingerprint == $f)' <<< "$inventory" >/dev/null; then
          incus image import ${image.config.system.build.metadata}/tarball/*.tar.xz \
            ${image.config.system.build.squashfs}/*.squashfs --project user-1000
        fi
        aliases=$(incus image alias list --project user-1000 --format json)
        if jq -e 'any(.[]; .name == "p-lab-base")' <<< "$aliases" >/dev/null; then
          incus image alias delete p-lab-base --project user-1000
        fi
        incus image alias create p-lab-base "$fingerprint" --project user-1000
      }
      ensure_image
      ${lib.optionalString automated ''
        # Exercise re-provisioning with the already-imported image as well.
        ensure_image
      ''}
      ${lib.optionalString (runtimeImage != null) ''
        # Keep the infrastructure fixture and production base image distinct.
        runtime_fingerprint=$(cat ${runtimeImage.config.system.build.metadata}/tarball/*.tar.xz \
          ${runtimeImage.config.system.build.squashfs}/*.squashfs | sha256sum)
        runtime_fingerprint=''${runtime_fingerprint%% *}
        if ! incus image list --project user-1000 --format json \
          | jq -e --arg f "$runtime_fingerprint" 'any(.[]; .fingerprint == $f)' >/dev/null; then
          incus image import ${runtimeImage.config.system.build.metadata}/tarball/*.tar.xz \
            ${runtimeImage.config.system.build.squashfs}/*.squashfs --project user-1000
        fi
      ''}
    '';
  };
  systemd.services.p-vm-public-probe = lib.mkIf publicEgressFixture {
    description = "Disposable gateway listener for public-egress denial tests";
    wantedBy = [ "multi-user.target" ];
    requires = [ "incus-preseed.service" ];
    after = [ "incus-preseed.service" ];
    serviceConfig = {
      ExecStart = "${pkgs.python3}/bin/python3 -m http.server 443 --bind 10.233.0.1";
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
  };
  systemd.services.p-vm-host-public-probe = lib.mkIf publicEgressFixture {
    description = "Disposable host-public listener for egress denial tests";
    wantedBy = [ "multi-user.target" ];
    requires = [ "p-vm-prepare.service" ];
    after = [ "p-vm-prepare.service" ];
    serviceConfig = {
      ExecStart = "${pkgs.python3}/bin/python3 -m http.server 443 --bind 8.8.8.8";
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
  };
  systemd.services.p-vm-dnat-target-probe = lib.mkIf publicEgressFixture {
    description = "Disposable routed public DNAT target for egress denial tests";
    wantedBy = [ "multi-user.target" ];
    requires = [ "p-vm-prepare.service" ];
    after = [ "p-vm-prepare.service" ];
    serviceConfig = {
      ExecStart = "${pkgs.iproute2}/bin/ip netns exec p-vm-dnat-target ${pkgs.python3}/bin/python3 -m http.server 443 --bind 8.8.4.10";
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
  };

  systemd.services.incus-preseed.serviceConfig = lib.mkIf automated {
    StandardOutput = "journal+console";
    StandardError = "journal+console";
  };
  systemd.services.incus-preseed.unitConfig.OnFailure = lib.mkIf automated "p-vm-failed.service";
  systemd.services.p-vm-failed = lib.mkIf automated {
    description = "Report VM provisioning failure and power off";
    serviceConfig = {
      Type = "oneshot";
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
    path = [ pkgs.systemd ];
    script = ''
      journalctl -b -u incus-preseed -u p-vm-prepare --no-pager
      echo P_VM_SMOKE_FAIL > /dev/console
      systemctl poweroff --no-block
    '';
  };

  systemd.services.p-vm-test = lib.mkIf automated {
    description = "Run infrastructure smoke test and power off";
    wantedBy = [ "multi-user.target" ];
    requires = [ "p-vm-prepare.service" ];
    after = [ "p-vm-prepare.service" ];
    unitConfig.OnFailure = "p-vm-failed.service";
    serviceConfig = {
      Type = "oneshot";
      # The serial product suite grows with each lifecycle gate. Keep its
      # aggregate budget below the runner's 1200s deadline; individual test
      # and operation deadlines still bound failures within each gate.
      TimeoutStartSec = 1100;
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
    path = [
      pkgs.util-linux
      pkgs.systemd
    ];
    script = ''
      if runuser -u pdev -- ${smokeScript}/bin/p-vm-smoke ${
        lib.optionalString (productTest != null) ''
          \
                  && runuser -u pdev -- env \
                    P_TEST_NETWORK_PROOF_BINARY=${publicNetworkProof}/bin/p-public-network-proof \
                    ${productTest}/bin/p-product-integration''
      }; then
        ${lib.optionalString (productTest != null) ''
          # Even with guest-accessible socket modes, an unrelated host user
          # cannot traverse the unmounted private endpoint ancestor.
          if runuser -u nobody -- test -x /var/lib/p-vm/endpoints/pdev; then
            echo P_VM_SMOKE_FAIL > /dev/console
            systemctl poweroff --no-block
            exit 1
          fi
        ''}
        ${lib.optionalString (productTest != null) "printf '%s\\n' ${lib.escapeShellArg productMarker} > /dev/console"}
        echo P_VM_SMOKE_PASS > /dev/console
      else
        echo P_VM_SMOKE_FAIL > /dev/console
      fi
      systemctl poweroff --no-block
    '';
  };
}
