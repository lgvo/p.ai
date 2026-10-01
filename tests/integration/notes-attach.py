"""CLI attachment regression; terminal queries serviced during backend waits."""
import errno
import fcntl
import json
import os
import pty
import select
import struct
import subprocess
import sys
import termios
import time
import uuid as uuid_module
from tui_terminal import TerminalScreen, TerminalStream

socket, uuid = sys.argv[1:]
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 35, 120, 0, 0))
child = subprocess.Popen(["p", "attach", socket, uuid], stdin=slave, stdout=slave,
                         stderr=slave, env={**os.environ, "TERM": "xterm-256color"}, start_new_session=True)
os.close(slave)
screen = TerminalScreen(120, 35, lambda data: os.write(master, data.encode() if isinstance(data, str) else data))
terminal = TerminalStream(screen)


def read(timeout=0.03):
    if select.select([master], [], [], timeout)[0]:
        try:
            terminal.feed(os.read(master, 65536))
        except OSError as exc:
            if exc.errno != errno.EIO:
                raise


def observe_process(argv):
    command = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    deadline = time.monotonic() + 40
    try:
        while command.poll() is None:
            assert time.monotonic() < deadline, "attachment inspection timed out"
            read()
        out, err = command.communicate()
        return subprocess.CompletedProcess(argv, command.returncode, out, err)
    finally:
        if command.poll() is None:
            command.kill()
            command.communicate()


def observe():
    command = observe_process(["p", "api", socket, "session.inspect", json.dumps({"v": 1, "uuid": uuid})])
    result = json.loads(command.stdout)
    if command.returncode:
        if result.get("error", {}).get("kind") == "busy":
            return None
        raise AssertionError((command.stdout, command.stderr))
    return result["result"]["session"]["attached_count"]


try:
    deadline = time.monotonic() + 60
    while observe() != 1:
        assert child.poll() is None, "CLI attachment exited before confirmation"
        assert time.monotonic() < deadline
        read()
    token = uuid_module.uuid4().hex
    # Use the attached shell's inherited environment: no caller-supplied
    # bus variables. Reinstallation reloads the real user manager.
    script = ("printf '%s\\n' \"$XDG_RUNTIME_DIR\" \"$DBUS_SESSION_BUS_ADDRESS\" > /workspace/notes-user-bus57; "
              "python3 examples/notes/install.py > /workspace/notes-shell-install57.log 2>&1; "
              "printf '%s' \"$?\" > /workspace/notes-shell-install57.status; "
              "systemctl --user is-active p-project-notes-db.service p-project-notes-web.service p-project-notes-worker.service "
              "> /workspace/notes-shell-services57.log 2>&1; "
              "printf '%s' \"$?\" > /workspace/notes-shell-services57.status; "
              "printf cli-attachment > /workspace/notes-attachment57; "
              f"printf {token} > /workspace/notes-shell-ready57\r")
    os.write(master, script.encode())
    deadline = time.monotonic() + 30
    while True:
        marker = observe_process(["incus", "--force-local", "--project", "user-1000", "exec", f"p-{uuid}",
                                  "--user", "1000", "--group", "1000", "--", "cat", "/workspace/notes-shell-ready57"])
        if marker.returncode == 0 and marker.stdout.decode() == token:
            break
        assert time.monotonic() < deadline, "attached shell commands did not finish"
        read()
    os.write(master, b"\x02d")
    deadline = time.monotonic() + 30
    while child.poll() is None:
        assert time.monotonic() < deadline, "detach did not close CLI attachment"
        read()
    assert child.returncode == 0, child.returncode
    while observe() != 0:
        assert time.monotonic() < deadline, "detach retained its lease"
        read()
    print("P_NOTES_CLI_ATTACHMENT_PASS")
finally:
    if child.poll() is None:
        child.terminate()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()
    os.close(master)
