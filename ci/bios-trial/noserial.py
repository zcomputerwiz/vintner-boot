#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Boots a BIOS test disk and checks that the screen stays empty.

WO-0010 1.4 and R10: from power-on to the boot splash the screen shows nothing.
GRUB clears its "Welcome to GRUB!" line and, on a machine without a serial
port, the error line of the failed "serial" command; with a serial port it
writes its "vintner: trying" lines there only. The guest's own output goes to
the serial port, so the only evidence is the VGA text screen: this takes
screenshots while the VM runs, and counts the text lines of the last one (the
cursor bar is not a line). Any line is a failure.

Usage: noserial.py DISK.img [serial]  (exit 0 if the screen has no text line)
With "serial" the VM has a serial port (to /dev/null), as QEMU has by default.
"""
import json, os, socket, subprocess, sys, tempfile, time

disk = sys.argv[1]
serial = ["-serial", "null"] if sys.argv[2:] == ["serial"] else ["-serial", "none"]
accel = "kvm" if os.access("/dev/kvm", os.W_OK) else "tcg"
tmp = tempfile.mkdtemp()
sock_path = os.path.join(tmp, "qmp.sock")
proc = subprocess.Popen(
    ["qemu-system-x86_64", "-machine", f"q35,accel={accel}", "-m", "512", "-nodefaults",
     "-vga", "std", "-display", "none", *serial, "-no-reboot",
     "-drive", f"file={disk},format=raw,if=virtio",
     "-qmp", f"unix:{sock_path},server,nowait"],
    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    for _ in range(100):
        if os.path.exists(sock_path):
            break
        time.sleep(0.1)
    s = socket.socket(socket.AF_UNIX)
    s.connect(sock_path)
    r = s.makefile("r")
    r.readline()

    def cmd(c):
        s.sendall((json.dumps(c) + "\n").encode())
        while True:
            line = json.loads(r.readline())
            if "return" in line or "error" in line:
                return line

    cmd({"execute": "qmp_capabilities"})
    last = None
    start = time.time()
    # Frames from 0.7 s (GRUB has cleared the SeaBIOS banner) until the guest is gone.
    time.sleep(0.7)
    while time.time() - start < 4.0:
        shot = os.path.join(tmp, "s.ppm")
        try:
            res = cmd({"execute": "screendump", "arguments": {"filename": shot, "format": "ppm"}})
        except (BrokenPipeError, ConnectionResetError, ValueError):
            break
        if "error" in res:
            break
        with open(shot, "rb") as f:
            last = f.read()
        time.sleep(0.2)
finally:
    proc.kill()
    proc.wait()

if last is None:
    print("noserial: no screenshot")
    sys.exit(2)
head, rest = last.split(b"\n", 1)
dims, rest = rest.split(b"\n", 1)
w, h = map(int, dims.split())
_, px = rest.split(b"\n", 1)
lines = 0
for y0 in range(0, h, 16):  # VGA text cells are 16 pixels high
    # A text line has lit pixels on several pixel rows; the cursor is one thin bar.
    lit = sum(1 for y in range(y0, min(y0 + 16, h)) if any(px[y * w * 3:(y + 1) * w * 3]))
    if lit >= 4:
        lines += 1
print(f"noserial: {lines} text line(s) on the screen")
sys.exit(0 if lines == 0 else 1)
