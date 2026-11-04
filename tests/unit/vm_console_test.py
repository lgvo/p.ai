"""Run the shared interactive console wrapper on a real, test-owned PTY."""

import copy
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import subprocess
import sys
import termios
import unittest


WRAPPER = Path(__file__).resolve().parents[2] / "dev" / "vm" / "run-console.sh"
FRAME = b"\x1b[1;21HDETAIL\nNEXT\x1b[1;21HZ\nW"
QEMU_CHILD = r"""
import copy, os, sys, termios
saved = termios.tcgetattr(0)
assert not saved[1] & termios.ONLCR, 'wrapper did not clear ONLCR before QEMU startup'
active = copy.deepcopy(saved)
active[1] |= termios.OPOST
active[3] &= ~(termios.ECHO | termios.ICANON | termios.ISIG)
termios.tcsetattr(0, termios.TCSANOW, active)
assert termios.tcgetattr(0)[1] & termios.OPOST
os.write(1, b'\x1b[1;21HDETAIL\nNEXT\x1b[1;21HZ\nW')
assert sys.argv[2:] == ['argument with spaces', '--literal'], sys.argv
# Real QEMU restores its startup snapshot, which still has ONLCR disabled.
termios.tcsetattr(0, termios.TCSANOW, saved)
sys.exit(int(sys.argv[1]))
"""


class VMConsoleTests(unittest.TestCase):
    def run_console(self, command, *, output_processing=True):
        master, slave = pty.openpty()
        self.addCleanup(os.close, master)
        self.addCleanup(os.close, slave)
        initial = termios.tcgetattr(slave)
        initial[1] |= termios.ONLCR
        if output_processing:
            initial[1] |= termios.OPOST
        else:
            initial[1] &= ~termios.OPOST
        # Use nondefault settings too, so restoring defaults cannot pass.
        initial[0] |= termios.IXOFF
        initial[6][termios.VEOF] = b'\x05'
        termios.tcsetattr(slave, termios.TCSANOW, initial)
        initial = copy.deepcopy(termios.tcgetattr(slave))
        process = subprocess.Popen([shutil.which("bash"), str(WRAPPER), *command],
                                   stdin=slave, stdout=slave, stderr=subprocess.PIPE,
                                   start_new_session=True)
        try:
            _, stderr = process.communicate(timeout=5)
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()
        captured = bytearray()
        while select.select([master], [], [], 0)[0]:
            captured.extend(os.read(master, 4096))
        self.assertEqual(termios.tcgetattr(slave), initial, "original terminal modes were not restored")
        return process.returncode, bytes(captured), stderr

    def test_qemu_reenables_opost_without_inserting_carriage_returns(self):
        for output_processing in (True, False):
            with self.subTest(output_processing=output_processing):
                status, output, stderr = self.run_console(
                    [sys.executable, "-c", QEMU_CHILD, "0", "argument with spaces", "--literal"],
                    output_processing=output_processing)
                self.assertEqual(status, 0, stderr.decode())
                self.assertEqual(output, FRAME)

    def test_failed_qemu_preserves_exit_status_bytes_and_original_modes(self):
        status, output, stderr = self.run_console(
            [sys.executable, "-c", QEMU_CHILD, "17", "argument with spaces", "--literal"])
        self.assertEqual(status, 17, stderr.decode())
        self.assertEqual(output, FRAME)

    def test_missing_command_restores_original_modes(self):
        status, _, stderr = self.run_console(["/nonexistent/p-test-qemu"])
        self.assertEqual(status, 127, stderr.decode())

    def test_child_signal_restores_original_modes(self):
        child = "import os,signal,termios; state=termios.tcgetattr(0); state[3]&=~termios.ECHO; termios.tcsetattr(0,termios.TCSANOW,state); os.kill(os.getpid(),signal.SIGTERM)"
        status, _, stderr = self.run_console([sys.executable, "-c", child])
        self.assertEqual(status, 143, stderr.decode())

    def test_noninteractive_invocation_refused(self):
        result = subprocess.run([shutil.which("bash"), str(WRAPPER), "true"],
                                stdin=subprocess.DEVNULL, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 2)
        self.assertIn(b"interactive terminal required", result.stderr)

    def test_signals_to_wrapper_stop_child_before_restoring_modes(self):
        child = r"""import os,signal,sys,termios
state=termios.tcgetattr(0)
state[1] |= termios.OPOST
state[3] &= ~(termios.ECHO | termios.ICANON)
termios.tcsetattr(0,termios.TCSANOW,state)
def stopped(*_):
 os.write(1,b'STOPPED')
 sys.exit(0)
for signum in (signal.SIGTERM,signal.SIGHUP,signal.SIGINT):
 signal.signal(signum,stopped)
os.write(1,b'READY')
while True:
 signal.pause()
"""
        for signum in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
            with self.subTest(signum=signum):
                master, slave = pty.openpty()
                process = None
                try:
                    initial = copy.deepcopy(termios.tcgetattr(slave))
                    process = subprocess.Popen(
                        [shutil.which("bash"), str(WRAPPER), sys.executable, "-c", child],
                        stdin=slave, stdout=slave, stderr=subprocess.PIPE, start_new_session=True)
                    self.assertTrue(select.select([master], [], [], 3)[0], "child did not start")
                    self.assertEqual(os.read(master, 4096), b'READY')
                    os.kill(process.pid, signum)
                    _, stderr = process.communicate(timeout=3)
                    self.assertEqual(process.returncode, 128 + signum, stderr.decode())
                    self.assertTrue(select.select([master], [], [], 1)[0], "child did not stop")
                    self.assertEqual(os.read(master, 4096), b'STOPPED')
                    self.assertEqual(termios.tcgetattr(slave), initial)
                finally:
                    if process is not None and process.poll() is None:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.communicate()
                    os.close(master)
                    os.close(slave)


if __name__ == "__main__":
    unittest.main()
