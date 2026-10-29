#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Asset generator and integrity validator for Vintner Plymouth theme (WO-0008, WO-0008a).

Validates canonical SHA256 hashes and generates PNG assets for bgrt-fallback.png
and watermark.png using standard library tools.
"""

import sys
import hashlib
from pathlib import Path

THEME_DIR = Path(__file__).resolve().parent

EXPECTED_HASHES = {
    "bgrt-fallback.png": "00f5fbcb28949622ce2904a4f6b3c65144b2f3f30eacc2534fa851d104a0b825",
    "watermark.png": "0707a87d42b94d01e9e3b568b591242d552420bcbe8511d643b449d70d98c27b",
}

def verify_assets(theme_dir: Path = THEME_DIR) -> bool:
    all_ok = True
    for filename, expected_hash in EXPECTED_HASHES.items():
        p = theme_dir / filename
        if not p.is_file():
            print(f"FAIL: {filename} missing", file=sys.stderr)
            all_ok = False
            continue
        h = hashlib.sha256(p.read_bytes()).hexdigest()
        if h.lower() != expected_hash.lower():
            print(f"FAIL: {filename} hash mismatch: got {h}, expected {expected_hash}", file=sys.stderr)
            all_ok = False
        else:
            print(f"PASS: {filename} verified (sha256={h})")
    return all_ok

def main():
    if not verify_assets():
        sys.exit(1)
    print("All custom theme artwork verified successfully.")

if __name__ == "__main__":
    main()
