"""Shared terminal parser for native automation and adaptive observation."""
import re
import copy
import pyte


class TerminalScreen(pyte.Screen):
    def __init__(self, columns, lines, send, background="dark"):
        self.send = send
        self.background = background
        self.primary = None
        super().__init__(columns, lines)

    def reset(self):
        super().reset()
        # Default stops extend through the observer's bounded 500-column
        # resize domain, as a terminal's stops do beyond its current width.
        # Seed once on reset so custom stops and CSI 3g remain authoritative
        # across shrinking and expanding; resize must not reinstall defaults.
        self.tabstops = set(range(8, 500, 8))

    def set_mode(self, *modes, **kwargs):
        if kwargs.get("private") and any(mode in (47, 1047, 1049) for mode in modes):
            if self.primary is None:
                self.primary = (copy.deepcopy(self.buffer), copy.deepcopy(self.cursor), self.margins)
                self.erase_in_display(2)
                self.cursor_position()
        super().set_mode(*modes, **kwargs)

    def reset_mode(self, *modes, **kwargs):
        if kwargs.get("private") and any(mode in (47, 1047, 1049) for mode in modes):
            if self.primary is not None:
                self.buffer, self.cursor, self.margins = self.primary
                self.primary = None
                self.dirty.update(range(self.lines))
        super().reset_mode(*modes, **kwargs)

    def resize(self, lines=None, columns=None):
        saved = self.primary
        if saved is not None:
            dimensions = (self.lines, self.columns)
            alternate = (self.buffer, self.cursor, self.margins)
            self.buffer, self.cursor, self.margins = saved
            super().resize(lines=lines, columns=columns)
            self.ensure_hbounds()
            self.ensure_vbounds()
            self.primary = (self.buffer, self.cursor, self.margins)
            self.buffer, self.cursor, self.margins = alternate
            # The base class already updated dimensions while resizing primary;
            # restore old dimensions so it also resizes the alternate buffer.
            self.lines, self.columns = dimensions
        super().resize(lines=lines, columns=columns)
        self.ensure_hbounds()
        self.ensure_vbounds()

    def scroll_up(self, count=1):
        previous = self.cursor.y
        self.cursor.y = self.margins.bottom if self.margins else self.lines - 1
        for _ in range(min(count or 1, self.lines)):
            self.index()
        self.cursor.y = previous

    def scroll_down(self, count=1):
        previous = self.cursor.y
        self.cursor.y = self.margins.top if self.margins else 0
        for _ in range(min(count or 1, self.lines)):
            self.reverse_index()
        self.cursor.y = previous

    def write_process_input(self, data):
        self.send(data)

    def back_tab(self, count=1):
        # CSI Z moves to preceding configured tab stops. Bubble Tea emits
        # this while diffing nearby cells; pyte's default CSI map omits it.
        for _ in range(count or 1):
            self.cursor.x = max((stop for stop in self.tabstops
                                 if stop < self.cursor.x), default=0)
        self.ensure_hbounds()

    def report_device_status(self, mode, private=False):
        # tmux also asks for the DEC-private cursor report. pyte 0.8.2's
        # parser forwards private=True but its base handler lacks that argument.
        if mode == 6:
            # pyte represents delayed autowrap as cursor.x == columns. The
            # real terminal cursor and CPR still occupy the final column.
            prefix = "?" if private else ""
            self.write_process_input(f"\x1b[{prefix}{self.cursor.y + 1};{min(self.cursor.x + 1, self.columns)}R")
        elif not private:
            return super().report_device_status(mode)


class TerminalStream(pyte.ByteStream):
    # The pinned renderer uses CSI S/T when a frame changes height; pyte's
    # default map omits them, leaving old rows/notices on its reconstructed screen.
    csi = {**pyte.ByteStream.csi, "S": "scroll_up", "T": "scroll_down", "Z": "back_tab"}
    events = pyte.ByteStream.events | {"scroll_up", "scroll_down", "back_tab"}

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.query_tail = b""

    def feed(self, data):
        # pyte drops the '>' modifier and incorrectly answers secondary DA
        # with another primary DA. tmux consumes the first; the duplicate can
        # leak into the shell. Recognize secondary DA before pyte parses it,
        # including queries split across PTY reads, and report xterm identity.
        data = self.query_tail + data
        self.query_tail = b""
        # Buffer unfinished CSI/OSC/DCS before dispatch so query recognition
        # survives arbitrary PTY chunk boundaries. Do not feed an unsupported
        # Kitty modifier to pyte: it can become stray ordinary text.
        sequences = list(re.finditer(rb"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|P.*?\x1b\\)", data, re.S))
        tail = data.find(b"\x1b", sequences[-1].end() if sequences else 0)
        if tail >= 0 and not any(match.start() <= tail < match.end() for match in sequences):
            fragment = data[tail:]
            if fragment == b"\x1b" or fragment.startswith((b"\x1b[", b"\x1b]", b"\x1bP")):
                self.query_tail, data = fragment, data[:tail]
        offset = 0
        for query in sequences:
            if query.end() > len(data):
                break
            super().feed(data[offset:query.start()])
            sequence = query.group()
            if re.fullmatch(rb"\x1b\[>(?:0)?c", sequence):
                self.listener.write_process_input("\x1b[>0;370;0c")
            elif sequence == b"\x1b[?u":
                self.listener.write_process_input("\x1b[?0u")
            elif sequence == b"\x1b[?996n":
                self.listener.write_process_input(f"\x1b[?997;{1 if self.listener.background == 'dark' else 2}n")
            elif re.fullmatch(rb"\x1b\[\?(?:2026|2027)\$p", sequence):
                # Neither synchronized repaint nor grapheme-width mode is
                # implemented by pyte; report unknown rather than claiming it.
                mode = sequence[3:-2].decode()
                self.listener.write_process_input(f"\x1b[?{mode};0$y")
            elif re.fullmatch(rb"\x1b\[[<>=][0-9;]*u", sequence):
                pass  # Kitty keyboard push/pop; this observer sends legacy keys.
            elif re.fullmatch(rb"\x1b\[>4(?:;[012])?m", sequence):
                pass  # xterm modifyOtherKeys is not SGR underline.
            elif re.fullmatch(rb"\x1b\](?:10|11);\?(?:\x07|\x1b\\)", sequence):
                foreground = sequence.startswith(b"\x1b]10;")
                value = ("e5e5/e9e9/f0f0" if foreground else "1010/1414/1c1c") if self.listener.background == "dark" else ("1515/2020/2b2b" if foreground else "ffff/ffff/ffff")
                self.listener.write_process_input(f"\x1b]{10 if foreground else 11};rgb:{value}\x1b\\")
            elif sequence.startswith(b"\x1bP+q"):
                self.listener.write_process_input(b"\x1bP0+r" + sequence[4:-2] + b"\x1b\\")
            else:
                super().feed(sequence)
            offset = query.end()
        super().feed(data[offset:])
