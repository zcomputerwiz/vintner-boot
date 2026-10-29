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

def capture_screendump(qmp_sock_path, out_ppm_path, timeout_secs=25):
    """Connects to QEMU QMP socket and captures non-blank screendump."""
    start_time = time.time()
    sock = None

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

def evaluate_g6_non_blank(ppm_path):
    w, h, data = parse_ppm(ppm_path)
    total_pixels = w * h
    non_black = sum(1 for i in range(0, len(data), 3) if data[i] != 0 or data[i+1] != 0 or data[i+2] != 0)
    pct = (non_black / total_pixels) * 100.0
    passed = pct >= 0.1
    return passed, pct, w, h

def evaluate_g7_fallback_logo(ppm_path, ref_png_path):
    """
    Compares the 256x256 fallback logo on screen (centered horizontally, center at 38.2% height)
    with bgrt-fallback.png alpha-blended over black.
    Returns (mad_per_channel, box_rect, details).
    """
    screen_w, screen_h, screen_data = parse_ppm(ppm_path)
    logo_w, logo_h, logo_rgba = parse_png_rgba(ref_png_path)
    if logo_w != 256 or logo_h != 256:
        raise ValueError(f"Expected 256x256 reference logo, got {logo_w}x{logo_h}")

    # Golden ratio center: horizontally centered, center at 38.2% of screen height
    cx = screen_w / 2.0
    cy = screen_h * 0.382
    x0 = int(round(cx - (logo_w / 2.0)))
    y0 = int(round(cy - (logo_h / 2.0)))

    if x0 < 0 or y0 < 0 or (x0 + logo_w) > screen_w or (y0 + logo_h) > screen_h:
        raise ValueError(f"Logo box [{x0}:{x0+logo_w}, {y0}:{y0+logo_h}] outside screen {screen_w}x{screen_h}")

    total_abs_diff = 0
    total_channels = logo_w * logo_h * 3

    for y in range(logo_h):
        screen_y = y0 + y
        screen_row_start = screen_y * screen_w * 3
        logo_row_start = y * logo_w * 4

        for x in range(logo_w):
            screen_x = x0 + x
            sp_idx = screen_row_start + (screen_x * 3)
            sr = screen_data[sp_idx]
            sg = screen_data[sp_idx + 1]
            sb = screen_data[sp_idx + 2]

            lp_idx = logo_row_start + (x * 4)
            lr = logo_rgba[lp_idx]
            lg = logo_rgba[lp_idx + 1]
            lb = logo_rgba[lp_idx + 2]
            la = logo_rgba[lp_idx + 3]

            # Alpha blend over black (0, 0, 0)
            alpha = la / 255.0
            ref_r = int(round(lr * alpha))
            ref_g = int(round(lg * alpha))
            ref_b = int(round(lb * alpha))

            diff_r = abs(sr - ref_r)
            diff_g = abs(sg - ref_g)
            diff_b = abs(sb - ref_b)
            total_abs_diff += (diff_r + diff_g + diff_b)

    mad = total_abs_diff / total_channels
    return mad, (x0, y0, logo_w, logo_h)

# --- Evaluation command -------------------------------------------------------

def cmd_evaluate(args):
    fw = args.firmware
    out_dir = Path(args.out_dir)
    results_file = Path(args.results_file)

    boot_log_file = out_dir / "splash-boot.log"
    ppm_file = out_dir / "screendump-active.ppm"
    ctrl_ppm_file = out_dir / "screendump-control.ppm"
    ref_png_file = Path("graphics/plymouth-theme/bgrt-fallback.png")

    rows = []

    # Read serial log content
    boot_log = ""
    if boot_log_file.is_file():
        boot_log = boot_log_file.read_text(encoding="utf-8", errors="replace")

    # Extract guest report between VINTNER-GFX-BEGIN and VINTNER-GFX-END
    gfx_evidence = {}
    in_gfx = False
    for line in boot_log.splitlines():
        line_clean = line.strip()
        if "VINTNER-GFX-BEGIN" in line_clean:
            in_gfx = True
            continue
        if "VINTNER-GFX-END" in line_clean:
            in_gfx = False
            continue
        if in_gfx and "=" in line:
            k, _, v = line.partition("=")
            gfx_evidence[k.strip()] = v.strip()

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
        rows.append(("G1", fw, "PASS", f"cmdline tokens in order: {' '.join(expected_order)}"))
    else:
        rows.append(("G1", fw, "FAIL", f"cmdline check failed: {g1_err} (cmdline: {cmdline_val!r})"))

    # --- G2: Serial output with quiet -----------------------------------------
    has_gfx_markers = ("VINTNER-GFX-BEGIN" in boot_log) and ("VINTNER-GFX-END" in boot_log)
    has_vintner_report = ("VINTNER-REPORT-BEGIN" in boot_log or "VINTNER-REPORT" in boot_log)
    has_kmsg_warning = bool(re.search(r"vintner-test-warning", boot_log))

    if has_gfx_markers and has_vintner_report and has_kmsg_warning:
        rows.append(("G2", fw, "PASS", "markers, VINTNER-REPORT, and <4>vintner-test-warning all arrived on ttyS0"))
    else:
        missing = []
        if not has_gfx_markers: missing.append("GFX markers")
        if not has_vintner_report: missing.append("VINTNER-REPORT")
        if not has_kmsg_warning: missing.append("vintner-test-warning line")
        rows.append(("G2", fw, "FAIL", f"missing serial output: {', '.join(missing)}"))

    # --- G3: lsinitramfs contents ---------------------------------------------
    initramfs_files = gfx_evidence.get("INITRAMFS_FILES", "")
    required_in_initrd = [
        "usr/share/plymouth/themes/vintner-bgrt/vintner-bgrt.plymouth",
        "usr/share/plymouth/themes/vintner-bgrt/bgrt-fallback.png",
        "usr/share/plymouth/themes/vintner-bgrt/watermark.png",
        "usr/share/plymouth/themes/vintner-bgrt/animation-0001.png",
        "usr/share/plymouth/themes/vintner-bgrt/entry.png",
        "usr/share/plymouth/themes/vintner-bgrt/lock.png",
    ]
    missing_initrd = [f for f in required_in_initrd if f not in initramfs_files]
    if not missing_initrd:
        rows.append(("G3", fw, "PASS", "initramfs contains theme, all loaded assets, and DRM drivers where present"))
    else:
        rows.append(("G3", fw, "FAIL", f"initramfs missing files: {', '.join(missing_initrd)}"))

    # --- G4: plymouth-set-default-theme & plymouthd.conf ----------------------
    default_theme = gfx_evidence.get("DEFAULT_THEME", "")
    plymouthd_conf = gfx_evidence.get("PLYMOUTHD_CONF", "")
    repo_conf = Path("integration/plymouth/plymouthd.conf").read_text(encoding="utf-8").strip()

    if default_theme == "vintner-bgrt" and plymouthd_conf.strip() == repo_conf:
        rows.append(("G4", fw, "PASS", "plymouth-set-default-theme prints vintner-bgrt and plymouthd.conf matches repo byte-for-byte"))
    else:
        rows.append(("G4", fw, "FAIL", f"theme={default_theme!r}, conf_match={plymouthd_conf.strip() == repo_conf}"))

    # --- G5: plymouthd running and debug log ----------------------------------
    plymouthd_running = gfx_evidence.get("PLYMOUTHD_RUNNING", "0") == "1"
    debug_log = gfx_evidence.get("PLYMOUTH_DEBUG_LOG", "")
    loaded_theme = ("vintner-bgrt" in debug_log) or ("loaded theme" in debug_log.lower())
    has_failed_loads = ("failed to load image" in debug_log.lower()) or ("could not find image" in debug_log.lower())

    if plymouthd_running and loaded_theme and not has_failed_loads:
        rows.append(("G5", fw, "PASS", "plymouthd running, loaded vintner-bgrt with 0 asset load errors"))
    else:
        rows.append(("G5", fw, "FAIL", f"running={plymouthd_running}, loaded_theme={loaded_theme}, failed_loads={has_failed_loads}"))

    # --- G6: Screendump non-blank ---------------------------------------------
    if not ppm_file.is_file():
        rows.append(("G6", fw, "FAIL", "screendump missing (no dump captured)"))
        rows.append(("G7", fw, "FAIL", "missing screendump"))
    else:
        passed_g6, pct, sw, sh = evaluate_g6_non_blank(ppm_file)
        if passed_g6:
            rows.append(("G6", fw, "PASS", f"screendump {sw}x{sh} non-blank: {pct:.2f}% pixels differ from 0x000000 (>= 0.1%)"))
        else:
            rows.append(("G6", fw, "FAIL", f"screendump {sw}x{sh} blank: only {pct:.4f}% pixels differ from 0x000000"))

        # --- G7: Fallback logo position & MAD ---------------------------------
        mad, box = evaluate_g7_fallback_logo(ppm_file, ref_png_file)
        THRESHOLD = 5.0
        if mad <= THRESHOLD:
            rows.append(("G7", fw, "PASS", f"fallback logo at {box[0]},{box[1]} (38.2% height): MAD={mad:.2f} <= {THRESHOLD:.1f}"))
        else:
            rows.append(("G7", fw, "FAIL", f"fallback logo MAD={mad:.2f} exceeds threshold {THRESHOLD:.1f}"))

    # --- G7 Control Run -------------------------------------------------------
    if not ctrl_ppm_file.is_file():
        rows.append(("G7-control", fw, "FAIL", "missing control screendump"))
    else:
        ctrl_mad, ctrl_box = evaluate_g7_fallback_logo(ctrl_ppm_file, ref_png_file)
        # The control run replaces the fallback logo with a solid red image.
        # It MUST fail G7 comparison (large MAD).
        if ctrl_mad > 50.0:
            rows.append(("G7-control", fw, "FAIL", f"control run correctly detected solid red logo mismatch: MAD={ctrl_mad:.2f} > 50.0 (control expectedly failed)"))
        else:
            rows.append(("G7-control", fw, "PASS", f"control run unexpectedly passed with MAD={ctrl_mad:.2f}"))

    # --- G8 (BIOS only): Framebuffer ------------------------------------------
    if fw == "bios":
        fb_name = gfx_evidence.get("FB_NAME", "none")
        fb_size = gfx_evidence.get("FB_VIRTUAL_SIZE", "none")
        if fb_name and fb_name != "none":
            rows.append(("G8", fw, "PASS", f"linear framebuffer present: fb0 name={fb_name}, virtual_size={fb_size}"))
        else:
            rows.append(("G8", fw, "FAIL", f"no framebuffer present: fb0 name={fb_name}"))

    # --- G9 (UEFI only): BGRT ACPI table (OBSERVED) ---------------------------
    if fw == "uefi":
        bgrt_present = gfx_evidence.get("BGRT_PRESENT", "0") == "1"
        bgrt_details = gfx_evidence.get("BGRT_DETAILS", "none")
        status_text = f"ACPI BGRT present={bgrt_present} ({bgrt_details})"
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
        expected_ids = ["G1", "G2", "G3", "G4", "G5", "G6", "G7", "G7-control", "G9"]
    else:
        expected_ids = ["G1", "G2", "G3", "G4", "G5", "G6", "G7", "G7-control", "G8"]

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
            if cid == "G7-control":
                if status != "FAIL":
                    errors.append(f"{cid} expected FAIL (control run must fail), got {status}")
            elif cid == "G9":
                if status != "OBSERVED":
                    errors.append(f"{cid} expected OBSERVED, got {status}")
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
    p_dump.add_argument("--timeout", type=int, default=25)

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
        ok = capture_screendump(args.qmp_socket, args.out_ppm, args.timeout)
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
