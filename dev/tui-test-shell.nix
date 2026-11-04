let
  lab = builtins.getFlake "path:${toString ./vm}";
  pkgs = lab.inputs.nixpkgs.legacyPackages.x86_64-linux;
in
pkgs.mkShellNoCC {
  packages = [
    (pkgs.python3.withPackages (ps: [ ps.pyte ps.pillow ]))
    pkgs.dejavu_fonts
    pkgs.go
    pkgs.tmux
    pkgs.bash
    pkgs.coreutils
  ];
  P_TUI_MOCK_SHELL = "1";
}
