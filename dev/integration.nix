{ selectedStepsText ? "", outerHostIPv4Text ? "", outerHostLANIPv4Text ? "" }:
let
  # Reuse the lab's locked nixpkgs without copying VM disks or module caches
  # into a flake source. package.nix explicitly filters the production source.
  lab = builtins.getFlake "path:${toString ./vm}";
  nixpkgs = lab.inputs.nixpkgs;
  system = "x86_64-linux";
  pkgs = nixpkgs.legacyPackages.${system};
  # Runner captures actual host addresses, never credentials. Reject text
  # outside the IPv4 grammar before embedding it in the guest nftables rules.
  outerHostIPv4 = if outerHostIPv4Text == "" then [ ] else nixpkgs.lib.splitString "\n" outerHostIPv4Text;
  outerHostLANIPv4 = if outerHostLANIPv4Text == "" then [ ] else nixpkgs.lib.splitString "\n" outerHostLANIPv4Text;
  validIPv4 = address:
    let parts = nixpkgs.lib.splitString "." address;
    in builtins.length parts == 4 && builtins.all
      (part: builtins.match "(0|[1-9][0-9]{0,2})" part != null
        && nixpkgs.lib.toInt part <= 255) parts;
  validPrefix = prefix:
    let parts = nixpkgs.lib.splitString "/" prefix;
    in (builtins.length parts == 1 && validIPv4 prefix)
      || (builtins.length parts == 2 && validIPv4 (builtins.head parts)
        && builtins.match "(0|[1-9][0-9]?)" (builtins.elemAt parts 1) != null
        && nixpkgs.lib.toInt (builtins.elemAt parts 1) <= 32);
  stepEntries = builtins.readDir ../tests/integration/steps;
  validStepName = name: builtins.match "[A-Za-z0-9][A-Za-z0-9._-]*\\.sh" name != null;
  availableSteps = builtins.filter
    (name: validStepName name && stepEntries.${name} == "regular")
    (builtins.attrNames stepEntries);
  requestedSteps = if selectedStepsText == "" then
    throw "Use dev/test-vm for the full suite; each guest requires an explicit routing-compatible selection."
    else nixpkgs.lib.splitString "\n" selectedStepsText;
  selectedSteps =
    assert builtins.all (name: builtins.elem name availableSteps) requestedSteps;
    assert builtins.length requestedSteps == builtins.length (nixpkgs.lib.unique requestedSteps);
    assert !builtins.elem "37-public-egress.sh" requestedSteps || builtins.length requestedSteps == 1;
    builtins.filter (name: builtins.elem name requestedSteps) availableSteps;
  selectedMarker = if selectedSteps == [ ] then "P_PRODUCT_INTEGRATION_PASS"
    else "P_PRODUCT_INTEGRATION_SELECTED_PASS ${nixpkgs.lib.concatStringsSep "," selectedSteps}";
  pPackage = pkgs.callPackage ./package.nix { };
  # Fixture-only composition of the production Git service and authority APIs.
  # The production package above runs the shared Go checks.
  gitFixture = pPackage.overrideAttrs {
    subPackages = [ "tests/integration/cmd/git-fixture" ];
    doCheck = false;
  };
  bundleFixture = pPackage.overrideAttrs {
    subPackages = [ "tests/integration/cmd/bundle-fixture" ];
    doCheck = false;
  };
  runtimeFixture = pPackage.overrideAttrs {
    subPackages = [ "tests/integration/cmd/runtime-fixture" ];
    doCheck = false;
  };
  builderFixture = pPackage.overrideAttrs {
    subPackages = [ "tests/integration/cmd/builder-fixture" ];
    doCheck = false;
  };
  attachmentFixture = pPackage.overrideAttrs {
    subPackages = [ "tests/integration/cmd/attachment-fixture" ];
    doCheck = false;
  };
  originFixture = pPackage.overrideAttrs {
    subPackages = [ "tests/integration/cmd/origin-fixture" ];
    doCheck = false;
  };
  nixActivationFixture = pPackage.overrideAttrs (old: {
    subPackages = [ "tests/integration/cmd/nix-activation-fixture" ];
    doCheck = false;
    # Keep buildGoModule's GOFLAGS (-trimpath) while forcing a portable guest binary.
    env = old.env // { CGO_ENABLED = "0"; };
  });
  # Runs inside the minimal runtime image to exercise its mounted Unix socket.
  sessionFixture = pkgs.runCommand "p-session-fixture" { nativeBuildInputs = [ pkgs.go ]; } ''
    export GOCACHE="$TMPDIR/go-cache" GOMODCACHE="$TMPDIR/go-modules"
    export GOPROXY=off GOTOOLCHAIN=local
    mkdir -p "$out/bin"
    cd ${pPackage.src}
    GO111MODULE=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
      -o "$out/bin/session-fixture" ./tests/integration/cmd/session-fixture
  '';
  # Test-only immutable client shim: while the count file exists, append the
  # builder each image publish names (`--force-local --project P publish
  # NAME ...`), so a scenario counts its builds.
  publishCountIncusWrapper = pkgs.writeShellScriptBin "p-incus-publish-count-wrapper" ''
    set -eu
    count=/tmp/p-vm-publish-count-1000
    if test "''${4-}" = publish && test -f "$count"; then
      printf '%s\n' "''${5-}" >> "$count"
    fi
    exec ${pkgs.incus}/bin/incus "$@"
  '';
  wasiFilter =
    pkgs.runCommand "p-wasi-filter-log-fixture"
      {
        nativeBuildInputs = [ pkgs.go ];
      }
      ''
        export GOCACHE="$TMPDIR/go-cache"
        export GOMODCACHE="$TMPDIR/go-modules"
        export GOPROXY=off GOTOOLCHAIN=local
        mkdir -p "$out"
        cd ${pPackage.src}
        GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build \
          -o "$out/filter.wasm" ./plugins/examples/filter-log
        cp plugins/examples/filter-log/plugin.json "$out/plugin.json"
      '';
  wasiHostile =
    pkgs.runCommand "p-wasi-hostile-fixture"
      {
        nativeBuildInputs = [ pkgs.go ];
      }
      ''
        export GOCACHE="$TMPDIR/go-cache"
        export GOMODCACHE="$TMPDIR/go-modules"
        export GOPROXY=off GOTOOLCHAIN=local
        mkdir -p "$out"
        cd ${pPackage.src}
        GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build \
          -o "$out/filter.wasm" ./internal/plugin/testdata/wasi-hostile
        cp plugins/examples/filter-log/plugin.json "$out/plugin.json"
      '';
  image = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [ ./vm/image.nix ];
  };
  runtimeImage = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [ ../runtime/image.nix ] ++ nixpkgs.lib.optional
      (builtins.any (name: builtins.elem name selectedSteps)
        [ "57-developer-workflow.sh" "58-notes-tui.sh" ]) ./notes-runtime.nix;
  };
  runtimeIdentity = pkgs.runCommand "p-runtime-image-fingerprint" { } ''
    cat ${runtimeImage.config.system.build.metadata}/tarball/*.tar.xz \
      ${runtimeImage.config.system.build.squashfs}/*.squashfs | sha256sum | cut -d' ' -f1 > "$out"
  '';
  productTest = pkgs.writeShellApplication {
    name = "p-product-integration";
    runtimeInputs = with pkgs; [
      pPackage
      gitFixture
      bundleFixture
      runtimeFixture
      builderFixture
      attachmentFixture
      originFixture
      nixActivationFixture
      sessionFixture
      bash
      coreutils
      diffutils
      binutils
      gnugrep
      gnused
      gawk
      jq
      git
      openssh
      incus
      socat
      util-linux
      systemd
      (python3.withPackages (ps: [ ps.pyte ]))
    ];
    text = ''
      export P_TEST_SOURCE=${pPackage.src}
      export P_TEST_WASI_FILTER=${wasiFilter}
      export P_TEST_WASI_HOSTILE=${wasiHostile}
      export P_TEST_RUNTIME_IMAGE_FILE=${runtimeIdentity}
      export P_TEST_INCUS_WRAPPER=${publishCountIncusWrapper}/bin/p-incus-publish-count-wrapper
      export P_TEST_NIX_FIXTURE=${nixActivationFixture}
      export P_TEST_SELECTED_STEPS=${nixpkgs.lib.escapeShellArg (nixpkgs.lib.concatStringsSep "\n" selectedSteps)}
      export P_TEST_NFT_BINARY=${pkgs.nftables}/bin/nft
      export P_TEST_OUTER_HOST_IPV4=${nixpkgs.lib.escapeShellArg outerHostIPv4Text}
      export P_TEST_OUTER_HOST_LAN_IPV4=${nixpkgs.lib.escapeShellArg outerHostLANIPv4Text}
      export P_TEST_PUBLIC_EGRESS_FIXTURE=${if selectedSteps == [ ] || nixpkgs.lib.elem "37-public-egress.sh" selectedSteps then "1" else "0"}
    ''
    + builtins.readFile ../tests/integration/run.sh;
  };
  vm =
    (nixpkgs.lib.nixosSystem {
      inherit system;
      specialArgs = {
        dnsOverHTTPSModule = ../runtime/dns-over-https.nix;
        outerHostIPv4 = assert builtins.all validIPv4 outerHostIPv4; outerHostIPv4;
        outerHostLANIPv4 = assert builtins.all validPrefix outerHostLANIPv4; outerHostLANIPv4;
        inherit
          image
          runtimeImage
          pPackage
          productTest
          selectedSteps
          ;
        automated = true;
        demo = false;
        demoPublic = false;
      };
      modules = [ ./vm/machine.nix ];
    }).config.system.build.vm;
  runner = pkgs.writeShellApplication {
    name = "p-vm-integration-test";
    runtimeInputs = with pkgs; [
      coreutils
      gnugrep
    ];
    text = builtins.readFile ./vm/verify-markers.sh + ''
      log_dir="$(realpath -m "''${P_VM_LOG_DIR:-.cache/p-vm}")"
      mkdir -p "$log_dir"
      work_dir="$(mktemp -d -t p-vm-integration.XXXXXXXX)"
      trap 'rm -rf -- "$work_dir"' EXIT
      export NIX_DISK_IMAGE="$work_dir/disk.qcow2"
      log_file="$log_dir/integration-$(date -u +%Y%m%dT%H%M%SZ)-$$.log"
      expected_marker=${nixpkgs.lib.escapeShellArg selectedMarker}
      echo "Booting a fresh product-test VM; console log: $log_file"
      status=0
      timeout --foreground --kill-after=30s "''${P_VM_TIMEOUT:-1200}" \
        ${vm}/bin/run-p-vm-vm >"$log_file" 2>&1 || status=$?
      cat "$log_file"
      if [ "$status" -ne 0 ] || ! vm_log_has_marker "$log_file" 'P_VM_SMOKE_PASS' \
        || ! vm_log_has_marker "$log_file" "$expected_marker"; then
        echo "Product VM integration failed (runner status $status); see $log_file" >&2
        exit 1
      fi
      ${if selectedSteps == [ ] then ''
        echo "Product VM integration passed; fresh VM disk removed."
      '' else ''
        echo ${nixpkgs.lib.escapeShellArg "Selected product VM integration steps passed: ${nixpkgs.lib.concatStringsSep ", " selectedSteps}; fresh VM disk removed."}
      ''}
    '';
  };
in
{
  inherit
    pPackage
    gitFixture
    bundleFixture
    originFixture
    nixActivationFixture
    runtimeFixture
    builderFixture
    wasiFilter
    wasiHostile
    vm
    runner
    ;
}
