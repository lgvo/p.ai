"""Protocol/unit fixtures for the PTY observer; no daemon or credentials."""
import ast
from pathlib import Path
import re
import subprocess
import sys
import time
import unittest

import pyte
from tui_terminal import TerminalScreen, TerminalStream


source = ast.parse(Path(__file__).with_name("tui-drive.py").read_text())
definitions = [node for node in source.body
               if isinstance(node, (ast.ClassDef, ast.FunctionDef))
               and node.name == "observe_process"]
responses = []
reads = []
namespace = {"TerminalScreen": lambda width, height: TerminalScreen(width, height, responses.append), "TerminalStream": TerminalStream, "pyte": pyte, "re": re, "subprocess": subprocess, "time": time,
             "send": responses.append, "read": lambda timeout: reads.append(time.monotonic())}
exec(compile(ast.Module(body=definitions, type_ignores=[]), __file__, "exec"), namespace)


class ObserverTests(unittest.TestCase):
    def test_alternate_screen_restores_primary_cells_styles_and_cursor(self):
        screen = namespace["TerminalScreen"](20, 6)
        stream = TerminalStream(screen)
        stream.feed(b"\x1b[31mCONSOLE\x1b[3;5H\x1b[?1049h\x1b[0mBROWSER")
        self.assertTrue(screen.display[0].startswith("BROWSER"))
        self.assertIsNotNone(screen.primary)
        stream.feed(b"\x1b[?1049l")
        self.assertTrue(screen.display[0].startswith("CONSOLE"))
        self.assertEqual(screen.buffer[0][0].fg, "red")
        self.assertEqual((screen.cursor.y, screen.cursor.x), (2, 4))
        self.assertIsNone(screen.primary)

    def test_keyboard_color_and_termcap_queries_split_at_every_boundary(self):
        pairs = [(b"\x1b[?u", "\x1b[?0u"),
                 (b"\x1b[?996n", "\x1b[?997;1n"),
                 (b"\x1b[?2026$p", "\x1b[?2026;0$y"),
                 (b"\x1b[?2027$p", "\x1b[?2027;0$y"),
                 (b"\x1b]10;?\x07", "\x1b]10;rgb:e5e5/e9e9/f0f0\x1b\\"),
                 (b"\x1b]11;?\x1b\\", "\x1b]11;rgb:1010/1414/1c1c\x1b\\"),
                 (b"\x1bP+q544e\x1b\\", b"\x1bP0+r544e\x1b\\")]
        for query, reply in pairs:
            for split in range(len(query) + 1):
                responses.clear()
                screen = namespace["TerminalScreen"](20, 6)
                stream = TerminalStream(screen)
                stream.feed(query[:split])
                stream.feed(query[split:] + b"\x1b[>1u\x1b[>4;2mOK\x1b[>4m\x1b[<u")
                self.assertEqual(responses, [reply], (query, split))
                self.assertTrue(screen.display[0].startswith("OK"))
                self.assertFalse(screen.buffer[0][0].underscore)

    def test_resize_updates_saved_primary_and_alternate_cursor_bounds(self):
        screen = namespace["TerminalScreen"](120, 35)
        stream = TerminalStream(screen)
        stream.feed(b"\x1b[35;120HX\x1b[?1049h\x1b[34;119HY")
        screen.resize(lines=24, columns=80)
        self.assertLess(screen.cursor.y, 24)
        self.assertLess(screen.cursor.x, 80)
        stream.feed(b"\x1b[?1049l")
        self.assertLess(screen.cursor.y, 24)
        self.assertLess(screen.cursor.x, 80)
        self.assertEqual((screen.lines, screen.columns), (24, 80))

    def test_delayed_autowrap_cursor_reports_final_column(self):
        responses.clear()
        screen = namespace["TerminalScreen"](5, 2)
        stream = TerminalStream(screen)
        stream.feed(b"HELLO\x1b[6n\x1b[?6n")
        self.assertEqual(responses, ["\x1b[1;5R", "\x1b[?1;5R"])

    def test_device_queries_split_at_every_boundary(self):
        for query in (b"\x1b[>c", b"\x1b[>0c"):
            for split in range(len(query) + 1):
                with self.subTest(query=query, split=split):
                    responses.clear()
                    screen = namespace["TerminalScreen"](100, 28)
                    terminal = namespace["TerminalStream"](screen)
                    terminal.feed(b"\x1b[c" + query[:split])
                    terminal.feed(query[split:] + b"HELLO\x1b[?6n")
                    self.assertEqual(responses, ["\x1b[?6c", "\x1b[>0;370;0c", "\x1b[?1;6R"])
                    self.assertTrue(screen.display[0].startswith("HELLO"))
                    self.assertFalse(terminal.query_tail)

    def test_region_scrolling_preserves_cursor_and_external_rows(self):
        screen = namespace["TerminalScreen"](10, 5)
        terminal = namespace["TerminalStream"](screen)
        terminal.feed(b"\x1b[1;1HTOP\x1b[2;1HA\x1b[3;1HB\x1b[4;1HC\x1b[5;1HBOTTOM")
        terminal.feed(b"\x1b[2;4r\x1b[3;3H\x1b[S")
        self.assertEqual([line.strip() for line in screen.display], ["TOP", "B", "C", "", "BOTTOM"])
        self.assertEqual((screen.cursor.y, screen.cursor.x), (2, 2))
        terminal.feed(b"\x1b[T")
        self.assertEqual([line.strip() for line in screen.display], ["TOP", "", "B", "C", "BOTTOM"])
        self.assertEqual((screen.cursor.y, screen.cursor.x), (2, 2))

    def test_observation_drains_and_preserves_output(self):
        reads.clear()
        result = namespace["observe_process"](
            [sys.executable, "-c", "import time; time.sleep(.2); print('observed')"],
            timeout=2, text=True)
        self.assertEqual((result.returncode, result.stdout, result.stderr), (0, "observed\n", ""))
        self.assertGreaterEqual(len(reads), 5)

    def test_observation_timeout(self):
        with self.assertRaises(subprocess.TimeoutExpired):
            namespace["observe_process"](
                [sys.executable, "-c", "import time; time.sleep(5)"], timeout=.1)


if __name__ == "__main__":
    unittest.main()
