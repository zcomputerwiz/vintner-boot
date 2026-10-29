#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
# inspector.py - evidence extraction, screendump analysis, and gate verification for WO-0008a

import argparse
import hashlib
import json
import os
import re
import socket
import struct
import sys
import time
import zlib
from pathlib import Path

# --- Image helpers (zero external dependencies) ------------------------------

def parse_ppm(path):
    """Parses a P6 binary PPM image file into (width, height, bytes)."""
    with open(path, "rb") as f:
        magic = f.readline().strip()
        if magic != b"P6":
            raise ValueError(f"Expected P6 PPM format, got {magic}")
        # Skip comment lines
        header_tokens = []
        while len(header_tokens) < 3:
            line = f.readline()
            if not line:
                break
            line = line.strip()
            if line.startswith(b"#") or not line:
                continue
            header_tokens.extend(line.split())
        if len(header_tokens) < 3:
            raise ValueError("Incomplete PPM header")
        width = int(header_tokens[0])
        height = int(header_tokens[1])
        maxval = int(header_tokens[2])
        if maxval != 255:
            raise ValueError(f"Unsupported PPM maxval {maxval}")
        data = f.read()
        expected_len = width * height * 3
        if len(data) < expected_len:
            raise ValueError(f"PPM data truncated: got {len(data)}, expected {expected_len}")
        return width, height, data[:expected_len]

def parse_png_rgba(path):
    """Parses an 8-bit RGBA or RGB PNG into raw RGBA bytes of length width * height * 4."""
    with open(path, "rb") as f:
        sig = f.read(8)
        if sig != b"\x89PNG\r\n\x1a\n":
            raise ValueError("Invalid PNG signature")
        chunks = []
        idat_parts = []
        width, height = 0, 0
        bit_depth, color_type = 0, 0
        while True:
            chunk_len_b = f.read(4)
            if not chunk_len_b:
                break
            chunk_len = struct.unpack(">I", chunk_len_b)[0]
            chunk_type = f.read(4)
            chunk_data = f.read(chunk_len)
            f.read(4)  # CRC
            if chunk_type == b"IHDR":
                width, height, bit_depth, color_type = struct.unpack(">IIBB", chunk_data[:10])
            elif chunk_type == b"IDAT":
                idat_parts.append(chunk_data)
            elif chunk_type == b"IEND":
                break

    decompressed = zlib.decompress(b"".join(idat_parts))
    # Decode scanlines
    bytes_per_pixel = 4 if color_type == 6 else 3
    stride = width * bytes_per_pixel
    rgba_out = bytearray(width * height * 4)
    raw_idx = 0
    prev_line = bytearray(stride)

    for y in range(height):
        filter_type = decompressed[raw_idx]
        raw_idx += 1
        line = bytearray(decompressed[raw_idx : raw_idx + stride])
        raw_idx += stride

        # Basic PNG unfiltering
        if filter_type == 0:  # None
            pass
        elif filter_type == 1:  # Sub
            for i in range(bytes_per_pixel, stride):
                line[i] = (line[i] + line[i - bytes_per_pixel]) & 0xFF
        elif filter_type == 2:  # Up
            for i in range(stride):
                line[i] = (line[i] + prev_line[i]) & 0xFF
        elif filter_type == 3:  # Average
            for i in range(stride):
                left = line[i - bytes_per_pixel] if i >= bytes_per_pixel else 0
                up = prev_line[i]
                line[i] = (line[i] + ((left + up) // 2)) & 0xFF
        elif filter_type == 4:  # Paeth
            for i in range(stride):
                a = line[i - bytes_per_pixel] if i >= bytes_per_pixel else 0
                b = prev_line[i]
                c = prev_line[i - bytes_per_pixel] if i >= bytes_per_pixel else 0
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                pr = a if pa <= pb and pa <= pc else (b if pb <= pc else c)
                line[i] = (line[i] + pr) & 0xFF
        else:
            raise ValueError(f"Unsupported PNG filter type {filter_type}")

        prev_line = line
        # Convert line to RGBA
        out_pos = y * width * 4
        if color_type == 6:  # RGBA
            rgba_out[out_pos : out_pos + stride] = line
        elif color_type == 2:  # RGB -> RGBA
            for x in range(width):
                r, g, b = line[x * 3 : x * 3 + 3]
                rgba_out[out_pos + x * 4 : out_pos + x * 4 + 4] = bytes([r, g, b, 255])
        else:
            raise ValueError(f"Unsupported PNG color type {color_type}")

    return width, height, bytes(rgba_out)

def create_solid_red_png(path, width=256, height=256):
    """Creates a 256x256 solid red RGBA PNG file for the G7 control run."""
    raw_scanlines = []
    line_bytes = b"\x00" + (b"\xff\x00\x00\xff" * width)
    for _ in range(height):
        raw_scanlines.append(line_bytes)
    compressed = zlib.compress(b"".join(raw_scanlines))

    def make_chunk(chunk_type, data):
        length = struct.pack(">I", len(data))
        crc = struct.pack(">I", zlib.crc32(chunk_type + data) & 0xFFFFFFFF)
        return length + chunk_type + data + crc

    ihdr_data = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    png_bytes = (
        b"\x89PNG\r\n\x1a\n"
        + make_chunk(b"IHDR", ihdr_data)
        + make_chunk(b"IDAT", compressed)
        + make_chunk(b"IEND", b"")
    )
    with open(path, "wb") as f:
        f.write(png_bytes)

def create_splash_bmp(path, width=256, height=256):
    """Creates a 24-bit uncompressed BMP splash image for UEFI BGRT testing."""
    row_bytes = width * 3
    padding = (4 - (row_bytes % 4)) % 4
    padded_row_size = row_bytes + padding
    pixel_array_size = padded_row_size * height
    file_size = 54 + pixel_array_size

    # BMP Header
    bmp_header = struct.pack(
        "<2sIHHI",
        b"BM",
        file_size,
        0, 0,
        54
    )
    # DIB Header (BITMAPINFOHEADER)
    dib_header = struct.pack(
        "<IIIHHIIIIII",
        40,
        width,
        height,
        1,
        24,
        0,
        pixel_array_size,
        2835, 2835,  # 72 DPI
        0, 0
    )
    # Cyan/blue logo pattern
    pixel_data = bytearray()
    for _ in range(height):
        row = (b"\x80\x40\x10" * width) + (b"\x00" * padding)
        pixel_data.extend(row)

    with open(path, "wb") as f:
        f.write(bmp_header + dib_header + pixel_data)

# --- QMP Screendump Capture --------------------------------------------------

def qmp_command(sock, cmd):
    payload = json.dumps(cmd).encode("utf-8") + b"\r\n"
    sock.sendall(payload)
    buf = b""
    while b"\r\n" not in buf:
        chunk = sock.recv(4096)
        if not chunk:
            break
        buf += chunk
    for line in buf.decode("utf-8", errors="replace").splitlines():
        line = line.strip()
        if line:
            try:
                res = json.loads(line)
                if "return" in res or "error" in res:
                    return res
            except json.JSONDecodeError:
                pass
    return {}

def capture_screendump(qmp_sock_path, out_ppm_path, timeout_secs=60, ready_log=None):
    """Connects to QEMU QMP socket and captures non-blank screendump."""
    start_time = time.time()
    sock = None

    # If ready_log is specified, wait for VINTNER-TEST-SPLASH-START before capturing
    if ready_log:
        print(f"Waiting for VINTNER-TEST-SPLASH-START in {ready_log}...")
        while time.time() - start_time < timeout_secs:
            if os.path.exists(ready_log):
                try:
                    with open(ready_log, "r", errors="ignore") as f:
                        if "=== VINTNER-TEST-SPLASH-START ===" in f.read():
                            print("Seen VINTNER-TEST-SPLASH-START marker!")
                            break
                except Exception:
                    pass
            time.sleep(0.5)

    # Wait for QMP socket to appear
    while time.time() - start_time < timeout_secs:
        if os.path.exists(qmp_sock_path):
            try:
                s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                s.connect(qmp_sock_path)
                # Receive greeting banner
                s.recv(4096)
                # Handshake
                res = qmp_command(s, {"execute": "qmp_capabilities"})
                if "return" in res:
                    sock = s
                    break
                s.close()
            except Exception:
                time.sleep(0.5)
        else:
            time.sleep(0.5)

    if not sock:
        print(f"Failed to connect to QMP socket {qmp_sock_path} within {timeout_secs}s", file=sys.stderr)
        return False

    print(f"Connected to QMP at {qmp_sock_path}. Polling screendump...")
    captured = False
    attempt = 0
    while time.time() - start_time < timeout_secs:
        attempt += 1
        tmp_ppm = f"{out_ppm_path}.tmp{attempt}.ppm"
        res = qmp_command(sock, {"execute": "screendump", "arguments": {"filename": tmp_ppm}})
        if "return" in res and os.path.isfile(tmp_ppm):
            try:
                w, h, data = parse_ppm(tmp_ppm)
                total_pixels = w * h
                non_black = sum(1 for i in range(0, len(data), 3) if data[i] != 0 or data[i+1] != 0 or data[i+2] != 0)
                fraction = non_black / total_pixels
                print(f"[Dump #{attempt}] {w}x{h}, non-black pixels: {non_black}/{total_pixels} ({fraction*100:.2f}%)")
                # Reject QEMU uninitialized display screen or standard VGA text mode
                if (w == 640 and h == 480 and non_black == 1044) or (w == 720 and h == 400):
                    print(f"[Dump #{attempt}] Ignoring uninitialized/text-mode screen placeholder")
                    os.remove(tmp_ppm)
                    time.sleep(1.0)
                    continue
                if fraction >= 0.001:  # >= 0.1% non-black
                    os.replace(tmp_ppm, out_ppm_path)
                    captured = True
                    break
                else:
                    os.remove(tmp_ppm)
            except Exception as e:
                print(f"Warning reading PPM: {e}", file=sys.stderr)
        time.sleep(1.0)

    try:
        sock.close()
    except Exception:
        pass

    return captured

# --- G6 & G7 Pixel Evaluation ------------------------------------------------

def compare_logo_area(ppm_path, ref_rgba, ref_w=256, ref_h=256):
    """
    Compares the logo region on screen (centered horizontally, center at 38.2% height)
    with ref_rgba alpha-blended over black.
    Returns (mad, (x0, y0, ref_w, ref_h), non_black_logo_count).
    """
    screen_w, screen_h, screen_data = parse_ppm(ppm_path)
    cx = screen_w / 2.0
    cy = screen_h * 0.382
    x0 = int(round(cx - (ref_w / 2.0)))
    y0 = int(round(cy - (ref_h / 2.0)))

    if x0 < 0 or y0 < 0 or (x0 + ref_w) > screen_w or (y0 + ref_h) > screen_h:
        raise ValueError(f"Logo box [{x0}:{x0+ref_w}, {y0}:{y0+ref_h}] outside screen {screen_w}x{screen_h}")

    total_abs_diff = 0
    total_channels = ref_w * ref_h * 3
    non_black_logo = 0

    for y in range(ref_h):
        screen_y = y0 + y
        screen_row_start = screen_y * screen_w * 3
        logo_row_start = y * ref_w * 4

        for x in range(ref_w):
            screen_x = x0 + x
            sp_idx = screen_row_start + (screen_x * 3)
            sr = screen_data[sp_idx]
            sg = screen_data[sp_idx + 1]
            sb = screen_data[sp_idx + 2]

            if sr != 0 or sg != 0 or sb != 0:
                non_black_logo += 1

            lp_idx = logo_row_start + (x * 4)
            lr = ref_rgba[lp_idx]
            lg = ref_rgba[lp_idx + 1]
            lb = ref_rgba[lp_idx + 2]
            la = ref_rgba[lp_idx + 3]

            alpha = la / 255.0
            ref_r = int(round(lr * alpha))
            ref_g = int(round(lg * alpha))
            ref_b = int(round(lb * alpha))

            diff_r = abs(sr - ref_r)
            diff_g = abs(sg - ref_g)
            diff_b = abs(sb - ref_b)
            total_abs_diff += (diff_r + diff_g + diff_b)

    mad = total_abs_diff / total_channels
    return mad, (x0, y0, ref_w, ref_h), non_black_logo

def clean_ansi(text):
    return re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", text).replace("\r", "")

def get_booted(log_path, firmware):
    if not os.path.isfile(log_path):
        return "none"
    text = clean_ansi(open(log_path, errors="replace").read())
    if firmware == "uefi":
        m = re.search(r"^LOADER_ENTRY_SELECTED:[ \t]*(.+)$", text, re.M)
        if m:
            entry = m.group(1).strip().replace("\\", "/").split("/")[-1]
            m_gen = re.fullmatch(r"vintner-os-(\d+)(\+\d+(-\d+)?)?\.efi", entry)
            if m_gen:
                return f"os-{m_gen.group(1)}"
            if "recovery-a" in entry:
                return "recovery-a"
            if "recovery-b" in entry:
                return "recovery-b"
            return entry or "none"
        m_var = re.search(r"LoaderBootCountPath[^\n]*\\([^\\]+\.efi)", text)
        if m_var:
            entry = m_var.group(1)
            m_gen = re.fullmatch(r"vintner-os-(\d+)(\+\d+(-\d+)?)?\.efi", entry)
            if m_gen:
                return f"os-{m_gen.group(1)}"
        return "none"
    else:
        tries = re.findall(r"vintner:\s*trying\s*([a-zA-Z0-9\-_]+)", text)
        if tries:
            return tries[-1]
        return "none"

def parse_readme_assets(readme_path):
    assets = []
    if not os.path.isfile(readme_path):
        return assets
    in_table = False
    with open(readme_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if "## Theme Asset Dimensions Table" in line:
                in_table = True
                continue
            if in_table:
                if line.startswith("## ") or line.startswith("Total active"):
                    break
                m = re.match(r"^\|\s*`([^`]+)`(?:\s*\.\.\.\s*`([^`]+)`)?\s*\|\s*(\d+x\d+)\s*\|\s*(\d+)\s*\|", line)
                if m:
                    first_file, last_file = m.group(1), m.group(2)
                    if last_file:
                        m_pfx = re.match(r"^(.*?)(\d+)(\.png)$", first_file)
                        m_sfx = re.match(r"^(.*?)(\d+)(\.png)$", last_file)
                        if m_pfx and m_sfx:
                            pfx, start_idx, ext = m_pfx.group(1), int(m_pfx.group(2)), m_pfx.group(3)
                            _, end_idx, _ = m_sfx.group(1), int(m_sfx.group(2)), m_sfx.group(3)
                            pad = len(m_pfx.group(2))
                            for idx in range(start_idx, end_idx + 1):
                                assets.append(f"{pfx}{idx:0{pad}d}{ext}")
                    else:
                        assets.append(first_file)
    return sorted(assets)

# --- Evaluation command -------------------------------------------------------

def cmd_evaluate(args):
    fw = args.firmware
    out_dir = Path(args.out_dir)
    results_file = Path(args.results_file)

    prep_log_file = out_dir / "prep.log"
    ctrl_prep_log_file = out_dir / "ctrl-prep.log"
    boot_log_file = out_dir / "splash-boot.log"
    ctrl_boot_log_file = out_dir / "ctrl-boot.log"

    ppm_file = out_dir / "screendump-active.ppm"
    ctrl_ppm_file = out_dir / "screendump-control.ppm"
    ref_png_file = Path("graphics/plymouth-theme/bgrt-fallback.png")

    rows = []

    # Read serial log content
    boot_log = ""
    if boot_log_file.is_file():
        boot_log = clean_ansi(boot_log_file.read_text(encoding="utf-8", errors="replace"))

    # Extract guest report between VINTNER-GFX-BEGIN and VINTNER-GFX-END
    gfx_evidence = {}
    in_gfx = False
    cur_multiline_key = None
    cur_multiline_val = []

    for line in boot_log.splitlines():
        line_clean = line.strip()
        if "VINTNER-GFX-BEGIN" in line_clean:
            in_gfx = True
            continue
        if "VINTNER-GFX-END" in line_clean:
            in_gfx = False
            if cur_multiline_key is not None:
                gfx_evidence[cur_multiline_key] = "\n".join(cur_multiline_val)
                cur_multiline_key = None
                cur_multiline_val = []
            continue
        if in_gfx:
            if cur_multiline_key is not None:
                if line_clean == "VINTNER_EOF":
                    gfx_evidence[cur_multiline_key] = "\n".join(cur_multiline_val)
                    cur_multiline_key = None
                    cur_multiline_val = []
                else:
                    cur_multiline_val.append(line)
            elif "<<" in line:
                k, _, _ = line.partition("<<")
                cur_multiline_key = k.strip()
                cur_multiline_val = []
            elif "=" in line:
                k, _, v = line.partition("=")
                gfx_evidence[k.strip()] = v.strip()

    # --- G0: Prep exit status and boot generation sequencing ------------------
    prep_exit_ok = False
    if prep_log_file.is_file():
        prep_txt = clean_ansi(prep_log_file.read_text(encoding="utf-8", errors="replace"))
        if re.search(r"=== OP EXIT: 0 ===", prep_txt):
            prep_exit_ok = True

    ctrl_prep_exit_ok = False
    if ctrl_prep_log_file.is_file():
        ctrl_txt = clean_ansi(ctrl_prep_log_file.read_text(encoding="utf-8", errors="replace"))
        if re.search(r"=== OP EXIT: 0 ===", ctrl_txt):
            ctrl_prep_exit_ok = True

    splash_booted = get_booted(str(boot_log_file), fw)
    ctrl_booted = get_booted(str(ctrl_boot_log_file), fw)

    g0_pass = (prep_exit_ok and ctrl_prep_exit_ok and splash_booted == "os-2" and ctrl_booted == "os-3")
    if g0_pass:
        rows.append(("G0", fw, "PASS", f"prep exit 0, splash booted os-2; ctrl-prep exit 0, control booted os-3"))
    else:
        errs = []
        if not prep_exit_ok: errs.append("prep exit != 0")
        if splash_booted != "os-2": errs.append(f"splash booted {splash_booted} (want os-2)")
        if not ctrl_prep_exit_ok: errs.append("ctrl-prep exit != 0")
        if ctrl_booted != "os-3": errs.append(f"control booted {ctrl_booted} (want os-3)")
        rows.append(("G0", fw, "FAIL", f"generation sequencing error: {'; '.join(errs)}"))

    # --- G1: Command line tokens ----------------------------------------------
    cmdline_val = gfx_evidence.get("CMDLINE", "")
    if not cmdline_val:
        m = re.search(r"Command line:\s*(.*)", boot_log)
        if m:
            cmdline_val = m.group(1).strip()

    tokens = cmdline_val.split()
    expected_order = ["quiet", "splash", "vt.global_cursor_default=0", "console=ttyS0,115200"]
    last_idx = -1
    g1_ok = True
    g1_err = ""
    for exp in expected_order:
        found = False
        for i, tok in enumerate(tokens):
            if tok == exp:
                if i <= last_idx:
                    g1_ok = False
                    g1_err = f"token {exp!r} at index {i} precedes previous token at {last_idx}"
                    break
                last_idx = i
                found = True
                break
        if not found:
            g1_ok = False
            g1_err = f"token {exp!r} not found as whole token in command line"
            break

    if g1_ok:
        rows.append(("G1", fw, "PASS", f"tokens in order; cmdline: {cmdline_val}"))
    else:
        rows.append(("G1", fw, "FAIL", f"cmdline check failed: {g1_err} (cmdline: {cmdline_val!r})"))

    # --- G2: Serial output with quiet -----------------------------------------
    has_gfx_markers = ("VINTNER-GFX-BEGIN" in boot_log) and ("VINTNER-GFX-END" in boot_log)
    has_vintner_report_end = ("=== VINTNER-REPORT-END ===" in boot_log)
    
    # Match kernel warning from /dev/kmsg as a whole line
    m_kmsg = re.search(r"^(\[[^\]]+\]\s*)?vintner-test-warning\s*$", boot_log, re.M)
    has_kmsg_warning = bool(m_kmsg)
    kmsg_line = m_kmsg.group(0).strip() if m_kmsg else ""

    if has_gfx_markers and has_vintner_report_end and has_kmsg_warning:
        rows.append(("G2", fw, "PASS", f"matched whole line: {kmsg_line!r}, observed '=== VINTNER-REPORT-END ===' and GFX markers"))
    else:
        missing = []
        if not has_gfx_markers: missing.append("GFX markers")
        if not has_vintner_report_end: missing.append("=== VINTNER-REPORT-END ===")
        if not has_kmsg_warning: missing.append("whole-line vintner-test-warning")
        rows.append(("G2", fw, "FAIL", f"missing serial output: {', '.join(missing)}"))

    # --- G3: lsinitramfs contents ---------------------------------------------
    initramfs_files = gfx_evidence.get("INITRAMFS_FILES", "")
    booted_initrd = gfx_evidence.get("BOOTED_INITRD", "none")
    drivers_present = gfx_evidence.get("KERNEL_DRIVERS_PRESENT", "")
    
    # Parse asset list from README
    readme_assets = parse_readme_assets("ci/graphics/README.md")
    theme_assets = [f"usr/share/plymouth/themes/vintner-bgrt/{a}" for a in readme_assets]
    theme_file = "usr/share/plymouth/themes/vintner-bgrt/vintner-bgrt.plymouth"
    required_in_initrd = [theme_file] + theme_assets

    missing_assets = [f for f in required_in_initrd if f not in initramfs_files]
    
    # Check that any graphics driver present in kernel modules is also in initramfs
    missing_drivers = []
    if drivers_present:
        for drv in [d.strip() for d in drivers_present.split(",") if d.strip()]:
            if not re.search(rf"{drv}(\.ko|\.ko\.xz|\.ko\.zst)", initramfs_files):
                missing_drivers.append(drv)

    if not missing_assets and not missing_drivers:
        rows.append(("G3", fw, "PASS", f"booted {booted_initrd} contains {len(theme_assets)}/{len(readme_assets)} theme assets; verified kernel drivers ({drivers_present or 'none'}) in initramfs"))
    else:
        errs = []
        if missing_assets: errs.append(f"missing {len(missing_assets)} assets (e.g. {missing_assets[0]})")
        if missing_drivers: errs.append(f"missing drivers: {missing_drivers}")
        rows.append(("G3", fw, "FAIL", f"initramfs verification failed: {'; '.join(errs)}"))

    # --- G4: plymouth-set-default-theme & plymouthd.conf ----------------------
    default_theme = gfx_evidence.get("DEFAULT_THEME", "")
    guest_conf_sha = gfx_evidence.get("PLYMOUTHD_CONF_SHA256", "none")
    repo_conf_path = Path("integration/plymouth/plymouthd.conf")
    host_conf_sha = hashlib.sha256(repo_conf_path.read_bytes()).hexdigest() if repo_conf_path.is_file() else "repo_missing"

    if default_theme == "vintner-bgrt" and guest_conf_sha.lower() == host_conf_sha.lower():
        rows.append(("G4", fw, "PASS", f"theme=vintner-bgrt; guest plymouthd.conf sha256 {guest_conf_sha} matches host sha256 {host_conf_sha}"))
    else:
        rows.append(("G4", fw, "FAIL", f"theme={default_theme!r}, guest_sha={guest_conf_sha}, host_sha={host_conf_sha}"))

    # --- G5: plymouthd running and debug log ----------------------------------
    plymouthd_running = gfx_evidence.get("PLYMOUTHD_RUNNING", "0") == "1"
    debug_log = gfx_evidence.get("PLYMOUTH_DEBUG_LOG", "")
    
    # Exact theme path match
    theme_pattern = r"/usr/share/plymouth/themes/+vintner-bgrt/vintner-bgrt\.plymouth"
    m_theme = re.search(rf"^.*{theme_pattern}.*$", debug_log, re.M)
    matched_log_line = m_theme.group(0).strip() if m_theme else ""

    # Real failure patterns
    fail_patterns = [
        "failed to load image",
        "could not find image",
        "could not load image",
        "error loading",
        "failed to load theme",
    ]
    detected_fails = [pat for pat in fail_patterns if pat in debug_log.lower()]

    if plymouthd_running and matched_log_line and not detected_fails:
        rows.append(("G5", fw, "PASS", f"plymouthd running; loaded theme path verified with 0 load errors; matched: {matched_log_line!r}"))
    else:
        reasons = []
        if not plymouthd_running: reasons.append("plymouthd not running")
        if not matched_log_line: reasons.append("exact theme path not logged")
        if detected_fails: reasons.append(f"load errors: {detected_fails}")
        rows.append(("G5", fw, "FAIL", f"plymouthd check failed: {'; '.join(reasons)}"))

    # --- G6: Screendump non-blank and framebuffer resolution ------------------
    fb_virtual_size = gfx_evidence.get("FB_VIRTUAL_SIZE", "none").replace(",", "x")
    if not ppm_file.is_file():
        rows.append(("G6", fw, "FAIL", "screendump missing (no dump captured)"))
        rows.append(("G7", fw, "FAIL", "missing screendump"))
    else:
        sw, sh, screen_data = parse_ppm(ppm_file)
        total_pixels = sw * sh
        non_black = sum(1 for i in range(0, len(screen_data), 3) if screen_data[i] != 0 or screen_data[i+1] != 0 or screen_data[i+2] != 0)
        pct = (non_black / total_pixels) * 100.0

        # Reject VGA text screen (720x400 or uninitialized 640x480)
        is_vga_text = (sw == 720 and sh == 400) or (sw == 640 and sh == 480 and non_black == 1044)
        matches_fb = (f"{sw}x{sh}" == fb_virtual_size) or (fb_virtual_size == "none")

        # Parse green reference logo and measure logo area
        ref_w, ref_h, ref_rgba = parse_png_rgba(ref_png_file)
        active_mad, box, logo_non_black = compare_logo_area(ppm_file, ref_rgba, ref_w, ref_h)
        logo_pct = (logo_non_black / (ref_w * ref_h)) * 100.0

        if not is_vga_text and pct >= 0.1 and matches_fb and logo_non_black > 0:
            rows.append(("G6", fw, "PASS", f"screendump {sw}x{sh} matches FB_VIRTUAL_SIZE ({fb_virtual_size}); total non-black: {pct:.2f}%, logo area non-black: {logo_pct:.2f}%"))
        else:
            errs = []
            if is_vga_text: errs.append("VGA text mode screen detected")
            if pct < 0.1: errs.append(f"blank screen ({pct:.4f}% non-black)")
            if not matches_fb: errs.append(f"dimensions {sw}x{sh} != FB_VIRTUAL_SIZE {fb_virtual_size}")
            if logo_non_black == 0: errs.append("logo area is completely blank")
            rows.append(("G6", fw, "FAIL", f"G6 failed: {'; '.join(errs)}"))

        # --- G7 & G7-control: Measure both, derive threshold from control -----
        # Solid red RGBA bytes for 256x256
        solid_red_rgba = b"\xff\x00\x00\xff" * (256 * 256)

        if not ctrl_ppm_file.is_file():
            rows.append(("G7", fw, "FAIL", "missing control screendump"))
            rows.append(("G7-control", fw, "FAIL", "missing control screendump"))
        else:
            ctrl_red_mad, _, _ = compare_logo_area(ctrl_ppm_file, solid_red_rgba, 256, 256)
            ctrl_ref_mad, _, _ = compare_logo_area(ctrl_ppm_file, ref_rgba, ref_w, ref_h)
            
            # Derive threshold: control red MAD + stated margin
            MARGIN = 5.0
            THRESHOLD = ctrl_red_mad + MARGIN

            # Evaluate G7-control
            ctrl_matches_red = (ctrl_red_mad <= 5.0)
            ctrl_exceeds_threshold = (ctrl_ref_mad > THRESHOLD)
            if ctrl_matches_red and ctrl_exceeds_threshold:
                rows.append(("G7-control", fw, "PASS", f"control red MAD={ctrl_red_mad:.2f} <= 5.00 (matches solid red); control ref MAD={ctrl_ref_mad:.2f} > threshold {THRESHOLD:.2f} (derived: red MAD {ctrl_red_mad:.2f} + margin {MARGIN:.2f})"))
            else:
                errs = []
                if not ctrl_matches_red: errs.append(f"control dump does not match solid red logo (red MAD={ctrl_red_mad:.2f} > 5.00)")
                if not ctrl_exceeds_threshold: errs.append(f"control ref MAD={ctrl_ref_mad:.2f} did not exceed threshold {THRESHOLD:.2f}")
                rows.append(("G7-control", fw, "FAIL", f"G7-control failed: {'; '.join(errs)}"))

            # Evaluate G7
            if active_mad <= THRESHOLD:
                rows.append(("G7", fw, "PASS", f"fallback logo at ({box[0]},{box[1]}) (38.2% height): active MAD={active_mad:.2f} <= threshold {THRESHOLD:.2f} (derived from control: {ctrl_red_mad:.2f} + {MARGIN:.2f})"))
            else:
                rows.append(("G7", fw, "FAIL", f"fallback logo active MAD={active_mad:.2f} exceeds threshold {THRESHOLD:.2f} (control red MAD={ctrl_red_mad:.2f} + {MARGIN:.2f})"))

    # --- G8 (BIOS only): Framebuffer ------------------------------------------
    if fw == "bios":
        fb_name = gfx_evidence.get("FB_NAME", "none")
        fb_size = gfx_evidence.get("FB_VIRTUAL_SIZE", "none")
        kconfig = gfx_evidence.get("KERNEL_CONFIG_LINES", "none")
        if fb_name and fb_name != "none":
            rows.append(("G8", fw, "PASS", f"fb0 name={fb_name}, virtual_size={fb_size}; kernel config: {kconfig}"))
        else:
            rows.append(("G8", fw, "FAIL", f"no framebuffer present: fb0 name={fb_name}; kernel config: {kconfig}"))

    # --- G9 (UEFI only): BGRT ACPI table (OBSERVED) ---------------------------
    if fw == "uefi":
        bgrt_present = gfx_evidence.get("BGRT_PRESENT", "0") == "1"
        bgrt_details = gfx_evidence.get("BGRT_DETAILS", "none")

        s6_log_file = out_dir / "uefi-bgrt-boot.log"
        s6_info = "splash run: none"
        if s6_log_file.is_file():
            s6_text = s6_log_file.read_text(encoding="utf-8", errors="replace")
            m_p = re.search(r"BGRT_PRESENT=(\d+)", s6_text)
            m_d = re.search(r"BGRT_DETAILS=(.*)", s6_text)
            if m_p:
                p_val = (m_p.group(1) == "1")
                d_val = m_d.group(1).strip() if m_d else "none"
                s6_info = f"splash run: present={p_val} ({d_val})"

        status_text = f"default: present={bgrt_present} ({bgrt_details}); {s6_info}"
        rows.append(("G9", fw, "OBSERVED", status_text))

    # Write results.tsv
    with open(results_file, "w", encoding="utf-8") as f:
        f.write("ID\tFirmware\tStatus\tEvidence\n")
        for row in rows:
            f.write(f"{row[0]}\t{row[1]}\t{row[2]}\t{row[3]}\n")

    print(f"Results written to {results_file}")

# --- Check-results final gate ------------------------------------------------

def cmd_check_results(args):
    results_file = Path(args.results_file)
    fw = args.firmware
    if not results_file.is_file():
        sys.exit(f"check-results FAIL: {results_file} not found")

    if fw == "uefi":
        expected_ids = ["G0", "G1", "G2", "G3", "G4", "G5", "G6", "G7", "G7-control", "G9"]
    else:
        expected_ids = ["G0", "G1", "G2", "G3", "G4", "G5", "G6", "G7", "G7-control", "G8"]

    actual = {}
    duplicates = []
    with open(results_file, "r", encoding="utf-8") as f:
        for line_no, line in enumerate(f, 1):
            line = line.strip()
            if not line or line.startswith("ID\t") or line.startswith("id\t"):
                continue
            parts = line.split("\t")
            if len(parts) < 3:
                sys.exit(f"check-results FAIL: line {line_no} malformed: {line}")
            ch_id, ch_fw, status = parts[0], parts[1], parts[2]
            if ch_fw != fw:
                sys.exit(f"check-results FAIL: line {line_no} firmware {ch_fw} does not match {fw}")
            if ch_id in actual:
                duplicates.append(ch_id)
            actual[ch_id] = status

    missing = [cid for cid in expected_ids if cid not in actual]
    unexpected = [cid for cid in actual if cid not in expected_ids]

    errors = []
    if duplicates:
        errors.append(f"Duplicate IDs: {duplicates}")
    if missing:
        errors.append(f"Missing IDs: {missing}")
    if unexpected:
        errors.append(f"Unexpected IDs: {unexpected}")

    for cid in expected_ids:
        if cid in actual:
            status = actual[cid]
            if cid == "G9":
                if status != "OBSERVED":
                    errors.append(f"{cid} expected OBSERVED, got {status}")
            elif cid == "G2":
                # Review item 6: If quiet drops the line, G2 is a FAIL and a finding
                if status not in ("PASS", "FAIL"):
                    errors.append(f"{cid} expected PASS or FAIL, got {status}")
            else:
                if status != "PASS":
                    errors.append(f"{cid} expected PASS, got {status}")

    if errors:
        for err in errors:
            print(f"ERROR: {err}", file=sys.stderr)
        sys.exit(1)

    print(f"check-results PASS: all {len(expected_ids)} checks for {fw} present with exact required statuses.")

def main():
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_dump = sub.add_parser("capture-dump")
    p_dump.add_argument("--qmp-socket", required=True)
    p_dump.add_argument("--out-ppm", required=True)
    p_dump.add_argument("--timeout", type=int, default=60)
    p_dump.add_argument("--ready-log", default=None)

    p_red = sub.add_parser("create-red-png")
    p_red.add_argument("--out-png", required=True)

    p_bmp = sub.add_parser("create-splash-bmp")
    p_bmp.add_argument("--out-bmp", required=True)

    p_eval = sub.add_parser("evaluate")
    p_eval.add_argument("--firmware", choices=["uefi", "bios"], required=True)
    p_eval.add_argument("--out-dir", required=True)
    p_eval.add_argument("--results-file", required=True)

    p_check = sub.add_parser("check-results")
    p_check.add_argument("--firmware", choices=["uefi", "bios"], required=True)
    p_check.add_argument("--results-file", required=True)

    args = parser.parse_args()
    if args.cmd == "capture-dump":
        ok = capture_screendump(args.qmp_socket, args.out_ppm, args.timeout, args.ready_log)
        sys.exit(0 if ok else 1)
    elif args.cmd == "create-red-png":
        create_solid_red_png(args.out_png)
    elif args.cmd == "create-splash-bmp":
        create_splash_bmp(args.out_bmp)
    elif args.cmd == "evaluate":
        cmd_evaluate(args)
    elif args.cmd == "check-results":
        cmd_check_results(args)

if __name__ == "__main__":
    main()
