#!/usr/bin/env python3
"""Idempotently install the three user units; start and enable explicitly."""
import argparse
import os
import subprocess
import sys
from pathlib import Path
from database import binary


def quoted(value):
    value = str(value)
    if any(c in value for c in "\n\r\0"):
        raise ValueError("unit paths must not contain newline or NUL")
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%") + '"'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument("--postgres-bin")
    parser.add_argument("--state", help="private data root override (default: $HOME/.local/state/p-notes)")
    parser.add_argument("--unit-dir", default=str(Path.home() / ".config/systemd/user"))
    parser.add_argument("--no-reload", action="store_true", help="render-only testing outside a user manager")
    args = parser.parse_args()
    python = str(Path(args.python).resolve())
    subprocess.run([python, "-c", "import psycopg; print('Psycopg', psycopg.__version__)"], check=True)
    postgres = binary("postgres", args.postgres_bin)
    postgres_bin = Path(postgres).parent
    for name in ["initdb", "pg_ctl", "psql"]:
        binary(name, postgres_bin)
    subprocess.run([postgres, "--version"], check=True)
    source = Path(__file__).resolve().parent
    target = Path(args.unit_dir).absolute()
    target.mkdir(parents=True, exist_ok=True)
    # A source token occurs both alone and as an executable argument prefix.
    replacements = {"@PYTHON@": quoted(python), "@SOURCE@": quoted(source),
                    "@POSTGRES_BIN@": quoted(postgres_bin),
                    "@STATE@": "Environment=" + quoted("P_NOTES_STATE=" + str(Path(args.state).absolute())) if args.state else ""}
    for service in ["db", "web", "worker"]:
        name = f"p-project-notes-{service}.service"
        text = (source / name).read_text()
        # WorkingDirectory is a scalar path, not shell argv: enclosing quotes
        # become literal path bytes. Preserve spaces and escape unit specifiers.
        text = text.replace("WorkingDirectory=@SOURCE@", "WorkingDirectory=" + str(source).replace("%", "%%"))
        # Quotes must enclose the entire source/file argument, including its suffix.
        for filename in ["database.py", "manage.py", "web.py", "worker.py"]:
            text = text.replace("@SOURCE@/" + filename, quoted(source / filename))
        for key, value in replacements.items():
            text = text.replace(key, value)
        dest = target / name
        if not dest.exists() or dest.read_text() != text:
            temporary = target / (name + ".tmp")
            temporary.write_text(text)
            os.replace(temporary, dest)
        print(f"installed {dest}")
    if not args.no_reload:
        subprocess.run(["systemctl", "--user", "daemon-reload"], check=True)
    print("Installed only. Start the database, web and worker explicitly; enablement is optional.")


if __name__ == "__main__":
    main()
