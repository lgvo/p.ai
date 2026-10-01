let
  lab = builtins.getFlake "path:${toString ./vm}";
  pkgs = lab.inputs.nixpkgs.legacyPackages.x86_64-linux;
in
pkgs.mkShellNoCC {
  packages = [
    pkgs.postgresql
    pkgs.systemd
    (pkgs.python3.withPackages (ps: [ ps.psycopg ]))
  ];
}
