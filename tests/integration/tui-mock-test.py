"""Fast production-TUI integration against the disposable socket fixture.

All mutations below originate as PTY keyboard input. mock.inspect provides
independent state/effect oracles; mock.configure injects one-shot faults.
This validates UI wiring and local terminal transport, not Incus/Git/systemd.
"""
import importlib.util
import json
import os
import re
import signal
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("observer", Path(__file__).with_name("tui-observe.py"))
observer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(observer)

# An explicit contract coverage gate: a removed UI request fails a test even
# if a recognisable heading still appears. New TUI methods must join this set.
REQUIRED_METHODS = {
    "system.hello", "project.list", "session.list", "operation.list",
    "project.branches", "project.retained_branches", "origin.refresh", "origin.sources",
    "project.create", "session.create", "session.inspect", "session.start", "session.stop",
    "session.rename", "session.discard", "session.delete", "operation.inspect", "operation.wait", "operation.retry",
    "session.services", "session.service.action", "session.service.logs",
    "session.attach", "attachment.claim",
}


class MockTUITests(unittest.TestCase):
    methods = set()

    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory(prefix="p-tui-build-", dir="/tmp")
        cls.cli = str(Path(cls.build.name) / "p")
        cls.server = str(Path(cls.build.name) / "server")
        for target, package in ((cls.cli, "./cmd/p"), (cls.server, "./tests/integration/cmd/tui-mock")):
            subprocess.run(["go", "build", "-o", target, package], cwd=ROOT, check=True, timeout=180)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="p-tui-", dir="/tmp")
        self.state_dir = Path(self.tmp.name)
        self.sock = str(self.state_dir / "control.sock")
        self.log = open(self.state_dir / "server.log", "w+")
        dataset = "empty" if "empty" in self._testMethodName else "portfolio"
        self.backend = subprocess.Popen([self.server, "--state-dir", self.tmp.name, "--dataset", dataset],
                                        stdout=self.log, stderr=self.log)
        self.ui = None
        try:
            deadline = time.monotonic() + 5
            while not Path(self.sock).exists():
                if self.backend.poll() is not None or time.monotonic() > deadline:
                    self.log.seek(0)
                    self.fail("Fixture did not start: " + self.log.read())
                time.sleep(.02)
            self.ui = observer.Observer([self.cli, "tui", self.sock], str(self.state_dir / "frames"), 120, 35)
            self.wait("All project sessions")
            self.wait("q/Esc back")  # Wait for the complete initial frame, including controls.
            if dataset == "portfolio":
                # The view renders before its sessions load, while the list
                # still reads "0–0 of 0"; no test may act or read before then.
                self.wait("of 120 · selected")
        except BaseException:
            self.tearDown()
            raise

    def tearDown(self):
        if self.ui is not None:
            # Preserve useful artifacts on failure without slowing every key
            # by rasterizing a frame. Copy only from this owned temp directory.
            failures = self._outcome.result.failures + self._outcome.result.errors
            if any(test is self or getattr(test, "test_case", None) is self for test, _ in failures):
                out = ROOT / ".cache" / "tui-mock-failures" / self._testMethodName
                out.mkdir(parents=True, exist_ok=True)
                self.ui.out = out
                self.ui.capture({"op": "observe", "label": "failure"})
                (out / "fixture.json").write_text(json.dumps(self.rpc("mock.inspect"), indent=2))
            self.__class__.methods.update(c["method"] for c in self.rpc("mock.inspect")["calls"])
            self.ui.close()
        self.backend.terminate()
        try:
            self.backend.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.backend.kill()
            self.backend.wait(timeout=5)
        self.assertEqual(self.backend.returncode, 0, "Fixture did not shut down cleanly")
        self.assertFalse((self.state_dir / "tmux.sock").exists(), "tmux server socket leaked")
        self.log.close()
        self.tmp.cleanup()

    def rpc(self, rpc_method, **params):
        # A connection agrees the protocol with system.hello before its request.
        with socket.socket(socket.AF_UNIX) as client:
            client.settimeout(5)
            client.connect(self.sock)
            with client.makefile("rb") as stream:
                for request_id, method, body in ((0, "system.hello", {"protocol": "p.control/v2"}),
                                                 (1, rpc_method, params)):
                    client.sendall(json.dumps({"jsonrpc": "2.0", "id": request_id, "method": method,
                                               "params": body}).encode() + b"\n")
                    reply = json.loads(stream.readline())
                    self.assertNotIn("error", reply, reply)
        return reply["result"]

    def screen(self):
        return "\n".join(self.ui.screen.display)

    def wait(self, text, timeout=5):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.ui.pump(.02)
            if text in self.screen():
                return
            if self.ui.process.poll() is not None:
                break
        self.fail(f"Expected {text!r}:\n{self.screen()}")

    def key(self, text, expect=None):
        self.ui.send(text)
        self.ui.pump(.04)
        if expect:
            self.wait(expect)

    def effect(self, check, timeout=5):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.ui.pump(.02)
            state = self.rpc("mock.inspect")
            if check(state):
                return state
        self.fail("Expected fixture effect did not occur: " + json.dumps(state))

    def session(self, branch="feat/device-login", project="forge"):
        return next(s for s in self.rpc("mock.inspect")["sessions"].values()
                    if s["project"] == project and s["branch"] == branch)

    def count(self, method):
        return sum(c["method"] == method for c in self.rpc("mock.inspect")["calls"])

    def params(self, method):
        """The params of each call of method, in the order the fixture got them."""
        return [c["params"] for c in self.rpc("mock.inspect")["calls"] if c["method"] == method]

    def choose_project(self, name="forge"):
        self.key("c", "Create · project")
        self.key("/" + name + "\r")
        self.key("\r", "Create · branch")
        self.wait("retained/00")

    def detach(self):
        self.key("\x02d", "project sessions")
        self.effect(lambda state: all(s["attached_count"] == 0 for s in state["sessions"].values()))

    def test_01_navigation_reports_policy_branches_operations_search_resize(self):
        self.assertEqual(len(self.rpc("mock.inspect")["sessions"]), 120)
        # All movement bindings are sent through the terminal decoder.
        page = int(re.search(r'1–(\d+) of 120', self.screen()).group(1))
        for key, selected in (("j", 2), ("k", 1), ("\x1b[B", 2), ("\x1b[A", 1),
                              ("\x1b[6~", page + 1), ("\x1b[5~", 1),
                              ("\x06", page + 1), ("\x02", 1), ("\x04", page // 2 + 1),
                              ("\x15", 1), ("G", 120), ("gg", 1), ("\x1b[F", 120), ("\x1b[H", 1)):
            self.key(key, f"· selected {selected} ")  # The count line, once the key is drawn.
            start, end = map(int, re.search(r'(\d+)–(\d+) of 120', self.screen()).groups())
            self.assertEqual((start, end), (max(1, selected - page + 1), max(page, selected)), f"Viewport failed for {key!r}")
        self.key("ggD", "Session details")
        self.wait("UUID:")
        self.key("G", "Policy digest:")
        self.key("ggA", "Agent reports")
        self.key("q", "Session details")
        self.key("S", "Project services")
        self.key("q", "Session details")
        self.key("q", "All project sessions")
        self.key("ggA", "Agent reports")
        self.wait("Waiting for review")
        self.key("?", "P · keys")
        self.key("q", "Agent reports")
        self.key("q", "All project sessions")
        self.key("p", "Session policy and environment")
        self.key("Ggg")
        self.key("q", "All project sessions")
        self.key("b", "Retained branches · forge")
        self.wait("retained/00")
        self.key("G", "retained/31")
        self.key("?", "P · keys")
        self.key("q", "Retained branches")
        self.key("qP", "All projects")
        self.key("/orbit\r")
        self.key("\r", "[orbit] project sessions")
        self.key("/zzzzzz", "No matching entries")
        self.key("\x7f\x1b", "[orbit] project sessions")
        self.key("/Codex\r", "fix/cache-race")
        self.key("q", "[orbit] project sessions")
        self.key("q", "All project sessions")
        self.key("r")
        self.key("O", "session.rename · blocked")
        self.key("\r", "The runtime failed; check Incus. Retry it once that is resolved.")
        self.key("r", "Operation completed")
        self.assertEqual(next(iter(self.rpc("mock.inspect")["operations"].values()))["status"], "completed")
        self.key("q", "All project sessions")
        for cols, rows in ((80, 24), (48, 16), (120, 35)):
            self.ui.handle({"op": "resize", "cols": cols, "rows": rows, "wait": .1})
            self.assertIn("X delete", self.screen())
            self.assertIn("D details", self.screen())
            self.key("gg")
            visible = int(re.search(r'1–(\d+) of 120', self.screen()).group(1))
            self.key("\x1b[6~", f"· selected {visible + 1} ")
            self.key("gg")
            self.assertEqual((self.ui.screen.columns, self.ui.screen.lines), (cols, rows))

    def test_01_selected_service_summary_states(self):
        self.ui.handle({"op": "resize", "cols": 80, "rows": 24, "wait": .1})
        self.key("A", "Agent reports")
        self.rpc("mock.configure", method="session.services", services_empty=True)
        self.key("qj", "All project sessions")
        self.wait("no project units observed")
        self.key("S", "p-project-api.service")
        self.key("q", "All project sessions")
        self.wait("3 units observed")
        self.key("A", "Agent reports")
        self.rpc("mock.configure", method="session.services", delay_ms=800, error="failed", reason="services")
        self.key("qk", "All project sessions")
        self.wait("loading…")
        self.wait("unavailable")
        self.key("D", "Session details")
        self.key("G", "user service manager refused")
        self.key("q", "All project sessions")
        self.key("A", "Agent reports")
        self.rpc("mock.configure", method="session.list", error="internal", reason="store")
        self.key("qr", "All project sessions")
        self.wait("unavailable · stale inventory")
        self.wait("STALE")
        self.wait("failed unexpectedly")
        self.key("r")
        self.wait("3 units observed")
        # The refresh that succeeded cleared STALE and the failure's sentence.
        self.assertNotIn("STALE", self.screen())
        self.assertNotIn("failed unexpectedly", self.screen())

    def test_02_services_journal_and_failures(self):
        sid = self.session()["uuid"]
        self.key("S", "p-project-api.service")
        self.key("s")
        self.effect(lambda st: st["services"][sid][0]["state"] == "stopped")
        self.wait("inactive (dead)")
        self.key("s")
        self.effect(lambda st: st["services"][sid][0]["state"] == "running")
        self.wait("active (running)")
        self.key("r")
        self.effect(lambda st: any(c["method"] == "session.service.action" and c["params"].get("action") == "restart" for c in st["calls"]))
        self.wait("active (running)")
        self.key("?", "P · keys")
        self.key("q", "Project services")
        self.key("J", "journal tail")
        self.wait("mock 090")
        self.key("gg", "mock 001")
        self.key("l")
        self.assertNotIn("mock 001", self.screen())
        self.key("h", "mock 001")
        self.key("/request=40\r", "find: request=40")
        self.key("n", "mock 040")
        self.key("N", "mock 040")
        self.key("f", "follow true")
        self.key("\x1b[5~", "follow false")
        self.key("G", "mock 090")
        self.key("q", "journal tail")  # first Back clears the find query
        self.key("q", "Project services")
        self.key("\r", "journal tail")
        self.key("qq", "All project sessions")
        self.rpc("mock.configure", method="session.services", error="failed", reason="services")
        self.key("S", "user service manager refused")
        self.assertIn("Service inventory unavailable", self.screen())
        before = self.count("session.service.action")
        self.key("sr\r")
        self.assertEqual(self.count("session.service.action"), before)
        self.key("q", "All project sessions")
        self.rpc("mock.configure", method="session.services", delay_ms=200)
        self.key("S", "Loading service inventory")
        self.key("q", "All project sessions")
        self.ui.pump(.3)
        self.assertIn("All project sessions", self.screen())
        self.rpc("mock.configure", method="session.service.action", error="conflict", reason="session_not_running")
        self.key("S", "p-project-api.service")
        self.key("r", "Start this session first.")
        self.key("q", "All project sessions")

    def test_02b_queued_services_keep_the_captured_unit_and_back_cancels(self):
        sid = self.session()["uuid"]
        self.key("S", "p-project-api.service")
        self.rpc("mock.configure", method="session.services", delay_ms=300)
        self.key("r", "Waiting for daemon")
        self.key("s", "Queued stop for p-project-api.service")
        self.key("j")  # selection changes while the captured action waits
        self.effect(lambda st: st["services"][sid][0]["state"] == "stopped")
        self.assertEqual(self.rpc("mock.inspect")["services"][sid][1]["state"], "running")
        self.wait("Mock database")
        before = self.count("session.service.action")
        self.rpc("mock.configure", method="session.services", delay_ms=300)
        self.key("r", "Waiting for daemon")
        self.key("s", "Queued stop for p-project-database.service")
        self.key("q", "All project sessions")
        self.ui.pump(.4)
        self.assertEqual(self.count("session.service.action"), before + 1)  # accepted restart only
        self.assertEqual(self.rpc("mock.inspect")["services"][sid][1]["state"], "running")

    def test_03_attachment_stop_start_rename_and_cancel(self):
        sid = self.session()["uuid"]
        self.key("s", "Confirm action")
        self.key("\r", "All project sessions")  # default No
        self.assertEqual(self.count("session.stop"), 0)
        self.key("\r", "MOCK · Ctrl-B d detach")
        self.effect(lambda st: st["sessions"][sid]["attached_count"] == 1)
        self.key("printf '%s%s\\n' TUI_ MOCK_OK; echo kept > kept.txt\r", "TUI_MOCK_OK")
        # Verify the actual tmux pane and resize, not just a UI heading.
        self.ui.handle({"op": "resize", "cols": 80, "rows": 24, "wait": .15})
        pane = subprocess.check_output(["tmux", "-S", str(self.state_dir / "tmux.sock"), "display-message", "-p", "-t", sid, "#{pane_width}x#{pane_height}"], text=True).strip()
        self.assertEqual(pane, "80x23")
        self.detach()
        self.key("\r", "TUI_MOCK_OK")
        self.key("cat kept.txt\r", "kept")
        self.detach()
        self.key("R", "Stop this session before renaming")  # the daemon refuses a running Rename
        self.assertNotIn("New assigned branch name", self.screen())
        self.key("s", "Confirm action")
        self.key("n", "project sessions")
        self.assertEqual(self.count("session.stop"), 0)
        self.key("s", "Confirm action")
        self.key("y", "Stopped. Files and branch retained")
        self.assertEqual(self.session()["session_condition"], "stopped")
        self.key("q", "project sessions")
        stops = self.count("session.stop")
        self.key("s", "Session is already stopped.")  # no confirmation, as in the prototype
        self.assertEqual(self.count("session.stop"), stops)
        self.key("R", "New assigned branch name")
        self.key("qgG", "name> qgG")
        self.key("\x7f", "name> qg▏")
        self.key("\x1b", "project sessions")
        self.assertEqual(self.count("session.rename"), 0)
        self.key("R", "New assigned branch name")
        self.key("renamed\r", "Confirm action")
        self.key("y", "Operation completed")
        self.assertEqual(self.session("renamed")["uuid"], sid)
        self.key("q", "project sessions")
        self.key("\r", "MOCK · Ctrl-B d detach")
        self.key("cat kept.txt\r", "kept")  # Stop/Start keeps this owned workspace
        self.detach()
        # p attach's own error survives the browser's redraw.
        self.rpc("mock.configure", method="session.attach", error="conflict", reason="session_not_running")
        self.key("\r", "Attachment ended: Start this session first.")

    def test_04_create_local_origin_retained_and_project(self):
        # Open another session first: the page that reads the created session
        # before entry must not show that one.
        self.key("D", "Session details")
        self.key("q", "All project sessions")
        for choice, branch in (("local", "feat/local"), ("origin", "feat/origin"), ("retained", "retained/00")):
            with self.subTest(choice=choice):
                self.choose_project()
                if choice == "retained":
                    self.key("/retained/00\r")
                    self.key("\r", "Create · policy review")
                else:
                    self.key("\r", "Create · source")
                    if choice == "origin":
                        self.key("/External\r")
                    self.key("\r", "New branch name")
                    self.key(branch + "\r", "Create · policy review")
                if choice == "local":
                    self.rpc("mock.configure", method="session.inspect", delay_ms=1500)
                    self.key("\r", "Leaving startup prevents automatic entry")
                    readiness = self.screen()
                    self.assertIn("forge / " + branch, readiness)
                    self.assertNotIn("Condition:", readiness)
                    self.wait("MOCK · Ctrl-B d detach")
                else:
                    self.key("\r", "MOCK · Ctrl-B d detach")
                self.assertEqual(self.session(branch)["session_condition"], "ready")
                self.detach()
        calls = [c["params"] for c in self.rpc("mock.inspect")["calls"] if c["method"] == "session.create"]
        self.assertIn("source", calls[0])
        self.assertIn("expected_origin_url", calls[1])
        self.assertEqual(calls[2]["choice"], "existing")
        self.key("c", "Create · project")
        self.key("\r", "Project path")
        self.key("new-project\r", "SSH origin URL")
        self.key("\r", "Create · policy review")
        self.key("q", "SSH origin URL")
        self.assertNotIn("new-project", self.rpc("mock.inspect")["projects"])
        self.key("\r", "Create · policy review")
        self.key("\r", "MOCK · Ctrl-B d detach")
        self.assertEqual(self.session("main", "new-project")["session_condition"], "ready")
        self.detach()

    def test_05_removal_preview_discard_delete_running_and_error(self):
        # atlas is stopped; forge/fix/token-refresh is running and unattached,
        # so P stops it as part of the removal.
        for action, project, branch in (("d", "atlas", "feat/ui"), ("X", "atlas", "fix/cache"), ("d", "forge", "fix/token-refresh")):
            with self.subTest(action=action, branch=branch):
                kind = "discard" if action == "d" else "delete"
                # Select an exact branch through fuzzy search.
                self.key("/" + project + " " + branch + "\r")
                sid = self.session(branch, project)["uuid"]
                running = self.session(branch, project)["session_condition"] == "ready"
                method = "session." + kind
                before = len(self.params(method))
                self.key(action, "Removal loss preview")
                # The review wraps its sentences at the terminal's width.
                review = " ".join(self.screen().split())
                # The CLI's sentence for the container's loss.
                self.assertIn("the container of session " + sid + " and everything in it", review)
                self.assertIn("P does not look inside", review)
                self.assertEqual("P stops the running container" in review, running)
                if action == "d":
                    self.assertIn("Discard keeps this branch in P", review)
                else:
                    self.assertIn("Delete removes this branch from P's repository", review)
                # The review is the plan of a dry run: the uuid only, no key or tag.
                self.assertEqual(self.params(method)[before:], [{"uuid": sid}])
                self.key("Ggg")
                self.key("n", "Enter open")
                self.assertIn(sid, self.rpc("mock.inspect")["sessions"])
                self.assertEqual(self.params(method)[before:], [{"uuid": sid}])
                self.key(action, "Removal loss preview")
                self.key("y", "Operation completed")
                state = self.rpc("mock.inspect")
                self.assertNotIn(sid, state["sessions"])
                self.assertEqual(branch in state["refs"][project], action == "d")
                # A second dry run planned the authorized removal; only then did
                # the run send the same uuid with a key and the plan's tag.
                calls = self.params(method)[before:]
                self.assertEqual(calls[:2], [{"uuid": sid}, {"uuid": sid}])
                self.assertEqual(len(calls), 3)
                run = calls[2]
                self.assertEqual(set(run), {"uuid", "key", "if_match"})
                self.assertEqual(run["uuid"], sid)
                self.assertRegex(run["if_match"], r"^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$")
                ops = [op for op in state["operations"].values() if op["idempotency_key"] == run["key"]]
                self.assertEqual(len(ops), 1)
                self.assertEqual((ops[0]["kind"], ops[0]["session_uuid"], ops[0]["request"]["if_match"]),
                                 (method, sid, run["if_match"]))
                self.key("q", "Enter open")
                self.key("q", "All project sessions")  # clear search
        self.rpc("mock.configure", method="project.branches", error="failed", reason="git")
        self.key("b", "P's Git repository failed")
        self.assertIn("No matching entries", self.screen())
        self.key("r", "retained/00")  # read again
        self.key("q", "All project sessions")
        # A run whose plan changed removes nothing; the page reviews again.
        sid = self.session()["uuid"]
        before = len(self.params("session.discard"))
        self.key("d", "Removal loss preview")
        self.rpc("mock.configure", method="session.discard", error="precondition_failed", reason="changed")
        self.key("y", "Nothing was removed")
        self.wait("Removal loss preview")
        calls = self.params("session.discard")[before:]
        self.assertEqual([set(c) for c in calls], [{"uuid"}, {"uuid", "key", "if_match"}, {"uuid"}])
        self.assertIn(sid, self.rpc("mock.inspect")["sessions"])
        self.key("n", "All project sessions")
        self.rpc("mock.configure", method="session.start", error="conflict", reason="principal_missing")
        self.key("\r", "Git identity is gone")
        self.key("q", "All project sessions")

    def test_06_empty_inventory_creation_and_quit(self):
        self.wait("No matching entries")
        self.key("\rAsSpbdXR")  # no selected session: no mutation/inspection
        self.assertEqual(self.count("session.start"), 0)
        self.assertEqual(self.count("session.services"), 0)
        self.key("?", "P · keys")
        self.key("\x03", "All project sessions")
        self.key("c", "Create · project")
        self.key("\r", "Project path")
        self.key("\r", "Enter a project path")
        self.key("demo\r", "SSH origin URL")
        self.key("\x1b", "Project path")
        self.key("\x03", "All project sessions")
        self.key("q")
        self.ui.process.wait(timeout=5)
        self.assertEqual(self.ui.process.returncode, 0)

    def launcher(self):
        launcher = str(ROOT / "dev" / "tui-mock")
        for args in (("--dataset", "unknown"), ("--dataset",), ("--state-dir", "/tmp"),
                     ("--theme", "unknown"), ("--theme",), ("--theme=light;touch /tmp/p-theme-injected",)):
            result = subprocess.run([launcher, *args], capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 2)
        self.ui.close()
        self.ui = observer.Observer([launcher, "--dataset=small", "--theme=light"], str(self.state_dir / "launcher"), 80, 24)
        self.wait("All project sessions", timeout=30)
        self.wait("of 4 · selected")  # The sessions load after the view renders.
        # Find only this launcher's descendant server; concurrent exploratory
        # terminals and their private directories are never cleanup targets.
        pending, children = [self.ui.process.pid], []
        while pending:
            pid = pending.pop()
            path = Path(f"/proc/{pid}/task/{pid}/children")
            if path.exists():
                descendants = [int(value) for value in path.read_text().split()]
                children.extend(descendants)
                pending.extend(descendants)
        owned = []
        for pid in children:
            command = Path(f"/proc/{pid}/cmdline")
            if command.exists():
                argv = command.read_bytes().split(b"\0")
                if b"--state-dir" in argv:
                    owned.append((pid, Path(os.fsdecode(argv[argv.index(b"--state-dir") + 1]))))
        self.assertEqual(len(owned), 1, "Background fixture server missing")
        pid, directory = owned[0]
        self.assertTrue((directory / "control.sock").is_socket())
        return pid, directory

    def test_06_theme_detection_and_overrides(self):
        # Exercise the actual query, decoder and renderer in a real PTY.
        for background, mode, accent in (("light", "auto", "5b21b6"),
                                          ("dark", "auto", "c4a7ff"),
                                          ("dark", "light", "5b21b6"),
                                          ("light", "dark", "c4a7ff")):
            with self.subTest(background=background, mode=mode):
                self.ui.close()
                self.ui = observer.Observer([self.cli, "tui", self.sock, "--theme", mode],
                                            str(self.state_dir / f"theme-{background}-{mode}"),
                                            80, 24, background=background)
                self.wait("All project sessions")
                self.ui.pump(.15)
                cells = [cell for row in self.ui.screen.buffer.values() for cell in row.values()]
                self.assertTrue(any(cell.data == "A" and cell.fg == accent for cell in cells),
                                "Terminal background or explicit mode did not reach renderer")
                if mode == "auto":
                    raw = (self.ui.out / "terminal.ansi").read_bytes()
                    self.assertIn(b"\x1b]11;?", raw, "Automatic mode never queried its background")
                # A later terminal report changes auto while overrides remain.
                self.ui.send("\x1b]11;rgb:ffff/ffff/ffff\x1b\\")
                self.ui.pump(.1)
                expected = "c4a7ff" if mode == "dark" else "5b21b6"
                cells = [cell for row in self.ui.screen.buffer.values() for cell in row.values()]
                self.assertTrue(any(cell.data == "A" and cell.fg == expected for cell in cells))
                if mode != "dark":
                    self.assertTrue(any(cell.fg == "137333" for cell in cells), "Light runtime needs readable green")
        for mode in ("", "unknown", "LIGHT"):
            result = subprocess.run([self.cli, "tui", self.sock, "--theme", mode], capture_output=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(b"theme must be auto, light, or dark", result.stderr)

    def test_07_launcher_owns_background_server_and_cleans_up(self):
        pid, directory = self.launcher()
        self.key("\r", "MOCK · Ctrl-B d detach")
        self.key("printf '%s%s\\n' LAUNCHER_ OK\r", "LAUNCHER_OK")
        self.key("\x02d", "All project sessions")
        self.key("q")
        self.ui.process.wait(timeout=5)
        self.assertEqual(self.ui.process.returncode, 0)
        self.assertFalse(directory.exists(), "Launcher workspace leaked")
        self.assertFalse(Path(f"/proc/{pid}").exists(), "Background fixture server leaked")

    def test_08_launcher_term_while_attached_cleans_up(self):
        pid, directory = self.launcher()
        self.key("\r", "MOCK · Ctrl-B d detach")
        self.ui.process.send_signal(signal.SIGTERM)  # launcher PID, not process group
        self.ui.process.wait(timeout=5)
        self.assertEqual(self.ui.process.returncode, 143)
        self.assertFalse(directory.exists(), "TERM left the workspace alive")
        self.assertFalse(Path(f"/proc/{pid}").exists(), "TERM left the server alive")

    def test_zz_all_tui_rpc_actions_covered(self):
        # Fail when production starts issuing an unrepresented socket method.
        # The TUI calls methods through internal/api's typed values, such as
        # api.SessionList; map each one it names to its method name.
        names = dict(re.findall(r'^\s*(\w+)\s*=\s*Method\[.*\]\{"([a-z_.]+)"\}',
                                (ROOT / "internal" / "api" / "method.go").read_text(), re.M))
        source = "\n".join(path.read_text() for path in (ROOT / "internal" / "tui").glob("*.go")
                           if not path.name.endswith("_test.go"))
        production = {names[name] for name in re.findall(r'\bapi\.(\w+)', source) if name in names}
        self.assertIn("session.list", production, "The method scan no longer finds the TUI's calls")
        self.assertFalse(production - REQUIRED_METHODS, "Update the action matrix for new TUI methods: " + str(production - REQUIRED_METHODS))
        self.assertFalse(REQUIRED_METHODS - self.methods, "Missing socket coverage: " + str(REQUIRED_METHODS - self.methods))


if __name__ == "__main__":
    unittest.main(verbosity=2)
