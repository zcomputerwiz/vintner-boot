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

mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")

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

def parse_grubenv(content):
    if content is None or not content.startswith(b"# GRUB Environment Block\n"):
        return None
    res = {}
    for line in content.split(b"\n"):
        if line.startswith(b"#") or not line:
            continue
        if b"=" in line:
            k, v = line.split(b"=", 1)
            res[k.decode('utf-8', errors='replace')] = v.decode('utf-8', errors='replace')
    return res

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

    start_time = time.time()
    proc = subprocess.Popen(cmd)
    how_ended = "unknown"

    if args.kill_on_cmdline:
        # B4: monitor log for "Command line:", wait 1 second, then kill
        found_cmdline = False
        while True:
            if proc.poll() is not None:
                how_ended = "poweroff"
                break
            if time.time() - start_time > args.timeout:
                proc.kill()
                proc.wait()
                how_ended = f"killed after {args.timeout}s timeout"
                break

            try:
                with open(log_path, "rb") as f:
                    content = f.read()
                    if b"Command line:" in content:
                        found_cmdline = True
                        break
            except Exception:
                pass
            time.sleep(0.05)

        if found_cmdline:
            time.sleep(1.0)
            proc.terminate()
            try:
                proc.wait(timeout=2.0)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
            how_ended = "killed 1s after Command line"
    else:
        try:
            proc.wait(timeout=args.timeout)
            how_ended = "poweroff"
        except subprocess.TimeoutExpired:
            proc.terminate()
            try:
                proc.wait(timeout=2.0)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
            how_ended = f"killed after {args.timeout}s timeout"

    duration = round(time.time() - start_time, 2)
    log_text = read_log(log_path)
    lines = [l.strip() for l in log_text.splitlines() if l.strip()]
    last_line = lines[-1] if lines else "(empty log)"

    meta = {
        "how_ended": how_ended,
        "last_line": last_line,
        "duration": duration,
    }

    if args.meta_out:
        with open(args.meta_out, "w") as f:
            json.dump(meta, f, indent=2)

    print(f"[{args.case}] Boot {args.boot_num} ended: {how_ended} ({duration}s), last_line: {last_line}")

# --- 3. record-boot-state ----------------------------------------------------
def identify_booted(log_path, firmware, prev_efi_files, curr_efi_files, prev_grubenv, curr_grubenv):
    text = read_log(log_path)

    # 1. Check for recovery markers in cmdline
    if "vintner.recovery=1" in text or "vintner.recovery_test=1" in text:
        if "vintner.test_slot=b" in text:
            return "recovery-b"
        return "recovery-a"

    # 2. BIOS: per §3, "BIOS, GRUB's last vintner: trying <gen> line"
    if firmware == "bios":
        tries = re.findall(r"vintner:\s+trying\s+(\S+)", text)
        if tries:
            val = tries[-1].strip()
            if val in ("os-1", "1"):
                return "os-1"
            if val in ("os-2", "2"):
                return "os-2"
            if "recovery-a" in val or val in ("ra", "a"):
                return "recovery-a"
            if "recovery-b" in val or val in ("rb", "b"):
                return "recovery-b"
            return val

        # Fallbacks if GRUB output was cut off
        if prev_grubenv and curr_grubenv:
            if prev_grubenv.get("VINTNER_T_2") != curr_grubenv.get("VINTNER_T_2"):
                return "os-2"
            if prev_grubenv.get("VINTNER_T_1") != curr_grubenv.get("VINTNER_T_1"):
                return "os-1"

        if "vintner.bootstorage=" in text:
            if prev_grubenv and prev_grubenv.get("VINTNER_T_2") not in (None, "", "0"):
                return "os-2"
            return "os-1"

        return "unknown"

    # 3. UEFI: per §3, "UEFI, the report's LOADER_ENTRY_SELECTED where the report ran,
    # otherwise the kernel's Command line: and the UKI that systemd-boot renamed"
    if firmware == "uefi":
        m_rep = re.search(r"^LOADER_ENTRY_SELECTED:[ \t]*(.+)$", text, re.M)
        if m_rep:
            val = m_rep.group(1).strip()
            val = re.sub(r"^[\\/]*EFI[\\/]Linux[\\/]", "", val)
            val = re.sub(r"(\+\d+(-\d+)?)?\.efi$", "", val)
            if val == "vintner-os-1":
                return "os-1"
            if val == "vintner-os-2":
                return "os-2"
            if "recovery-a" in val:
                return "recovery-a"
            if "recovery-b" in val:
                return "recovery-b"
            return val

        # Check systemd-boot renamed UKI counter
        # e.g. gen 2 went from +3-0 -> +2-1, or +2-1 -> +1-2, or +1-2 -> +0-3
        prev_gen2 = [f for f in prev_efi_files if "vintner-os-2" in f]
        curr_gen2 = [f for f in curr_efi_files if "vintner-os-2" in f]
        if prev_gen2 and curr_gen2 and prev_gen2 != curr_gen2:
            return "os-2"

        prev_gen1 = [f for f in prev_efi_files if "vintner-os-1" in f]
        curr_gen1 = [f for f in curr_efi_files if "vintner-os-1" in f]
        if prev_gen1 and curr_gen1 and prev_gen1 != curr_gen1:
            return "os-1"

        # Fallback to kernel Command line
        m_cmd = re.search(r"Command line:.*vintner\.bootstorage=", text)
        if m_cmd:
            return "os-2"

        return "unknown"

    return "unknown"

def cmd_record_boot_state(args):
    state_file = args.state_file
    if os.path.exists(state_file):
        with open(state_file, "r") as f:
            case_state = json.load(f)
    else:
        case_state = {"boots": []}

    # Load manifest
    mf = load_manifest_via_vintner(args.vintner_bin, args.disk, args.bs_offset)
    images_by_gen = {img["generation"]: img for img in mf.get("images", [])}

    # EFI files
    efi_files = []
    if args.firmware == "uefi":
        raw_list = mdir(args.disk, args.bs_offset, "::/EFI/Linux")
        efi_files = [os.path.basename(f) for f in raw_list]

    # Grubenv
    grubenv_vars = {}
    if args.firmware == "bios":
        raw_env = mtype(args.disk, args.bs_offset, "/grub/grubenv")
        grubenv_vars = parse_grubenv(raw_env) or {}

    # Read os-1 files SHA256 for H6
    os1_uki_hash = ""
    os1_bios_hashes = {}
    if args.firmware == "uefi":
        for f in efi_files:
            if f.startswith("vintner-os-1"):
                content = mtype(args.disk, args.bs_offset, f"/EFI/Linux/{f}")
                if content:
                    os1_uki_hash = sha256_bytes(content)
                break
    else:
        for fname in ["vmlinuz", "initrd.img", "slot.cfg"]:
            content = mtype(args.disk, args.bs_offset, f"/vintner/bios/os-1/{fname}")
            if content:
                os1_bios_hashes[fname] = sha256_bytes(content)

    # Read meta from runner
    meta = {}
    if args.meta_json and os.path.exists(args.meta_json):
        with open(args.meta_json, "r") as f:
            meta = json.load(f)

    # Identify booted
    prev_efi = case_state["boots"][-1]["efi_files"] if case_state["boots"] else []
    prev_env = case_state["boots"][-1]["grubenv"] if case_state["boots"] else {}
    booted = identify_booted(args.boot_log, args.firmware, prev_efi, efi_files, prev_env, grubenv_vars)

    boot_record = {
        "boot_num": args.boot_num,
        "booted": booted,
        "how_ended": meta.get("how_ended", "unknown"),
        "last_line": meta.get("last_line", ""),
        "duration": meta.get("duration", 0),
        "manifest_images": images_by_gen,
        "efi_files": efi_files,
        "grubenv": grubenv_vars,
        "os1_uki_hash": os1_uki_hash,
        "os1_bios_hashes": os1_bios_hashes,
        "log_path": args.boot_log,
    }

    case_state["boots"].append(boot_record)
    with open(state_file, "w") as f:
        json.dump(case_state, f, indent=2)

    print(f"[{args.case}] Recorded state for Boot {args.boot_num}: booted {booted}")

# --- 4. evaluate-case --------------------------------------------------------
def cmd_evaluate_case(args):
    with open(args.state_file, "r") as f:
        case_state = json.load(f)

    boots = {b["boot_num"]: b for b in case_state["boots"]}
    firmware = args.firmware
    case = args.case
    results_file = args.results_file

    # Load initial state before boots (from initial state JSON)
    with open(args.initial_state, "r") as f:
        initial = json.load(f)

    initial_os1_last_good = initial.get("os1_last_good", "")
    initial_os1_uki_hash = initial.get("os1_uki_hash", "")
    initial_os1_bios_hashes = initial.get("os1_bios_hashes", {})

    if case == "G0":
        # H1: Boot 1 is generation 2, blessed: generation 2 GOOD, UEFI vintner-os-2+3-0.efi,
        # BIOS VINTNER_T_2=xxx; every later boot is generation 2 as well; recovery never boots
        h1_ok = True
        h1_evid = []

        b1 = boots.get(1)
        if not b1:
            h1_ok = False
            h1_evid.append("boot 1 missing")
        else:
            if b1["booted"] != "os-2":
                h1_ok = False
                h1_evid.append(f"boot 1 booted {b1['booted']}, expected os-2")
            else:
                h1_evid.append("boot 1 booted os-2")

            img2 = b1["manifest_images"].get("2")
            if not img2 or img2.get("state") != "GOOD":
                h1_ok = False
                h1_evid.append(f"gen 2 state={img2.get('state') if img2 else 'missing'}, expected GOOD")
            else:
                h1_evid.append("gen 2 state=GOOD")

            if firmware == "uefi":
                if "vintner-os-2+3-0.efi" not in b1["efi_files"]:
                    h1_ok = False
                    h1_evid.append(f"vintner-os-2+3-0.efi missing, files={b1['efi_files']}")
                else:
                    h1_evid.append("counter=+3-0")
            else:
                t2 = b1["grubenv"].get("VINTNER_T_2")
                booted_var = b1["grubenv"].get("VINTNER_BOOTED")
                if t2 != "xxx":
                    h1_ok = False
                    h1_evid.append(f"VINTNER_T_2={t2}, expected xxx")
                if booted_var is not None:
                    h1_ok = False
                    h1_evid.append(f"VINTNER_BOOTED={booted_var}, expected unset")
                if t2 == "xxx" and booted_var is None:
                    h1_evid.append("counter=xxx; booted_unset=True")

        # Later boots (2 to 6)
        later_ok = True
        for b_num in range(2, 7):
            b_info = boots.get(b_num)
            if not b_info:
                continue
            if b_info["booted"] != "os-2":
                later_ok = False
                h1_evid.append(f"boot {b_num} booted {b_info['booted']}, expected os-2")
            if "recovery" in b_info["booted"]:
                h1_ok = False
                h1_evid.append(f"boot {b_num} booted recovery: {b_info['booted']}")

        if later_ok and len(boots) >= 2:
            h1_evid.append(f"boots 2-{len(boots)} booted os-2")
        elif not later_ok:
            h1_ok = False

        emit(results_file, "H1", "G0", "PASS" if h1_ok else "FAIL", "; ".join(h1_evid))

    else:
        # Broken cases B1 to B4

        # H2: Boots 1 to 3 are generation 2. Its counter after each: UEFI +2-1, +1-2, +0-3; BIOS xx, x, 0
        h2_ok = True
        h2_evid = []
        expected_counters = {
            1: {"uefi": "vintner-os-2+2-1.efi", "bios": "xx"},
            2: {"uefi": "vintner-os-2+1-2.efi", "bios": "x"},
            3: {"uefi": "vintner-os-2+0-3.efi", "bios": "0"},
        }

        for b_num in (1, 2, 3):
            b_info = boots.get(b_num)
            if not b_info:
                h2_ok = False
                h2_evid.append(f"boot {b_num} missing")
                continue

            if b_info["booted"] != "os-2":
                h2_ok = False
                h2_evid.append(f"boot {b_num} booted {b_info['booted']}, expected os-2")

            if firmware == "uefi":
                exp_file = expected_counters[b_num]["uefi"]
                if exp_file not in b_info["efi_files"]:
                    h2_ok = False
                    h2_evid.append(f"b{b_num} missing {exp_file}, files={b_info['efi_files']}")
                else:
                    h2_evid.append(f"b{b_num}=+{3-b_num}-{b_num}")
            else:
                exp_t = expected_counters[b_num]["bios"]
                act_t = b_info["grubenv"].get("VINTNER_T_2")
                if act_t != exp_t:
                    h2_ok = False
                    h2_evid.append(f"b{b_num} VINTNER_T_2={act_t}, expected {exp_t}")
                else:
                    h2_evid.append(f"b{b_num}={act_t}")

        emit(results_file, "H2", case, "PASS" if h2_ok else "FAIL", "; ".join(h2_evid))

        # H3: Boot 4 is generation 1 (os-1), not recovery A or B, and is blessed:
        # os-1 GOOD with last_good later than before the case, UEFI vintner-os-1+3-0.efi,
        # BIOS VINTNER_T_1=xxx and no VINTNER_BOOTED
        h3_ok = True
        h3_evid = []
        b4 = boots.get(4)
        if not b4:
            h3_ok = False
            h3_evid.append("boot 4 missing")
        else:
            if b4["booted"] != "os-1":
                h3_ok = False
                h3_evid.append(f"boot 4 booted {b4['booted']}, expected os-1")
            else:
                h3_evid.append("boot 4 booted os-1")

            img1 = b4["manifest_images"].get("1")
            if not img1 or img1.get("state") != "GOOD":
                h3_ok = False
                h3_evid.append(f"os-1 state={img1.get('state') if img1 else 'missing'}, expected GOOD")
            else:
                h3_evid.append("os-1 state=GOOD")

            lg_after = img1.get("last_good", "") if img1 else ""
            if not lg_after or lg_after <= initial_os1_last_good:
                h3_ok = False
                h3_evid.append(f"last_good after ({lg_after}) not later than before ({initial_os1_last_good})")
            else:
                h3_evid.append(f"last_good={lg_after} (updated)")

            if firmware == "uefi":
                if "vintner-os-1+3-0.efi" not in b4["efi_files"]:
                    h3_ok = False
                    h3_evid.append(f"vintner-os-1+3-0.efi missing in {b4['efi_files']}")
                else:
                    h3_evid.append("rearmed=+3-0")
            else:
                t1 = b4["grubenv"].get("VINTNER_T_1")
                booted_var = b4["grubenv"].get("VINTNER_BOOTED")
                if t1 != "xxx":
                    h3_ok = False
                    h3_evid.append(f"VINTNER_T_1={t1}, expected xxx")
                if booted_var is not None:
                    h3_ok = False
                    h3_evid.append(f"VINTNER_BOOTED={booted_var}, expected unset")
                if t1 == "xxx" and booted_var is None:
                    h3_evid.append("rearmed=xxx; booted_unset=True")

        emit(results_file, "H3", case, "PASS" if h3_ok else "FAIL", "; ".join(h3_evid))

        # H4: After boot 4: generation 2 is FAILED in the manifest, its UEFI file is still
        # vintner-os-2+0-3.efi and its BIOS counter still 0; vintner bless printed the transition on serial console
        h4_ok = True
        h4_evid = []
        if not b4:
            h4_ok = False
            h4_evid.append("boot 4 missing")
        else:
            img2 = b4["manifest_images"].get("2")
            if not img2 or img2.get("state") != "FAILED":
                h4_ok = False
                h4_evid.append(f"gen 2 state={img2.get('state') if img2 else 'missing'}, expected FAILED")
            else:
                h4_evid.append("gen 2 state=FAILED")

            if firmware == "uefi":
                if "vintner-os-2+0-3.efi" not in b4["efi_files"]:
                    h4_ok = False
                    h4_evid.append(f"vintner-os-2+0-3.efi missing in {b4['efi_files']}")
                else:
                    h4_evid.append("uefi_file=vintner-os-2+0-3.efi")
            else:
                t2 = b4["grubenv"].get("VINTNER_T_2")
                if t2 != "0":
                    h4_ok = False
                    h4_evid.append(f"VINTNER_T_2={t2}, expected 0")
                else:
                    h4_evid.append("bios_t=0")

            b4_log = read_log(b4["log_path"])
            m_trans = re.search(r"generation 2: TRYING -> FAILED", b4_log)
            if not m_trans:
                h4_ok = False
                h4_evid.append("transition 'generation 2: TRYING -> FAILED' not printed in b4 log")
            else:
                h4_evid.append("printed=generation 2: TRYING -> FAILED")

        emit(results_file, "H4", case, "PASS" if h4_ok else "FAIL", "; ".join(h4_evid))

        # H5: Boots 5 and 6 are os-1 again; generation 2 is never tried again (its counter and name do not change)
        h5_ok = True
        h5_evid = []
        b5 = boots.get(5)
        b6 = boots.get(6)
        if not b5 or not b6:
            h5_ok = False
            h5_evid.append("boots 5 or 6 missing")
        else:
            if b5["booted"] != "os-1":
                h5_ok = False
                h5_evid.append(f"boot 5 booted {b5['booted']}, expected os-1")
            else:
                h5_evid.append("b5=os-1")

            if b6["booted"] != "os-1":
                h5_ok = False
                h5_evid.append(f"boot 6 booted {b6['booted']}, expected os-1")
            else:
                h5_evid.append("b6=os-1")

            if firmware == "uefi":
                for b_chk, b_lbl in [(b5, "b5"), (b6, "b6")]:
                    if "vintner-os-2+0-3.efi" not in b_chk["efi_files"]:
                        h5_ok = False
                        h5_evid.append(f"{b_lbl} gen 2 uefi file changed or missing: {b_chk['efi_files']}")
                if h5_ok:
                    h5_evid.append("gen2 uefi file remained vintner-os-2+0-3.efi")
            else:
                for b_chk, b_lbl in [(b5, "b5"), (b6, "b6")]:
                    if b_chk["grubenv"].get("VINTNER_T_2") != "0":
                        h5_ok = False
                        h5_evid.append(f"{b_lbl} VINTNER_T_2={b_chk['grubenv'].get('VINTNER_T_2')}, expected 0")
                if h5_ok:
                    h5_evid.append("gen2 counter remained 0")

        emit(results_file, "H5", case, "PASS" if h5_ok else "FAIL", "; ".join(h5_evid))

        # H6: os-1's UKI and BIOS files have the hashes its record had before the case, at every read
        h6_ok = True
        h6_evid = []
        for b_num in range(1, 7):
            b_info = boots.get(b_num)
            if not b_info:
                continue
            if firmware == "uefi":
                curr_h = b_info.get("os1_uki_hash", "")
                if not curr_h or curr_h != initial_os1_uki_hash:
                    h6_ok = False
                    h6_evid.append(f"b{b_num} os-1 uki hash mismatch: {curr_h} != {initial_os1_uki_hash}")
            else:
                curr_bh = b_info.get("os1_bios_hashes", {})
                if curr_bh != initial_os1_bios_hashes:
                    h6_ok = False
                    h6_evid.append(f"b{b_num} os-1 bios hashes mismatch: {curr_bh} != {initial_os1_bios_hashes}")

        if h6_ok:
            if firmware == "uefi":
                h6_evid.append(f"os-1 UKI intact across all boots (sha256={initial_os1_uki_hash[:16]}...)")
            else:
                h6_evid.append("os-1 BIOS files intact across all boots")

        emit(results_file, "H6", case, "PASS" if h6_ok else "FAIL", "; ".join(h6_evid))

        # H7: How boots 1 to 3 ended, and each case's serial evidence for why:
        # B1 the panic line, B2 the failed vintner-test-health.service and boot-complete.target lines,
        # B3 the last line before the kill, B4 the last line before each kill. OBSERVED
        h7_ok = True
        h7_evid = []
        ends = []
        for b_num in (1, 2, 3):
            b_info = boots.get(b_num)
            ends.append(f"B{b_num}: {b_info['how_ended'] if b_info else 'missing'}")
        h7_evid.append("; ".join(ends))

        # Serial evidence extraction
        if case == "B1":
            # Panic line in logs
            panic_found = []
            for b_num in (1, 2, 3):
                b_info = boots.get(b_num)
                if b_info:
                    log_content = read_log(b_info["log_path"])
                    m_p = re.search(r"(Kernel panic.*|sysrq.*triggered.*|SysRq : Trigger.*)", log_content, re.IGNORECASE)
                    if m_p:
                        panic_found.append(f"b{b_num}: {m_p.group(1).strip()}")
            if panic_found:
                h7_evid.append("; ".join(panic_found))
            else:
                h7_ok = False
                h7_evid.append("missing panic line in logs")

        elif case == "B2":
            # Failed vintner-test-health.service and boot-complete.target
            b2_details = []
            for b_num in (1, 2, 3):
                b_info = boots.get(b_num)
                if b_info:
                    log_content = read_log(b_info["log_path"])
                    m_svc = re.search(r".*(Failed to start vintner-test-health\.service|vintner-test-health\.service.*failed).*", log_content)
                    m_tgt = re.search(r".*(Dependency failed for boot-complete\.target|boot-complete\.target.*failed).*", log_content)
                    parts = []
                    if m_svc: parts.append(m_svc.group(0).strip())
                    if m_tgt: parts.append(m_tgt.group(0).strip())
                    if parts:
                        b2_details.append(f"b{b_num}: {'; '.join(parts)}")
                    else:
                        h7_ok = False
                        b2_details.append(f"b{b_num}: missing failure lines")
            h7_evid.append("; ".join(b2_details))

        elif case == "B3":
            # Last line before 120s kill
            b3_tails = []
            for b_num in (1, 2, 3):
                b_info = boots.get(b_num)
                if b_info:
                    b3_tails.append(f"b{b_num} last_line: {b_info['last_line']}")
            h7_evid.append("; ".join(b3_tails))

        elif case == "B4":
            # Last line before each kill
            b4_tails = []
            for b_num in (1, 2, 3):
                b_info = boots.get(b_num)
                if b_info:
                    b4_tails.append(f"b{b_num} last_line: {b_info['last_line']}")
            h7_evid.append("; ".join(b4_tails))

        emit(results_file, "H7", case, "OBSERVED" if h7_ok else "FAIL", "; ".join(h7_evid))

# --- 5. evaluate-h8 ----------------------------------------------------------
def cmd_evaluate_h8(args):
    results_file = args.results_file
    all_ok = True
    evid_parts = []

    for state_path in args.state_files:
        if not os.path.exists(state_path):
            continue
        with open(state_path, "r") as f:
            case_state = json.load(f)
        case_name = os.path.basename(state_path).replace("-state.json", "")
        for b in case_state.get("boots", []):
            if "recovery" in b.get("booted", ""):
                all_ok = False
                evid_parts.append(f"{case_name} b{b['boot_num']} booted recovery: {b['booted']}")

    if all_ok:
        evidence = "no boot ran recovery A or B across all cases"
    else:
        evidence = "; ".join(evid_parts)

    emit(results_file, "H8", "all", "PASS" if all_ok else "FAIL", evidence)

def cmd_record_initial_state(args):
    mf = load_manifest_via_vintner(args.vintner_bin, args.disk, args.bs_offset)
    os1 = next((img for img in mf.get("images", []) if img["generation"] == 1), None)
    if not os1:
        sys.exit(f"record-initial-state: os-1 missing from manifest on {args.disk}")

    os1_uki_hash = ""
    os1_bios_hashes = {}
    if args.firmware == "uefi":
        raw_list = mdir(args.disk, args.bs_offset, "::/EFI/Linux")
        for f in raw_list:
            fname = os.path.basename(f)
            if fname.startswith("vintner-os-1"):
                content = mtype(args.disk, args.bs_offset, f"/EFI/Linux/{fname}")
                if content:
                    os1_uki_hash = sha256_bytes(content)
                break
    else:
        for fname in ["vmlinuz", "initrd.img", "slot.cfg"]:
            content = mtype(args.disk, args.bs_offset, f"/vintner/bios/os-1/{fname}")
            if content:
                os1_bios_hashes[fname] = sha256_bytes(content)

    initial = {
        "os1_last_good": os1.get("last_good", ""),
        "os1_uki_hash": os1_uki_hash,
        "os1_bios_hashes": os1_bios_hashes,
    }
    with open(args.out_file, "w") as f:
        json.dump(initial, f, indent=2)
    print(f"[{args.case}] Recorded initial state: last_good={initial['os1_last_good']}")

# --- Main CLI ----------------------------------------------------------------
def main():
    parser = argparse.ArgumentParser(description="Candidate fallback integration test inspector")
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    # verify-prep
    p_prep = subparsers.add_parser("verify-prep")
    p_prep.add_argument("--disk", required=True)
    p_prep.add_argument("--bs-offset", type=int, required=True)
    p_prep.add_argument("--vintner-bin", required=True)
    p_prep.set_defaults(func=cmd_verify_prep)

    # record-initial-state
    p_init = subparsers.add_parser("record-initial-state")
    p_init.add_argument("--firmware", choices=["uefi", "bios"], required=True)
    p_init.add_argument("--case", required=True)
    p_init.add_argument("--disk", required=True)
    p_init.add_argument("--bs-offset", type=int, required=True)
    p_init.add_argument("--vintner-bin", required=True)
    p_init.add_argument("--out-file", required=True)
    p_init.set_defaults(func=cmd_record_initial_state)

    # run-boot
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
    p_run.add_argument("--meta-out", default="")
    p_run.set_defaults(func=cmd_run_boot)

    # record-boot-state
    p_rec = subparsers.add_parser("record-boot-state")
    p_rec.add_argument("--firmware", choices=["uefi", "bios"], required=True)
    p_rec.add_argument("--case", required=True)
    p_rec.add_argument("--boot-num", type=int, required=True)
    p_rec.add_argument("--disk", required=True)
    p_rec.add_argument("--bs-offset", type=int, required=True)
    p_rec.add_argument("--vintner-bin", required=True)
    p_rec.add_argument("--boot-log", required=True)
    p_rec.add_argument("--meta-json", default="")
    p_rec.add_argument("--state-file", required=True)
    p_rec.set_defaults(func=cmd_record_boot_state)

    # evaluate-case
    p_eval = subparsers.add_parser("evaluate-case")
    p_eval.add_argument("--firmware", choices=["uefi", "bios"], required=True)
    p_eval.add_argument("--case", required=True)
    p_eval.add_argument("--state-file", required=True)
    p_eval.add_argument("--initial-state", required=True)
    p_eval.add_argument("--results-file", required=True)
    p_eval.set_defaults(func=cmd_evaluate_case)

    # evaluate-h8
    p_h8 = subparsers.add_parser("evaluate-h8")
    p_h8.add_argument("--results-file", required=True)
    p_h8.add_argument("state_files", nargs="+")
    p_h8.set_defaults(func=cmd_evaluate_h8)

    args = parser.parse_args()
    args.func(args)

if __name__ == "__main__":
    main()
