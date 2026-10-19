#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later

import argparse
from datetime import datetime
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")

CASES = ["C0", "T1", "T2", "T3", "T4", "T5", "T6", "M1", "M2", "G1", "G2"]
CHECKS = ["P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8", "P9"]

CASE_TESTPOINTS = {
    "C0": None,
    "T1": "staged-recorded",
    "T2": "staging-written",
    "T3": "staging-verified",
    "T4": "renamed",
    "T5": "grubenv-written",
    "T6": "trying-recorded",
    "M1": "manifest-written#1",
    "M2": "manifest-written#2",
    "G1": "gc-record-removed",
    "G2": "gc-grubenv-written",
}

UKI_RE = re.compile(r"vintner-os-(\d+)(\+\d+(-\d+)?)?\.efi")
ANSI_ESCAPE_RE = re.compile(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])")

LOADER_STEMS = {f"vintner-os-{i}": f"os-{i}" for i in range(1, 21)}
LOADER_STEMS.update({"vintner-recovery-a": "recovery-a", "vintner-recovery-b": "recovery-b"})
GRUB_DIRS = {f"os-{i}": f"os-{i}" for i in range(1, 21)}
GRUB_DIRS.update({"recovery-a": "recovery-a", "recovery-b": "recovery-b"})

def clean_ansi(text):
    return ANSI_ESCAPE_RE.sub("", text).replace("\r", "")

def read_log(path):
    return clean_ansi(open(path, errors="replace").read()) if path and os.path.isfile(path) else ""

def sha256_bytes(b):
    return hashlib.sha256(b).hexdigest()

def mtype(disk, offset, path):
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    if r.returncode == 0:
        return r.stdout
    if b"not found" in r.stderr:
        return None
    raise RuntimeError(f"mtype {path} on {disk}: {r.stderr.decode(errors='replace').strip()}")

def mdir(disk, offset, path="::/", dirs=False):
    r = subprocess.run(["mdir", "-s", "-b", "-i", f"{disk}@@{offset}", path], env=mtools_env, capture_output=True)
    if r.returncode != 0:
        if b"not found" in r.stderr:
            return []
        raise RuntimeError(f"mdir {path} on {disk}: {r.stderr.decode(errors='replace').strip()}")
    lines = []
    for l in r.stdout.decode("utf-8", errors="replace").splitlines():
        l = l.strip()
        if l.startswith("::"):
            l = l[2:]
        if l and (dirs or not l.endswith("/")):
            lines.append(l if l.startswith("/") else "/" + l)
    return sorted(lines)

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
        for line in res.stderr.splitlines():
            line = line.strip()
            m = re.fullmatch(r"loaded from copy ([ab]), seq \d+", line)
            if m:
                picked = f"manifest-{m.group(1)}"
                break
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
    if not m or not re.fullmatch(r'(\s*"[^"]*";)*\s*', m.group(1)):
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
        return GRUB_DIRS.get(t, f"grub={t}")

    m = re.search(r"^LOADER_ENTRY_SELECTED:[ \t]*(.*?)[ \t]*$", text, re.M)
    if m and m.group(1):
        val = m.group(1).strip()
        stem = re.fullmatch(r"(.+?)(\+\d+(-\d+)?)?\.efi", val)
        lookup = stem.group(1) if stem else val
        return LOADER_STEMS.get(lookup, f"entry={val}")

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

    if firmware == "uefi" and re.search(r"Linux version \S+", text) and prev is not None:
        good_gens = [int(g) for g, img in prev.get("images", {}).items() if img.get("state") == "GOOD"]
        if good_gens:
            return f"os-{max(good_gens)}"

    return "unknown"

def emit(results_file, check_id, case, status, evidence):
    evidence = re.sub(r"[\x00-\x1f\x7f]", " ", str(evidence))
    evidence = re.sub(r"\s+", " ", evidence).strip()
    row = f"{check_id}\t{case}\t{status}\t{evidence}"
    with open(results_file, "a") as f:
        f.write(row + "\n")
    print(row)

def parse_fsck_anomalies(text):
    if not text:
        return []
    ignored_patterns = [
        r"^fsck\.fat\s+\d+",
        r"^.*:\s+\d+\s+files,\s+\d+/\d+\s+clusters$",
        r"^0x[0-9a-fA-F]+:\s+Dirty bit is set\.",
        r"^Dirty bit is set\.\s*Fs was not properly unmounted and some data may be corrupt\.",
        r"^Fs was not properly unmounted and some data may be corrupt\.",
        r"^Automatically removing dirty bit\.",
        r"^There are differences between boot sector and its backup\.",
        r"^This is mostly harmless\.\s*Differences:\s*\(offset:original/backup\)",
        r"^Differences:\s*\(offset:original/backup\)",
        r"^\s*\d+:\s*[0-9a-fA-F]+/[0-9a-fA-F]+\s*$",
        r"^Free cluster summary wrong\s*\(\d+\s+vs\.\s+really\s+\d+\)",
        r"^\s*Auto-correcting\.",
        r"^\s*Leaving filesystem unchanged\.",
        r"^Reclaimed\s+\d+\s+unused cluster.*",
        r"^[1-3]\)\s+Copy\s+",
        r"^[1-3]\)\s+No action",
        r"^\[No action\]",
        r"^\s*Not automatically fixing this\.",
    ]
    anomalies = []
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        if any(re.search(pat, line) for pat in ignored_patterns):
            continue
        anomalies.append(line)
    return anomalies

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

    def kill_proc(p):
        p.terminate()
        try:
            p.wait(timeout=2.0)
        except subprocess.TimeoutExpired:
            p.kill()
            p.wait()

    start_time = time.time()
    proc = subprocess.Popen(cmd)
    timed_out = False
    try:
        proc.wait(timeout=args.timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        kill_proc(proc)
    finally:
        if proc.poll() is None:
            kill_proc(proc)

    duration = round(time.time() - start_time, 2)
    log_content = read_log(log_path)
    lines = [l.strip() for l in log_content.splitlines() if l.strip()]
    last_line = lines[-1] if lines else "(empty log)"

    if timed_out:
        how_ended = f"killed after {args.timeout}s timeout"
    elif proc.returncode == 0:
        if any(re.fullmatch(r"\[\s*\d+\.\d+\] sysrq: Resetting", l) for l in lines):
            how_ended = "reset"
        elif any(re.fullmatch(r"\[\s*\d+\.\d+\] reboot: Power down", l) for l in lines):
            how_ended = "poweroff"
        else:
            how_ended = f"qemu exited with status {proc.returncode}"
    else:
        how_ended = f"qemu exited with status {proc.returncode}"

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

    raw_efi = mdir(disk, off, "::/EFI/Linux")
    raw_bios = mdir(disk, off, "::/vintner/bios", dirs=True)
    raw_staging = mdir(disk, off, "::/vintner/staging", dirs=True)

    mdir_anomalies = []
    efi_files = []
    for p in raw_efi:
        if not p.startswith("/EFI/Linux"):
            mdir_anomalies.append(f"mdir /EFI/Linux returned out-of-tree entry: {p}")
        efi_files.append(os.path.basename(p))
    efi_files = sorted(efi_files)

    bios_files = []
    for p in raw_bios:
        if not p.startswith("/vintner/bios"):
            mdir_anomalies.append(f"mdir /vintner/bios returned out-of-tree entry: {p}")
        bios_files.append(p)

    staging_files = []
    for p in raw_staging:
        if not p.startswith("/vintner/staging"):
            mdir_anomalies.append(f"mdir /vintner/staging returned out-of-tree entry: {p}")
        staging_files.append(p)

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
        ld_path = os.path.join(lib, "ld-linux-x86-64.so.2")
        grub_editenv_path = os.path.join(args.root, "usr/bin/grub-editenv")
        if os.path.isfile(ld_path) and os.path.isfile(grub_editenv_path):
            out = subprocess.run([ld_path, "--library-path", lib,
                                  grub_editenv_path, f"{prefix}-grubenv", "list"],
                                 capture_output=True, text=True, check=True).stdout
            with open(f"{prefix}-grubenv.txt", "w") as f:
                f.write(out)
            grubenv = dict(l.partition("=")[::2] for l in out.splitlines() if "=" in l)

    # fsck.fat -n on boot storage partition
    fsck_out_path = f"{prefix}-fsck.txt"
    if shutil.which("fsck.fat"):
        with tempfile.NamedTemporaryFile(suffix=".fat", delete=False) as tmp_part:
            tmp_path = tmp_part.name
            with open(disk, "rb") as f_disk:
                f_disk.seek(off)
                remaining = 512 * 1024 * 1024
                while remaining > 0:
                    chunk = f_disk.read(min(remaining, 4 * 1024 * 1024))
                    if not chunk:
                        break
                    tmp_part.write(chunk)
                    remaining -= len(chunk)
        try:
            r = subprocess.run(["fsck.fat", "-n", tmp_path], capture_output=True, text=True)
            with open(fsck_out_path, "w") as f:
                f.write(r.stdout + r.stderr)
        finally:
            if os.path.exists(tmp_path):
                os.remove(tmp_path)
    else:
        with open(fsck_out_path, "w") as f:
            f.write("")

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
        "mdir_anomalies": mdir_anomalies,
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

    op_meta = json.load(open(f"{prefix}-op-meta.json")) if os.path.isfile(f"{prefix}-op-meta.json") else None
    b1_meta = json.load(open(f"{prefix}-boot1-meta.json")) if os.path.isfile(f"{prefix}-boot1-meta.json") else None
    rec_meta = json.load(open(f"{prefix}-rec-op-meta.json")) if os.path.isfile(f"{prefix}-rec-op-meta.json") else None
    b2_meta = json.load(open(f"{prefix}-boot2-meta.json")) if os.path.isfile(f"{prefix}-boot2-meta.json") else None

    retained_file_path = f"{prefix}-retained-kernels.txt"
    retained_content = open(retained_file_path).read() if os.path.isfile(retained_file_path) else None

    p = 4 if case in ("G1", "G2") else 2

    # Check 6 pre-op GOOD set assertion
    pre_good = sorted(int(g) for g, img in pre.get("images", {}).items() if img.get("state") == "GOOD")
    expected_pre_good = [1, 2, 3] if case in ("G1", "G2") else [1]
    if pre_good != expected_pre_good:
        fail_ev = f"pre-op GOOD set is {pre_good}, expected exactly {expected_pre_good}"
        for ch in ("P1", "P2", "P3", "P4", "P5", "P6", "P7"):
            emit(results_file, ch, case, "FAIL", fail_ev)
        emit(results_file, "P8", case, "OBSERVED", fail_ev)
        emit(results_file, "P9", case, "FAIL", fail_ev)
        return

    # Check manifest readability across reads
    for r_lbl, r_data in [("pre", pre), ("op", op_read), ("boot1", b1_read), ("rec_op", rec_read), ("boot2", b2_read)]:
        if r_data.get("manifest") is None:
            fail_ev = f"manifest in read {r_lbl} is unreadable"
            for ch in ("P1", "P2", "P3", "P4", "P5", "P6", "P7"):
                emit(results_file, ch, case, "FAIL", fail_ev)
            emit(results_file, "P8", case, "OBSERVED", fail_ev)
            emit(results_file, "P9", case, "FAIL", fail_ev)
            return

    # --- P1 ---
    p1_ok = True
    p1_ev = []

    if op_meta is None:
        p1_ok = False
        p1_ev.append("op_meta missing")
    else:
        how = op_meta.get("how_ended", "")
        rc = op_meta.get("returncode")
        if "killed" in how or rc != 0:
            p1_ok = False
            p1_ev.append(f"op boot did not exit cleanly: {how} (rc={rc})")
        else:
            p1_ev.append(f"op boot ended: {how}")

    op_lines = op_log.splitlines()
    if case == "C0":
        tp_found = [l for l in op_lines if re.fullmatch(r"\[\s*\d+\.\d+\] vintner-testpoint:.*", l)]
        pub_found = [l for l in op_lines if l == "vintner publish: published generation 2"]
        if tp_found:
            p1_ok = False
            p1_ev.append(f"unexpected testpoint in C0: {tp_found[0]}")
        else:
            p1_ev.append("no testpoint triggered")
        if not pub_found:
            p1_ok = False
            p1_ev.append("missing published generation 2 in C0")
        else:
            p1_ev.append(pub_found[0])
        if op_meta and op_meta.get("how_ended") != "poweroff":
            p1_ok = False
            p1_ev.append(f"C0 expected poweroff, got {op_meta.get('how_ended')}")
    else:
        want_tp = CASE_TESTPOINTS[case].split("#")[0]
        tp_indices = [i for i, l in enumerate(op_lines) if re.fullmatch(r"\[\s*\d+\.\d+\] vintner-testpoint: " + re.escape(want_tp), l)]
        sysrq_indices = [i for i, l in enumerate(op_lines) if re.fullmatch(r"\[\s*\d+\.\d+\] sysrq: Resetting", l)]
        pub_indices = [i for i, l in enumerate(op_lines) if l == f"vintner publish: published generation {p}"]
        powerdown_indices = [i for i, l in enumerate(op_lines) if re.fullmatch(r"\[\s*\d+\.\d+\] reboot: Power down", l)]

        if len(tp_indices) != 1:
            p1_ok = False
            p1_ev.append(f"testpoint '{want_tp}' matched {len(tp_indices)} times (expected exactly 1)")
        else:
            p1_ev.append(op_lines[tp_indices[0]])

        if not sysrq_indices:
            p1_ok = False
            p1_ev.append("missing sysrq: Resetting")
        else:
            p1_ev.append(op_lines[sysrq_indices[0]])
            if tp_indices and not (tp_indices[0] < sysrq_indices[0]):
                p1_ok = False
                p1_ev.append("sysrq: Resetting appeared before testpoint")

        if case in ("G1", "G2"):
            if len(pub_indices) != 1:
                p1_ok = False
                p1_ev.append(f"published generation {p} matched {len(pub_indices)} times (expected exactly 1)")
            else:
                p1_ev.append(op_lines[pub_indices[0]])
                if tp_indices and not (pub_indices[0] < tp_indices[0]):
                    p1_ok = False
                    p1_ev.append("published line appeared after testpoint")
        else:
            if pub_indices:
                p1_ok = False
                p1_ev.append(f"unexpected publish line: {op_lines[pub_indices[0]]}")
            else:
                p1_ev.append(f"no published generation {p} line")

        if powerdown_indices:
            p1_ok = False
            p1_ev.append(f"unexpected reboot: Power down line found: {op_lines[powerdown_indices[0]]}")
        else:
            p1_ev.append("no reboot: Power down")

        if op_meta and op_meta.get("how_ended") != "reset":
            p1_ok = False
            p1_ev.append(f"expected reset, got {op_meta.get('how_ended')}")

        if case == "M2":
            staged_indices = [i for i, l in enumerate(op_lines) if l == "vintner publish: staged generation 2"]
            if len(staged_indices) != 1:
                p1_ok = False
                p1_ev.append(f"expected exactly one 'vintner publish: staged generation 2', found {len(staged_indices)}")
            else:
                p1_ev.append(op_lines[staged_indices[0]])
                staged_idx = staged_indices[0]
                repair_lines = [op_lines[i] for i in range(staged_idx)
                                if op_lines[i].startswith("REPORT:") or
                                   op_lines[i].startswith("ACTION:") or
                                   re.match(r"^generation \d+: \S+ -> \S+$", op_lines[i])]
                if repair_lines:
                    p1_ok = False
                    p1_ev.append(f"unexpected step 4 repair before staging: {repair_lines[0]}")
                else:
                    p1_ev.append("no step 4 repair before staging")
        elif case == "M1":
            if tp_indices:
                tp_idx = tp_indices[0]
                repair_lines = [op_lines[i] for i in range(tp_idx)
                                if op_lines[i].startswith("REPORT:") or
                                   op_lines[i].startswith("ACTION:") or
                                   re.match(r"^generation \d+: \S+ -> \S+$", op_lines[i])]
                if repair_lines:
                    p1_ok = False
                    p1_ev.append(f"unexpected step 4 repair before testpoint: {repair_lines[0]}")
                else:
                    p1_ev.append("no step 4 repair before testpoint")
            staged_indices = [i for i, l in enumerate(op_lines) if l == "vintner publish: staged generation 2"]
            if staged_indices:
                p1_ok = False
                p1_ev.append(f"unexpected staged generation 2 line before testpoint: {op_lines[staged_indices[0]]}")

    emit(results_file, "P1", case, "PASS" if p1_ok else "FAIL", "; ".join(p1_ev))

    # Identify boots
    lbl_b1 = identify_booted(b1_log, fw, op_read, b1_read)
    lbl_b2 = identify_booted(b2_log, fw, rec_read, b2_read)

    # --- P2 ---
    if case == "C0":
        want_b1 = f"os-{p}"
    elif case in ("T1", "T2", "T3", "M1"):
        want_b1 = "os-1"
    elif case == "T4":
        want_b1 = f"os-{p}" if fw == "uefi" else "os-1"
    elif case in ("T5", "T6", "M2", "G1", "G2"):
        want_b1 = f"os-{p}"

    p2_ok = (lbl_b1 == want_b1) and (lbl_b1 not in ("recovery-a", "recovery-b", "recovery", "unknown"))
    emit(results_file, "P2", case, "PASS" if p2_ok else "FAIL", f"boot1 booted {lbl_b1}, expected {want_b1}")

    # --- P3 ---
    b1_gen = int(lbl_b1.replace("os-", "")) if lbl_b1.startswith("os-") else -1
    p3_ok = True
    p3_ev = []
    if b1_gen < 0:
        p3_ok = False
        p3_ev.append(f"unknown booted generation: {lbl_b1}")
    else:
        if str(p) in pre.get("images", {}):
            p3_ok = False
            p3_ev.append(f"generation {p} already had record before case")
        else:
            p3_ev.append(f"generation {p} had no record before case")

        b1_img = b1_read.get("images", {}).get(str(b1_gen))
        if not b1_img or b1_img.get("state") != "GOOD":
            p3_ok = False
            p3_ev.append(f"gen {b1_gen} state is {b1_img.get('state') if b1_img else 'missing'}, expected GOOD")
        else:
            p3_ev.append(f"gen {b1_gen} state=GOOD")

        lg_after = parse_time(b1_img.get("last_good") if b1_img else None)
        pre_img = pre.get("images", {}).get(str(b1_gen))
        if lg_after is None:
            p3_ok = False
            p3_ev.append("boot1 last_good cannot be parsed")
        elif pre_img is not None:
            lg_before = parse_time(pre_img.get("last_good"))
            if lg_before is None:
                p3_ok = False
                p3_ev.append("pre-case last_good exists but cannot be parsed")
            elif not (lg_after > lg_before):
                p3_ok = False
                p3_ev.append(f"last_good not advanced: before={lg_before} after={lg_after}")
            else:
                p3_ev.append(f"last_good advanced ({lg_before} -> {lg_after})")
        else:
            p3_ev.append(f"last_good set ({lg_after})")

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
    p4_ok = True
    p4_ev = []

    good_before = [int(g) for g, img in pre.get("images", {}).items() if img.get("state") == "GOOD"]
    if case in ("G1", "G2"):
        good_before = [g for g in good_before if g != 1]

    for g in sorted(good_before):
        pre_rec = pre["images"][str(g)]
        for r_name, r_data in [("pre", pre), ("op", op_read), ("boot1", b1_read), ("rec_op", rec_read), ("boot2", b2_read)]:
            r_img = r_data.get("images", {}).get(str(g))
            if r_img is not None:
                if r_img.get("state") != "GOOD":
                    p4_ok = False
                    p4_ev.append(f"gen {g} in {r_name} state={r_img.get('state')}, expected GOOD")
                want_uefi_sha = (pre_rec.get("uefi") or {}).get("sha256")
                if want_uefi_sha:
                    ukis = uki_names(r_data, g)
                    if len(ukis) != 1:
                        p4_ok = False
                        p4_ev.append(f"gen {g} in {r_name} UKIs={ukis}, expected exactly 1")
                    else:
                        uki_path = f"/EFI/Linux/{ukis[0]}"
                        got_sha = (r_data.get("file_hashes") or {}).get(uki_path)
                        if got_sha != want_uefi_sha:
                            p4_ok = False
                            p4_ev.append(f"gen {g} in {r_name} UKI {ukis[0]} sha mismatch (got {got_sha}, want {want_uefi_sha})")
                bios_files_dict = (pre_rec.get("bios") or {}).get("files", {})
                for fname, want_sha in bios_files_dict.items():
                    fpath = f"/vintner/bios/os-{g}/{fname}"
                    got_sha = (r_data.get("file_hashes") or {}).get(fpath)
                    if got_sha != want_sha:
                        p4_ok = False
                        p4_ev.append(f"gen {g} in {r_name} {fname} sha mismatch (got {got_sha}, want {want_sha})")
            else:
                removed_line = f"vintner publish: removed generation {g}"
                if any(l == removed_line for l in rec_log.splitlines()):
                    p4_ev.append(f"gen {g} removed in {r_name} with recovery publish log: {removed_line}")
                else:
                    p4_ok = False
                    p4_ev.append(f"gen {g} missing in {r_name} without '{removed_line}' in recovery publish log")

    if p4_ok:
        p4_ev.append(f"images {good_before} still GOOD with matching 64-char hashes at all reads (or properly removed)")
    emit(results_file, "P4", case, "PASS" if p4_ok else "FAIL", "; ".join(p4_ev))

    # --- P5 ---
    p5_ok = True
    p5_ev = []

    b1_next = b1_read.get("next_generation")
    if case in ("G1", "G2"):
        want_next = 5
    elif case == "M1":
        want_next = None
    else:
        want_next = 3

    if want_next is not None:
        if b1_next != want_next:
            p5_ok = False
            p5_ev.append(f"next_generation={b1_next}, expected {want_next}")
        else:
            p5_ev.append(f"next_generation={b1_next}")

    if case in ("T1", "T2", "T3", "M1"):
        stg = b1_read.get("staging_files", [])
        if stg:
            p5_ok = False
            p5_ev.append(f"staging not empty: {stg}")
        else:
            p5_ev.append("staging empty")

    grubenv = b1_read.get("grubenv")
    if grubenv is None:
        p5_ok = False
        p5_ev.append("grubenv unreadable")
        order_list = []
    else:
        order_list = grubenv.get("VINTNER_ORDER", "").split()

    if case == "C0":
        if fw == "bios":
            if order_list != ["2", "1"]:
                p5_ok = False
                p5_ev.append(f"VINTNER_ORDER={order_list}, expected ['2', '1']")
            else:
                p5_ev.append("VINTNER_ORDER=['2', '1']")
        else:
            if not order_list or order_list[0] != "2":
                p5_ok = False
                p5_ev.append(f"VINTNER_ORDER={order_list}, expected '2' first")
            else:
                p5_ev.append("VINTNER_ORDER starts with 2")
        c2 = (grubenv or {}).get("VINTNER_T_2")
        if c2 != "xxx":
            p5_ok = False
            p5_ev.append(f"VINTNER_T_2={c2}, expected xxx")
        else:
            p5_ev.append("VINTNER_T_2=xxx")

        p_img = b1_read.get("images", {}).get(str(p))
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")

    elif case in ("T1", "T2", "T3"):
        p_img = b1_read.get("images", {}).get(str(p))
        if p_img is not None:
            p5_ok = False
            p5_ev.append(f"gen {p} record still present: {p_img}")
        else:
            p5_ev.append(f"gen {p} removed")

        if order_list != ["1"]:
            p5_ok = False
            p5_ev.append(f"VINTNER_ORDER={order_list}, expected ['1']")
        else:
            p5_ev.append("VINTNER_ORDER=['1']")
        if "VINTNER_T_2" in (grubenv or {}):
            p5_ok = False
            p5_ev.append(f"VINTNER_T_2 unexpectedly present: {grubenv['VINTNER_T_2']}")
        else:
            p5_ev.append("VINTNER_T_2 absent")

    elif case == "T4":
        if order_list != ["1"]:
            p5_ok = False
            p5_ev.append(f"VINTNER_ORDER={order_list}, expected ['1']")
        else:
            p5_ev.append("VINTNER_ORDER=['1']")
        if "VINTNER_T_2" in (grubenv or {}):
            p5_ok = False
            p5_ev.append(f"VINTNER_T_2 unexpectedly present: {grubenv['VINTNER_T_2']}")
        else:
            p5_ev.append("VINTNER_T_2 absent")

        p_img = b1_read.get("images", {}).get(str(p))
        if fw == "uefi":
            if not p_img or p_img.get("state") != "GOOD":
                p5_ok = False
                p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
            else:
                p5_ev.append(f"gen {p} GOOD")
            ukis = uki_names(b1_read, p)
            if ukis != [f"vintner-os-{p}+3-0.efi"]:
                p5_ok = False
                p5_ev.append(f"gen {p} UKIs={ukis}, expected [vintner-os-{p}+3-0.efi]")
            else:
                p5_ev.append(f"gen {p} UKI +3-0 in place")
            r7_matches = [l for l in b1_log.splitlines() if re.fullmatch(r"\[\s*\d+\.\d+\] vintner\[\d+\]: generation 2 has BIOS counter \"\"", l)]
            if not r7_matches:
                p5_ok = False
                p5_ev.append("missing rule 7 report: generation 2 has BIOS counter \"\"")
            else:
                p5_ev.append(r7_matches[0].strip())
        else:
            if not p_img or p_img.get("state") != "STAGED":
                p5_ok = False
                p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected STAGED")
            else:
                p5_ev.append(f"gen {p} STAGED")
            if "/vintner/bios/os-2/" not in b1_read.get("bios_files", []):
                p5_ok = False
                p5_ev.append("entry '/vintner/bios/os-2/' not present in bios_files")
            else:
                p5_ev.append("'/vintner/bios/os-2/' present")
            r6_matches = [l for l in b1_log.splitlines() if re.fullmatch(r"\[\s*\d+\.\d+\] vintner\[\d+\]: generation 2 is STAGED and in place but does not verify", l)]
            if not r6_matches:
                p5_ok = False
                p5_ev.append("missing rule 6 report: generation 2 is STAGED and in place but does not verify")
            else:
                p5_ev.append(r6_matches[0].strip())

    elif case in ("T5", "T6"):
        p_img = b1_read.get("images", {}).get(str(p))
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")
        if not order_list or order_list[0] != "2":
            p5_ok = False
            p5_ev.append(f"VINTNER_ORDER={order_list}, expected '2' first")
        else:
            p5_ev.append("VINTNER_ORDER starts with 2")
        c2 = (grubenv or {}).get("VINTNER_T_2")
        if c2 != "xxx":
            p5_ok = False
            p5_ev.append(f"VINTNER_T_2={c2}, expected xxx")
        else:
            p5_ev.append("VINTNER_T_2=xxx")

    elif case == "M1":
        p_img = b1_read.get("images", {}).get(str(p))
        if p_img is not None:
            p5_ok = False
            p5_ev.append(f"gen 2 record still present after boot1: {p_img}")
        op_has_p = "2" in op_read.get("images", {})
        op_next = op_read.get("next_generation")
        if op_has_p:
            if op_next != 3 or b1_next != 3:
                p5_ok = False
                p5_ev.append(f"M1 disagreement: op has gen 2 (next={op_next}), b1 next={b1_next}")
            else:
                p5_ev.append("M1 consistent (save reached disk): op next=3, b1 gen 2 removed by rule 2 with next=3")
        else:
            if op_next != 2 or b1_next != 2:
                p5_ok = False
                p5_ev.append(f"M1 disagreement: op lacks gen 2 (next={op_next}), b1 next={b1_next}")
            else:
                p5_ev.append("M1 consistent (save did not reach disk): op next=2, b1 gen 2 absent with next=2")

        if order_list != ["1"]:
            p5_ok = False
            p5_ev.append(f"VINTNER_ORDER={order_list}, expected ['1']")
        else:
            p5_ev.append("VINTNER_ORDER=['1']")
        if "VINTNER_T_2" in (grubenv or {}):
            p5_ok = False
            p5_ev.append(f"VINTNER_T_2 unexpectedly present: {grubenv['VINTNER_T_2']}")
        else:
            p5_ev.append("VINTNER_T_2 absent")

    elif case == "M2":
        p_img = b1_read.get("images", {}).get(str(p))
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")
        picked_after_op = op_read.get("picked_copy")
        if picked_after_op not in ("manifest-a", "manifest-b"):
            p5_ok = False
            p5_ev.append(f"picked_copy={picked_after_op}, expected manifest-a or manifest-b")
        else:
            p5_ev.append(f"Load picked {picked_after_op}")

        if not order_list or order_list[0] != "2":
            p5_ok = False
            p5_ev.append(f"VINTNER_ORDER={order_list}, expected '2' first")
        else:
            p5_ev.append("VINTNER_ORDER starts with 2")
        c2 = (grubenv or {}).get("VINTNER_T_2")
        if c2 != "xxx":
            p5_ok = False
            p5_ev.append(f"VINTNER_T_2={c2}, expected xxx")
        else:
            p5_ev.append("VINTNER_T_2=xxx")

    elif case == "G1":
        p_img = b1_read.get("images", {}).get(str(p))
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")
        if "1" in b1_read.get("images", {}):
            p5_ok = False
            p5_ev.append("gen 1 still in manifest")
        else:
            p5_ev.append("gen 1 has no manifest record")

        u1 = uki_names(b1_read, 1)
        b1_slot = "/vintner/bios/os-1/" in b1_read.get("bios_files", []) or any(f.startswith("/vintner/bios/os-1/") for f in b1_read.get("bios_files", []))
        if not u1 or not b1_slot:
            p5_ok = False
            p5_ev.append(f"gen 1 files missing: uki={u1}, bios={b1_slot}")
        else:
            p5_ev.append("gen 1 files remain")

        r3_uefi = [l for l in b1_log.splitlines() if re.fullmatch(r"\[\s*\d+\.\d+\] vintner\[\d+\]: untracked uefi file vintner-os-1(\+\d+-\d+)?\.efi", l)]
        r3_bios = [l for l in b1_log.splitlines() if re.fullmatch(r"\[\s*\d+\.\d+\] vintner\[\d+\]: untracked bios directory os-1", l)]
        if not r3_uefi:
            p5_ok = False
            p5_ev.append("missing rule 3 untracked uefi file report")
        else:
            p5_ev.append(r3_uefi[0].strip())
        if not r3_bios:
            p5_ok = False
            p5_ev.append("missing rule 3 untracked bios directory report")
        else:
            p5_ev.append(r3_bios[0].strip())

        if order_list != ["4", "3", "2", "1"]:
            p5_ok = False
            p5_ev.append(f"VINTNER_ORDER={order_list}, expected ['4', '3', '2', '1']")
        else:
            p5_ev.append("VINTNER_ORDER=['4', '3', '2', '1']")
        c4 = (grubenv or {}).get("VINTNER_T_4")
        if c4 != "xxx":
            p5_ok = False
            p5_ev.append(f"VINTNER_T_4={c4}, expected xxx")
        else:
            p5_ev.append("VINTNER_T_4=xxx")

    elif case == "G2":
        p_img = b1_read.get("images", {}).get(str(p))
        if not p_img or p_img.get("state") != "GOOD":
            p5_ok = False
            p5_ev.append(f"gen {p} state={p_img.get('state') if p_img else 'missing'}, expected GOOD")
        else:
            p5_ev.append(f"gen {p} GOOD")
        if "1" in b1_read.get("images", {}):
            p5_ok = False
            p5_ev.append("gen 1 still in manifest")
        else:
            p5_ev.append("gen 1 has no manifest record")

        u1 = uki_names(b1_read, 1)
        b1_slot = "/vintner/bios/os-1/" in b1_read.get("bios_files", []) or any(f.startswith("/vintner/bios/os-1/") for f in b1_read.get("bios_files", []))
        if not u1 or not b1_slot:
            p5_ok = False
            p5_ev.append(f"gen 1 files missing: uki={u1}, bios={b1_slot}")
        else:
            p5_ev.append("gen 1 files remain")

        r3_uefi = [l for l in b1_log.splitlines() if re.fullmatch(r"\[\s*\d+\.\d+\] vintner\[\d+\]: untracked uefi file vintner-os-1(\+\d+-\d+)?\.efi", l)]
        r3_bios = [l for l in b1_log.splitlines() if re.fullmatch(r"\[\s*\d+\.\d+\] vintner\[\d+\]: untracked bios directory os-1", l)]
        if not r3_uefi:
            p5_ok = False
            p5_ev.append("missing rule 3 untracked uefi file report")
        else:
            p5_ev.append(r3_uefi[0].strip())
        if not r3_bios:
            p5_ok = False
            p5_ev.append("missing rule 3 untracked bios directory report")
        else:
            p5_ev.append(r3_bios[0].strip())

        if order_list != ["4", "3", "2"]:
            p5_ok = False
            p5_ev.append(f"VINTNER_ORDER={order_list}, expected ['4', '3', '2']")
        else:
            p5_ev.append("VINTNER_ORDER=['4', '3', '2']")
        if "VINTNER_T_1" in (grubenv or {}):
            p5_ok = False
            p5_ev.append(f"VINTNER_T_1 unexpectedly present: {grubenv['VINTNER_T_1']}")
        else:
            p5_ev.append("VINTNER_T_1 absent")
        c4 = (grubenv or {}).get("VINTNER_T_4")
        if c4 != "xxx":
            p5_ok = False
            p5_ev.append(f"VINTNER_T_4={c4}, expected xxx")
        else:
            p5_ev.append("VINTNER_T_4=xxx")

    # Item 20 fsck.fat clean check after boot 1
    b1_fsck_path = f"{prefix}-read-boot1-fsck.txt"
    b1_fsck_text = open(b1_fsck_path).read() if os.path.isfile(b1_fsck_path) else ""
    fsck_anoms = parse_fsck_anomalies(b1_fsck_text)
    if fsck_anoms:
        p5_ok = False
        p5_ev.append(f"boot1 fsck corruption: {'; '.join(fsck_anoms)}")
    else:
        p5_ev.append("boot1 fsck clean")

    emit(results_file, "P5", case, "PASS" if p5_ok else "FAIL", "; ".join(p5_ev))

    # --- P6 ---
    p6_ok = True
    p6_ev = []
    want_rec_gen = b1_read.get("next_generation")
    rec_lines = rec_log.splitlines()

    has_op_exit = any(l == "=== OP EXIT: 0 ===" for l in rec_lines)
    want_pub_line = f"vintner publish: published generation {want_rec_gen}"
    has_pub_line = any(l == want_pub_line for l in rec_lines)

    if not has_op_exit:
        p6_ok = False
        p6_ev.append("recovery publish missing whole line '=== OP EXIT: 0 ==='")
    else:
        p6_ev.append("OP EXIT: 0")

    if not has_pub_line:
        p6_ok = False
        p6_ev.append(f"recovery publish missing whole line '{want_pub_line}'")
    else:
        p6_ev.append(want_pub_line)

    want_b2 = f"os-{want_rec_gen}"
    if lbl_b2 != want_b2:
        p6_ok = False
        p6_ev.append(f"boot2 booted {lbl_b2}, expected {want_b2}")
    else:
        p6_ev.append(f"boot2 booted {lbl_b2}")

    rec_b2_img = b2_read.get("images", {}).get(str(want_rec_gen))
    if not rec_b2_img or rec_b2_img.get("state") != "GOOD":
        p6_ok = False
        p6_ev.append(f"gen {want_rec_gen} after boot2 state={rec_b2_img.get('state') if rec_b2_img else 'missing'}, expected GOOD")
    else:
        p6_ev.append(f"gen {want_rec_gen} GOOD")

    if fw == "uefi":
        ukis = uki_names(b2_read, want_rec_gen)
        want_uki = f"vintner-os-{want_rec_gen}+3-0.efi"
        if ukis != [want_uki]:
            p6_ok = False
            p6_ev.append(f"boot2 UKIs={ukis}, expected [{want_uki}]")
        else:
            p6_ev.append(f"boot2 UKI={want_uki}")
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
    p7_ok = True
    p7_ev = []
    rec_mf = rec_read.get("manifest")
    if not rec_mf:
        p7_ok = False
        p7_ev.append("recovery manifest missing or unreadable")
    else:
        images_list = rec_mf.get("images", [])
        expected_pkgs = []
        for img in images_list:
            pkg = (img.get("kernel") or {}).get("package")
            if not pkg:
                p7_ok = False
                p7_ev.append(f"image {img.get('generation')} missing kernel package")
            else:
                expected_pkgs.append(pkg)
        expected_pkgs = sorted(set(expected_pkgs))
        want_entries = expected_retained(expected_pkgs)
        got_entries = parse_retained_file(retained_content)

        if got_entries is None or got_entries != want_entries:
            p7_ok = False
            p7_ev.append(f"retained entries={got_entries}, expected={want_entries}")
        else:
            p7_ev.append(f"retained packages {expected_pkgs} match manifest")

    emit(results_file, "P7", case, "PASS" if p7_ok else "FAIL", "; ".join(p7_ev))

    # --- P8 ---
    p8_items = []
    for log_name, log_text in [("boot1", b1_log), ("rec_op", rec_log), ("boot2", b2_log)]:
        for l in log_text.splitlines():
            l_str = l.strip()
            if any(k in l_str for k in ["REPORT:", "ACTION:", "untracked", "does not verify", "missing files", "-> removed", "-> GOOD", "-> TRYING"]):
                p8_items.append(f"[{log_name}] {l_str}")

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

    for r_name, r_data in [("pre", pre), ("op", op_read), ("boot1", b1_read), ("rec_op", rec_read), ("boot2", b2_read)]:
        anoms = r_data.get("mdir_anomalies", [])
        for a in anoms:
            p8_items.append(f"[{r_name} mdir anomaly] {a}")

    for r_name in ["pre", "op", "boot1", "rec_op", "boot2"]:
        fsck_path = f"{prefix}-read-{r_name}-fsck.txt"
        fsck_text = open(fsck_path).read() if os.path.isfile(fsck_path) else ""
        anoms = parse_fsck_anomalies(fsck_text)
        for a in anoms:
            p8_items.append(f"[{r_name} fsck anomaly] {a}")

    p8_ev = "; ".join(p8_items) if p8_items else "none"
    emit(results_file, "P8", case, "OBSERVED", p8_ev)

    # --- P9 ---
    p9_ok = True
    p9_ev = []
    boots = [
        ("op", op_log, pre, op_read),
        ("boot1", b1_log, op_read, b1_read),
        ("rec_op", rec_log, b1_read, rec_read),
        ("boot2", b2_log, rec_read, b2_read),
    ]
    for b_name, b_log, prev_r, curr_r in boots:
        if not b_log:
            p9_ok = False
            p9_ev.append(f"{b_name}=missing log")
        else:
            lbl = identify_booted(b_log, fw, prev_r, curr_r)
            if not re.fullmatch(r"os-\d+", lbl):
                p9_ok = False
                p9_ev.append(f"{b_name}={lbl} (invalid)")
            else:
                p9_ev.append(f"{b_name}={lbl}")

    emit(results_file, "P9", case, "PASS" if p9_ok else "FAIL", "; ".join(p9_ev))

def cmd_evaluate(args):
    with open(args.results_file, "w") as f:
        f.write("ID\tCase\tStatus\tEvidence\n")

    for case in CASES:
        evaluate_case(case, args.firmware, args.out_dir, args.results_file)

    print("\nEvaluation complete.")

def cmd_check_results(args):
    results_file = args.results_file
    if not os.path.isfile(results_file):
        sys.exit(f"check-results FAIL: {results_file} not found")

    expected_pairs = {(ch, cs) for ch in CHECKS for cs in CASES}
    actual_rows = {}
    duplicates = []
    unexpected = []

    with open(results_file, "r") as f:
        for line_no, line in enumerate(f, 1):
            line = line.strip()
            if not line or line.startswith("ID\t") or line.startswith("id\t"):
                continue
            parts = line.split("\t")
            if len(parts) < 3:
                sys.exit(f"check-results FAIL: line {line_no} malformed: {line}")
            ch, cs, status = parts[0], parts[1], parts[2]
            pair = (ch, cs)
            if pair in actual_rows:
                duplicates.append(pair)
            if pair not in expected_pairs:
                unexpected.append(pair)
            actual_rows[pair] = status

    missing = expected_pairs - set(actual_rows.keys())
    bad_status = []
    for pair, status in actual_rows.items():
        ch, cs = pair
        if ch == "P8":
            if status != "OBSERVED":
                bad_status.append(f"{ch}:{cs} has status {status}, expected OBSERVED")
        elif (ch, cs) == ("P5", "T4"):
            if status not in ("PASS", "FAIL"):
                bad_status.append(f"{ch}:{cs} has status {status}, expected PASS or FAIL")
        else:
            if status != "PASS":
                bad_status.append(f"{ch}:{cs} has status {status}, expected PASS")

    errors = []
    if duplicates:
        errors.append(f"Duplicate rows: {sorted(duplicates)}")
    if unexpected:
        errors.append(f"Unexpected rows: {sorted(unexpected)}")
    if missing:
        errors.append(f"Missing rows: {sorted(missing)}")
    if bad_status:
        errors.append(f"Failing checks: {bad_status}")

    if errors:
        for err in errors:
            print(f"ERROR: {err}", file=sys.stderr)
        sys.exit(1)

    print(f"check-results PASS: all {len(expected_pairs)} checks present with expected status.")

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

    p_check = sub.add_parser("check-results")
    p_check.add_argument("--results-file", required=True)

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
    elif args.cmd == "check-results":
        cmd_check_results(args)

if __name__ == "__main__":
    main()
