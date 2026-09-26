{
  lib,
  buildGoModule,
  callPackage,
  git,
  openssh,
  python3,
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
  ];
  checkPhase = ''
    runHook preCheck
    go test ./...
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
