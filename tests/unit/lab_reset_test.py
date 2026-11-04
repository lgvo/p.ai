"""Exercise reset against an isolated checkout; never touch a developer VM."""

import fcntl
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class LabResetTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="p-lab-reset-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "checkout"
        (self.repo / "dev").mkdir(parents=True)
        source = Path(__file__).resolve().parents[2] / "dev" / "reset-lab-vm"
        self.script = self.repo / "dev" / "reset-lab-vm"
        shutil.copyfile(source, self.script)
        self.cache = self.repo / ".cache" / "p-vm"
        self.public = self.make_state(self.cache / "demo-public")
        self.offline = self.make_state(self.cache / "demo")
        self.legacy = self.make_state(self.cache / "demo-notes")
        (self.cache / "disk.qcow2").write_text("infrastructure lab")
        self.env = {key: value for key, value in os.environ.items()
                    if key not in {"P_DEMO_STATE_DIR", "P_VM_STATE_DIR"}}

    def make_state(self, path):
        path.mkdir(parents=True)
        (path / "disk.qcow2").write_text("guest state")
        (path / "console.log").write_text("retained log")
        return path

    def run_reset(self, *args, answer="yes\n", env=None):
        return subprocess.run([sys.executable, "-u", str(self.script), *args],
                              input=answer, text=True, capture_output=True,
                              cwd=self.repo, env=self.env | (env or {}), timeout=5)

    def assert_preserved(self, path):
        self.assertEqual((path / "disk.qcow2").read_text(), "guest state")

    def test_default_public_confirmed_deletes_only_selected_disk(self):
        result = self.run_reset()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(str(self.public / "disk.qcow2"), result.stdout)
        self.assertIn("guest Nix store", result.stdout)
        self.assertFalse((self.public / "disk.qcow2").exists())
        self.assertEqual((self.public / "console.log").read_text(), "retained log")
        self.assertTrue((self.public / "vm.lock").is_file())
        self.assertTrue((self.cache / "integration.lock").is_file())
        self.assert_preserved(self.offline)
        self.assert_preserved(self.legacy)
        self.assertEqual((self.cache / "disk.qcow2").read_text(), "infrastructure lab")

    def test_confirmation_defaults_to_no_including_eof(self):
        for answer in ("", "\n", "no\n", "y\n", "YES\n"):
            with self.subTest(answer=answer):
                result = self.run_reset(answer=answer)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("cancelled", result.stdout)
                self.assert_preserved(self.public)

    def test_offline_and_explicit_public(self):
        result = self.run_reset("--offline")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.offline / "disk.qcow2").exists())
        self.assert_preserved(self.public)
        result = self.run_reset("--public")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.public / "disk.qcow2").exists())

    def test_environment_precedence_and_relative_paths(self):
        alternate = self.make_state(self.repo / "alternate lab")
        result = self.run_reset("--offline", env={
            "P_DEMO_STATE_DIR": "alternate lab", "P_VM_STATE_DIR": str(self.legacy)})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((alternate / "disk.qcow2").exists())
        self.assert_preserved(self.offline)
        self.assert_preserved(self.legacy)
        result = self.run_reset(env={"P_DEMO_STATE_DIR": "", "P_VM_STATE_DIR": str(self.legacy)})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.legacy / "disk.qcow2").exists())
        self.assert_preserved(self.public)

    def test_active_checkout_and_state_locks_refuse_even_yes(self):
        for path, message in ((self.cache / "integration.lock", "active for this checkout"),
                              (self.public / "vm.lock", "already using")):
            with self.subTest(path=path), path.open("a") as held:
                fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
                result = self.run_reset()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(message, result.stderr)
                self.assertNotIn("Type yes", result.stdout)
                self.assert_preserved(self.public)

    def test_locks_held_through_confirmation(self):
        process = subprocess.Popen([sys.executable, "-u", str(self.script)],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True, env=self.env)
        try:
            self.assertIn("Reset disk:", process.stdout.readline())
            for path in (self.cache / "integration.lock", self.public / "vm.lock"):
                with path.open("a") as attempted:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(attempted, fcntl.LOCK_EX | fcntl.LOCK_NB)
            stdout, stderr = process.communicate("yes\n", timeout=5)
            self.assertEqual(process.returncode, 0, stderr)
            self.assertIn("Lab disk removed", stdout)
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()

    def test_changed_disk_during_confirmation_refused(self):
        process = subprocess.Popen([sys.executable, "-u", str(self.script)],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True, env=self.env)
        try:
            self.assertIn("Reset disk:", process.stdout.readline())
            (self.public / "disk.qcow2").write_text("different state")
            _, stderr = process.communicate("yes\n", timeout=5)
            self.assertNotEqual(process.returncode, 0)
            self.assertIn("changed during confirmation", stderr)
            self.assertEqual((self.public / "disk.qcow2").read_text(), "different state")
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()

    def test_missing_disk_or_directory_is_noop(self):
        missing = self.root / "absent"
        result = self.run_reset(env={"P_DEMO_STATE_DIR": str(missing)})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(missing.exists())
        (self.public / "disk.qcow2").unlink()
        result = self.run_reset()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("nothing to reset", result.stdout)

    def test_unsafe_selected_directories_refused(self):
        link = self.root / "linked"
        link.symlink_to(self.public, target_is_directory=True)
        ancestor = self.root / "linked-ancestor"
        ancestor.symlink_to(self.cache, target_is_directory=True)
        for selected in (str(link), str(ancestor / "demo-public"), "/", str(self.repo),
                         str(self.cache), str(self.public / ".." / "demo-public")):
            with self.subTest(selected=selected):
                result = self.run_reset(env={"P_DEMO_STATE_DIR": selected})
                self.assertNotEqual(result.returncode, 0)
                self.assert_preserved(self.public)

    def test_symlink_locks_refused_without_modifying_target(self):
        target = self.root / "outside"
        target.write_text("keep me")
        for path in (self.cache / "integration.lock", self.public / "vm.lock"):
            with self.subTest(path=path):
                path.unlink(missing_ok=True)
                path.symlink_to(target)
                result = self.run_reset()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(target.read_text(), "keep me")
                self.assert_preserved(self.public)
                path.unlink()

    def test_symlink_directory_and_hardlinked_disks_refused(self):
        disk = self.public / "disk.qcow2"
        target = self.root / "outside"
        target.write_text("keep me")
        for kind in ("symlink", "directory", "hardlink"):
            with self.subTest(kind=kind):
                disk.unlink()
                if kind == "symlink":
                    disk.symlink_to(target)
                elif kind == "directory":
                    disk.mkdir()
                else:
                    os.link(target, disk)
                result = self.run_reset()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(target.read_text(), "keep me")
                if kind == "directory":
                    disk.rmdir()
                else:
                    disk.unlink()
                disk.write_text("guest state")

    def test_checkout_cache_symlink_refused(self):
        moved = self.root / "moved-cache"
        self.cache.rename(moved)
        self.cache.symlink_to(moved, target_is_directory=True)
        result = self.run_reset()
        self.assertNotEqual(result.returncode, 0)
        self.assert_preserved(self.public)

    def test_help_and_invalid_options_do_not_delete(self):
        self.assertEqual(self.run_reset("--help").returncode, 0)
        self.assertNotEqual(self.run_reset("--public", "--offline").returncode, 0)
        self.assertNotEqual(self.run_reset("--yes").returncode, 0)
        self.assert_preserved(self.public)


if __name__ == "__main__":
    unittest.main()
