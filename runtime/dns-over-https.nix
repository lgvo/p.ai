{ config, lib, pkgs, ... }:
let
  cfg = config.services.p-dns-over-https;
  # Keep the standard pinned package and TLS transport; refuse redirects so
  # an endpoint cannot introduce an uncached host/system-DNS lookup or another
  # resolver authority. The package check exercises its actual Fetch client.
  resolverPackage = pkgs.dnscrypt-proxy.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./dnscrypt-proxy-no-redirect.patch ];
    doCheck = true;
    checkPhase = ''
      runHook preCheck
      go test ./dnscrypt-proxy -run '^TestPDoHRefusesHTTPRedirects$' -count=1
      runHook postCheck
    '';
  });
  # Static DNS stamps: DoH, literal IPv4 bootstrap, ordinary CA/hostname
  # verification, /dns-query. No DNS query is needed to find either endpoint.
  settings = {
    server_names = [ "p-cloudflare" "p-quad9" ];
    listen_addresses = [ "127.0.0.1:53" ];
    ipv4_servers = true;
    ipv6_servers = false;
    dnscrypt_servers = false;
    doh_servers = true;
    odoh_servers = false;
    force_tcp = true;
    http3 = false;
    http3_probe = false;
    proxy = "";
    http_proxy = "";
    bootstrap_resolvers = [ ];
    ignore_system_dns = true;
    netprobe_timeout = 0;
    netprobe_address = "1.1.1.1:443";
    timeout = 5000;
    cert_ignore_timestamp = false;
    tls_key_log_file = "";
    cache = true;
    sources = { };
    static = {
      p-cloudflare.stamp = "sdns://AgcAAAAAAAAABzEuMS4xLjEAEmNsb3VkZmxhcmUtZG5zLmNvbQovZG5zLXF1ZXJ5";
      p-quad9.stamp = "sdns://AgMAAAAAAAAABzkuOS45LjkADWRucy5xdWFkOS5uZXQKL2Rucy1xdWVyeQ";
    };
  };
  resolverConfig = (pkgs.formats.toml { }).generate "p-dns-over-https.toml" settings;
in
{
  options.services.p-dns-over-https = {
    enable = lib.mkEnableOption "P's fixed loopback DNS-over-HTTPS resolver";
    autostart = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Start at boot; runtime images instead start after trusted public-network preparation.";
    };
  };
  config = lib.mkIf cfg.enable {
    systemd.services.p-dns-over-https = {
      description = "P fixed DNS-over-HTTPS resolver";
      wantedBy = lib.optional cfg.autostart "multi-user.target";
      after = lib.optional cfg.autostart "network-online.target";
      wants = lib.optional cfg.autostart "network-online.target";
      environment = {
        SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
        HTTP_PROXY = "";
        HTTPS_PROXY = "";
        ALL_PROXY = "";
        http_proxy = "";
        https_proxy = "";
        all_proxy = "";
      };
      serviceConfig = {
        Type = "exec";
        ExecStart = "${lib.getExe resolverPackage} -config ${resolverConfig}";
        DynamicUser = true;
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
        NoNewPrivileges = true;
        PrivateDevices = true;
        PrivateTmp = true;
        ProtectHome = true;
        ProtectSystem = "strict";
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictAddressFamilies = [ "AF_INET" "AF_UNIX" ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        LockPersonality = true;
        Restart = "on-failure";
        RestartSec = 2;
        TimeoutStartSec = 10;
        TimeoutStopSec = 5;
      };
    };
  };
}
