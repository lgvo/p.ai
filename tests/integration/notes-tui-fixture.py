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


identity_keys = ("user.p.instance_uuid", "user.p.session_uuid", "user.p.workspace_owner",
                 "user.p.project_path", "user.p.contract_version", "user.p.image_fingerprint")


def native_instance(name):
    values = json.loads(incus("list", "^" + name + "$", "--format", "json").stdout)
    if len(values) != 1 or values[0]["name"] != name:
        raise RuntimeError("exact fixture instance is absent or ambiguous")
    return values[0]


def helper_identity(item, operation, process_limit):
    if item["name"] != "p-workspace-" + str(uuid_module.UUID(operation["id"])):
        raise RuntimeError("helper name differs from its exact operation")
    configs = (item["config"], item["expanded_config"])
    wanted = {"limits.cpu": "1", "limits.memory": "768MiB", "limits.processes": process_limit,
              "user.p.session_uuid": operation["id"], "user.p.workspace_owner": session,
              "user.p.project_path": operation["project"], "user.p.contract_version": "p.workspace-helper/v1",
              "user.p.instance_uuid": operation["evidence"]["instance_uuid"],
              "user.p.image_fingerprint": operation["evidence"]["base_fingerprint"]}
    if any(config.get(key) != value for config in configs for key, value in wanted.items()):
        raise RuntimeError("helper fixture identity or bounded resources changed")
    native_uuid = str(uuid_module.UUID(item["config"]["volatile.uuid"]))
    return {"name": item["name"], "native_uuid": native_uuid,
            "labels": {key: item["config"].get(key) for key in identity_keys}}


def inspection_fault(mode, record_path):
    record_path = Path(record_path)
    if mode == "block-inspection":
        if record_path.exists():
            raise RuntimeError("refusing to replace an existing fault record")
        source = native_instance("p-" + session)
        if source["status"] != "Stopped" or source["config"].get("user.p.session_uuid") != session:
            raise RuntimeError("fault fixture requires its exact stopped source")
        source_uuid = source["config"]["volatile.uuid"]
        source_generation = source["config"]["volatile.uuid.generation"]
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            instances = json.loads(incus("list", "--format", "json").stdout)
            candidates = [item for item in instances
                          if item["config"].get("user.p.workspace_owner") == session
                          and item["config"].get("user.p.contract_version") == "p.workspace-helper/v1"]
            if len(candidates) > 1:
                raise RuntimeError("multiple owner helpers; refusing a fault")
            if not candidates:
                time.sleep(0.03)
                continue
            helper = candidates[0]
            op_id = str(uuid_module.UUID(helper["config"]["user.p.session_uuid"]))
            operation = rpc("operation.inspect", id=op_id)["operation"]
            if operation["kind"] != "workspace.loss.inspect" or operation["session_uuid"] != session:
                raise RuntimeError("unexpected owner helper operation")
            if operation["status"] != "running" or operation["phase"] != "init-issued":
                raise RuntimeError("missed safe init-issued injection phase; no fault applied")
            if helper["status"] != "Running":
                time.sleep(0.03)
                continue
            evidence = operation["evidence"]
            if (evidence["original_status"] != "Stopped" or evidence["source_incus_uuid"] != source_uuid
                    or evidence["source_generation"] != source_generation):
                raise RuntimeError("accepted source identity differs from the fixture")
            identity = helper_identity(helper, operation, "256")
            current = rpc("operation.inspect", id=op_id)["operation"]
            if (time.monotonic() >= deadline or current["status"] != "running"
                    or current["phase"] != "init-issued" or current["evidence"] != evidence):
                raise RuntimeError("safe injection window ended; no fault applied")
            # Persist the restoration identity before the sole reversible
            # resource change. Never edit ownership labels or durable state.
            record = {"source": session, "source_native_uuid": source_uuid,
                      "source_generation": source_generation, "operation": operation,
                      "helper": identity, "original_limit": "256", "changed_limit": "255",
                      "native_before": helper}
            with os.fdopen(os.open(record_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as output:
                json.dump(record, output)
            incus("config", "set", identity["name"], "limits.processes", "255")
            changed = native_instance(identity["name"])
            if helper_identity(changed, operation, "255") != identity:
                raise RuntimeError("helper identity changed during injection; record retained")
            current = rpc("operation.inspect", id=op_id)["operation"]
            print(json.dumps({"fault": "applied", "record": str(record_path), "operation": op_id,
                              "phase_after": current["phase"], "status_after": current["status"]}))
            return
        raise RuntimeError("helper did not reach the safe injection window; no fault applied")
    record = json.loads(record_path.read_text())
    if record["source"] != session or record["original_limit"] != "256" or record["changed_limit"] != "255":
        raise RuntimeError("fault record is outside this fixture")
    source = native_instance("p-" + session)
    if (source["status"] != "Stopped" or source["config"].get("user.p.session_uuid") != session
            or source["config"]["volatile.uuid"] != record["source_native_uuid"]
            or source["config"]["volatile.uuid.generation"] != record["source_generation"]):
        raise RuntimeError("fault source identity changed; refusing restoration")
    operation = rpc("operation.inspect", id=record["operation"]["id"])["operation"]
    immutable = lambda evidence: {key: value for key, value in evidence.items() if key != "result"}
    if (operation["kind"] != "workspace.loss.inspect" or operation["session_uuid"] != session
            or immutable(operation["evidence"]) != immutable(record["operation"]["evidence"])
            or operation["status"] != "blocked"):
        raise RuntimeError("fault operation is not the same blocked inspection")
    helper = native_instance(record["helper"]["name"])
    if helper_identity(helper, operation, "255") != record["helper"]:
        raise RuntimeError("fault helper identity changed; refusing restoration")
    incus("config", "set", helper["name"], "limits.processes", "256")
    restored = native_instance(helper["name"])
    if helper_identity(restored, operation, "256") != record["helper"]:
        raise RuntimeError("helper identity changed during restoration")
    print(json.dumps({"fault": "restored", "operation": operation["id"], "native_uuid": record["helper"]["native_uuid"]}))


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--socket", required=True)
parser.add_argument("--uuid", required=True)
parser.add_argument("mode", choices=["seed", "install", "setup", "snapshot", "services", "assert-empty", "add", "sql", "wait-jobs", "fail-worker", "recover-worker", "block-inspection", "restore-inspection"])
parser.add_argument("value", nargs="?")
args = parser.parse_args()
session = str(uuid_module.UUID(args.uuid))
os.environ["INCUS_SOCKET"] = "/var/lib/incus/unix.socket.user"

if args.mode in {"block-inspection", "restore-inspection"}:
    if args.value is None:
        parser.error("inspection fault requires a private record path")
    inspection_fault(args.mode, args.value)
elif args.mode == "seed":
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
