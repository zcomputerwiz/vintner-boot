#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# test-plymouth-theme.sh - static verification of Plymouth theme, assets, GRUB and hooks (WO-0008a)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

echo "=== Vintner Plymouth Theme Static Verification (WO-0008a) ==="

python3 - << 'EOF'
import sys
import os
import glob
import struct
import configparser
import subprocess
from pathlib import Path

failures = 0

def check_pass(check_id, desc, details=""):
    msg = f"PASS: [{check_id}] {desc}"
    if details:
        msg += f" - {details}"
    print(msg)

def check_fail(check_id, desc, error=""):
    global failures
    failures += 1
    msg = f"FAIL: [{check_id}] {desc}"
    if error:
        msg += f" - {error}"
    print(msg, file=sys.stderr)

# ---------------------------------------------------------------------------
# S1: The theme file parses; [Plymouth Theme] has ModuleName=two-step;
#     [two-step] has ImageDir=/usr/share/plymouth/themes/vintner-bgrt;
#     [boot-up] has UseFirmwareBackground=true.
# ---------------------------------------------------------------------------
theme_path = Path("graphics/plymouth-theme/vintner-bgrt.plymouth")
if not theme_path.is_file():
    check_fail("S1", "Theme file parsing", f"File not found: {theme_path}")
else:
    try:
        cp = configparser.ConfigParser(interpolation=None)
        with open(theme_path, "r", encoding="utf-8") as f:
            cp.read_file(f)

        s1_errors = []
        if not cp.has_section("Plymouth Theme"):
            s1_errors.append("Missing section [Plymouth Theme]")
        elif cp.get("Plymouth Theme", "ModuleName", fallback=None) != "two-step":
            s1_errors.append(f"ModuleName expected 'two-step', got {cp.get('Plymouth Theme', 'ModuleName', fallback=None)!r}")

        if not cp.has_section("two-step"):
            s1_errors.append("Missing section [two-step]")
        elif cp.get("two-step", "ImageDir", fallback=None) != "/usr/share/plymouth/themes/vintner-bgrt":
            s1_errors.append(f"ImageDir expected '/usr/share/plymouth/themes/vintner-bgrt', got {cp.get('two-step', 'ImageDir', fallback=None)!r}")

        if not cp.has_section("boot-up"):
            s1_errors.append("Missing section [boot-up]")
        elif cp.get("boot-up", "UseFirmwareBackground", fallback=None) != "true":
            s1_errors.append(f"UseFirmwareBackground expected 'true', got {cp.get('boot-up', 'UseFirmwareBackground', fallback=None)!r}")

        if s1_errors:
            check_fail("S1", "Theme file configuration", "; ".join(s1_errors))
        else:
            check_pass("S1", "Theme file configuration", "ModuleName=two-step, ImageDir=/usr/share/plymouth/themes/vintner-bgrt, UseFirmwareBackground=true")
    except Exception as e:
        check_fail("S1", "Theme file parsing", str(e))

# ---------------------------------------------------------------------------
# S2: Every image file the theme or plugin loads exists, is a valid PNG
#     (signature + IHDR header unpack), and matches table dimensions (WxH),
#     bit depth (8), and color type (6=RGBA) parsed from ci/graphics/README.md.
# ---------------------------------------------------------------------------
import re

readme_path = Path("ci/graphics/README.md")
EXPECTED_ASSETS = {}
if not readme_path.is_file():
    check_fail("S2", "README asset table parsing", f"Missing {readme_path}")
else:
    in_table = False
    for line in readme_path.read_text(encoding="utf-8").splitlines():
        line_clean = line.strip()
        if "## Theme Asset Dimensions Table" in line_clean:
            in_table = True
            continue
        if in_table:
            if line_clean.startswith("## ") or line_clean.startswith("Total active"):
                in_table = False
                continue
            m = re.match(r"^\|\s*`([^`]+)`(?:\s*\.\.\.\s*`([^`]+)`)?\s*\|\s*(\d+x\d+)\s*\|\s*(\d+)\s*\|", line_clean)
            if m:
                first_file, last_file, dim, count = m.group(1), m.group(2), m.group(3), int(m.group(4))
                if last_file:
                    m_pfx = re.match(r"^(.*?)(\d+)(\.png)$", first_file)
                    m_sfx = re.match(r"^(.*?)(\d+)(\.png)$", last_file)
                    if m_pfx and m_sfx:
                        pfx, start_idx, ext = m_pfx.group(1), int(m_pfx.group(2)), m_pfx.group(3)
                        _, end_idx, _ = m_sfx.group(1), int(m_sfx.group(2)), m_sfx.group(3)
                        width_pad = len(m_pfx.group(2))
                        for idx in range(start_idx, end_idx + 1):
                            EXPECTED_ASSETS[f"{pfx}{idx:0{width_pad}d}{ext}"] = dim
                else:
                    EXPECTED_ASSETS[first_file] = dim

PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"
theme_dir = Path("graphics/plymouth-theme")

s2_errors = []
for filename, expected_dim in sorted(EXPECTED_ASSETS.items()):
    p = theme_dir / filename
    if not p.is_file():
        s2_errors.append(f"Missing file: {filename}")
        continue

    try:
        data = p.read_bytes()
        if len(data) < 26:
            s2_errors.append(f"{filename}: File too short for PNG header ({len(data)} bytes)")
            continue

        if data[:8] != PNG_SIGNATURE:
            s2_errors.append(f"{filename}: Invalid PNG signature")
            continue

        if data[12:16] != b"IHDR":
            s2_errors.append(f"{filename}: Missing IHDR chunk at offset 12")
            continue

        w, h, bit_depth, color_type = struct.unpack(">IIBB", data[16:26])
        actual_dim = f"{w}x{h}"
        if actual_dim != expected_dim:
            s2_errors.append(f"{filename}: Dimension mismatch: got {actual_dim}, want {expected_dim}")
        if bit_depth != 8:
            s2_errors.append(f"{filename}: Bit depth mismatch: got {bit_depth}, want 8")
        if color_type != 6:
            s2_errors.append(f"{filename}: Color type mismatch: got {color_type}, want 6 (RGBA)")
    except Exception as e:
        s2_errors.append(f"{filename}: {e}")

if s2_errors:
    check_fail("S2", "Theme image validity and dimensions", f"{len(s2_errors)} errors: {'; '.join(s2_errors[:3])}")
else:
    check_pass("S2", "Theme image validity and dimensions", f"All {len(EXPECTED_ASSETS)} PNG assets valid: 8-bit RGBA with exact WxH parsed from README table")

# ---------------------------------------------------------------------------
# S3: No PNG in graphics/plymouth-theme/ is unlisted in the table
# ---------------------------------------------------------------------------
actual_pngs = {p.name for p in theme_dir.glob("*.png")}
unlisted = sorted(actual_pngs - set(EXPECTED_ASSETS.keys()))

if unlisted:
    check_fail("S3", "Unlisted PNG files", f"Found {len(unlisted)} unlisted PNG files: {unlisted}")
else:
    check_pass("S3", "Unlisted PNG files", f"No stray PNGs ({len(actual_pngs)} PNGs match table exactly)")

# ---------------------------------------------------------------------------
# S4: grub-script-check firmware/bios/grub.cfg passes, and the file loads a
#     video module and sets gfxmode before the linux command.
# ---------------------------------------------------------------------------
grub_cfg = Path("firmware/bios/grub.cfg")
if not grub_cfg.is_file():
    check_fail("S4", "GRUB configuration check", f"Missing {grub_cfg}")
else:
    # Check grub-script-check command
    gsc_found = any(os.access(os.path.join(path, "grub-script-check"), os.X_OK) for path in os.environ.get("PATH", "").split(os.pathsep))
    if not gsc_found:
        check_fail("S4", "GRUB configuration check", "grub-script-check is missing from PATH (do not fall back to text search)")
    else:
        r = subprocess.run(["grub-script-check", str(grub_cfg)], capture_output=True, text=True)
        if r.returncode != 0:
            check_fail("S4", "GRUB configuration syntax", f"grub-script-check failed: {r.stderr.strip()}")
        else:
            # Check for video module and gfxmode before linux command
            lines = grub_cfg.read_text(encoding="utf-8").splitlines()
            linux_idx = -1
            video_mod_idx = -1
            gfxmode_idx = -1

            for idx, line in enumerate(lines):
                line_stripped = line.strip()
                if line_stripped.startswith("#"):
                    continue
                if "linux " in line_stripped and linux_idx == -1:
                    linux_idx = idx
                if ("insmod all_video" in line_stripped or "insmod vbe" in line_stripped) and video_mod_idx == -1:
                    video_mod_idx = idx
                if ("set gfxmode=" in line_stripped or "gfxmode=" in line_stripped) and gfxmode_idx == -1:
                    gfxmode_idx = idx

            s4_errors = []
            if linux_idx == -1:
                s4_errors.append("No 'linux' command found in grub.cfg")
            if video_mod_idx == -1:
                s4_errors.append("No video module loaded ('insmod all_video')")
            elif linux_idx != -1 and video_mod_idx >= linux_idx:
                s4_errors.append(f"Video module loaded at line {video_mod_idx+1} after linux command at line {linux_idx+1}")

            if gfxmode_idx == -1:
                s4_errors.append("No 'set gfxmode=' found")
            elif linux_idx != -1 and gfxmode_idx >= linux_idx:
                s4_errors.append(f"gfxmode set at line {gfxmode_idx+1} after linux command at line {linux_idx+1}")

            if s4_errors:
                check_fail("S4", "GRUB configuration structure", "; ".join(s4_errors))
            else:
                check_pass("S4", "GRUB configuration check", "grub-script-check passes; video module loaded and gfxmode set before linux")

# ---------------------------------------------------------------------------
# S5: debian/vintner-core.install installs theme files and plymouthd.conf to
#     the paths above, and every source path it names exists.
# ---------------------------------------------------------------------------
install_file = Path("debian/vintner-core.install")
if not install_file.is_file():
    check_fail("S5", "Debian packaging rules", f"Missing {install_file}")
else:
    s5_errors = []
    has_theme_install = False
    has_plymouthd_install = False

    for line_num, line in enumerate(install_file.read_text(encoding="utf-8").splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        if len(parts) < 2:
            s5_errors.append(f"Line {line_num}: Malformed install entry: {line}")
            continue

        src_pattern, dst_dir = parts[0], parts[1]
        matches = glob.glob(src_pattern)
        if not matches:
            s5_errors.append(f"Line {line_num}: Source pattern {src_pattern!r} matches no files")

        if "usr/share/plymouth/themes/vintner-bgrt" in dst_dir:
            has_theme_install = True
        if "etc/plymouth" in dst_dir and "plymouthd.conf" in src_pattern:
            has_plymouthd_install = True

    if not has_theme_install:
        s5_errors.append("No rule installing to usr/share/plymouth/themes/vintner-bgrt/")
    if not has_plymouthd_install:
        s5_errors.append("No rule installing plymouthd.conf to etc/plymouth/")

    if s5_errors:
        check_fail("S5", "Debian packaging rules", "; ".join(s5_errors))
    else:
        check_pass("S5", "Debian packaging rules", "Theme assets and plymouthd.conf correctly mapped and all source files exist")

# ---------------------------------------------------------------------------
# S6: The initramfs hook is executable in git, has the right shebang, and sh -n passes.
# ---------------------------------------------------------------------------
hook_path = Path("integration/initramfs-hooks/plymouth-vintner")
if not hook_path.is_file():
    check_fail("S6", "Initramfs hook check", f"Missing {hook_path}")
else:
    s6_errors = []
    # Check git executable bit
    r = subprocess.run(["git", "ls-files", "-s", str(hook_path)], capture_output=True, text=True)
    if r.returncode != 0 or not r.stdout.startswith("100755"):
        s6_errors.append(f"Git mode is not 100755 (got {r.stdout.strip()})")

    # Check shebang
    first_line = hook_path.read_text(encoding="utf-8").splitlines()[0].strip() if hook_path.stat().st_size > 0 else ""
    if first_line != "#!/bin/sh":
        s6_errors.append(f"Expected shebang '#!/bin/sh', got {first_line!r}")

    # Check sh -n syntax
    r_sh = subprocess.run(["sh", "-n", str(hook_path)], capture_output=True, text=True)
    if r_sh.returncode != 0:
        s6_errors.append(f"sh -n failed: {r_sh.stderr.strip()}")

    if s6_errors:
        check_fail("S6", "Initramfs hook validation", "; ".join(s6_errors))
    else:
        check_pass("S6", "Initramfs hook validation", "Executable mode 100755, '#!/bin/sh' shebang, and 'sh -n' syntax valid")

print(f"\nVerification summary: {6 - failures}/6 checks passed.")
if failures > 0:
    sys.exit(1)
EOF

echo "All static Plymouth theme checks passed successfully."
