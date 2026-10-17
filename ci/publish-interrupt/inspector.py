#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later

import sys
import os
import re
import json
import time
import subprocess
import hashlib
import tempfile
import argparse
from datetime import datetime

mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")

CASES = ["C0", "T1", "T2", "T3", "T4", "T5", "T6", "M1", "M2", "G1", "G2"]
CASE_TESTPOINTS = {
    "C0": None,
    "T1": "staged-recorded",
    "T2": "staging-written",
    "T3": "staging-verified",
    "T4": "renamed",
    "T5": "grubenv-written",
    "T6": "trying-recorded",
    "M1": "manifest-written",
    "M2": "manifest-written",
    "G1": "gc-record-removed",
    "G2": "gc-grubenv-written",
}

UKI_RE = re.compile(r"vintner-os-(\d+)(\+\d+(-\d+)?)?\.efi")

def clean_ansi(text):
    return re.sub(r"\x1b\[[0-9;:]*[a-zA-Z]", "", text).replace("\r", "")

def read_log(path):
    return clean_ansi(open(path, errors="replace").read()) if path and os.path.isfile(path) else ""

def sha256_bytes(b):
    return hashlib.sha256(b).hexdigest()

def mtype(disk, offset, path):
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    if r.returncode == 0:
        return r.stdout
    return None

def mdir(disk, offset, path="::/", dirs=False):
    norm_base = path.strip()
    if not norm_base.startswith("::"):
        norm_base = "::" + norm_base
    if not norm_base.endswith("/"):
        norm_base += "/"

    result = []
    visited_dirs = set()
    queue = [norm_base]

    while queue:
        cur = queue.pop(0)
        if cur in visited_dirs:
            continue
        visited_dirs.add(cur)

        r = subprocess.run(["mdir", "-b", "-i", f"{disk}@@{offset}", cur], env=mtools_env, capture_output=True)
        if r.returncode != 0:
            continue

        for line in r.stdout.decode("utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line:
                continue
            is_dir = line.endswith("/")
            if not line.startswith(cur):
                # Cycle or out-of-tree entry (e.g. cluster 0 pointing to root)
                continue
            if is_dir:
                if line not in visited_dirs:
                    queue.append(line)
                if dirs:
                    p = line[2:] if line.startswith("::") else line
                    result.append(p if p.startswith("/") else "/" + p)
            else:
                p = line[2:] if line.startswith("::") else line
                result.append(p if p.startswith("/") else "/" + p)

    return sorted(result)


def load_manifest_via_vintner(vintner_bin, disk, offset):
    with tempfile.TemporaryDirectory() as tmpdir:
        a = mtype(disk, offset, "/vintner/state/manifest-a")
        b = mtype(disk, offset, "/vintner/state/manifest-b")
        if not a and not b:
            return None, None
        if a:
            with open(os.path.join(tmpdir, "manifest-a"), "wb") as f:
                f.write(a)
        if b:
            with open(os.path.join(tmpdir, "manifest-b"), "wb") as f:
                f.write(b)
        res = subprocess.run([vintner_bin, "manifest", "show", "--state-dir", tmpdir], capture_output=True, text=True)
        if res.returncode != 0:
            return None, None
        picked = None
        m = re.search(r"loaded from copy ([ab])", res.stderr)
        if m:
            picked = f"manifest-{m.group(1)}"
        return json.loads(res.stdout), picked

def parse_time(s):
    try:
        return datetime.fromisoformat(s[:-1] + "+00:00" if s.endswith("Z") else s)
    except (AttributeError, TypeError, ValueError):
        return None

def uki_names(read, gen):
    return sorted(n for n in read.get("efi_files", []) if (m := UKI_RE.fullmatch(n)) and int(m.group(1)) == gen)

def counter(read, firmware, gen):
    if firmware == "uefi":
        return uki_names(read, gen)
    if read.get("grubenv") is None:
        return "(grubenv unreadable)"
    return read["grubenv"].get(f"VINTNER_T_{gen}")

def fmt_counter(firmware, gen, val):
    return f"gen {gen} UKIs={val}" if firmware == "uefi" else f"VINTNER_T_{gen}={val}"

def booted_absent(read):
    env = read.get("grubenv")
    if env is None:
        return False, "grubenv unreadable"
    if "VINTNER_BOOTED" in env:
        return False, f"VINTNER_BOOTED={env['VINTNER_BOOTED']}"
    return True, "VINTNER_BOOTED absent"

def state_of(read, gen):
    img = (read.get("images") or {}).get(str(gen))
    return img.get("state") if img else "missing"

def quote_meta(s):
    return re.sub(r"([\\.+*?()|\[\]{}^$])", r"\\\1", s)

def expected_retained(pkgs):
    return None if None in pkgs else sorted({"^" + quote_meta(p) + "$" for p in pkgs})

def parse_retained_file(content):
    if not content:
        return None
    m = re.search(r"APT::NeverAutoRemove\s*\{(.*?)\};", content, re.DOTALL)
    if not m:
        return None
    entries = sorted(re.findall(r'"([^"]*)"', m.group(1)))
    return entries

def identify_booted(log, firmware, prev, curr):
    text = clean_ansi(log)

    for line in re.findall(r"Command line: (.*)$", text, re.M):
        toks = line.split()
        if "vintner.recovery=1" in toks or "vintner.recovery_test=1" in toks:
            if "vintner.test_slot=b" in toks:
                return "recovery-b"
            return "recovery-a" if "vintner.recovery_test=1" in toks else "recovery"

    if firmware == "bios":
        tries = re.findall(r"vintner: trying (\S+)", text)
        if not tries:
            return "unknown"
        t = tries[-1]
        m = re.fullmatch(r"(os-\d+|recovery-[ab])", t)
        return m.group(1) if m else f"grub={t}"

    m = re.search(r"^LOADER_ENTRY_SELECTED:[ \t]*(.*?)[ \t]*$", text, re.M)
    if m and m.group(1):
        val = m.group(1)
        stem = re.fullmatch(r"vintner-(os-\d+|recovery-[ab])(\+\d+(-\d+)?)?\.efi", val)
        if stem:
            return stem.group(1)
        stem_simple = re.fullmatch(r"(.+?)(\+\d+(-\d+)?)?\.efi", val)
        return stem_simple.group(1) if stem_simple else val

    if prev is not None and curr is not None:
        def by_gen(read):
            res = {}
            for n in read.get("efi_files", []):
                mm = UKI_RE.fullmatch(n)
                if mm:
                    res.setdefault(int(mm.group(1)), []).append(n)
            return {g: sorted(v) for g, v in res.items()}
        p, c = by_gen(prev), by_gen(curr)
        changed = [g for g in sorted(set(p) | set(c)) if p.get(g) != c.get(g)]
        if len(changed) == 1:
            return f"os-{changed[0]}"

    return "unknown"

def emit(results_file, check_id, case, status, evidence):
    evidence = re.sub(r"[\t\r\n]", " ", str(evidence).strip())
    row = f"{check_id}\t{case}\t{status}\t{evidence}"
    with open(results_file, "a") as f:
        f.write(row + "\n")
    print(row)

# --- Subcommands ---

def cmd_verify_prep_base(args):
    mf, _ = load_manifest_via_vintner(args.vintner_bin, args.disk, args.bs_offset)
    if not mf:
        sys.exit("verify-prep-base FAIL: manifest unreadable")
    images = mf.get("images", [])
    if len(images) != 1 or images[0]["generation"] != 1 or images[0]["state"] != "GOOD":
        sys.exit(f"verify-prep-base FAIL: expected exactly os-1 GOOD, got: {images}")
    staging = mdir(args.disk, args.bs_offset, "::/vintner/staging", dirs=True)
    if staging:
        sys.exit(f"verify-prep-base FAIL: staging is not empty: {staging}")
    print("verify-prep-base PASS: base disk verified with os-1 GOOD and empty staging")

def cmd_verify_prep_gc(args):
    mf, _ = load_manifest_via_vintner(args.vintner_bin, args.disk, args.bs_offset)
    if not mf:
        sys.exit("verify-prep-gc FAIL: manifest unreadable")
    images = mf.get("images", [])
    gens = sorted(img["generation"] for img in images)
    states = [img["state"] for img in images]
    if gens != [1, 2, 3] or any(s != "GOOD" for s in states):
        sys.exit(f"verify-prep-gc FAIL: expected generations 1, 2, 3 all GOOD, got: {images}")
    if mf.get("next_generation") != 4:
        sys.exit(f"verify-prep-gc FAIL: expected next_generation 4, got: {mf.get('next_generation')}")
    staging = mdir(args.disk, args.bs_offset, "::/vintner/staging", dirs=True)
    if staging:
        sys.exit(f"verify-prep-gc FAIL: staging is not empty: {staging}")
    print("verify-prep-gc PASS: GC disk verified with generations 1, 2, 3 all GOOD, next_generation 4, empty staging")

def cmd_run_boot(args):
    log_path = args.log
    with open(log_path, "w") as f:
        pass

    accel = "tcg"
    if os.path.exists("/dev/kvm") and os.access("/dev/kvm", os.W_OK):
        accel = "kvm"

    cmd = [
        "qemu-system-x86_64",
        "-machine", f"q35,accel={accel}",
        "-m", "2048",
        "-smp", "2",
        "-nodefaults",
        "-display", "none",
        "-monitor", "none",
        "-serial", f"file:{log_path}",
        "-no-reboot",
        "-drive", f"file={args.disk},format=raw,if=virtio",
        "-drive", f"file={args.seed},format=raw,if=virtio",
        "-nic", "user,model=virtio-net-pci",
    ]

    if args.firmware == "uefi":
        if not args.code_fd or not args.vars:
            sys.exit("run-boot: --code-fd and --vars required for uefi")
        cmd.extend([
            "-drive", f"if=pflash,format=raw,unit=0,file={args.code_fd},readonly=on",
            "-drive", f"if=pflash,format=raw,unit=1,file={args.vars}",
        ])

    def exited(rc):
        return "poweroff" if rc == 0 else f"qemu exited with status {rc}"

    start_time = time.time()
    proc = subprocess.Popen(cmd)
    try:
        how_ended = exited(proc.wait(timeout=args.timeout))
    except subprocess.TimeoutExpired:
        proc.terminate()
        try:
            proc.wait(timeout=2.0)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        how_ended = f"killed after {args.timeout}s timeout"
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()

    duration = round(time.time() - start_time, 2)
    lines = [l.strip() for l in read_log(log_path).splitlines() if l.strip()]
    last_line = lines[-1] if lines else "(empty log)"

    meta = {
        "how_ended": how_ended,
        "returncode": proc.returncode,
        "last_line": last_line,
        "duration": duration,
    }
    if args.meta_out:
        with open(args.meta_out, "w") as f:
            json.dump(meta, f, indent=2)

    print(f"[{args.case}] Boot {args.boot_num} ended: {how_ended} (returncode {proc.returncode}, {duration}s), last_line: {last_line}")

def cmd_read(args):
    disk, off, prefix = args.disk, args.bs_offset, args.out_prefix
    mf, picked = load_manifest_via_vintner(args.vintner_bin, disk, off)

    raw_a = mtype(disk, off, "/vintner/state/manifest-a")
    raw_b = mtype(disk, off, "/vintner/state/manifest-b")

    efi_files = sorted(os.path.basename(p) for p in mdir(disk, off, "::/EFI/Linux"))
    bios_files = mdir(disk, off, "::/vintner/bios", dirs=True)
    staging_files = mdir(disk, off, "::/vintner/staging", dirs=True)

    with open(f"{prefix}-efi-linux.txt", "w") as f:
        f.write("".join(n + "\n" for n in efi_files))
    with open(f"{prefix}-bios.txt", "w") as f:
        f.write("".join(p + "\n" for p in bios_files))
    with open(f"{prefix}-staging.txt", "w") as f:
        f.write("".join(p + "\n" for p in staging_files))

    grubenv = None
    raw_env = mtype(disk, off, "/grub/grubenv")
    if raw_env is not None:
        with open(f"{prefix}-grubenv", "wb") as f:
            f.write(raw_env)
        lib = os.path.join(args.root, "usr/lib/x86_64-linux-gnu")
        out = subprocess.run([os.path.join(lib, "ld-linux-x86-64.so.2"), "--library-path", lib,
                              os.path.join(args.root, "usr/bin/grub-editenv"), f"{prefix}-grubenv", "list"],
                             capture_output=True, text=True, check=True).stdout
        with open(f"{prefix}-grubenv.txt", "w") as f:
            f.write(out)
        grubenv = dict(l.partition("=")[::2] for l in out.splitlines() if "=" in l)

    # File hashes for all files on boot storage (UEFI and BIOS)
    file_hashes = {}
    for n in efi_files:
        p = f"/EFI/Linux/{n}"
        c = mtype(disk, off, p)
        file_hashes[p] = sha256_bytes(c) if c is not None else None
    for p in bios_files:
        if not p.endswith("/"):
            c = mtype(disk, off, p)
            file_hashes[p] = sha256_bytes(c) if c is not None else None

    read = {
        "manifest": mf,
        "picked_copy": picked,
        "manifest_a_sha256": sha256_bytes(raw_a) if raw_a else None,
        "manifest_b_sha256": sha256_bytes(raw_b) if raw_b else None,
        "images": {str(img["generation"]): img for img in (mf.get("images", []) if mf else [])},
        "next_generation": mf.get("next_generation") if mf else None,
        "efi_files": efi_files,
        "bios_files": bios_files,
        "staging_files": staging_files,
        "grubenv": grubenv,
        "file_hashes": file_hashes,
    }
    with open(f"{prefix}.json", "w") as f:
        json.dump(read, f, indent=2)

    images_summary = sorted(read["images"]) if mf else "(no valid manifest)"
    print(f"read {prefix}: gens {images_summary}, picked {picked}; /EFI/Linux {efi_files}; staging {staging_files}")

# --- Check Evaluation ---

def evaluate_case(case, fw, out_dir, results_file):
    prefix = os.path.join(out_dir, case)
    pre = json.load(open(f"{prefix}-read-pre.json"))
    op_read = json.load(open(f"{prefix}-read-op.json"))
    b1_read = json.load(open(f"{prefix}-read-boot1.json"))
    rec_read = json.load(open(f"{prefix}-read-rec-op.json"))
    b2_read = json.load(open(f"{prefix}-read-boot2.json"))

    op_log = read_log(f"{prefix}-op.log")
    b1_log = read_log(f"{prefix}-boot1.log")
    rec_log = read_log(f"{prefix}-rec-op.log")
    b2_log = read_log(f"{prefix}-boot2.log")

    op_meta = json.load(open(f"{prefix}-op-meta.json")) if os.path.isfile(f"{prefix}-op-meta.json") else {}
    b1_meta = json.load(open(f"{prefix}-boot1-meta.json")) if os.path.isfile(f"{prefix}-boot1-meta.json") else {}
    rec_meta = json.load(open(f"{prefix}-rec-op-meta.json")) if os.path.isfile(f"{prefix}-rec-op-meta.json") else {}
    b2_meta = json.load(open(f"{prefix}-boot2-meta.json")) if os.path.isfile(f"{prefix}-boot2-meta.json") else {}

    retained_file_path = f"{prefix}-retained-kernels.txt"
    retained_content = open(retained_file_path).read() if os.path.isfile(retained_file_path) else ""

    p = 4 if case in ("G1", "G2") else 2

    # --- P1 ---
    # Operation boot log checks
    p1_ok = True
    p1_ev = []
    if case == "C0":
        tp_found = [l for l in op_log.splitlines() if "vintner-testpoint:" in l]
        pub_found = [l for l in op_log.splitlines() if f"vintner publish: published generation {p}" in l]
        if tp_found:
            p1_ok = False
            p1_ev.append(f"unexpected testpoint in C0: {tp_found[0]}")
        else:
            p1_ev.append("no testpoint triggered")
        if not pub_found:
            p1_ok = False
            p1_ev.append(f"missing published generation {p} in C0")
        else:
            p1_ev.append(pub_found[0].strip())
    else:
        want_tp = CASE_TESTPOINTS[case]
        tp_lines = [l for l in op_log.splitlines() if re.search(r"vintner-testpoint:\s*" + re.escape(want_tp) + r"$", l)]
        sysrq_lines = [l for l in op_log.splitlines() if "sysrq: Resetting" in l]
        pub_lines = [l for l in op_log.splitlines() if f"vintner publish: published generation {p}" in l]
        powerdown_lines = [l for l in op_log.splitlines() if "reboot: Power down" in l]

        if not tp_lines:
            p1_ok = False
            p1_ev.append(f"missing testpoint '{want_tp}' line")
        else:
            p1_ev.append(tp_lines[0].strip())

        if not sysrq_lines:
            p1_ok = False
            p1_ev.append("missing sysrq: Resetting")
        else:
            p1_ev.append(sysrq_lines[0].strip())

        if case in ("G1", "G2"):
            # Test point is after published line in G1/G2
            if not pub_lines:
                p1_ok = False
                p1_ev.append(f"missing published generation {p} line before testpoint")
            else:
                p1_ev.append(pub_lines[0].strip())
        else:
            if pub_lines:
                p1_ok = False
                p1_ev.append(f"unexpected publish line: {pub_lines[0].strip()}")
            else:
                p1_ev.append(f"no published generation {p} line")

        if powerdown_lines:
            p1_ok = False
            p1_ev.append("unexpected reboot: Power down line found")
        else:
            p1_ev.append("no reboot: Power down")

    emit(results_file, "P1", case, "PASS" if p1_ok else "FAIL", "; ".join(p1_ev))

    # Identify boots
    b1_label = identify_booted(b1_log, fw, op_read, b1_read)
    b2_label = identify_booted(b2_log, fw, rec_read, b2_read)

    # --- P2 ---
    # Boot 1 boots expected image from table
    if case == "C0":
        want_b1 = f"os-{p}"
    elif case in ("T1", "T2", "T3", "M1"):
        want_b1 = "os-1"
    elif case == "T4":
        want_b1 = f"os-{p}" if fw == "uefi" else "os-1"
    elif case in ("T5", "T6", "M2", "G1", "G2"):
        want_b1 = f"os-{p}"

    p2_ok = (b1_label == want_b1) and (b1_label not in ("recovery-a", "recovery-b", "recovery"))
    emit(results_file, "P2", case, "PASS" if p2_ok else "FAIL", f"boot1 booted {b1_label}, expected {want_b1}")

    # --- P3 ---
    # Boot 1's image is blessed GOOD and re-armed
    b1_gen = int(b1_label.replace("os-", "")) if b1_label.startswith("os-") else -1
    p3_ok = True
    p3_ev = []
    if b1_gen < 0:
        p3_ok = False
        p3_ev.append(f"unknown booted generation: {b1_label}")
    else:
        b1_img = b1_read.get("images", {}).get(str(b1_gen))
        if not b1_img or b1_img.get("state") != "GOOD":
            p3_ok = False
            p3_ev.append(f"gen {b1_gen} state is {b1_img.get('state') if b1_img else 'missing'}, expected GOOD")
        else:
            p3_ev.append(f"gen {b1_gen} state=GOOD")

        lg_after = parse_time(b1_img.get("last_good") if b1_img else None)
        pre_img = pre.get("images", {}).get(str(b1_gen))
        lg_before = parse_time(pre_img.get("last_good") if pre_img else None)
        if lg_after is None:
            p3_ok = False
            p3_ev.append("last_good is None")
        elif lg_before is not None and not (lg_after > lg_before):
            p3_ok = False
            p3_ev.append(f"last_good not advanced: before={lg_before} after={lg_after}")
        else:
            p3_ev.append(f"last_good updated ({lg_after})")

        if fw == "uefi":
            ukis = uki_names(b1_read, b1_gen)
            want_uki = f"vintner-os-{b1_gen}+3-0.efi"
            if ukis != [want_uki]:
                p3_ok = False
                p3_ev.append(f"UKIs={ukis}, expected [{want_uki}]")
            else:
                p3_ev.append(f"re-armed UKI={want_uki}")
        else:
            c = counter(b1_read, fw, b1_gen)
            absent, b_msg = booted_absent(b1_read)
            if c != "xxx" or not absent:
                p3_ok = False
                p3_ev.append(f"counter={c} (want xxx); {b_msg}")
            else:
                p3_ev.append(f"counter=xxx; {b_msg}")

    emit(results_file, "P3", case, "PASS" if p3_ok else "FAIL", "; ".join(p3_ev))

    # --- P4 ---
    # Every image that was GOOD before the case is still GOOD and hashes match
    p4_ok = True
    p4_ev = []
    # On GC disk, gen 1 is removed by GC; retained images are gen 2 and 3.
    # On Base disk, retained image is gen 1.
    good_before = [int(g) for g, img in pre.get("images", {}).items() if img.get("state") == "GOOD"]
    if case in ("G1", "G2"):
        good_before = [g for g in good_before if g != 1]

    for g in sorted(good_before):
        pre_rec = pre["images"][str(g)]
        for r_name, r_data in [("op", op_read), ("boot1", b1_read), ("rec_op", rec_read), ("boot2", b2_read)]:
            r_img = r_data.get("images", {}).get(str(g))
            if not r_img or r_img.get("state") != "GOOD":
                p4_ok = False
                p4_ev.append(f"gen {g} in {r_name} state={r_img.get('state') if r_img else 'missing'}")
            # Check file hashes
            if fw == "uefi" and pre_rec.get("uefi"):
                want_sha = pre_rec["uefi"].get("sha256")
                ukis = uki_names(r_data, g)
                if not ukis:
                    p4_ok = False
                    p4_ev.append(f"gen {g} in {r_name} has no UKIs")
                else:
                    uki_path = f"/EFI/Linux/{ukis[0]}"
                    got_sha = r_data.get("file_hashes", {}).get(uki_path)
                    if got_sha != want_sha:
                        p4_ok = False
                        p4_ev.append(f"gen {g} in {r_name} UKI sha mismatch")
            if fw == "bios" and pre_rec.get("bios"):
                for fname, want_sha in pre_rec["bios"].get("files", {}).items():
                    fpath = f"/vintner/bios/os-{g}/{fname}"
                    got_sha = r_data.get("file_hashes", {}).get(fpath)
                    if got_sha != want_sha:
                        p4_ok = False
                        p4_ev.append(f"gen {g} in {r_name} {fname} sha mismatch")

    if p4_ok:
        p4_ev.append(f"images {good_before} still GOOD with matching file hashes at all reads")
    emit(results_file, "P4", case, "PASS" if p4_ok else "FAIL", "; ".join(p4_ev))

    # --- P5 ---
    # After boot 1, state of p, next_generation, staging, and BIOS order/counters
    p5_ok = True
    p5_ev = []
    # Staging must be empty in all cases
    stg = b1_read.get("staging_files", [])
    if stg:
        p5_ok = False
        p5_ev.append(f"staging not empty: {stg}")
    else:
        p5_ev.append("staging empty")

    b1_next = b1_read.get("next_generation")
    p_img = b1_read.get("images", {}).get(str(p))

    if case == "C0":
        if b1_next != 3 or not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, next_gen={b1_next}")
        else:
            p5_ev.append(f"gen {p} GOOD, next_gen=3")
        if fw == "bios":
            order = (b1_read.get("grubenv") or {}).get("VINTNER_ORDER", "")
            if "2" not in order.split() or "1" not in order.split():
                p5_ok = False
                p5_ev.append(f"VINTNER_ORDER={order}")
            else:
                p5_ev.append(f"VINTNER_ORDER={order}")

    elif case in ("T1", "T2", "T3"):
        if p_img is not None:
            p5_ok = False
            p5_ev.append(f"gen {p} record still present: {p_img}")
        else:
            p5_ev.append(f"gen {p} removed")
        if b1_next != 3:
            p5_ok = False
            p5_ev.append(f"next_gen={b1_next}, expected 3")
        else:
            p5_ev.append("next_gen=3")
        if fw == "bios":
            order = (b1_read.get("grubenv") or {}).get("VINTNER_ORDER", "")
            c = (b1_read.get("grubenv") or {}).get(f"VINTNER_T_{p}")
            if order.split() != ["1"] or c is not None:
                p5_ok = False
                p5_ev.append(f"VINTNER_ORDER={order}, VINTNER_T_{p}={c}")
            else:
                p5_ev.append(f"VINTNER_ORDER={order}, VINTNER_T_{p} unset")

    elif case == "T4":
        if fw == "uefi":
            if not p_img or p_img.get("state") != "GOOD":
                p5_ok = False
                p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
            else:
                p5_ev.append(f"gen {p} GOOD")
            ukis = uki_names(b1_read, p)
            if ukis != [f"vintner-os-{p}+3-0.efi"]:
                p5_ok = False
                p5_ev.append(f"gen {p} UKIs={ukis}")
            else:
                p5_ev.append(f"gen {p} UKI +3-0 in place")
            m_rule7 = re.search(r"generation 2 has no BIOS counter|rule 7|BIOS counter", b1_log)
            p5_ev.append("rule 7 reported" if m_rule7 else "rule 7 check")
        else:
            # BIOS
            if not p_img or p_img.get("state") != "STAGED":
                p5_ok = False
                p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected STAGED")
            else:
                p5_ev.append(f"gen {p} STAGED")
            slot_dir = f"/vintner/bios/os-{p}"
            bios_has_slot = any(f.startswith(slot_dir) for f in b1_read.get("bios_files", []))
            if not bios_has_slot:
                p5_ok = False
                p5_ev.append(f"{slot_dir} missing from bios_files")
            else:
                p5_ev.append(f"{slot_dir} present")
            order = (b1_read.get("grubenv") or {}).get("VINTNER_ORDER", "")
            if order.split() != ["1"]:
                p5_ok = False
                p5_ev.append(f"VINTNER_ORDER={order}, expected 1")
            else:
                p5_ev.append("VINTNER_ORDER=1")

    elif case in ("T5", "T6"):
        if not p_img or p_img.get("state") != "GOOD" or b1_next != 3:
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, next_gen={b1_next}")
        else:
            p5_ev.append(f"gen {p} GOOD, next_gen=3")

    elif case == "M1":
        if p_img is None and b1_next == 3:
            p5_ev.append("save reached disk: gen 2 removed by rule 2, next_generation=3")
        elif p_img is None and b1_next == 2:
            p5_ev.append("save did not reach disk: gen 2 absent, next_generation=2")
        else:
            p5_ok = False
            p5_ev.append(f"unexpected M1 state: gen {p} {p_img}, next_gen={b1_next}")

    elif case == "M2":
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")
        picked_after_op = op_read.get("picked_copy")
        p5_ev.append(f"Load picked {picked_after_op} after op boot")

    elif case == "G1":
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")
        gen1_img = b1_read.get("images", {}).get("1")
        if gen1_img is not None:
            p5_ok = False
            p5_ev.append(f"gen 1 record still present: {gen1_img}")
        else:
            p5_ev.append("gen 1 has no record")
        # gen 1 files remain
        if fw == "uefi":
            u1 = uki_names(b1_read, 1)
            p5_ev.append(f"gen 1 UKIs={u1}")
        else:
            b1_slot = any(f.startswith("/vintner/bios/os-1") for f in b1_read.get("bios_files", []))
            p5_ev.append(f"gen 1 slot present={b1_slot}")
            order = (b1_read.get("grubenv") or {}).get("VINTNER_ORDER", "")
            if "1" not in order.split():
                p5_ok = False
                p5_ev.append(f"VINTNER_ORDER={order} does not contain 1")
            else:
                p5_ev.append(f"VINTNER_ORDER still lists 1 ({order})")

    elif case == "G2":
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")
        gen1_img = b1_read.get("images", {}).get("1")
        if gen1_img is not None:
            p5_ok = False
            p5_ev.append(f"gen 1 record still present: {gen1_img}")
        else:
            p5_ev.append("gen 1 has no record")
        if fw == "bios":
            order = (b1_read.get("grubenv") or {}).get("VINTNER_ORDER", "")
            c1 = (b1_read.get("grubenv") or {}).get("VINTNER_T_1")
            if "1" in order.split() or c1 is not None:
                p5_ok = False
                p5_ev.append(f"VINTNER_ORDER={order}, VINTNER_T_1={c1}")
            else:
                p5_ev.append(f"VINTNER_ORDER={order} (no 1), VINTNER_T_1 absent")

    emit(results_file, "P5", case, "PASS" if p5_ok else "FAIL", "; ".join(p5_ev))

    # --- P6 ---
    # Recovery publish exits 0 and prints published generation <n> == next_generation after boot 1
    # Boot 2 boots generation <n>, and it is GOOD and re-armed
    p6_ok = True
    p6_ev = []
    want_rec_gen = b1_read.get("next_generation")
    pub_match = re.search(r"vintner publish: published generation (\d+)", rec_log)
    if not pub_match or int(pub_match.group(1)) != want_rec_gen:
        p6_ok = False
        p6_ev.append(f"recovery published generation={pub_match.group(1) if pub_match else 'none'}, expected {want_rec_gen}")
    else:
        p6_ev.append(f"recovery published generation {want_rec_gen}")

    if b2_label != f"os-{want_rec_gen}":
        p6_ok = False
        p6_ev.append(f"boot2 booted {b2_label}, expected os-{want_rec_gen}")
    else:
        p6_ev.append(f"boot2 booted os-{want_rec_gen}")

    b2_img = b2_read.get("images", {}).get(str(want_rec_gen))
    if not b2_img or b2_img.get("state") != "GOOD":
        p6_ok = False
        p6_ev.append(f"gen {want_rec_gen} in boot2 state={b2_img.get('state') if b2_img else 'missing'}")
    else:
        p6_ev.append(f"gen {want_rec_gen} GOOD in boot2")

    if fw == "uefi":
        ukis = uki_names(b2_read, want_rec_gen)
        if ukis != [f"vintner-os-{want_rec_gen}+3-0.efi"]:
            p6_ok = False
            p6_ev.append(f"boot2 UKIs={ukis}")
        else:
            p6_ev.append(f"boot2 UKI re-armed (+3-0)")
    else:
        c = counter(b2_read, fw, want_rec_gen)
        absent, b_msg = booted_absent(b2_read)
        if c != "xxx" or not absent:
            p6_ok = False
            p6_ev.append(f"boot2 counter={c}, {b_msg}")
        else:
            p6_ev.append(f"boot2 counter=xxx, {b_msg}")

    emit(results_file, "P6", case, "PASS" if p6_ok else "FAIL", "; ".join(p6_ev))

    # --- P7 ---
    # 50vintner-retained-kernels lists exactly the kernel packages of the images the manifest then holds
    p7_ok = True
    p7_ev = []
    rec_mf = rec_read.get("manifest") or {}
    expected_pkgs = sorted(set(img["kernel"]["package"] for img in rec_mf.get("images", []) if img.get("kernel", {}).get("package")))
    want_entries = expected_retained(expected_pkgs)
    got_entries = parse_retained_file(retained_content)

    if got_entries != want_entries:
        p7_ok = False
        p7_ev.append(f"retained entries={got_entries}, expected={want_entries}")
    else:
        p7_ev.append(f"retained packages {expected_pkgs} match manifest")

    emit(results_file, "P7", case, "PASS" if p7_ok else "FAIL", "; ".join(p7_ev))

    # --- P8 ---
    # Reconciliation reports, state changes, and leftover unrecorded files (OBSERVED)
    p8_items = []
    # Collect reports and state changes
    for log_name, log_text in [("boot1", b1_log), ("rec_op", rec_log), ("boot2", b2_log)]:
        for l in log_text.splitlines():
            l_str = l.strip()
            if any(k in l_str for k in ["REPORT:", "ACTION:", "untracked", "does not verify", "missing files", "-> removed", "-> GOOD", "-> TRYING"]):
                p8_items.append(f"[{log_name}] {l_str}")

    # Check for leftover unrecorded files in read-boot2
    b2_images = b2_read.get("images", {})
    unrec_ukis = [u for u in b2_read.get("efi_files", []) if (m := UKI_RE.fullmatch(u)) and m.group(1) not in b2_images]
    unrec_bios = []
    for f in b2_read.get("bios_files", []):
        m = re.match(r"^/vintner/bios/os-(\d+)", f)
        if m and m.group(1) not in b2_images:
            unrec_bios.append(f)
    if unrec_ukis:
        p8_items.append(f"unrecorded UKIs after boot2: {unrec_ukis}")
    if unrec_bios:
        p8_items.append(f"unrecorded BIOS files after boot2: {sorted(set(unrec_bios))}")
    if b2_read.get("staging_files"):
        p8_items.append(f"leftover staging after boot2: {b2_read.get('staging_files')}")

    if fw == "bios":
        order = (b2_read.get("grubenv") or {}).get("VINTNER_ORDER", "").split()
        unrec_order = [g for g in order if g not in b2_images]
        if unrec_order:
            p8_items.append(f"unrecorded in VINTNER_ORDER after boot2: {unrec_order}")

    p8_ev = "; ".join(p8_items) if p8_items else "none"
    emit(results_file, "P8", case, "OBSERVED", p8_ev)

    # --- P9 ---
    # No boot ran recovery A or B
    rec_booted = identify_booted(rec_log, fw, b1_read, rec_read)
    boots = [("boot1", b1_label), ("rec_op", rec_booted), ("boot2", b2_label)]
    p9_ok = all(lbl not in ("recovery-a", "recovery-b", "recovery") for _, lbl in boots)
    p9_ev = ", ".join(f"{b}={lbl}" for b, lbl in boots)
    emit(results_file, "P9", case, "PASS" if p9_ok else "FAIL", f"boots: {p9_ev}")

def cmd_evaluate(args):
    with open(args.results_file, "w") as f:
        f.write("ID\tCase\tStatus\tEvidence\n")

    for case in CASES:
        evaluate_case(case, args.firmware, args.out_dir, args.results_file)

    print("\nEvaluation complete.")

def main():
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_vb = sub.add_parser("verify-prep-base")
    p_vb.add_argument("--disk", required=True)
    p_vb.add_argument("--bs-offset", type=int, required=True)
    p_vb.add_argument("--vintner-bin", required=True)

    p_vg = sub.add_parser("verify-prep-gc")
    p_vg.add_argument("--disk", required=True)
    p_vg.add_argument("--bs-offset", type=int, required=True)
    p_vg.add_argument("--vintner-bin", required=True)

    p_boot = sub.add_parser("run-boot")
    p_boot.add_argument("--firmware", required=True)
    p_boot.add_argument("--disk", required=True)
    p_boot.add_argument("--seed", required=True)
    p_boot.add_argument("--log", required=True)
    p_boot.add_argument("--case", required=True)
    p_boot.add_argument("--boot-num", required=True)
    p_boot.add_argument("--vars")
    p_boot.add_argument("--code-fd")
    p_boot.add_argument("--timeout", type=float, default=120)
    p_boot.add_argument("--meta-out")

    p_read = sub.add_parser("read")
    p_read.add_argument("--disk", required=True)
    p_read.add_argument("--bs-offset", type=int, required=True)
    p_read.add_argument("--vintner-bin", required=True)
    p_read.add_argument("--root", required=True)
    p_read.add_argument("--out-prefix", required=True)

    p_eval = sub.add_parser("evaluate")
    p_eval.add_argument("--firmware", required=True)
    p_eval.add_argument("--out-dir", required=True)
    p_eval.add_argument("--results-file", required=True)

    args = parser.parse_args()
    if args.cmd == "verify-prep-base":
        cmd_verify_prep_base(args)
    elif args.cmd == "verify-prep-gc":
        cmd_verify_prep_gc(args)
    elif args.cmd == "run-boot":
        cmd_run_boot(args)
    elif args.cmd == "read":
        cmd_read(args)
    elif args.cmd == "evaluate":
        cmd_evaluate(args)

if __name__ == "__main__":
    main()
