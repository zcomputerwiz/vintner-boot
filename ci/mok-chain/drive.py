#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Runs one QEMU boot with the serial console on a pipe and types into it.

usage: drive.py LOG TIMEOUT [--step PATTERN KEYS]... -- QEMU-COMMAND...

Each step waits until PATTERN appears in the serial output after the point where
the previous step matched, then sends KEYS. In KEYS, <DN> is the down arrow, <CR>
is Return and <WAIT> pauses one second. The run ends when QEMU exits, or after
TIMEOUT seconds (QEMU is then killed). The exit status is 0 if every step
matched, 3 if not; the outcome of the boot is read from LOG by the caller.
"""
import os
import select
import subprocess
import sys
import time


def keys(spec):
    out = []
    for part in spec.replace("<", "\0<").replace(">", ">\0").split("\0"):
        if part == "<DN>":
            out.append(b"\x1b[B")
        elif part == "<CR>":
            out.append(b"\r")
        elif part == "<WAIT>":
            out.append(None)
        elif part:
            out.append(part.encode())
    return out


def main():
    log_path, timeout = sys.argv[1], float(sys.argv[2])
    args = sys.argv[3:]
    steps = []
    while args and args[0] == "--step":
        steps.append((args[1].encode(), keys(args[2])))
        args = args[3:]
    if not args or args[0] != "--":
        sys.exit(__doc__)
    proc = subprocess.Popen(args[1:], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    fd = proc.stdout.fileno()
    buf, pos, deadline = b"", 0, time.monotonic() + timeout
    with open(log_path, "wb") as log:
        while time.monotonic() < deadline:
            ready, _, _ = select.select([fd], [], [], 0.2)
            if ready:
                chunk = os.read(fd, 65536)
                if not chunk:
                    break
                buf += chunk
                log.write(chunk)
                log.flush()
            while steps:
                at = buf.find(steps[0][0], pos)
                if at < 0:
                    break
                pos = at + len(steps[0][0])
                # The menu is drawn after its title: let it finish before typing.
                time.sleep(1.5)
                for k in steps[0][1]:
                    if k is None:
                        time.sleep(1)
                        continue
                    try:
                        proc.stdin.write(k)
                        proc.stdin.flush()
                    except BrokenPipeError:
                        break
                    time.sleep(0.4)
                steps.pop(0)
            if proc.poll() is not None and not ready:
                break
    if proc.poll() is None:
        proc.kill()
    proc.wait()
    for pattern, _ in steps:
        print(f"drive.py: never saw {pattern.decode()!r}", file=sys.stderr)
    sys.exit(3 if steps else 0)


if __name__ == "__main__":
    main()
