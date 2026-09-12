#!/usr/bin/env python3
"""Drive an interactive program on a real pty and assert what it prints.

The scenario suite uses this for everything that needs a terminal: an ssh
session with a tty, Ctrl-C delivery, window resizes. It is deliberately small
and dependency free, and the step language is meant to be readable in a diff.

Usage:
    ptydrive.py --script FILE [--transcript FILE] [--timeout SEC] -- CMD [ARGS...]

Steps (one per line, '#' comments and blank lines ignored):
    expect   REGEX        wait until REGEX appears in the output
    send     TEXT         write TEXT followed by CR
    sendraw  HEX          write raw bytes, e.g. sendraw 03 for Ctrl-C
    ctrl     LETTER       write a control character, e.g. ctrl C
    resize   ROWS COLS    change the window size (sends SIGWINCH)
    sleep    SECONDS
    touch    PATH         create PATH, to hand control to the calling script
    waitfile PATH [SECS]  block until PATH exists
    expect_exit CODE      wait for the child and check its exit status

Exit status is 0 only if every step succeeded.
"""

import argparse
import errno
import os
import pty
import re
import select
import shlex
import signal
import struct
import sys
import termios
import time
import fcntl


class Failure(Exception):
    pass


class Session:
    def __init__(self, argv, timeout, transcript):
        self.timeout = timeout
        self.buf = ""
        self.transcript = open(transcript, "w", encoding="utf-8", errors="replace") if transcript else None
        self.status = None
        self.pid, self.fd = pty.fork()
        if self.pid == 0:  # child
            try:
                os.execvp(argv[0], argv)
            finally:
                os._exit(127)
        self.resize(24, 80)

    def log(self, text):
        if self.transcript:
            self.transcript.write(text)
            self.transcript.flush()

    def resize(self, rows, cols):
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def write(self, data: bytes):
        self.log("\n[sent %r]\n" % data)
        os.write(self.fd, data)

    def _read_some(self, deadline):
        remaining = deadline - time.time()
        if remaining <= 0:
            return False
        r, _, _ = select.select([self.fd], [], [], min(remaining, 0.5))
        if not r:
            return True
        try:
            chunk = os.read(self.fd, 65536)
        except OSError as e:
            if e.errno in (errno.EIO, errno.EBADF):  # child closed the pty
                return False
            raise
        if not chunk:
            return False
        text = chunk.decode("utf-8", "replace")
        self.buf += text
        self.log(text)
        return True

    def expect(self, pattern):
        rx = re.compile(pattern)
        deadline = time.time() + self.timeout
        while True:
            m = rx.search(self.buf)
            if m:
                self.buf = self.buf[m.end():]
                return
            if not self._read_some(deadline):
                if rx.search(self.buf):
                    self.buf = ""
                    return
                raise Failure("timeout waiting for %r; last output: %r"
                              % (pattern, self.buf[-400:]))

    def wait_exit(self, want):
        deadline = time.time() + self.timeout
        while time.time() < deadline:
            if not self._read_some(deadline):
                break
        try:
            _, status = os.waitpid(self.pid, 0)
        except ChildProcessError:
            raise Failure("child already reaped")
        code = os.waitstatus_to_exitcode(status)
        self.status = code
        if code != want:
            raise Failure("exit status %s, want %s" % (code, want))

    def close(self):
        try:
            os.close(self.fd)
        except OSError:
            pass
        try:
            os.kill(self.pid, signal.SIGKILL)
            os.waitpid(self.pid, 0)
        except (ProcessLookupError, ChildProcessError):
            pass
        if self.transcript:
            self.transcript.close()


def run_steps(session, steps):
    for lineno, raw in steps:
        parts = shlex.split(raw)
        op, args = parts[0], parts[1:]
        try:
            if op == "expect":
                session.expect(args[0])
            elif op in ("send", "sendline"):
                session.write(((args[0] if args else "") + "\r").encode())
            elif op == "sendraw":
                session.write(bytes(int(x, 16) for x in args))
            elif op == "ctrl":
                session.write(bytes([ord(args[0].upper()) - 64]))
            elif op == "resize":
                session.resize(int(args[0]), int(args[1]))
            elif op == "sleep":
                time.sleep(float(args[0]))
            elif op == "touch":
                open(args[0], "w").close()
            elif op == "waitfile":
                limit = time.time() + (float(args[1]) if len(args) > 1 else session.timeout)
                while not os.path.exists(args[0]):
                    if time.time() > limit:
                        raise Failure("timeout waiting for file %s" % args[0])
                    time.sleep(0.05)
            elif op == "expect_exit":
                session.wait_exit(int(args[0]))
            else:
                raise Failure("unknown step %r" % op)
        except Failure as e:
            raise Failure("line %d (%s): %s" % (lineno, raw.strip(), e))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--script", required=True)
    ap.add_argument("--transcript")
    ap.add_argument("--timeout", type=float, default=30.0)
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    args = ap.parse_args()

    argv = args.cmd[1:] if args.cmd and args.cmd[0] == "--" else args.cmd
    if not argv:
        print("ptydrive: no command given", file=sys.stderr)
        return 2

    steps = []
    with open(args.script, encoding="utf-8") as fh:
        for i, line in enumerate(fh, 1):
            line = line.strip()
            if line and not line.startswith("#"):
                steps.append((i, line))

    session = Session(argv, args.timeout, args.transcript)
    try:
        run_steps(session, steps)
    except Failure as e:
        print("ptydrive: %s" % e, file=sys.stderr)
        return 1
    finally:
        session.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
