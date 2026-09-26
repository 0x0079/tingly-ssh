#!/usr/bin/env python3
"""Record a command running in a pseudo-terminal as an asciicast v2 file.

A tiny stand-in for `asciinema rec` with no dependencies and no need for a
controlling terminal, so the demo can be recorded headless (CI, a container).

    cast.py OUT.cast COLS ROWS -- COMMAND [ARGS...]
"""
import fcntl, json, os, pty, select, struct, sys, termios, time

def main():
    out, cols, rows = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
    cmd = sys.argv[sys.argv.index("--") + 1:]
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.execvp(cmd[0], cmd)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    start = time.monotonic()
    dec = __import__("codecs").getincrementaldecoder("utf-8")("replace")
    with open(out, "w") as f:
        header = {"version": 2, "width": cols, "height": rows,
                  "timestamp": int(time.time()), "env": {"TERM": "xterm-256color"}}
        f.write(json.dumps(header) + "\n")
        while True:
            r, _, _ = select.select([fd], [], [], 0.5)
            if not r:
                if os.waitpid(pid, os.WNOHANG)[0]:
                    break
                continue
            try:
                data = os.read(fd, 65536)
            except OSError:
                break
            if not data:
                break
            text = dec.decode(data)
            if text:
                f.write(json.dumps([round(time.monotonic() - start, 4), "o", text]) + "\n")
                f.flush()

if __name__ == "__main__":
    main()
