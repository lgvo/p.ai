"""Persistent PTY for LLM-chosen actions; never runs a navigation scenario.

Requires pyte and Pillow. Start once, then observe/send/resize between reviews:
  python tui-observe.py --socket /tmp/review.sock start --out /tmp/frames -- p tui
  python tui-observe.py --socket /tmp/review.sock send --text 'P' --label projects
  python tui-observe.py --socket /tmp/review.sock observe --label selected
  python tui-observe.py --socket /tmp/review.sock resize --cols 80 --rows 24
  python tui-observe.py --socket /tmp/review.sock close

Every command saves a numbered PNG, cell JSON and plain text. The JSON preserves
coordinates, cursor, colors and attributes; actions.ndjson preserves the chosen
inputs. The server continuously services terminal queries between commands.
Use only disposable, credential-free terminals: input and frames are evidence.
"""
import argparse
import errno
import fcntl
import glob
import json
import os
from pathlib import Path
import pty
import select
import signal
import socket
import struct
import subprocess
import sys
import termios
import time

from PIL import Image, ImageDraw, ImageFont
from tui_terminal import TerminalScreen, TerminalStream


PALETTE = ["000000", "cd0000", "00cd00", "cdcd00", "0000ee", "cd00cd", "00cdcd", "e5e5e5",
           "7f7f7f", "ff0000", "00ff00", "ffff00", "5c5cff", "ff00ff", "00ffff", "ffffff"]
NAMES = dict(zip(("black", "red", "green", "brown", "blue", "magenta", "cyan", "white"), PALETTE))
NAMES.update({"bright" + name: value for name, value in zip(
    ("black", "red", "green", "brown", "blue", "magenta", "cyan", "white"), PALETTE[8:])})


def color(value, default):
    if value == "default":
        return default
    if value in NAMES:
        return "#" + NAMES[value]
    # pyte represents truecolor as six hex digits, including numeric-only RGB
    # (e.g. 137333). Those must not be interpreted as ANSI palette indexes.
    if value.isdigit() and len(value) < 6:
        index = int(value)
        if index < 16:
            return "#" + PALETTE[index]
        if index < 232:
            index -= 16
            levels = [0, 95, 135, 175, 215, 255]
            return tuple(levels[n] for n in (index // 36, index // 6 % 6, index % 6))
        return (8 + (index - 232) * 10,) * 3
    return "#" + value


def render(frame, filename, font_path=None):
    fonts = ([font_path] if font_path else []) + [
        "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
        *glob.glob("/nix/store/*-dejavu-fonts-*/share/fonts/truetype/DejaVuSansMono.ttf")]
    path = next((Path(path) for path in fonts if Path(path).is_file()), None)
    if path is None:
        raise RuntimeError("DejaVuSansMono.ttf required; pass --font")
    font = ImageFont.truetype(str(path), 15)
    styles = {}
    for bold, italic, suffix in ((False, False, ""), (True, False, "-Bold"),
                                 (False, True, "-Oblique"), (True, True, "-BoldOblique")):
        variant = path.with_name(path.stem + suffix + path.suffix)
        styles[bold, italic] = ImageFont.truetype(str(variant), 15) if variant.is_file() else font
    cell_width, cell_height = round(font.getlength("M")), 20
    light = frame.get("background", "dark") == "light"
    default_fg, default_bg = ("#15202b", "#ffffff") if light else ("#e5e9f0", "#10141c")
    image = Image.new("RGB", (frame["cols"] * cell_width, frame["rows"] * cell_height), default_bg)
    draw = ImageDraw.Draw(image)
    for y, row in enumerate(frame["cells"]):
        for x, cell in enumerate(row):
            fg = color(cell["fg"], default_fg)
            bg = color(cell["bg"], default_bg)
            if cell["reverse"]:
                fg, bg = bg, fg
            left, top = x * cell_width, y * cell_height
            draw.rectangle((left, top, left + cell_width - 1, top + cell_height - 1), fill=bg)
            if cell["data"]:
                draw.text((left, top), cell["data"], font=styles[cell["bold"], cell["italics"]], fill=fg)
            if cell["underscore"]:
                draw.line((left, top + 18, left + cell_width - 1, top + 18), fill=fg)
            if cell["strikethrough"]:
                draw.line((left, top + 10, left + cell_width - 1, top + 10), fill=fg)
    cursor = frame["cursor"]
    if not cursor["hidden"]:
        left, top = min(cursor["x"], frame["cols"]-1) * cell_width, cursor["y"] * cell_height
        draw.rectangle((left, top, left + cell_width - 1, top + cell_height - 1), outline="#ffffff")
    image.save(filename)


class Observer:
    def __init__(self, command, out, cols, rows, font=None, background="dark"):
        self.out = Path(out).resolve()
        self.out.mkdir(parents=True, exist_ok=True)
        self.font = font
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        # We emulate a terminal, whose output reaches the renderer verbatim.
        # QEMU serial forwarding does not itself disable the outer PTY's
        # ONLCR translation; CR insertion would corrupt cursor-relative frames.
        attributes = termios.tcgetattr(slave)
        attributes[1] &= ~termios.OPOST
        termios.tcsetattr(slave, termios.TCSANOW, attributes)
        environment = {key: value for key, value in os.environ.items()
                       if key not in ("NO_COLOR", "TERM_PROGRAM", "TERM_PROGRAM_VERSION")}
        # start deliberately outlives its invoking shell. nix-shell --run
        # removes inherited TMPDIR on return, which can destroy QEMU store.img
        # while the detached VM still needs it. Own a stable temporary root.
        temporary = self.out / "runtime-tmp"
        temporary.mkdir(mode=0o700, exist_ok=True)
        environment["TMPDIR"] = str(temporary)
        self.process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave,
                                        env={**environment, "TERM": "xterm-256color", "COLORTERM": "truecolor"},
                                        start_new_session=True)
        os.close(slave)
        os.set_blocking(self.master, False)
        self.screen = TerminalScreen(cols, rows, self.send, background)
        self.terminal = TerminalStream(self.screen)
        self.sequence = 0
        self.byte_count = 0
        self.command = command
        self.eof = False
        self.raw = open(self.out / "terminal.ansi", "wb")

    def send(self, data):
        os.write(self.master, data.encode() if isinstance(data, str) else data)

    def pump(self, duration=0):
        deadline = time.monotonic() + duration
        while not self.eof:
            # QEMU re-enables OPOST when initializing its serial console. Keep
            # the outer emulator transport byte-preserving throughout its life.
            attributes = termios.tcgetattr(self.master)
            if attributes[1] & termios.OPOST:
                attributes[1] &= ~termios.OPOST
                termios.tcsetattr(self.master, termios.TCSANOW, attributes)
            if select.select([self.master], [], [], max(0, min(.02, deadline - time.monotonic())))[0]:
                try:
                    data = os.read(self.master, 65536)
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise
                    data = b""
                if not data:
                    self.eof = True
                    break
                self.byte_count += len(data)
                self.raw.write(data)
                self.raw.flush()
                self.terminal.feed(data)
            if time.monotonic() >= deadline:
                break

    def capture(self, request):
        self.sequence += 1
        label = "".join(c if c.isalnum() or c in "-_" else "-" for c in request.get("label", request["op"]))[:70]
        base = self.out / f"{self.sequence:04d}-{label}"
        frame = {"cols": self.screen.columns, "rows": self.screen.lines,
                 "cursor": {"x": self.screen.cursor.x, "y": self.screen.cursor.y,
                            "hidden": self.screen.cursor.hidden},
                 "cells": [[self.screen.buffer[y][x]._asdict() for x in range(self.screen.columns)]
                           for y in range(self.screen.lines)],
                 "timestamp": time.time(), "bytes": self.byte_count,
                 "exit_code": self.process.poll(), "command": self.command,
                 "background": self.screen.background}
        Path(str(base) + ".json").write_text(json.dumps(frame))
        Path(str(base) + ".txt").write_text("\n".join(self.screen.display) + "\n")
        render(frame, str(base) + ".png", self.font)
        with open(self.out / "actions.ndjson", "a") as actions:
            actions.write(json.dumps({**request, "frame": str(base), "timestamp": frame["timestamp"],
                                      "bytes": self.byte_count}) + "\n")
        return {"frame": str(base), "exit_code": self.process.poll(),
                "screen": "\n".join(self.screen.display)}

    def handle(self, request):
        op = request["op"]
        if op == "send":
            self.send(bytes.fromhex(request["hex"]) if request.get("hex") else request.get("text", ""))
        elif op == "resize":
            cols, rows = request["cols"], request["rows"]
            if not 1 <= cols <= 500 or not 1 <= rows <= 200:
                raise ValueError("invalid terminal dimensions")
            self.screen.resize(lines=rows, columns=cols)
            fcntl.ioctl(self.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
            if self.process.poll() is None:
                os.killpg(self.process.pid, signal.SIGWINCH)
        elif op not in ("observe", "start", "close"):
            raise ValueError("unknown operation")
        self.pump(min(5, max(0, request.get("wait", .2))))
        return self.capture(request)

    def close(self):
        if self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=3)
        self.raw.close()
        os.close(self.master)


def request(path, payload):
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(15)
        client.connect(path)
        client.sendall(json.dumps(payload).encode() + b"\n")
        chunks = bytearray()
        while not chunks.endswith(b"\n"):
            block = client.recv(65536)
            if not block:
                raise RuntimeError("observer closed before responding")
            chunks.extend(block)
    result = json.loads(chunks)
    if "error" in result:
        raise RuntimeError(result["error"])
    return result


def serve(args):
    observer = Observer(args.command, args.out, args.cols, args.rows, args.font, args.background)
    with socket.socket(socket.AF_UNIX) as server:
        server.bind(args.socket)
        os.chmod(args.socket, 0o600)
        server.listen(4)
        server.settimeout(.02)
        try:
            while True:
                observer.pump()
                try:
                    client, _ = server.accept()
                except socket.timeout:
                    continue
                with client:
                    client.setblocking(False)
                    data = bytearray()
                    deadline = time.monotonic() + 1
                    while not data.endswith(b"\n"):
                        observer.pump()
                        if time.monotonic() > deadline:
                            break
                        if not select.select([client], [], [], .02)[0]:
                            continue
                        block = client.recv(65536)
                        if not block:
                            break
                        data.extend(block)
                        if len(data) > 1_000_000:
                            break
                    if not data.endswith(b"\n") or len(data) > 1_000_000:
                        continue
                    client.settimeout(1)
                    try:
                        payload = json.loads(data)
                    except ValueError:
                        continue
                    if not isinstance(payload, dict):
                        continue
                    try:
                        result = observer.handle(payload)
                    except Exception as error:
                        result = {"error": str(error)}
                    try:
                        client.sendall(json.dumps(result).encode() + b"\n")
                    except (BrokenPipeError, ConnectionResetError):
                        pass  # A dropped observer client must not end its PTY.
                    if payload.get("op") == "close":
                        break
        finally:
            observer.close()
            Path(args.socket).unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--socket", required=True)
    sub = parser.add_subparsers(dest="op", required=True)
    for op in ("start", "serve", "observe", "send", "resize", "close"):
        child = sub.add_parser(op)
        child.add_argument("--label", default=op)
        child.add_argument("--wait", type=float, default=.2)
        if op in ("start", "serve"):
            child.add_argument("--out", required=True)
            child.add_argument("--font")
            child.add_argument("--background", choices=("dark", "light"), default="dark")
            child.add_argument("--cols", type=int, default=120)
            child.add_argument("--rows", type=int, default=35)
            child.add_argument("command", nargs=argparse.REMAINDER)
        if op == "send":
            choice = child.add_mutually_exclusive_group(required=True)
            choice.add_argument("--text")
            choice.add_argument("--hex")
        if op == "resize":
            child.add_argument("--cols", required=True, type=int)
            child.add_argument("--rows", required=True, type=int)
    args = parser.parse_args()
    if args.op in ("start", "serve"):
        if args.command[:1] == ["--"]:
            args.command = args.command[1:]
        if not args.command:
            parser.error("a command is required")
    if args.op == "serve":
        serve(args)
        return
    if args.op == "start":
        if Path(args.socket).exists():
            parser.error("socket already exists; close its observer first")
        Path(args.out).mkdir(parents=True, exist_ok=True)
        command = [sys.executable, __file__, "--socket", args.socket, "serve", "--out", args.out,
                   "--cols", str(args.cols), "--rows", str(args.rows), "--background", args.background]
        if args.font:
            command += ["--font", args.font]
        with open(Path(args.out) / "observer.log", "ab") as log:
            server = subprocess.Popen(command + ["--", *args.command], stdin=subprocess.DEVNULL,
                                      stdout=log, stderr=log, start_new_session=True)
        deadline = time.monotonic() + 10
        while not Path(args.socket).exists():
            if server.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("observer startup failed; inspect observer.log")
            time.sleep(.02)
    payload = {k: v for k, v in vars(args).items() if k not in ("socket", "command", "out", "font")}
    print(json.dumps(request(args.socket, payload)))


if __name__ == "__main__":
    main()
