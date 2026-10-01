{
  lib,
  buildGoModule,
  callPackage,
  git,
  openssh,
  python3,
  proot,
  bash,
  coreutils,
  go-licenses,
}:
let
  source = lib.cleanSourceWith {
    src = ../.;
    filter =
      path: type:
      let
        name = baseNameOf path;
        relative = lib.removePrefix "${toString ../.}/" (toString path);
        top = builtins.head (lib.splitString "/" relative);
      in
      (
        toString path == toString ../.
        || builtins.elem top [
          "cmd"
          "internal"
          "pkg"
          "plugins"
          "runtime"
          "tests"
          "examples"
          "go.mod"
          "go.sum"
          "LICENSE"
        ]
      )
      && !(builtins.elem name [
        ".git"
        ".cache"
        ".prototype"
        ".direnv"
        "__pycache__"
        "result"
      ])
      && !(lib.hasSuffix ".pyc" name)
      && !(lib.hasPrefix "result-" name);
  };
  bundledPlugins = callPackage ./plugin-packages.nix { src = source; };
  goNotices = callPackage ./go-license-notices.nix { };
in
buildGoModule {
  pname = "p";
  version = "0.1.0-dev";
  src = source;
  vendorHash = "sha256-YZmEJBF15LmqH7gACdtJUBoLDZ/oO2oy2ILwQK8NAUY=";
  subPackages = [ "cmd/p" ];
  env.CGO_ENABLED = "0";
  nativeBuildInputs = [ go-licenses ];
  postBuild = ''
    go-licenses check --allowed_licenses=Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC ./cmd/p
    go-licenses save --save_path="$TMPDIR/p-module-notices" ./cmd/p
  '';
  postInstall = ''
    mkdir -p "$out/share/p/plugins"
    cp -R ${bundledPlugins}/plugins/. "$out/share/p/plugins/"
    cp LICENSE "$out/share/p/LICENSE"
    mkdir -p "$out/share/p/licenses"
    cp -R "$TMPDIR/p-module-notices" "$out/share/p/licenses/modules"
    cp -R ${goNotices} "$out/share/p/licenses/go"
    cp -R ${bundledPlugins}/licenses "$out/share/p/licenses/plugins"
    # The scanner saves detected package licenses, but not every nested or
    # supplementary attribution file (for example LICENSE-MMAP-GO).
    while IFS= read -r -d $'\0' notice; do
      relative="''${notice#vendor/}"
      mkdir -p "$out/share/p/licenses/vendor/$(dirname "$relative")"
      cp "$notice" "$out/share/p/licenses/vendor/$relative"
    done < <(find vendor -type f \( -name 'LICENSE*' -o -name 'NOTICE*' \
      -o -name 'COPYING*' -o -name 'AUTHORS*' -o -name 'PATENTS*' \) -print0)
  '';
  passthru = { inherit bundledPlugins; };
  doCheck = true;
  nativeCheckInputs = [
    git
    openssh
    python3
    proot
    bash
    coreutils
  ];
  checkPhase = ''
    runHook preCheck
    # Nix user namespaces can expose / as foreign-owned UID 65534. Keep
    # production ancestry checks intact: run all Go checks in a private owned
    # filesystem view, with actual euid/file owners/inodes and no fake root (-0).
    # This is a unit fixture; selected VM checks use the ordinary host ancestry.
    # Prove the real namespace foreign-root refusal separately; the same test
    # asserts permitted ownership again inside the private fixture view.
    go test ./internal/control -run '^TestProductionPathCheckRefusesForeignRoot$' -count=1 -v
    p_check_root="$(mktemp -d "$TMPDIR/p.XXX")"
    p_check_cache="$TMPDIR/p-unit-go-cache"
    mkdir -m 0700 "$p_check_root/build" "$p_check_root/t" "$p_check_root/tmp" "$p_check_root/bin" "$p_check_root/usr" "$p_check_root/etc"
    mkdir -m 0700 "$p_check_root/usr/bin"
    ln -s ${bash}/bin/bash "$p_check_root/bin/sh"
    ln -s ${coreutils}/bin/env "$p_check_root/usr/bin/env"
    mkdir -p "$p_check_cache"
    chmod 0700 "$p_check_root" "$p_check_cache"
    p_check_uid="$(id -u)"
    p_check_gid="$(id -g)"
    # Inert account lookup data for ssh-keygen; no host account/credential files.
    printf 'p-unit:x:%s:%s:P unit fixture:/nonexistent:/bin/sh\n' "$p_check_uid" "$p_check_gid" > "$p_check_root/etc/passwd"
    printf 'p-unit:x:%s:\n' "$p_check_gid" > "$p_check_root/etc/group"
    chmod 0600 "$p_check_root/etc/passwd" "$p_check_root/etc/group"
    echo "P_UNIT_FIXTURE_ROOT real_uid=$(id -u) namespace_root_uid=$(stat -c %u /) fixture_root_uid=$(stat -c %u "$p_check_root")"
    proot -r "$p_check_root" -b /nix -b /proc -b /dev \
      -b "$PWD:/build/source" -b "$p_check_cache:/build/go-cache" \
      -w /build/source ${bash}/bin/bash -euc '
        test "$(id -u)" = "$1"
        test "$(stat -c %u /)" = "$1"
        test "$(stat -c %u /t)" = "$1"
        test "$(stat -c %u /tmp)" = "$1"
        test "$(stat -c %a /)" = 700
        test "$(stat -c %a /t)" = 700
        test "$(stat -c %a /tmp)" = 700
        echo "P_UNIT_FIXTURE_VIEW real_uid=$(id -u) root_uid=$(stat -c %u /) tmp_uid=$(stat -c %u /t) literal_tmp_uid=$(stat -c %u /tmp)"
        export TMPDIR=/t GOCACHE=/build/go-cache
        go test ./...
      ' p-unit-check "$p_check_uid"
    python3 -I -B -m unittest discover -s tests/unit -p '*_test.py'
    runHook postCheck
  '';
  meta = {
    description = "P development-stream control plane";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" ];
    mainProgram = "p";
  };
}
