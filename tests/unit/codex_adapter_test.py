"""Auth-free contract tests for the pinned in-session Codex asset."""

import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import socket
import stat
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest import mock


ASSET = Path(__file__).resolve().parents[2] / "plugins/bundled/codex-adapter/p-codex-adapter"
sys.dont_write_bytecode = True
LOADER = importlib.machinery.SourceFileLoader("p_codex_adapter", str(ASSET))
SPEC = importlib.util.spec_from_loader(LOADER.name, LOADER)
ADAPTER = importlib.util.module_from_spec(SPEC)
LOADER.exec_module(ADAPTER)
UUID = "b5f6c1c2-1111-2222-3333-444455556666"
CHILD = "c5f6c1c2-1111-2222-3333-444455556666"


def hook(event):
    value = {
        "session_id": UUID, "turn_id": "turn-1", "cwd": "/workspace",
        "transcript_path": None, "hook_event_name": event, "model": "fixture",
        "permission_mode": "default",
    }
    if event == "UserPromptSubmit":
        value["prompt"] = "private prompt must never leave the guest"
    else:
        value.update(tool_name="Bash", tool_input={"command": "private command"})
        if event == "PreToolUse":
            value["tool_use_id"] = "tool-1"
    return value


class AdapterTests(unittest.TestCase):
    def test_version_probe_home_requires_real_root_owned_private_ancestors(self):
        safe = {
            "/": SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0, st_gid=0),
            "/var": SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0, st_gid=0),
            "/var/empty": SimpleNamespace(st_mode=stat.S_IFDIR | 0o555, st_uid=0, st_gid=0),
        }
        with mock.patch.object(ADAPTER.os, "lstat", side_effect=lambda path: safe[path]):
            self.assertTrue(ADAPTER.safe_version_home())
            safe["/var"] = SimpleNamespace(st_mode=stat.S_IFLNK | 0o777, st_uid=0, st_gid=0)
            self.assertFalse(ADAPTER.safe_version_home())
            safe["/var"] = SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0, st_gid=0)
            safe["/var/empty"] = SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0, st_gid=0)
            self.assertFalse(ADAPTER.safe_version_home())

    def test_closed_mapping_and_no_inferred_stop(self):
        source = "codex/session/" + UUID
        self.assertEqual(ADAPTER.hook_condition(hook("UserPromptSubmit")), ("running", "prompt submitted", source))
        self.assertEqual(ADAPTER.hook_condition(hook("PreToolUse")), ("running", "tool starting", source))
        self.assertEqual(ADAPTER.hook_condition(hook("PermissionRequest")), ("attention", "permission requested", source))
        self.assertIsNone(ADAPTER.hook_condition(hook("Stop")))
        self.assertIsNone(ADAPTER.hook_condition({**hook("PreToolUse"), "future_field": True}))
        self.assertIsNone(ADAPTER.hook_condition({**hook("PreToolUse"), "agent_id": CHILD}))
        self.assertIsNone(ADAPTER.hook_condition({**hook("PreToolUse"), "agent_id": None, "agent_type": None}))
        self.assertIsNone(ADAPTER.hook_condition({**hook("PreToolUse"), "agent_id": UUID, "agent_type": "worker"}))
        self.assertEqual(ADAPTER.hook_condition({**hook("PreToolUse"), "agent_id": CHILD, "agent_type": "worker"}),
                         ("running", "tool starting", "codex/thread/" + CHILD))
        self.assertEqual(ADAPTER.hook_condition({**hook("PreToolUse"), "tool_input": ["opaque"]}),
                         ("running", "tool starting", source))
        self.assertIsNone(ADAPTER.hook_condition({**hook("PermissionRequest"), "permission_mode": "unknown"}))
        self.assertIsNone(ADAPTER.hook_condition({**hook("UserPromptSubmit"), "prompt": None}))
        self.assertNotIn("Stop", ADAPTER.HOOKS_JSON.decode())
        legacy = {
            "type": "agent-turn-complete", "thread-id": CHILD, "turn-id": "turn-1", "cwd": "/workspace",
            "input-messages": ["private prompt"], "last-assistant-message": "private answer",
        }
        self.assertEqual(ADAPTER.notify_condition(legacy), ("idle", "turn complete", "codex/thread/" + CHILD))
        self.assertIsNone(ADAPTER.notify_condition({**legacy, "type": "api-error"}))

    def test_duplicate_nonfinite_and_size_refused(self):
        for data in (b'{"hook_event_name":"PreToolUse","hook_event_name":"Stop"}',
                     b'{"nested":{"x":1,"x":2}}', b'{"x":NaN}', b'{}{}', b'\xff',
                     b'a' * (ADAPTER.MAX_INPUT + 1)):
            with self.subTest(data=data[:40]):
                with self.assertRaises((ValueError, UnicodeError, json.JSONDecodeError)):
                    ADAPTER.parse(data)

    def test_unsupported_version_and_deep_input_emit_nothing(self):
        valid = json.dumps(hook("UserPromptSubmit")).encode()
        with mock.patch.object(ADAPTER, "pinned_codex", return_value=False), \
             mock.patch.object(ADAPTER, "report") as report, \
             mock.patch.object(sys, "argv", ["adapter", "hook"]), \
             mock.patch.object(sys, "stdin", SimpleNamespace(buffer=io.BytesIO(valid))):
            self.assertEqual(ADAPTER.run(), 0)
            report.assert_not_called()
        deep = b"[" * 2000 + b"0" + b"]" * 2000
        with mock.patch.object(ADAPTER, "pinned_codex", return_value=True), \
             mock.patch.object(ADAPTER, "report") as report, \
             mock.patch.object(sys, "argv", ["adapter", "hook"]), \
             mock.patch.object(sys, "stdin", SimpleNamespace(buffer=io.BytesIO(deep))):
            self.assertEqual(ADAPTER.run(), 0)
            report.assert_not_called()

    def test_fixed_content_free_unix_notification(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = str(Path(tmp) / "session.sock")
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            listener.bind(path)
            listener.listen(1)
            received = []

            def serve():
                conn, _ = listener.accept()
                with conn:
                    received.append(conn.recv(4096))

            thread = threading.Thread(target=serve)
            thread.start()
            try:
                with mock.patch.object(ADAPTER, "SOCKET", path):
                    ADAPTER.report(*ADAPTER.hook_condition(hook("PermissionRequest")))
                thread.join(2)
                self.assertFalse(thread.is_alive())
                self.assertEqual(json.loads(received[0]), {
                    "jsonrpc": "2.0", "method": "status.report", "params": {
                        "v": 1, "source": "codex/session/" + UUID, "condition": "attention",
                        "reason": "permission requested", "adapter": "codex", "adapter_version": "0.151.0"}})
                self.assertNotIn(b"private", received[0])
            finally:
                listener.close()

    def test_version_exact_and_init_preserves_user_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            executable = Path(tmp) / "fake-version"
            observed = Path(tmp) / "probe-environment"
            capture = "printf '%s|%s|%s\\n' \"$HOME\" \"$CODEX_HOME\" \"$PWD\" > " + shlex.quote(str(observed)) + "\n"
            executable.write_text("#!/bin/sh\n" + capture + "printf 'codex-cli 0.151.0\\n'\n")
            executable.chmod(0o700)
            with mock.patch.object(ADAPTER, "CODEX", str(executable)), \
                 mock.patch.object(ADAPTER, "safe_version_home", return_value=True):
                self.assertTrue(ADAPTER.pinned_codex())
                self.assertEqual(observed.read_text(), "/var/empty|/var/empty|/\n")
                executable.write_text("#!/bin/sh\n" + capture + "printf 'codex-cli 0.152.0\\n'\n")
                self.assertFalse(ADAPTER.pinned_codex())
            home = Path(tmp) / "home"
            home.mkdir(mode=0o700)
            (home / ".codex").mkdir(mode=0o700)
            auth = home / ".codex/auth.json"
            auth.write_text("dummy credential sentinel")
            with mock.patch.object(ADAPTER, "SESSION_UID", os.geteuid()), mock.patch.dict(os.environ, {"HOME": str(home)}):
                ADAPTER.init(str(home))
                ADAPTER.init(str(home))
                self.assertEqual(auth.read_text(), "dummy credential sentinel")
                config = home / ".codex/config.toml"
                config.write_text("user-managed = true\n")
                config.chmod(0o600)
                with self.assertRaisesRegex(ValueError, "user-managed"):
                    ADAPTER.init(str(home))
                self.assertEqual(config.read_text(), "user-managed = true\n")
                self.assertEqual(auth.read_text(), "dummy credential sentinel")

            second = Path(tmp) / "other-home"
            second.mkdir(mode=0o700)
            sentinel = Path(tmp) / "sentinel"
            sentinel.write_text("retain")
            (second / ".codex").symlink_to(sentinel)
            with mock.patch.object(ADAPTER, "SESSION_UID", os.geteuid()), mock.patch.dict(os.environ, {"HOME": str(second)}):
                with self.assertRaises(OSError):
                    ADAPTER.init(str(second))
            self.assertEqual(sentinel.read_text(), "retain")

            for leaf in ("hooks.json", "config.toml"):
                linked = Path(tmp) / (leaf + "-home")
                linked.mkdir(mode=0o700)
                (linked / ".codex").mkdir(mode=0o700)
                (linked / ".codex" / leaf).symlink_to(sentinel)
                with mock.patch.object(ADAPTER, "SESSION_UID", os.geteuid()), mock.patch.dict(os.environ, {"HOME": str(linked)}):
                    with self.assertRaises(OSError):
                        ADAPTER.init(str(linked))
                self.assertEqual(sentinel.read_text(), "retain")
                self.assertEqual(sorted(p.name for p in (linked / ".codex").iterdir()), [leaf])

            fresh = Path(tmp) / "fresh-home"
            fresh.mkdir(mode=0o700)
            with mock.patch.dict(os.environ, {"HOME": str(fresh)}):
                with mock.patch.object(ADAPTER, "SESSION_UID", os.geteuid() + 1):
                    with self.assertRaisesRegex(ValueError, "session user"):
                        ADAPTER.init(str(fresh))
                with mock.patch.object(ADAPTER, "SESSION_UID", os.geteuid()):
                    old_umask = os.umask(0o777)
                    try:
                        ADAPTER.init(str(fresh))
                    finally:
                        os.umask(old_umask)
            self.assertEqual((fresh / ".codex").stat().st_mode & 0o777, 0o700)
            self.assertEqual((fresh / ".codex/hooks.json").stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
