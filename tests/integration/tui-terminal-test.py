"""Protocol/unit fixtures for the PTY observer; no daemon or credentials."""
import ast
from pathlib import Path
import re
import subprocess
import sys
import time
import unittest

import pyte


source = ast.parse(Path(__file__).with_name("tui-drive.py").read_text())
definitions = [node for node in source.body
               if isinstance(node, (ast.ClassDef, ast.FunctionDef))
               and node.name in ("TerminalScreen", "TerminalStream", "observe_process")]
responses = []
reads = []
namespace = {"pyte": pyte, "re": re, "subprocess": subprocess, "time": time,
             "send": responses.append, "read": lambda timeout: reads.append(time.monotonic())}
exec(compile(ast.Module(body=definitions, type_ignores=[]), __file__, "exec"), namespace)


class ObserverTests(unittest.TestCase):
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
