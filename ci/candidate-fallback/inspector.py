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

CASES = ["G0", "B1", "B2", "B3", "B4"]
BOOTS = range(1, 7)
EXPECTED_LABELS = {"G0": ["os-2"] * 6}
EXPECTED_LABELS.update({c: ["os-2"] * 3 + ["os-1"] * 3 for c in CASES[1:]})
B4_KILLED = "killed 1s after Command line"
UKI_RE = re.compile(r"vintner-os-(\d+)(\+\d+(-\d+)?)?\.efi")
# Whole-value maps from what the loader reports to a boot label.
LOADER_STEMS = {"vintner-os-1": "os-1", "vintner-os-2": "os-2",
                "vintner-recovery-a": "recovery-a", "vintner-recovery-b": "recovery-b"}
GRUB_DIRS = {"os-1": "os-1", "os-2": "os-2", "recovery-a": "recovery-a", "recovery-b": "recovery-b"}

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
    for l in r.stdout.decode('utf-8', errors='replace').splitlines():
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
            raise RuntimeError(f"Neither manifest-a nor manifest-b found on boot storage (disk={disk})")
        if a:
            with open(os.path.join(tmpdir, "manifest-a"), "wb") as f:
                f.write(a)
        if b:
            with open(os.path.join(tmpdir, "manifest-b"), "wb") as f:
                f.write(b)
        res = subprocess.run([vintner_bin, "manifest", "show", "--state-dir", tmpdir], capture_output=True, text=True)
        if res.returncode != 0:
            raise RuntimeError(f"vintner manifest show failed: {res.stderr}")
        return json.loads(res.stdout)

def emit(results_file, check_id, case, status, evidence):
    evidence = re.sub(r"[\t\r\n]", " ", evidence)
    row = f"{check_id}\t{case}\t{status}\t{evidence}"
    with open(results_file, "a") as f:
        f.write(row + "\n")
    print(row)

# --- 1. verify-prep ---------------------------------------------------------
def cmd_verify_prep(args):
    mf = load_manifest_via_vintner(args.vintner_bin, args.disk, args.bs_offset)
    images = mf.get("images", [])
    if len(images) != 1 or images[0]["generation"] != 1 or images[0]["state"] != "GOOD":
        sys.exit(f"verify-prep FAIL: expected exactly one image os-1 GOOD, got: {images}")

    staging = mdir(args.disk, args.bs_offset, "::/vintner/staging")
    if staging:
        sys.exit(f"verify-prep FAIL: staging is not empty: {staging}")

    print("verify-prep PASS: disk has exactly one image os-1 GOOD and empty staging")

# --- 2. run-boot ------------------------------------------------------------
def cmd_run_boot(args):
    log_path = args.log
    # Truncate log file
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
        # QEMU exits 0 when the guest powers off (or reboots, with -no-reboot).
        return "poweroff" if rc == 0 else f"qemu exited with status {rc}"

    def kill():
        proc.terminate()
        try:
            proc.wait(timeout=2.0)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()

    start_time = time.time()
    proc = subprocess.Popen(cmd)
    try:
        if args.kill_on_cmdline:
            # B4: watch the log for the kernel's "Command line:", wait 1 second, then kill
            how_ended = None
            while how_ended is None:
                rc = proc.poll()
                if rc is not None:
                    how_ended = exited(rc) + " before Command line"
                elif time.time() - start_time > args.timeout:
                    kill()
                    how_ended = f"killed after {args.timeout}s timeout"
                else:
                    with open(log_path, "rb") as f:
                        if b"Command line:" in f.read():
                            time.sleep(1.0)
                            rc = proc.poll()
                            if rc is not None:
                                how_ended = f"exited before kill ({exited(rc)})"
                            else:
                                kill()
                                how_ended = B4_KILLED
                    time.sleep(0.05)
        else:
            try:
                how_ended = exited(proc.wait(timeout=args.timeout))
            except subprocess.TimeoutExpired:
                kill()
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

# --- 3. read: one read of boot storage on the host (§3) ----------------------
def cmd_read(args):
    disk, off, prefix = args.disk, args.bs_offset, args.out_prefix
    mf = load_manifest_via_vintner(args.vintner_bin, disk, off)
    efi_files = sorted(os.path.basename(p) for p in mdir(disk, off, "::/EFI/Linux"))
    bios_files = mdir(disk, off, "::/vintner/bios")
    with open(f"{prefix}-efi-linux.txt", "w") as f:
        f.write("".join(n + "\n" for n in efi_files))
    with open(f"{prefix}-bios.txt", "w") as f:
        f.write("".join(p + "\n" for p in bios_files))

    # grubenv through grub-editenv from the trixie root vintner-base.sh unpacked (not installed on the runner)
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

    # Every os-1 file on boot storage, hashed: its UKI(s) and its BIOS slot directory
    os1_paths = [f"/EFI/Linux/{n}" for n in efi_files if (m := UKI_RE.fullmatch(n)) and m.group(1) == "1"]
    os1_paths += [p for p in bios_files if os.path.dirname(p) == "/vintner/bios/os-1"]
    os1_hashes = {}
    for p in os1_paths:
        content = mtype(disk, off, p)
        os1_hashes[p] = sha256_bytes(content) if content is not None else None

    read = {
        "images": {str(img["generation"]): img for img in mf.get("images", [])},
        "efi_files": efi_files,
        "bios_files": bios_files,
        "grubenv": grubenv,
        "os1_hashes": os1_hashes,
    }
    with open(f"{prefix}.json", "w") as f:
        json.dump(read, f, indent=2)
    print(f"read {prefix}: gens {sorted(read['images'])}; /EFI/Linux {efi_files}; grubenv {grubenv}")

# --- 4. Evaluation: a pure function from the recorded state to result rows ---
def uki_names(read, gen):
    return sorted(n for n in read["efi_files"] if (m := UKI_RE.fullmatch(n)) and int(m.group(1)) == gen)

def counter(read, firmware, gen):
    # The generation's counter where its firmware keeps it: UKI file names (UEFI) or VINTNER_T_<gen> (BIOS).
    if firmware == "uefi":
        return uki_names(read, gen)
    if read.get("grubenv") is None:
        return "(grubenv unreadable)"
    return read["grubenv"].get(f"VINTNER_T_{gen}")

def fmt_counter(firmware, gen, val):
    return f"gen {gen} UKIs={val}" if firmware == "uefi" else f"VINTNER_T_{gen}={val}"

def want_counter(firmware, uefi_name, bios_value):
    return [uefi_name] if firmware == "uefi" else bios_value

def booted_absent(read):
    env = read.get("grubenv")
    if env is None:
        return False, "grubenv unreadable"
    if "VINTNER_BOOTED" in env:
        return False, f"VINTNER_BOOTED={env['VINTNER_BOOTED']}"
    return True, "VINTNER_BOOTED absent"

def state_of(read, gen):
    img = read["images"].get(str(gen))
    return img.get("state") if img else "missing"

def parse_time(s):
    try:
        return datetime.fromisoformat(s[:-1] + "+00:00" if s.endswith("Z") else s)
    except (AttributeError, TypeError, ValueError):
        return None

def present(bt):
    # A boot is present only with its read, its run metadata and its serial log.
    return bool(bt) and bt.get("read") is not None and bt.get("meta") is not None and bt.get("log") is not None

def identify_booted(log, firmware, prev, curr):
    text = clean_ansi(log)

    # Recovery: whole tokens of the kernel's command line
    for line in re.findall(r"Command line: (.*)$", text, re.M):
        toks = line.split()
        if "vintner.recovery=1" in toks or "vintner.recovery_test=1" in toks:
            if "vintner.test_slot=b" in toks:
                return "recovery-b"
            return "recovery-a" if "vintner.recovery_test=1" in toks else "recovery"

    if firmware == "bios":
        # GRUB's last "vintner: trying <dir>" line; <dir> is the slot directory (firmware/bios/grub.cfg)
        tries = re.findall(r"vintner: trying (\S+)", text)
        if not tries:
            return "unknown"
        return GRUB_DIRS.get(tries[-1], f"grub={tries[-1]}")

    # UEFI (a): the report's LOADER_ENTRY_SELECTED
    m = re.search(r"^LOADER_ENTRY_SELECTED:[ \t]*(.*?)[ \t]*$", text, re.M)
    if m and m.group(1):
        val = m.group(1)
        stem = re.fullmatch(r"(.+?)(\+\d+(-\d+)?)?\.efi", val)
        return LOADER_STEMS.get(stem.group(1) if stem else val, f"entry={val}")

    # UEFI (b): exactly one ordinary generation whose UKI file name changed since the previous read
    if prev is None or curr is None:
        return "unknown"
    def by_gen(read):
        res = {}
        for n in read["efi_files"]:
            mm = UKI_RE.fullmatch(n)
            if mm:
                res.setdefault(int(mm.group(1)), []).append(n)
        return {g: sorted(v) for g, v in res.items()}
    p, c = by_gen(prev), by_gen(curr)
    changed = [g for g in sorted(set(p) | set(c)) if p.get(g) != c.get(g)]
    return f"os-{changed[0]}" if len(changed) == 1 else "unknown"

def boot_labels(cs, firmware):
    labels, prev = {}, cs.get("op")
    for b in BOOTS:
        bt = cs["boots"].get(b)
        labels[b] = identify_booted(bt["log"], firmware, prev, bt["read"]) if present(bt) else "missing"
        prev = bt.get("read") if bt else None
    return labels

def boot_read(cs, b):
    bt = cs["boots"].get(b)
    return bt["read"] if present(bt) else None

def op_check(cs, firmware):
    # What the operation boot must have left: gen 2 a fresh TRYING candidate, os-1 GOOD and armed.
    op = cs.get("op")
    if op is None:
        return ["read missing"]
    errs = []
    for gen, want_state, uefi_name in ((2, "TRYING", "vintner-os-2+3-0.efi"), (1, "GOOD", "vintner-os-1+3-0.efi")):
        s = state_of(op, gen)
        if s != want_state:
            errs.append(f"gen {gen} state={s}, expected {want_state}")
        c = counter(op, firmware, gen)
        if c != want_counter(firmware, uefi_name, "xxx"):
            errs.append(fmt_counter(firmware, gen, c))
    lg = (op["images"].get("1") or {}).get("last_good")
    if parse_time(lg) is None:
        errs.append(f"os-1 last_good={lg}, not a timestamp")
    return errs

def check_h1(case, cs, fw, labels):
    ok = all(labels[b] == "os-2" for b in BOOTS)
    ev = ["boots 1-6: " + ",".join(labels[b] for b in BOOTS)]
    rd = boot_read(cs, 1)
    if rd is None:
        return False, ev + ["boot1 read missing"]
    s, c = state_of(rd, 2), counter(rd, fw, 2)
    ok &= s == "GOOD" and c == want_counter(fw, "vintner-os-2+3-0.efi", "xxx")
    ev += [f"boot1 gen 2 state={s}", "boot1 " + fmt_counter(fw, 2, c)]
    if fw == "bios":
        absent, e = booted_absent(rd)
        ok &= absent
        ev.append("boot1 " + e)
    return ok, ev

def check_h2(case, cs, fw, labels):
    ok, ev = True, []
    for b, uefi_name, bios_value in ((1, "vintner-os-2+2-1.efi", "xx"), (2, "vintner-os-2+1-2.efi", "x"),
                                     (3, "vintner-os-2+0-3.efi", "0")):
        rd = boot_read(cs, b)
        if rd is None:
            ok = False
            ev.append(f"boot{b} missing")
            continue
        c = counter(rd, fw, 2)
        ok &= labels[b] == "os-2" and c == want_counter(fw, uefi_name, bios_value)
        ev.append(f"boot{b} {labels[b]} {fmt_counter(fw, 2, c)}")
    return ok, ev

def check_h3(case, cs, fw, labels):
    rd, op = boot_read(cs, 4), cs.get("op")
    if rd is None or op is None:
        return False, ["boot4 missing" if rd is None else "read after the operation boot missing"]
    s, c = state_of(rd, 1), counter(rd, fw, 1)
    before = (op["images"].get("1") or {}).get("last_good")
    after = (rd["images"].get("1") or {}).get("last_good")
    tb, ta = parse_time(before), parse_time(after)
    try:
        later = tb is not None and ta is not None and ta > tb
    except TypeError:
        later = False
    ok = labels[4] == "os-1" and s == "GOOD" and later and c == want_counter(fw, "vintner-os-1+3-0.efi", "xxx")
    ev = [f"boot4 {labels[4]}", f"os-1 state={s}", f"last_good before={before} after={after}", fmt_counter(fw, 1, c)]
    if fw == "bios":
        absent, e = booted_absent(rd)
        ok &= absent
        ev.append(e)
    return ok, ev

def check_h4(case, cs, fw, labels):
    rd = boot_read(cs, 4)
    if rd is None:
        return False, ["boot4 missing"]
    s, c = state_of(rd, 2), counter(rd, fw, 2)
    # journald forwards the unit's stdout to the console as "[ uptime] vintner[pid]: <line>"
    m = re.search(r"^\[ *\d+\.\d+\] vintner\[\d+\]: generation 2: TRYING -> FAILED$", clean_ansi(cs["boots"][4]["log"]), re.M)
    ok = s == "FAILED" and c == want_counter(fw, "vintner-os-2+0-3.efi", "0") and m is not None
    ev = [f"gen 2 state={s}", fmt_counter(fw, 2, c),
          f"console line: {m.group(0)}" if m else "no console line equal to '[<uptime>] vintner[<pid>]: generation 2: TRYING -> FAILED' in boot4 log"]
    return ok, ev

def check_h5(case, cs, fw, labels):
    rd4 = boot_read(cs, 4)
    if rd4 is None:
        return False, ["boot4 missing"]
    ok = True
    ev = [f"boot4 gen 2 UKIs={uki_names(rd4, 2)} VINTNER_T_2={(rd4.get('grubenv') or {}).get('VINTNER_T_2')} state={state_of(rd4, 2)}"]
    for b in (5, 6):
        rd = boot_read(cs, b)
        if rd is None:
            ok = False
            ev.append(f"boot{b} missing")
            continue
        same_names = uki_names(rd, 2) == uki_names(rd4, 2)
        same_t = fw != "bios" or counter(rd, fw, 2) == counter(rd4, fw, 2)
        same_rec = rd["images"].get("2") == rd4["images"].get("2")
        ok &= labels[b] == "os-1" and same_names and same_t and same_rec and state_of(rd, 2) == "FAILED"
        ev.append(f"boot{b} {labels[b]} gen 2 UKIs={uki_names(rd, 2)} VINTNER_T_2={(rd.get('grubenv') or {}).get('VINTNER_T_2')} "
                  f"state={state_of(rd, 2)} record {'equal to' if same_rec else 'differs from'} boot4")
    return ok, ev

def check_h6(case, cs, fw, labels):
    pre = cs.get("pre")
    rec = pre["images"].get("1") if pre else None
    if not rec:
        return False, ["os-1 record before the operation boot missing"]
    if not rec.get(fw):
        return False, [f"os-1 record has no {fw} half: {sorted(rec)}"]
    expected = []  # (path or None for the UKI, label, sha256 from the record)
    if rec.get("uefi"):
        expected.append((None, "uefi.sha256", rec["uefi"].get("sha256")))
    if rec.get("bios"):
        for name, sha in sorted((rec["bios"].get("files") or {}).items()):
            expected.append((f"{rec['bios'].get('dir')}/{name}", f"bios.files[{name}]", sha))
    ok, bad = True, []
    if rec.get("bios") and not rec["bios"].get("files"):
        ok = False
        bad.append(f"record bios.files={rec['bios'].get('files')}, empty")
    for _, label, sha in expected:
        if not re.fullmatch(r"[0-9a-f]{64}", sha or ""):
            ok = False
            bad.append(f"record {label}={sha}, not a SHA-256")
    reads = [("op", cs.get("op"))] + [(f"boot{b}", boot_read(cs, b)) for b in BOOTS]
    uki_seen = set()
    for rname, rd in reads:
        if rd is None:
            ok = False
            bad.append(f"{rname}: read missing")
            continue
        ukis = uki_names(rd, 1)
        for path, label, sha in expected:
            if path is None:
                if len(ukis) != 1:
                    ok = False
                    bad.append(f"{rname}: os-1 UKIs={ukis}, expected exactly one")
                    continue
                path = f"/EFI/Linux/{ukis[0]}"
                uki_seen.add(ukis[0])
            got = rd["os1_hashes"].get(path)
            if got != sha:
                ok = False
                bad.append(f"{rname}: {path} sha256={got}, record {label}={sha}")
    ev = [f"record {label}={sha}" for _, label, sha in expected]
    if uki_seen:
        ev.append(f"os-1 UKI names read: {sorted(uki_seen)}")
    ev.append(f"reads compared: {','.join(r for r, _ in reads)}")
    return ok, ev + bad

def check_h7(case, cs, fw, labels):
    ok, ends, detail = True, [], []
    for b in (1, 2, 3):
        bt = cs["boots"].get(b)
        if not present(bt):
            ok = False
            ends.append(f"boot{b}: missing")
            continue
        how = bt["meta"].get("how_ended")
        ends.append(f"boot{b}: {how}")
        text = clean_ansi(bt["log"])
        lines = [l.strip() for l in text.splitlines() if l.strip()]
        if not how or not lines:
            ok = False
            detail.append(f"boot{b}: {'no how_ended in meta' if not how else 'empty log'}")
            continue
        if case == "B1":
            m = re.search(r"^.*Kernel panic - not syncing: .*$", text, re.M)
            ok &= m is not None
            detail.append(f"boot{b}: {m.group(0).strip()}" if m else f"boot{b}: no panic line")
        elif case == "B2":
            for pat in (r"^.*Failed to start vintner-test-health\.service( - .*)?\.$",
                        r"^.*Dependency failed for boot-complete\.target( - .*)?\.$"):
                m = re.search(pat, text, re.M)
                ok &= m is not None
                detail.append(f"boot{b}: {m.group(0).strip()}" if m else f"boot{b}: no line matching {pat}")
        else:
            detail.append(f"boot{b} last line: {lines[-1]}")
            if case == "B4" and how != B4_KILLED:
                ok = False
    return ok, ends + detail

def check_h8(state, all_labels):
    ok, ev = True, []
    for case in CASES:
        labels = [all_labels[case][b] for b in BOOTS]
        ok &= labels == EXPECTED_LABELS[case]
        ev.append(f"{case} {','.join(labels)}")
    return ("H8", "all", "PASS" if ok else "FAIL", "; ".join(ev))

def evaluate(state):
    fw = state["firmware"]
    rows, all_labels = [], {}
    for case in CASES:
        cs = state["cases"].get(case) or {"boots": {}}
        all_labels[case] = labels = boot_labels(cs, fw)
        op_errs = op_check(cs, fw)
        checks = [("H1", check_h1)] if case == "G0" else \
            [("H2", check_h2), ("H3", check_h3), ("H4", check_h4), ("H5", check_h5), ("H6", check_h6), ("H7", check_h7)]
        for cid, fn in checks:
            ok, ev = fn(case, cs, fw, labels)
            if op_errs:
                ok = False
                ev = ["after the operation boot: " + ", ".join(op_errs)] + ev
            status = ("OBSERVED" if cid == "H7" else "PASS") if ok else "FAIL"
            rows.append((cid, case, status, "; ".join(ev)))
    rows.append(check_h8(state, all_labels))
    return rows

def load_state(out_dir, firmware):
    def load_json(p):
        if not os.path.isfile(p):
            return None
        with open(p) as f:
            return json.load(f)
    def load_text(p):
        return open(p, errors="replace").read() if os.path.isfile(p) else None
    return {"firmware": firmware, "cases": {c: {
        "pre": load_json(f"{out_dir}/{c}-read-pre.json"),
        "op": load_json(f"{out_dir}/{c}-read-op.json"),
        "boots": {b: {"read": load_json(f"{out_dir}/{c}-read-boot{b}.json"),
                      "meta": load_json(f"{out_dir}/{c}-boot{b}-meta.json"),
                      "log": load_text(f"{out_dir}/{c}-boot{b}.log")} for b in BOOTS},
    } for c in CASES}}

def cmd_evaluate(args):
    for row in evaluate(load_state(args.out_dir, args.firmware)):
        emit(args.results_file, *row)

# --- Main CLI ----------------------------------------------------------------
def main():
    parser = argparse.ArgumentParser(description="Candidate fallback integration test inspector")
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    p_prep = subparsers.add_parser("verify-prep")
    p_prep.add_argument("--disk", required=True)
    p_prep.add_argument("--bs-offset", type=int, required=True)
    p_prep.add_argument("--vintner-bin", required=True)
    p_prep.set_defaults(func=cmd_verify_prep)

    p_run = subparsers.add_parser("run-boot")
    p_run.add_argument("--firmware", choices=["uefi", "bios"], required=True)
    p_run.add_argument("--disk", required=True)
    p_run.add_argument("--seed", required=True)
    p_run.add_argument("--log", required=True)
    p_run.add_argument("--case", required=True)
    p_run.add_argument("--boot-num", type=int, required=True)
    p_run.add_argument("--vars", default="")
    p_run.add_argument("--code-fd", default="")
    p_run.add_argument("--timeout", type=int, default=120)
    p_run.add_argument("--kill-on-cmdline", action="store_true")
    p_run.add_argument("--meta-out", required=True)
    p_run.set_defaults(func=cmd_run_boot)

    p_read = subparsers.add_parser("read")
    p_read.add_argument("--disk", required=True)
    p_read.add_argument("--bs-offset", type=int, required=True)
    p_read.add_argument("--vintner-bin", required=True)
    p_read.add_argument("--root", required=True, help="trixie root unpacked by vintner-base.sh, for grub-editenv")
    p_read.add_argument("--out-prefix", required=True)
    p_read.set_defaults(func=cmd_read)

    p_eval = subparsers.add_parser("evaluate")
    p_eval.add_argument("--firmware", choices=["uefi", "bios"], required=True)
    p_eval.add_argument("--out-dir", required=True)
    p_eval.add_argument("--results-file", required=True)
    p_eval.set_defaults(func=cmd_evaluate)

    args = parser.parse_args()
    args.func(args)

if __name__ == "__main__":
    main()
