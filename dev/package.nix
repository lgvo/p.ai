{
  lib,
  buildGoModule,
  git,
  openssh,
  python3,
}:
buildGoModule {
  pname = "p";
  version = "0.1.0-dev";
  src = lib.cleanSourceWith {
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
  vendorHash = "sha256-YZmEJBF15LmqH7gACdtJUBoLDZ/oO2oy2ILwQK8NAUY=";
  subPackages = [ "cmd/p" ];
  doCheck = true;
  nativeCheckInputs = [
    git
    openssh
    python3
  ];
  checkPhase = ''
    runHook preCheck
    go test ./...
    python3 -I -B -m unittest discover -s tests/unit -p codex_adapter_test.py
    runHook postCheck
  '';
  meta = {
    description = "P development-stream control plane";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" ];
    mainProgram = "p";
  };
}
