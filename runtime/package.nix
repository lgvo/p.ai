{ lib, buildGoModule, callPackage, go-licenses }:
let
  goNotices = callPackage ../dev/go-license-notices.nix { };
in
buildGoModule {
  pname = "p-runtime-kit";
  version = "0.1.0-dev";
  src = lib.cleanSourceWith {
    src = ../.;
    filter =
      path: type:
      let
        relative = lib.removePrefix "${toString ../.}/" (toString path);
      in
      toString path == toString ../.
      || lib.hasPrefix "cmd/" relative
      || lib.hasPrefix "internal/" relative
      || builtins.elem relative [
        "cmd"
        "internal"
        "go.mod"
        "go.sum"
        "LICENSE"
      ];
  };
  vendorHash = "sha256-YZmEJBF15LmqH7gACdtJUBoLDZ/oO2oy2ILwQK8NAUY=";
  subPackages = [ "cmd/p-runtime-kit" ];
  env.CGO_ENABLED = "0";
  nativeBuildInputs = [ go-licenses ];
  postBuild = ''
    go-licenses check --allowed_licenses=Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC ./cmd/p-runtime-kit
    go-licenses save --save_path="$TMPDIR/p-module-notices" ./cmd/p-runtime-kit
  '';
  postInstall = ''
    mkdir -p "$out/share/p/licenses"
    cp LICENSE "$out/share/p/LICENSE"
    cp -R "$TMPDIR/p-module-notices" "$out/share/p/licenses/modules"
    cp -R ${goNotices} "$out/share/p/licenses/go"
  '';
  doCheck = false;
}
