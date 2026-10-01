"""Real PTY checks for the persistent adaptive observer; no daemon/fixtures."""
import importlib.util
import json
import os
from pathlib import Path
import sys
import subprocess
import shutil
import socket
import tempfile
import termios
import time
import unittest


spec = importlib.util.spec_from_file_location("observer", Path(__file__).with_name("tui-observe.py"))
observer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(observer)


class ObserverTests(unittest.TestCase):
    def test_output_processing_is_observed_without_changing_child_modes(self):
        child = r"""import os,termios,time
state=termios.tcgetattr(0)
assert state[1] & termios.OPOST and state[1] & termios.ONLCR
time.sleep(.1)
assert termios.tcgetattr(0)[1] & termios.OPOST, 'observer changed child output processing'
os.write(1,b'\x1b[1;21HA\nB')
time.sleep(.1)
assert termios.tcgetattr(0)[1] & termios.OPOST, 'observer changed child output processing'
"""
        with tempfile.TemporaryDirectory() as out:
            terminal = observer.Observer([sys.executable, "-c", child], out, 80, 24)
            try:
                terminal.pump(.4)
                self.assertEqual(terminal.process.wait(timeout=2), 0)
                terminal.pump(.05)
                self.assertEqual(terminal.screen.display[0][20], "A")
                self.assertEqual(terminal.screen.display[1][0], "B")
                self.assertIn(b'A\r\nB', (Path(out) / "terminal.ansi").read_bytes())
            finally:
                terminal.close()

    def test_interactive_console_preserves_newline_columns_and_restores_modes(self):
        child = r"""import copy,os,termios,time
saved=termios.tcgetattr(0)
assert not saved[1] & termios.ONLCR
active=copy.deepcopy(saved)
active[1] |= termios.OPOST
active[3] &= ~(termios.ECHO | termios.ICANON | termios.ISIG)
termios.tcsetattr(0,termios.TCSANOW,active)
os.write(1,b'\x1b[1;21HDETAIL\nNEXT')
time.sleep(.1)
os.write(1,b'\x1b[1;21HZ\nW')
termios.tcsetattr(0,termios.TCSANOW,saved)
"""
        wrapper = Path(__file__).resolve().parents[2] / "dev" / "vm" / "run-console.sh"
        with tempfile.TemporaryDirectory() as out:
            terminal = observer.Observer([shutil.which("bash"), str(wrapper), sys.executable, "-c", child],
                                         out, 120, 35)
            try:
                terminal.pump(.3)
                self.assertEqual(terminal.process.wait(timeout=2), 0)
                terminal.pump(.05)
                self.assertEqual(terminal.screen.display[0][20:26], "ZETAIL")
                self.assertEqual(terminal.screen.display[1][26:30], "NEXT")
                self.assertEqual(terminal.screen.display[1][21], "W")
                self.assertEqual(terminal.screen.display[1][:20], " " * 20)
                self.assertEqual((terminal.screen.cursor.x, terminal.screen.cursor.y), (22, 1))
                raw = (Path(out) / "terminal.ansi").read_bytes()
                self.assertEqual(raw, b'\x1b[1;21HDETAIL\nNEXT\x1b[1;21HZ\nW')
                restored = termios.tcgetattr(terminal.master)
                self.assertTrue(restored[1] & termios.OPOST)
                self.assertTrue(restored[1] & termios.ONLCR)
            finally:
                terminal.close()

    def test_backward_tabs_preserve_diff_cell_positions(self):
        screen = observer.TerminalScreen(48, 16, lambda _: None)
        stream = observer.TerminalStream(screen)
        stream.feed(b"\x1b[1;21Habc\x1b[2ZXY")
        self.assertEqual(screen.cursor.x, 10)
        self.assertEqual(screen.display[0][8:10], "XY")
        self.assertEqual(screen.display[0][20:23], "abc")
        stream.feed(b"\x1b[1;7HCode\x1b[Zunknown")
        self.assertEqual(screen.display[0][8:15], "unknown")
        stream.feed(b"\x1b[1;2H\x1b[0Z!")
        self.assertEqual(screen.display[0][0], "!")

    def test_resize_preserves_default_custom_and_cleared_tabs(self):
        screen = observer.TerminalScreen(48, 16, lambda _: None)
        stream = observer.TerminalStream(screen)
        screen.resize(lines=35, columns=120)
        stream.feed(b"\x1b[1;48H\tA\x1b[2ZB")
        self.assertEqual(screen.display[0][48], "A")
        self.assertEqual(screen.display[0][40], "B")
        stream.feed(b"\x1b[3g\x1b[1;14H\x1bH")  # One custom stop at x=13.
        screen.resize(lines=16, columns=48)
        screen.resize(lines=35, columns=120)
        stream.feed(b"\x1b[1;1H\tC")
        self.assertEqual(screen.display[0][13], "C")
        self.assertEqual(screen.tabstops, {13})
        stream.feed(b"\x1b[3g")
        screen.resize(lines=16, columns=48)
        screen.resize(lines=35, columns=120)
        stream.feed(b"\x1b[1;1H\tD")
        self.assertEqual(screen.display[0][119], "D")
        self.assertEqual(screen.tabstops, set())

    def test_real_pty_expansion_renders_tabs_in_detail_column(self):
        child = """import os,signal,time
def resized(*_):
 os.write(1,b'\\x1b[2J\\x1b[Hleft'+b'\\t'*10+b'DETAIL\\x1b[ZDETAIL')
signal.signal(signal.SIGWINCH,resized)
os.write(1,b'compact')
time.sleep(5)
"""
        with tempfile.TemporaryDirectory() as out:
            terminal = observer.Observer([sys.executable, "-c", child], out, 48, 16)
            try:
                terminal.pump(.1)
                result = terminal.handle({"op": "resize", "cols": 120, "rows": 35, "wait": .1})
                frame = json.loads(Path(result["frame"] + ".json").read_text())
                self.assertEqual("".join(c["data"] for c in frame["cells"][0][80:86]), "DETAIL")
                self.assertEqual(frame["cursor"]["x"], 86)
            finally:
                terminal.close()

    def test_numeric_rgb_and_light_default_rendering(self):
        self.assertEqual(observer.color("137333", "#000000"), "#137333")
        self.assertEqual(observer.color("203", "#000000"), (255, 95, 95))
        with tempfile.TemporaryDirectory() as out:
            terminal = observer.Observer(["/bin/sh", "-c", "printf '\\033[38;2;19;115;51mGREEN\\033[0m'; sleep 2"],
                                         out, 20, 8, background="light")
            try:
                terminal.pump(.1)
                frame = terminal.capture({"op": "observe", "label": "light"})
                cells = json.loads(Path(frame["frame"] + ".json").read_text())
                self.assertEqual(cells["background"], "light")
                self.assertEqual(cells["cells"][0][0]["fg"], "137333")
                from PIL import Image
                pixels = Image.open(frame["frame"] + ".png")
                self.assertEqual(pixels.getpixel((19 * 9, 7 * 20)), (255, 255, 255))
                self.assertIn((19, 115, 51), {rgb for _, rgb in pixels.getcolors(pixels.width * pixels.height)})
            finally:
                terminal.close()

    def test_persistent_server_answers_queries_between_control_requests(self):
        child = """import os,tty,time
tty.setraw(0)
time.sleep(.6)
assert os.path.isdir(os.environ['TMPDIR']), 'invoking shell removed temporary root'
os.write(1,b'\\x1b[>0c')
data=b''
while not data.endswith(b'c'):
 data+=os.read(0,100)
os.write(1,b'\\r\\nIDLE_QUERY_REPLY '+data.hex().encode())
time.sleep(5)
"""
        with tempfile.TemporaryDirectory() as out:
            sock = str(Path(out) / "control.sock")
            script = str(Path(__file__).with_name("tui-observe.py"))
            invoking_tmp = Path(out) / "invoking-shell-tmp"
            invoking_tmp.mkdir()
            result = subprocess.run([sys.executable, script, "--socket", sock, "start", "--out", out,
                                     "--cols", "100", "--rows", "10", "--", sys.executable, "-c", child],
                                    capture_output=True, text=True, timeout=15,
                                    env={**os.environ, "TMPDIR": str(invoking_tmp)})
            self.assertEqual(result.returncode, 0, result.stderr)
            invoking_tmp.rmdir()  # Reproduce nix-shell --run cleanup after start.
            try:
                # Bad or interrupted control clients must not terminate the
                # native PTY command or stall its terminal-query drain loop.
                for data in (b"not-json\n", b'{"op":'):
                    with socket.socket(socket.AF_UNIX) as client:
                        client.connect(sock)
                        client.sendall(data)
                # No socket control requests while the child asks and waits.
                time.sleep(.7)
                frame = observer.request(sock, {"op": "observe", "wait": 0})
                self.assertIn("IDLE_QUERY_REPLY 1b5b3e303b3337303b3063", frame["screen"])
                observer.request(sock, {"op": "resize", "cols": 80, "rows": 24, "wait": 0})
            finally:
                observer.request(sock, {"op": "close", "wait": 0})

    def test_real_terminal_cursor_styles_and_resize(self):
        with tempfile.TemporaryDirectory() as out:
            terminal = observer.Observer(["/bin/sh", "-c", "printf '\033[2J\033[3;5H\033[31;44;1mCELL\033[0m'; sleep 10"], out, 20, 8)
            try:
                terminal.pump(.15)
                frame = terminal.handle({"op": "observe", "wait": 0, "label": "styled"})
                cells = json.loads(Path(frame["frame"] + ".json").read_text())
                self.assertEqual(cells["cells"][2][4]["data"], "C")
                self.assertEqual(cells["cells"][2][4]["fg"], "red")
                self.assertEqual(cells["cells"][2][4]["bg"], "blue")
                self.assertTrue(cells["cells"][2][4]["bold"])
                self.assertEqual(cells["cursor"], {"x": 8, "y": 2, "hidden": False})
                self.assertTrue(Path(frame["frame"] + ".png").is_file())
                terminal.handle({"op": "resize", "cols": 30, "rows": 10, "wait": 0})
                self.assertEqual((terminal.screen.columns, terminal.screen.lines), (30, 10))
            finally:
                terminal.close()

    def test_real_pty_query_response_without_user_input(self):
        # The child asks after the observer has already returned a capture. A
        # query reply must arrive while no LLM control request is being handled.
        child = """import os,tty,time
tty.setraw(0)
time.sleep(.2)
os.write(1,b'\\x1b[>0c\\x1b[?6n')
data=b''
while b'R' not in data:
 data+=os.read(0,100)
os.write(1,b'\\r\\nQUERY_REPLY '+data.hex().encode())
"""
        with tempfile.TemporaryDirectory() as out:
            terminal = observer.Observer([sys.executable, "-c", child], out, 100, 10)
            try:
                terminal.pump(.05)
                # This is the same drain loop that runs continuously in serve.
                deadline = time.monotonic() + 2
                while terminal.process.poll() is None and time.monotonic() < deadline:
                    terminal.pump(.02)
                terminal.pump(.05)
                self.assertEqual(terminal.process.poll(), 0)
                self.assertIn("QUERY_REPLY 1b5b3e303b3337303b30631b5b3f313b3152", "\n".join(terminal.screen.display))
            finally:
                terminal.close()


if __name__ == "__main__":
    unittest.main()
