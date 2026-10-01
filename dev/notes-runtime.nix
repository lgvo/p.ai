{ lib, pkgs, ... }:
{
  # Each session copies the example into its own workspace and initializes
  # its own cluster and units. No database or credentials are baked in.
  environment.systemPackages = [
    pkgs.postgresql
    (lib.hiPrio (pkgs.python3.withPackages (ps: [ ps.psycopg ])))
  ];
  environment.etc."p-notes-example".source = builtins.path {
    path = ../examples/notes;
    name = "p-notes-example";
    filter = path: _: baseNameOf path != "__pycache__" && !lib.hasSuffix ".pyc" path;
  };
}
