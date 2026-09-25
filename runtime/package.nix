{ lib, buildGoModule }:
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
      ];
  };
  vendorHash = "sha256-YZmEJBF15LmqH7gACdtJUBoLDZ/oO2oy2ILwQK8NAUY=";
  subPackages = [ "cmd/p-runtime-kit" ];
  doCheck = false;
}
