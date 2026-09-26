#!/usr/bin/env python3
"""Print what the terminal showed at given moments of an asciicast recording.

Lets you review a recording as text, without a player or a browser:

    pip install pyte
    demo/snapshot.py demo/out/demo.cast 12 25 45    # screens at t=12s, 25s, 45s, then the end
"""
import json
import sys

import pyte


def main():
    path, marks = sys.argv[1], sorted(float(x) for x in sys.argv[2:])
    lines = open(path).read().splitlines()
    header = json.loads(lines[0])
    screen = pyte.Screen(header["width"], header["height"])
    stream = pyte.Stream(screen)

    def dump(label):
        print(f"--- {label}")
        print("\n".join(row.rstrip() for row in screen.display))

    for line in lines[1:]:
        t, kind, data = json.loads(line)
        while marks and t > marks[0]:
            dump(f"t={marks.pop(0)}")
        if kind == "o":
            stream.feed(data)
    dump("end")


if __name__ == "__main__":
    main()
