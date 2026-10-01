#!/usr/bin/env python3
"""Initialize once, refuse uncertain state, then exec PostgreSQL for systemd."""
import argparse
import fcntl
import pwd
import os
import re
import shutil
import subprocess
from pathlib import Path

from db import state_dir


def binary(name, bin_dir=None):
    found = str(Path(bin_dir) / name) if bin_dir else shutil.which(name)
    if not found or not Path(found).is_file() or not os.access(found, os.X_OK):
        raise RuntimeError(f"Missing PostgreSQL executable {name}; supply --postgres-bin")
    return str(Path(found).resolve())


def initialize(bin_dir=None):
    postgres = binary("postgres", bin_dir)
    initdb = binary("initdb", bin_dir)
    version = subprocess.check_output([postgres, "--version"], text=True).strip()
    major = re.search(r"\b(\d+)(?:\.\d+)*\b", version).group(1)
    root = state_dir()
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    if root.is_symlink() or root.stat().st_uid != os.getuid():
        raise RuntimeError("Notes state directory must be owned by the current user and must not be a symlink")
    root.chmod(0o700)
    cluster = root / "cluster"
    staging = root / "cluster.initializing"
    with (root / "setup.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if staging.exists():
            raise RuntimeError(f"Incomplete cluster initialization at {staging}; preserve and inspect it before retrying")
        if cluster.exists() or cluster.is_symlink():
            required = ["PG_VERSION", "global/pg_control", "base", "pg_wal", "postgresql.conf", "pg_hba.conf"]
            if cluster.is_symlink() or not cluster.is_dir() or any(not (cluster / p).exists() for p in required):
                raise RuntimeError(f"Damaged or uncertain existing cluster at {cluster}; refusing to initialize")
            actual = (cluster / "PG_VERSION").read_text().strip()
            if actual != major:
                raise RuntimeError(f"PostgreSQL major mismatch: cluster {actual}, executable {major}; refusing to initialize")
        else:
            subprocess.run([initdb, "-D", str(staging), "--username", pwd.getpwuid(os.getuid()).pw_name,
                            "--auth-local=peer", "--auth-host=reject", "--encoding=UTF8",
                            "--locale=C", "--no-instructions"], check=True)
            staging.rename(cluster)
            print(f"initialized private PostgreSQL {major} cluster at {cluster}", flush=True)
        socket = root / "socket"
        socket.mkdir(mode=0o700, exist_ok=True)
        if socket.is_symlink() or socket.stat().st_uid != os.getuid():
            raise RuntimeError("PostgreSQL socket directory must be private and owned by the current user")
        socket.chmod(0o700)
        if len(os.fsencode(str(socket / ".s.PGSQL.5432"))) >= 104:
            raise RuntimeError("PostgreSQL socket path is too long; use a short P_NOTES_STATE path")
    return postgres, cluster, socket


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["init", "serve"])
    parser.add_argument("--postgres-bin")
    args = parser.parse_args()
    postgres, cluster, socket = initialize(args.postgres_bin)
    if args.action == "serve":
        print(f"starting PostgreSQL: private socket={socket}, TCP disabled", flush=True)
        os.execv(postgres, [postgres, "-D", str(cluster), "-k", str(socket),
                           "-c", "listen_addresses=", "-c", "unix_socket_permissions=0700",
                           "-c", "max_connections=30"])


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError) as exc:
        raise SystemExit(str(exc))
