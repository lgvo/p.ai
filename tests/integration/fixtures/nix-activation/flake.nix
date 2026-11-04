{
  description = "Offline Nix activation compatibility fixture";
  outputs = { self }:
    let
      # setup commits the pinned guest base's Bash store identity. Declare it
      # as a string dependency, without reading an ambient absolute path during
      # pure evaluation. The private Nix daemon supplies its registered closure.
      bashStore = "@BASH_STORE@";
      bash = builtins.appendContext "${bashStore}/bin/bash" {
        ${bashStore} = { path = true; };
      };
      common = {
        name = "p-activation-fixture";
        system = "x86_64-linux";
        builder = bash;
        args = [ "-c" "printf built > \"$out\"; printf built > \"$dev\"" ];
        outputs = [ "out" "dev" ];
        stdenv = ./stdenv;
        QUOTED = "double \"quote\", single 'quote', colon: and spaces";
        SHOULD_UNSET = "must disappear in setup";
        shellHook = "export HOOK_VALUE='hook ran'; export HOOK_OUT=\"$out\"; printf hook >> /tmp/p-nix-hook-marker";
      };
    in {
      devShells.x86_64-linux.default = builtins.derivation common;
      devShells.x86_64-linux.structured = builtins.derivation (common // {
        name = "p-activation-structured-fixture";
        __structuredAttrs = true;
        args = [ "-c" ". \"$NIX_ATTRS_SH_FILE\"; printf built > \"\${outputs[out]}\"; printf built > \"\${outputs[dev]}\"" ];
      });
      devShells.x86_64-linux.failing = builtins.derivation (common // {
        name = "p-activation-failing-fixture";
        args = [ "-c" "exit 17" ];
      });
    };
}
