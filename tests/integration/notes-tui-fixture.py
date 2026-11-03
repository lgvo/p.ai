"""Native sample observations/setup for the separately reviewed notes TUI driver.

This helper never navigates the TUI, creates projects, or deletes resources.
The caller supplies a test-owned session UUID and keeps servicing its PTY while
waiting for this process, as it does for ordinary owner API observations.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import time
import uuid as uuid_module


def command(argv, *, check=True):
    result = subprocess.run(argv, text=True, capture_output=True, timeout=90)
    if check and result.returncode:
        raise RuntimeError(f"{argv[:4]}: {result.stdout[-2000:]} {result.stderr[-1000:]}")
    return result


def incus(*args):
    return command(["incus", "--force-local", "--project", "user-1000", *args])


def guest(*args):
    return incus("exec", f"p-{session}", "--force-noninteractive", "--disable-stdin", "--user", "1000", "--group", "1000",
                 "--cwd", "/workspace", "--env", "HOME=/home/p", "--env",
                 "GIT_SSH=/usr/libexec/p/git-ssh", "--env", "XDG_RUNTIME_DIR=/run/user/1000",
                 "--env", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "--", *args).stdout


def rpc(method, **params):
    deadline = time.monotonic() + 40
    while True:
        result = command(["p", "api", args.socket, method, json.dumps({"v": 1, **params})], check=False)
        response = json.loads(result.stdout)
        if result.returncode == 0:
            return response["result"]
        if method in {"session.inspect", "session.services"} and response.get("error", {}).get("kind") == "busy" and time.monotonic() < deadline:
            time.sleep(0.1)
            continue
        raise RuntimeError(f"{method}: {response.get('error')} {result.stderr[-500:]}")


def sql(query):
    return guest("psql", "-h", "/home/p/.local/state/p-notes/socket", "-d", "notes", "-Atc", query).strip()


def action(unit, verb):
    return rpc("session.service.action", uuid=session,
               unit=f"p-project-notes-{unit}.service", action=verb)


def wait_jobs():
    deadline = time.monotonic() + 30
    while sql("SELECT count(*) FROM jobs WHERE completed_at IS NULL") != "0":
        if time.monotonic() >= deadline:
            raise RuntimeError("pending notes jobs did not complete")
        time.sleep(0.1)


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--socket", required=True)
parser.add_argument("--uuid", required=True)
parser.add_argument("mode", choices=["seed", "install", "setup", "snapshot", "services", "assert-empty", "add", "sql", "wait-jobs", "fail-worker", "recover-worker"])
parser.add_argument("value", nargs="?")
args = parser.parse_args()
session = str(uuid_module.UUID(args.uuid))
os.environ["INCUS_SOCKET"] = "/var/lib/incus/unix.socket.user"

if args.mode == "seed":
    source = Path(os.environ["P_TEST_SOURCE"]) / "examples/notes"
    guest("mkdir", "-p", "examples/notes")
    with tempfile.TemporaryDirectory(prefix="notes58-", dir=os.environ.get("P_TEST_TMP")) as root:
        archive_path = Path(root) / "source.tar"
        with tarfile.open(archive_path, "w") as archive:
            archive.add(source, arcname=".", filter=lambda entry:
                        None if "__pycache__" in Path(entry.name).parts or entry.name.endswith(".pyc") else entry)
        incus("file", "push", "--uid", "1000", "--gid", "1000", "--mode", "0600",
              str(archive_path), f"p-{session}/tmp/notes58-source.tar")
    guest("python3", "-c", 'import tarfile; tarfile.open("/tmp/notes58-source.tar").extractall("/workspace/examples/notes", filter="data")')
    guest("chmod", "-R", "u+w", "examples/notes")
    guest("rm", "/tmp/notes58-source.tar")
    print(json.dumps({"seeded": session}))
elif args.mode == "install":
    guest("python3", "examples/notes/install.py")
    print(json.dumps({"installed": session}))
elif args.mode == "setup":
    guest("python3", "examples/notes/install.py")
    action("db", "start")
    guest("python3", "examples/notes/manage.py", "migrate")
    action("web", "start")
    action("worker", "start")
    print(guest("python3", "examples/notes/client.py", "health").strip())
elif args.mode == "snapshot":
    print(json.dumps({"uuid": session, "source": guest("git", "rev-parse", "HEAD").strip(),
                      "notes": json.loads(guest("python3", "examples/notes/client.py", "list")),
                      "jobs": int(sql("SELECT count(*) FROM jobs")),
                      "pending": int(sql("SELECT count(*) FROM jobs WHERE completed_at IS NULL")),
                      "pids": guest("systemctl", "--user", "show", "p-project-notes-db.service",
                                    "p-project-notes-web.service", "p-project-notes-worker.service", "-p", "MainPID", "--value").split(),
                      "tmux_pid": guest("tmux", "-S", "/run/p-interactive/tmux.sock", "display-message", "-p", "#{pid}").strip(),
                      "services": rpc("session.services", uuid=session).get("services", [])}))
elif args.mode == "services":
    print(json.dumps({"api": rpc("session.services", uuid=session).get("services", []),
                      "native": guest("systemctl", "--user", "show", "p-project-notes-db.service",
                                      "p-project-notes-web.service", "p-project-notes-worker.service",
                                      "-p", "Id,ActiveState,SubState,MainPID,Result")}))
elif args.mode == "assert-empty":
    guest("test", "!", "-e", "/home/p/.local/state/p-notes/cluster")
    services = rpc("session.services", uuid=session).get("services", [])
    if services:
        raise RuntimeError(f"expected empty service discovery: {services}")
    print(json.dumps({"empty": session}))
elif args.mode == "add":
    if args.value is None:
        parser.error("add requires a note body")
    print(guest("python3", "examples/notes/client.py", "add", args.value).strip())
elif args.mode == "sql":
    if args.value is None:
        parser.error("sql requires a query")
    print(sql(args.value))
elif args.mode == "wait-jobs":
    wait_jobs()
    print(json.dumps({"pending": 0}))
elif args.mode == "fail-worker":
    guest("systemctl", "--user", "kill", "--signal=KILL", "--kill-whom=main", "p-project-notes-worker.service")
    print(json.dumps({"worker": "killed"}))
elif args.mode == "recover-worker":
    guest("systemctl", "--user", "reset-failed", "p-project-notes-worker.service")
    action("worker", "start")
    wait_jobs()
    print(json.dumps({"worker": "recovered", "pending": 0}))
