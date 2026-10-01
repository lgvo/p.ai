"""Authentication-free native TUI acceptance. Only fixture projects/units."""
import errno
import fcntl
import json
import os
import pty
import re
import select
import struct
import subprocess
import sys
import termios
import time
from tui_terminal import TerminalScreen, TerminalStream

mode, socket, uuid_file = sys.argv[1:]


class BusyObservation(Exception):
    pass


def observe_process(args, *, timeout, text=False):
    # Keep servicing terminal queries while a native/RPC observation waits.
    # Blocking subprocess.run can outlast tmux's query deadline: a late reply
    # then becomes ordinary shell input rather than a terminal response.
    child = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    deadline = time.monotonic() + timeout
    try:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(args, timeout)
            try:
                stdout, stderr = child.communicate(timeout=min(0.02, remaining))
                if text:
                    stdout, stderr = stdout.decode(), stderr.decode()
                return subprocess.CompletedProcess(args, child.returncode, stdout, stderr)
            except subprocess.TimeoutExpired:
                read(0)
    finally:
        if child.poll() is None:
            child.kill()
            child.communicate()


def rpc(method, **params):
    response = observe_process(
        ["p", "api", socket, method, json.dumps({"v": 1, **params})],
        text=True, timeout=40,
    )
    envelope = json.loads(response.stdout)
    if response.returncode:
        if envelope.get("error", {}).get("kind") == "busy":
            raise BusyObservation()
        raise AssertionError(f"{method}: {response.stdout[-2000:]} {response.stderr[-500:]}")
    return envelope["result"]


def await_state(test, label):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            observed = test()
            if observed:
                return observed
        except BusyObservation:
            # Actions hold the same authority lock as observations. Retry only
            # that explicit transient conflict; other failures remain failures.
            pass
        read(0.2)
    raise AssertionError(f"timed out waiting for {label}; terminal tail: {plain()[-2000:]}")


master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 28, 100, 0, 0))
env = {**os.environ, "TERM": "xterm-256color"}
process = subprocess.Popen(["p", "tui", socket], stdin=slave, stdout=slave,
                           stderr=slave, env=env, start_new_session=True)
os.close(slave)
captured = bytearray()



screen = TerminalScreen(100, 28, lambda data: send(data))
terminal = TerminalStream(screen)


def read(timeout=0.2):
    if select.select([master], [], [], timeout)[0]:
        try:
            data = os.read(master, 65536)
        except OSError as error:
            if error.errno != errno.EIO:
                raise
            return
        captured.extend(data)
        terminal.feed(data)
        if len(captured) > 2_000_000:
            del captured[:-1_000_000]
        return True


def plain():
    # Bubble Tea and tmux emit cursor-addressed incremental changes. Assert
    # the reconstructed terminal, never concatenated escape-stripped chunks.
    return "\n".join(screen.display)


def send(keys):
    os.write(master, keys if isinstance(keys, bytes) else keys.encode())


def expect(text):
    await_state(lambda: text in plain(), text)


def service_ui_ready(state):
    states = (state,) if isinstance(state, str) else state
    await_state(lambda: any(observation in plain() for observation in states)
                and all(message not in plain() for message in
                        ("Waiting for daemon", "Service observation unavailable", "busy:")),
                f"fresh service UI observation: {state}")


try:
    expect("All project sessions")
    if mode == "create":
        send("c"); expect("Create · project")
        send("\r"); expect("Project path>")
        send("tui56\r"); expect("SSH origin URL")
        send("\r"); expect("Create · policy review")
        send("\r")

        def created():
            sessions = rpc("session.list", limit=8)["sessions"]
            matches = [s for s in sessions if s["project"] == "tui56"]
            if matches and matches[0]["attached_count"] == 1:
                with open(uuid_file, "w", encoding="ascii") as stream:
                    stream.write(matches[0]["uuid"])
                return True
            return False

        await_state(created, "real confirmed attachment after creation")
        uuid = open(uuid_file, encoding="ascii").read()
        send("printf native-terminal > /workspace/tui-attachment; echo P_TUI_REAL_ATTACH_OK\r")
        expect("P_TUI_REAL_ATTACH_OK")

        def executed():
            observed = observe_process(["incus", "--force-local", "--project", "user-1000", "exec", "p-" + uuid,
                                       "--user", "1000", "--group", "1000", "--", "head", "-c", "128", "/workspace/tui-attachment"],
                                      timeout=15)
            if observed.returncode or observed.stdout != b"native-terminal":
                executed.diagnostic = f"exit={observed.returncode} bytes={observed.stdout.hex()} stderr={observed.stderr[-300:]!r}"
                return False
            return True

        try:
            # Echoed command text never proves execution. Wait for the actual
            # native effect before detach, retaining the independent shell check.
            await_state(executed, "native terminal command completed")
        except AssertionError as error:
            raise AssertionError(f"{error}; native marker: {getattr(executed, 'diagnostic', 'no observation')}") from error
        # Exercise ordinary Git from the actual interactive environment. Native
        # exec fixtures set GIT_SSH explicitly and cannot catch a missing wrapper
        # in the tmux host environment.
        send("git add tui-attachment && git -c user.name=P -c user.email=p@example.invalid commit -qm native-terminal && git push origin HEAD:main\r")
        await_state(lambda: any(ref["ref"] == "refs/heads/main"
                                for ref in rpc("project.branches", project="tui56", limit=8)["refs"]),
                    "interactive Git push reached P")
        send(b"\x02d")
        await_state(lambda: rpc("session.inspect", uuid=uuid)["session"]["attached_count"] == 0,
                    "detach lease teardown")
        expect("All project sessions")
        # Exercise creation-step cancellation against the real branch RPCs.
        # Deterministic model tests separately deliver old replies after Back.
        send("c"); expect("Create · project")
        send("j\r"); expect("Create · branch")
        send("\x1b"); expect("Create · project")
        send("j\r"); expect("Create · branch")
        send("?"); expect("P · keys")
        send("\x1b"); expect("Create · branch")
        expect("Create new branch")
        send("\r"); expect("Create · source")
        send("\x1b"); expect("Create · branch")
        expect("Create new branch")
        send("\x1b"); expect("Create · project")
        send("q"); expect("All project sessions")
        send("P"); expect("Select project")
        send("\x1b"); expect("All project sessions")
        send("/"); expect("fuzzy search>")
        send("no-match\r")
        expect("No matching entries")
        send("\x1b"); expect("All project sessions")
        send("A"); expect("Agent reports")
        send("q"); expect("All project sessions")
        send("s"); expect("Confirm action")
        send("\r"); expect("All project sessions")
        assert rpc("session.inspect", uuid=uuid)["session"]["session_condition"] == "ready"
        send("q")
    elif mode == "services":
        uuid = open(uuid_file, encoding="ascii").read()
        expect("tui56 / main")
        send("S"); expect("Project services")
        expect("p-project-demo.service")
        send("s")
        service_ui_ready("active (running)")
        await_state(lambda: any(s["unit"] == "p-project-demo.service" and s["active_state"] == "active"
                               for s in rpc("session.services", uuid=uuid)["services"]), "native user service start")
        # Wait for the TUI's own post-action inventory before navigating.
        expect("active (running)")
        send("\r"); expect("journal tail")
        expect("P_TUI_PROJECT_JOURNAL")
        send("/"); expect("find>")
        send("JOURNAL\r")
        send("q")  # Clear find before leaving the journal.
        time.sleep(0.2)
        send("q"); expect("Project services")
        expect("p-project-demo.service")
        previous = await_state(lambda: rpc("session.service.journal", uuid=uuid, unit="p-project-demo.service")["journal"].count("P_TUI_PROJECT_JOURNAL"),
                               "available baseline journal observation")
        read(0.2)
        service_ui_ready("active (running)")
        send("r")
        await_state(lambda: rpc("session.service.journal", uuid=uuid, unit="p-project-demo.service")["journal"].count("P_TUI_PROJECT_JOURNAL") > previous,
                    "native user service restart emitted a new journal entry")
        read(0.2)
        service_ui_ready("active (running)")
        send("s")
        service_ui_ready(("inactive (dead)", "installed"))

        def stopped():
            result = observe_process(["incus", "--force-local", "--project", "user-1000", "exec", "p-" + uuid,
                                     "--user", "1000", "--group", "1000", "--env", "XDG_RUNTIME_DIR=/run/user/1000",
                                     "--env", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "--",
                                     "/usr/libexec/p/systemctl", "--user", "show", "--property=ActiveState,SubState,MainPID",
                                     "p-project-demo.service"], text=True, timeout=15)
            properties = dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
            return result.returncode == 0 and properties == {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}

        # An unloaded inventory entry is unknown, not proof of Stop. Require
        # fresh native manager properties and absence of its main process.
        await_state(stopped, "native user service inactive/dead with MainPID=0")
        send("q"); expect("All project sessions")
        send("q")
    elif mode == "notes":
        from tui_notes_driver import run_notes
        run_notes(globals())
    elif mode == "remove":
        uuid = open(uuid_file, encoding="ascii").read()
        expect("tui56 / main")
        expect("stopped")
        send("X"); expect("Removal loss preview")
        send("\r")  # Enter is never destructive authorization.
        read(0.2)
        assert rpc("session.inspect", uuid=uuid)["session"]["session_condition"] == "stopped"
        send("n"); expect("All project sessions")
        send("X"); expect("Removal loss preview")
        send("y")
        await_state(lambda: all(s["uuid"] != uuid for s in rpc("session.list", limit=8)["sessions"]),
                    "confirmed TUI deletion removed the session registry entry")
        expect("Operation completed.")
        send("q"); expect("All project sessions")
        send("q")
    else:
        raise AssertionError(f"unknown mode {mode}")
    process.wait(timeout=15)
    assert process.returncode == 0, plain()[-2000:]
    while read(0):
        pass
    # Save the console once on browser entry and restore it once on final exit.
    # Neither the attachment handshake nor tmux detach may reveal it in between.
    screen_switches = [match[2] for match in re.finditer(
        rb"\x1b\[\?([0-9;]+)([hl])", captured)
        if any(int(parameter) in (47, 1047, 1049)
               for parameter in match[1].split(b";"))]
    assert screen_switches == [b"h", b"l"], screen_switches
    print(f"P_TUI_PTY_OK {mode}")
except Exception:
    print("P_TUI_DIAGNOSTIC_SCREEN\n" + plain(), flush=True)
    print("P_TUI_DIAGNOSTIC_RAW_HEX " + captured[-2048:].hex(), flush=True)
    raise
finally:
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    os.close(master)
