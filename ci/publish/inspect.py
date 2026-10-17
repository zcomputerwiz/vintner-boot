#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later

import sys
import os
import re
import json
import subprocess
import hashlib
import tempfile
import argparse

mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")

def clean_ansi(text):
    return re.sub(r"\x1b\[[0-9;:]*[a-zA-Z]", "", text).replace("\r", "")

def mtype(disk, offset, path):
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    if r.returncode == 0:
        return r.stdout
    if b"not found" in r.stderr or b"Cannot initialize" in r.stderr:
        return None
    raise RuntimeError(f"mtype {path} on {disk}: {r.stderr.decode(errors='replace').strip()}")

def mdir(disk, offset, path="::/"):
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
        if l and not l.endswith("/"):
            lines.append(l if l.startswith("/") else "/" + l)
    return sorted(lines)

def parse_grubenv(content):
    if content is None:
        return {}
    res = {}
    for line in content.split(b"\n"):
        if line.startswith(b"#") or not line:
            continue
        if b"=" in line:
            k, v = line.split(b"=", 1)
            res[k.decode('utf-8', errors='replace').strip()] = v.decode('utf-8', errors='replace').strip()
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
        return json.loads(res.stdout), (a == b)

def get_booted(log_path, firmware):
    if not os.path.isfile(log_path):
        return "none"
    text = clean_ansi(open(log_path, errors="replace").read())
    if firmware == "uefi":
        m = re.search(r"^LOADER_ENTRY_SELECTED:\s*(.+)$", text, re.M)
        if m:
            entry = m.group(1).strip()
            entry = entry.replace("\\", "/").split("/")[-1]
            m_gen = re.match(r"^vintner-os-(\d+)", entry)
            if m_gen:
                return f"os-{m_gen.group(1)}"
            if "recovery-a" in entry:
                return "recovery-a"
            if "recovery-b" in entry:
                return "recovery-b"
            return entry
        m_var = re.search(r"LoaderBootCountPath[^\n]*\\([^\\]+\.efi)", text)
        if m_var:
            entry = m_var.group(1)
            m_gen = re.match(r"^vintner-os-(\d+)", entry)
            if m_gen:
                return f"os-{m_gen.group(1)}"
        return "none"
    else:
        tries = re.findall(r"vintner:\s*trying\s*([a-zA-Z0-9\-_]+)", text)
        if tries:
            return tries[-1]
        return "none"

def compute_module_tree(root_mnt, abi):
    mod_dir = os.path.join(root_mnt, "lib", "modules", abi)
    if not os.path.isdir(mod_dir):
        return ""
    files = sorted([f for f in os.listdir(mod_dir) if os.path.isfile(os.path.join(mod_dir, f))])
    h = hashlib.sha256()
    for f in files:
        f_path = os.path.join(mod_dir, f)
        with open(f_path, "rb") as fp:
            f_sha = hashlib.sha256(fp.read()).hexdigest()
        h.update(f.encode('utf-8') + b"\x00" + f_sha.encode('ascii') + b"\x00")
    dkms_dir = os.path.join(root_mnt, "var", "lib", "dkms")
    if os.path.isdir(dkms_dir):
        dkms_entries = []
        for mod_name in sorted(os.listdir(dkms_dir)):
            m_dir = os.path.join(dkms_dir, mod_name)
            if not os.path.isdir(m_dir):
                continue
            for mod_ver in sorted(os.listdir(m_dir)):
                mv_dir = os.path.join(m_dir, mod_ver)
                if not os.path.isdir(mv_dir):
                    continue
                dkms_h = hashlib.sha256()
                rel_files = []
                for root, _, dfiles in os.walk(mv_dir):
                    for df in dfiles:
                        full_p = os.path.join(root, df)
                        rel_p = os.path.relpath(full_p, mv_dir).replace("\\", "/")
                        rel_files.append((rel_p, full_p))
                rel_files.sort(key=lambda x: x[0])
                for rel_p, full_p in rel_files:
                    with open(full_p, "rb") as dfp:
                        d_sha = hashlib.sha256(dfp.read()).hexdigest()
                    dkms_h.update(rel_p.encode('utf-8') + b"\x00" + d_sha.encode('ascii') + b"\x00")
                dkms_entries.append((mod_name, mod_ver, dkms_h.hexdigest()))
        for mn, mv, mh in dkms_entries:
            h.update(mn.encode('utf-8') + b"\x00" + mv.encode('utf-8') + b"\x00" + mh.encode('ascii') + b"\x00")
    return h.hexdigest()

def get_retained_packages(root_mnt):
    conf_path = os.path.join(root_mnt, "etc", "apt", "apt.conf.d", "50vintner-retained-kernels")
    if not os.path.isfile(conf_path):
        return []
    content = open(conf_path, "r", errors="replace").read()
    m = re.search(r"NeverAutoRemove\s*\{([^}]*)\};", content, re.DOTALL)
    if not m:
        return []
    body = m.group(1)
    patterns = re.findall(r'"\^([^\$]+)\$"', body)
    pkgs = [p.replace(r"\.", ".").replace(r"\+", "+") for p in patterns]
    return sorted(pkgs)

def get_guest_packages(root_mnt):
    dpkg_status = os.path.join(root_mnt, "var", "lib", "dpkg", "status")
    if not os.path.isfile(dpkg_status):
        return {}
    pkg_map = {}
    cur_pkg, cur_status = None, None
    with open(dpkg_status, "r", errors="replace") as f:
        for line in f:
            line = line.strip()
            if line.startswith("Package: "):
                cur_pkg = line.split(":", 1)[1].strip()
            elif line.startswith("Status: "):
                cur_status = line.split(":", 1)[1].strip()
            elif line == "":
                if cur_pkg and cur_status and "installed" in cur_status:
                    pkg_map[cur_pkg] = cur_status
                cur_pkg, cur_status = None, None
    return pkg_map

def verify_prep(disk, bs_offset, vintner_bin):
    m, identical = load_manifest_via_vintner(vintner_bin, disk, bs_offset)
    if not identical:
        sys.exit("FAIL: manifest-a and manifest-b differ on prepared disk")
    images = m.get("images", [])
    if len(images) != 1:
        sys.exit(f"FAIL: expected 1 image on prepared disk, found {len(images)}")
    img = images[0]
    if img.get("generation") != 1:
        sys.exit(f"FAIL: expected generation 1, got {img.get('generation')}")
    if img.get("state") != "GOOD":
        sys.exit(f"FAIL: expected state GOOD for image 1, got {img.get('state')}")
    staging_files = mdir(disk, bs_offset, "::/vintner/staging")
    if staging_files:
        sys.exit(f"FAIL: /vintner/staging on prepared disk is not empty: {staging_files}")
    print("Prepared disk verified: exactly 1 image (os-1, GOOD), staging empty")

def main():
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_prep = sub.add_parser("verify-prep")
    p_prep.add_argument("--disk", required=True)
    p_prep.add_argument("--bs-offset", type=int, required=True)
    p_prep.add_argument("--vintner-bin", required=True)

    p_case = sub.add_parser("inspect-case")
    p_case.add_argument("--firmware", required=True)
    p_case.add_argument("--case", required=True)
    p_case.add_argument("--disk", required=True)
    p_case.add_argument("--bs-offset", type=int, required=True)
    p_case.add_argument("--vintner-bin", required=True)
    p_case.add_argument("--prepared-disk", required=True)
    p_case.add_argument("--b1-disk")
    p_case.add_argument("--root-mount")
    p_case.add_argument("--boot1-log")
    p_case.add_argument("--boot2-log")
    p_case.add_argument("--op-log")
    p_case.add_argument("--round", type=int)
    p_case.add_argument("--results-file", required=True)

    p_e9 = sub.add_parser("record-e9")
    p_e9.add_argument("--results-file", required=True)
    p_e9.add_argument("--summary-file", required=True)

    args = parser.parse_args()

    if args.cmd == "verify-prep":
        verify_prep(args.disk, args.bs_offset, args.vintner_bin)
        return

    if args.cmd == "record-e9":
        # Reads all boot entries from summary file and appends E9 row
        lines = [l.strip() for l in open(args.summary_file).readlines() if l.strip()]
        all_ok = True
        evid_parts = []
        for line in lines:
            evid_parts.append(line)
            if "recovery" in line or "none" in line or "FAIL" in line:
                all_ok = False
        status = "PASS" if all_ok else "FAIL"
        evidence = "; ".join(evid_parts)
        with open(args.results_file, "a") as f:
            f.write(f"E9\tall\t{status}\t{evidence}\n")
        print(f"E9\tall\t{status}\t{evidence}")
        return

    if args.cmd == "inspect-case":
        fw = args.firmware
        case = args.case
        disk = args.disk
        bs_offset = args.bs_offset
        vintner_bin = args.vintner_bin
        prep_disk = args.prepared_disk
        mnt = args.root_mount
        b1_log = args.boot1_log
        b2_log = args.boot2_log
        op_log = args.op_log
        results_file = args.results_file

        m_prep, _ = load_manifest_via_vintner(vintner_bin, prep_disk, bs_offset)
        m_curr, identical_curr = load_manifest_via_vintner(vintner_bin, disk, bs_offset)

        grubenv_bytes = mtype(disk, bs_offset, "/grub/grubenv")
        env = parse_grubenv(grubenv_bytes)
        prep_env = parse_grubenv(mtype(prep_disk, bs_offset, "/grub/grubenv"))

        m_b1, env_b1 = None, None
        b1_disk = args.b1_disk or disk
        if args.b1_disk:
            m_b1, _ = load_manifest_via_vintner(vintner_bin, args.b1_disk, bs_offset)
            env_b1 = parse_grubenv(mtype(args.b1_disk, bs_offset, "/grub/grubenv"))
        else:
            m_b1, env_b1 = m_curr, env

        curr_images = m_curr.get("images", [])
        curr_gens = [img["generation"] for img in curr_images]

        if case == "C0":
            # Check E1
            b1_booted = get_booted(b1_log, fw)
            b2_booted = get_booted(b2_log, fw)

            ok = True
            evid_parts = []
            if m_curr.get("next_generation") != m_prep.get("next_generation"):
                ok = False
                evid_parts.append(f"next_gen changed: {m_prep.get('next_generation')}->{m_curr.get('next_generation')}")
            else:
                evid_parts.append(f"next_gen={m_curr.get('next_generation')}")

            if curr_gens != [1]:
                ok = False
                evid_parts.append(f"generations changed: {curr_gens}")
            else:
                evid_parts.append(f"images={curr_gens}")

            curr_uefi = mdir(disk, bs_offset, "::/EFI/Linux")
            prep_uefi = mdir(prep_disk, bs_offset, "::/EFI/Linux")
            if curr_uefi != prep_uefi:
                ok = False
                evid_parts.append(f"EFI/Linux changed: {curr_uefi} vs {prep_uefi}")
            else:
                evid_parts.append(f"EFI/Linux unchanged ({len(curr_uefi)} files)")

            curr_bios = mdir(disk, bs_offset, "::/vintner/bios")
            prep_bios = mdir(prep_disk, bs_offset, "::/vintner/bios")
            if curr_bios != prep_bios:
                ok = False
                evid_parts.append(f"vintner/bios changed: {curr_bios} vs {prep_bios}")
            else:
                evid_parts.append(f"vintner/bios unchanged ({len(curr_bios)} entries)")

            staging = mdir(disk, bs_offset, "::/vintner/staging")
            if staging:
                ok = False
                evid_parts.append(f"staging not empty: {staging}")
            else:
                evid_parts.append("staging empty")

            if env.get("VINTNER_ORDER") != prep_env.get("VINTNER_ORDER"):
                ok = False
                evid_parts.append(f"VINTNER_ORDER changed: {env.get('VINTNER_ORDER')}")
            else:
                evid_parts.append(f"VINTNER_ORDER={env.get('VINTNER_ORDER')}")

            if b2_booted != "os-1":
                ok = False
                evid_parts.append(f"boot2 booted {b2_booted}, expected os-1")
            else:
                evid_parts.append(f"booted={b2_booted}")

            status = "PASS" if ok else "FAIL"
            evid = "; ".join(evid_parts)
            with open(results_file, "a") as f:
                f.write(f"E1\tC0\t{status}\t{evid}\n")
            print(f"E1\tC0\t{status}\t{evid}")

        elif case in ("K1", "K2"):
            # Check E2, E3, E4
            b1_booted = get_booted(b1_log, fw)
            b2_booted = get_booted(b2_log, fw)

            # E2 evaluation on b1_disk (after the operation):
            e2_ok = True
            e2_evid = []
            b1_images = m_b1.get("images", [])
            img2_b1 = next((img for img in b1_images if img["generation"] == 2), None)
            if img2_b1 is None:
                e2_ok = False
                e2_evid.append("generation 2 missing from manifest after operation")
            else:
                if img2_b1.get("state") != "TRYING":
                    e2_ok = False
                    e2_evid.append(f"gen 2 state is {img2_b1.get('state')}, expected TRYING")
                else:
                    e2_evid.append("state=TRYING")

                abi = img2_b1["kernel"]["abi"]
                pkg = img2_b1["kernel"].get("package")
                e2_evid.append(f"abi={abi} pkg={pkg}")

                # Check module_tree
                if mnt:
                    expected_modtree = compute_module_tree(mnt, abi)
                    actual_modtree = img2_b1["kernel"].get("module_tree")
                    if actual_modtree != expected_modtree:
                        e2_ok = False
                        e2_evid.append(f"module_tree mismatch: manifest={actual_modtree} actual={expected_modtree}")
                    else:
                        e2_evid.append("module_tree matches guest")

                # UEFI file check
                uefi_file = mtype(b1_disk, bs_offset, "/EFI/Linux/vintner-os-2+3-0.efi")
                if not uefi_file:
                    e2_ok = False
                    e2_evid.append("vintner-os-2+3-0.efi missing")
                else:
                    sha = hashlib.sha256(uefi_file).hexdigest()
                    if sha != img2_b1.get("uefi", {}).get("sha256"):
                        e2_ok = False
                        e2_evid.append("vintner-os-2+3-0.efi sha256 mismatch")
                    else:
                        e2_evid.append("UEFI UKI verified (vintner-os-2+3-0.efi)")

                # BIOS directory check
                bios_vmlinuz = mtype(b1_disk, bs_offset, "/vintner/bios/os-2/vmlinuz")
                bios_initrd = mtype(b1_disk, bs_offset, "/vintner/bios/os-2/initrd.img")
                bios_slot = mtype(b1_disk, bs_offset, "/vintner/bios/os-2/slot.cfg")
                if not bios_vmlinuz or not bios_initrd or not bios_slot:
                    e2_ok = False
                    e2_evid.append("BIOS os-2 files missing")
                else:
                    v_sha = hashlib.sha256(bios_vmlinuz).hexdigest()
                    i_sha = hashlib.sha256(bios_initrd).hexdigest()
                    s_sha = hashlib.sha256(bios_slot).hexdigest()
                    mf = img2_b1.get("bios", {}).get("files", {})
                    if v_sha != mf.get("vmlinuz") or i_sha != mf.get("initrd.img") or s_sha != mf.get("slot.cfg"):
                        e2_ok = False
                        e2_evid.append("BIOS files sha256 mismatch")
                    else:
                        e2_evid.append("BIOS files verified (3/3)")

                # grubenv
                order = env_b1.get("VINTNER_ORDER", "").split()
                if len(order) < 2 or order[0] != "2" or order[1] != "1":
                    e2_ok = False
                    e2_evid.append(f"VINTNER_ORDER is {env_b1.get('VINTNER_ORDER')}, expected '2 1'")
                else:
                    e2_evid.append(f"VINTNER_ORDER={env_b1.get('VINTNER_ORDER')}")

                t2 = env_b1.get("VINTNER_T_2")
                if t2 != "xxx":
                    e2_ok = False
                    e2_evid.append(f"VINTNER_T_2 is {t2}, expected xxx")
                else:
                    e2_evid.append("VINTNER_T_2=xxx")

                # staging
                staging = mdir(b1_disk, bs_offset, "::/vintner/staging")
                if staging:
                    e2_ok = False
                    e2_evid.append(f"staging not empty: {staging}")
                else:
                    e2_evid.append("staging empty")

                # NeverAutoRemove
                if mnt:
                    retained = get_retained_packages(mnt)
                    expected_pkgs = sorted(list({img["kernel"]["package"] for img in b1_images if img["kernel"].get("package")}))
                    if retained != expected_pkgs:
                        e2_ok = False
                        e2_evid.append(f"NeverAutoRemove lists {retained}, expected {expected_pkgs}")
                    else:
                        e2_evid.append(f"NeverAutoRemove verified: {retained}")

            e2_status = "PASS" if e2_ok else "FAIL"
            e2_evidence = "; ".join(e2_evid)
            with open(results_file, "a") as f:
                f.write(f"E2\t{case}\t{e2_status}\t{e2_evidence}\n")
            print(f"E2\t{case}\t{e2_status}\t{e2_evidence}")

            # Check E3:
            e3_ok = True
            e3_evid = []
            if b2_booted != "os-2":
                e3_ok = False
                e3_evid.append(f"boot 2 ran {b2_booted}, expected os-2")
            else:
                e3_evid.append(f"next boot ran {b2_booted}")

            if img2 and img2.get("state") == "GOOD" and img2.get("first_good"):
                e3_evid.append(f"image 2 GOOD (first_good={img2.get('first_good')})")
            else:
                e3_ok = False
                e3_evid.append(f"image 2 state={img2.get('state') if img2 else 'none'}, first_good={img2.get('first_good') if img2 else 'none'}")

            e3_status = "PASS" if e3_ok else "FAIL"
            e3_evidence = "; ".join(e3_evid)
            with open(results_file, "a") as f:
                f.write(f"E3\t{case}\t{e3_status}\t{e3_evidence}\n")
            print(f"E3\t{case}\t{e3_status}\t{e3_evidence}")

            # Check E4:
            e4_ok = True
            e4_evid = []
            img1_prep = next((img for img in m_prep["images"] if img["generation"] == 1), None)
            img1_curr = next((img for img in curr_images if img["generation"] == 1), None)
            if not img1_curr:
                e4_ok = False
                e4_evid.append("generation 1 missing from manifest")
            else:
                if img1_curr.get("state") != "GOOD" or img1_curr.get("stale") is not False:
                    e4_ok = False
                    e4_evid.append(f"os-1 state={img1_curr.get('state')} stale={img1_curr.get('stale')}")
                else:
                    e4_evid.append("os-1 state=GOOD stale=false")

                if img1_curr.get("uefi", {}).get("sha256") != img1_prep.get("uefi", {}).get("sha256"):
                    e4_ok = False
                    e4_evid.append("os-1 UKI sha256 changed in manifest")
                if img1_curr.get("bios", {}).get("files") != img1_prep.get("bios", {}).get("files"):
                    e4_ok = False
                    e4_evid.append("os-1 BIOS hashes changed in manifest")

                # Verify actual files on boot storage for os-1
                os1_uefi = mtype(disk, bs_offset, "/EFI/Linux/vintner-os-1+3-0.efi")
                if not os1_uefi or hashlib.sha256(os1_uefi).hexdigest() != img1_prep["uefi"]["sha256"]:
                    e4_ok = False
                    e4_evid.append("os-1 UKI file missing or modified on boot storage")
                else:
                    e4_evid.append("os-1 UKI file intact (vintner-os-1+3-0.efi)")

                os1_v = mtype(disk, bs_offset, "/vintner/bios/os-1/vmlinuz")
                os1_i = mtype(disk, bs_offset, "/vintner/bios/os-1/initrd.img")
                os1_s = mtype(disk, bs_offset, "/vintner/bios/os-1/slot.cfg")
                mf1 = img1_prep["bios"]["files"]
                if not os1_v or hashlib.sha256(os1_v).hexdigest() != mf1["vmlinuz"] or \
                   not os1_i or hashlib.sha256(os1_i).hexdigest() != mf1["initrd.img"] or \
                   not os1_s or hashlib.sha256(os1_s).hexdigest() != mf1["slot.cfg"]:
                    e4_ok = False
                    e4_evid.append("os-1 BIOS files modified on boot storage")
                else:
                    e4_evid.append("os-1 BIOS files intact")

                if "1" not in env.get("VINTNER_ORDER", "").split():
                    e4_ok = False
                    e4_evid.append("os-1 missing from VINTNER_ORDER")
                else:
                    e4_evid.append("os-1 in VINTNER_ORDER")

                if env.get("VINTNER_T_1") != "xxx":
                    e4_ok = False
                    e4_evid.append(f"VINTNER_T_1 is {env.get('VINTNER_T_1')}")
                else:
                    e4_evid.append("VINTNER_T_1=xxx")

            e4_status = "PASS" if e4_ok else "FAIL"
            e4_evidence = "; ".join(e4_evid)
            with open(results_file, "a") as f:
                f.write(f"E4\t{case}\t{e4_status}\t{e4_evidence}\n")
            print(f"E4\t{case}\t{e4_status}\t{e4_evidence}")

        elif case == "K3":
            # Check E5
            ok = True
            evid_parts = []
            if curr_gens != [1, 2, 3]:
                ok = False
                evid_parts.append(f"generations={curr_gens}, expected [1, 2, 3]")
            else:
                evid_parts.append("exactly generations [1, 2, 3] present")

            if m_curr.get("next_generation") != 4:
                ok = False
                evid_parts.append(f"next_generation={m_curr.get('next_generation')}, expected 4")
            else:
                evid_parts.append("next_generation=4")

            # Check op_log contains "unchanged"
            op_text = clean_ansi(open(op_log, errors="replace").read()) if op_log and os.path.isfile(op_log) else ""
            if "unchanged" not in op_text:
                ok = False
                evid_parts.append("extra publish did not print 'unchanged'")
            else:
                evid_parts.append("extra publish printed 'unchanged'")

            status = "PASS" if ok else "FAIL"
            evid = "; ".join(evid_parts)
            with open(results_file, "a") as f:
                f.write(f"E5\tK3\t{status}\t{evid}\n")
            print(f"E5\tK3\t{status}\t{evid}")

        elif case == "U1":
            # Check E6
            ok = True
            evid_parts = []
            op_text = clean_ansi(open(op_log, errors="replace").read()) if op_log and os.path.isfile(op_log) else ""
            b1_text = clean_ansi(open(b1_log, errors="replace").read()) if b1_log and os.path.isfile(b1_log) else ""

            # Check op exit
            m_exit = re.search(r"=== OP EXIT: (-?\d+) ===", b1_text)
            exit_code = int(m_exit.group(1)) if m_exit else -1
            if exit_code != 0:
                ok = False
                evid_parts.append(f"exit code {exit_code}")
            else:
                evid_parts.append("exit=0")

            if "unchanged" not in op_text and "unchanged" not in b1_text:
                ok = False
                evid_parts.append("'unchanged' not printed")
            else:
                evid_parts.append("'unchanged' printed")

            if "DIFF_FAILED" in b1_text or "DIFF_FAILED" in op_text:
                ok = False
                evid_parts.append("boot storage listings differ")
            else:
                evid_parts.append("listings identical")

            status = "PASS" if ok else "FAIL"
            evid = "; ".join(evid_parts)
            with open(results_file, "a") as f:
                f.write(f"E6\tU1\t{status}\t{evid}\n")
            print(f"E6\tU1\t{status}\t{evid}")

        elif case == "G1":
            # Check E7
            ok = True
            evid_parts = []
            # After 4 rounds with retain=3, generations kept must be [3, 4, 5]
            if curr_gens != [3, 4, 5]:
                ok = False
                evid_parts.append(f"kept generations={curr_gens}, expected [3, 4, 5]")
            else:
                evid_parts.append(f"kept generations={curr_gens}")

            # Removed generations 1 and 2 must have no records, UKIs, BIOS dirs, order entries or counters
            for rem_gen in [1, 2]:
                if any(img["generation"] == rem_gen for img in curr_images):
                    ok = False
                    evid_parts.append(f"gen {rem_gen} still in manifest")
                if mtype(disk, bs_offset, f"/EFI/Linux/vintner-os-{rem_gen}+3-0.efi"):
                    ok = False
                    evid_parts.append(f"gen {rem_gen} UKI still on boot storage")
                if mtype(disk, bs_offset, f"/vintner/bios/os-{rem_gen}/slot.cfg"):
                    ok = False
                    evid_parts.append(f"gen {rem_gen} BIOS dir still on boot storage")
                if str(rem_gen) in env.get("VINTNER_ORDER", "").split():
                    ok = False
                    evid_parts.append(f"gen {rem_gen} still in VINTNER_ORDER")
                if f"VINTNER_T_{rem_gen}" in env:
                    ok = False
                    evid_parts.append(f"VINTNER_T_{rem_gen} still in grubenv")

            if ok:
                evid_parts.append("removed records/UKIs/BIOS dirs/counters gone")

            # Check NeverAutoRemove lists exactly the packages of the kept images
            if mnt:
                retained = get_retained_packages(mnt)
                expected_pkgs = sorted(list({img["kernel"]["package"] for img in curr_images if img["kernel"].get("package")}))
                if retained != expected_pkgs:
                    ok = False
                    evid_parts.append(f"NeverAutoRemove is {retained}, expected {expected_pkgs}")
                else:
                    evid_parts.append(f"NeverAutoRemove lists {retained}")

            status = "PASS" if ok else "FAIL"
            evid = "; ".join(evid_parts)
            with open(results_file, "a") as f:
                f.write(f"E7\tG1\t{status}\t{evid}\n")
            print(f"E7\tG1\t{status}\t{evid}")

        elif case == "R1":
            # Check E8
            b2_booted = get_booted(b2_log, fw)
            ok = True
            evid_parts = []

            images_e8 = m_b1.get("images", []) if args.b1_disk else curr_images
            disk_e8 = args.b1_disk or disk
            img1 = next((img for img in images_e8 if img["generation"] == 1), None)
            img2 = next((img for img in images_e8 if img["generation"] == 2), None)
            img3 = next((img for img in images_e8 if img["generation"] == 3), None)

            if not img2:
                ok = False
                evid_parts.append("gen 2 missing from manifest")
            else:
                if img2.get("stale") is not True:
                    ok = False
                    evid_parts.append(f"gen 2 stale={img2.get('stale')}, expected true")
                else:
                    evid_parts.append("gen 2 stale=true")

                # Check on boot storage
                has_uefi = mtype(disk_e8, bs_offset, "/EFI/Linux/vintner-os-2+3-0.efi") is not None
                has_bios = mtype(disk_e8, bs_offset, "/vintner/bios/os-2/slot.cfg") is not None
                if not has_uefi or not has_bios:
                    ok = False
                    evid_parts.append("gen 2 files missing on boot storage")
                else:
                    evid_parts.append("gen 2 files present on boot storage")

            if not img3:
                ok = False
                evid_parts.append("gen 3 missing from manifest")
            else:
                if img3.get("state") != "TRYING":
                    ok = False
                    evid_parts.append(f"gen 3 state={img3.get('state')}, expected TRYING")
                else:
                    evid_parts.append("gen 3 state=TRYING")

            if not img1 or img1.get("state") != "GOOD":
                ok = False
                evid_parts.append(f"os-1 state={img1.get('state') if img1 else 'missing'}, expected GOOD")
            else:
                evid_parts.append("os-1 state=GOOD")

            if b2_booted != "os-3":
                ok = False
                evid_parts.append(f"boot2 booted {b2_booted}, expected os-3")
            else:
                evid_parts.append("reboot ran os-3")

            status = "PASS" if ok else "FAIL"
            evid = "; ".join(evid_parts)
            with open(results_file, "a") as f:
                f.write(f"E8\tR1\t{status}\t{evid}\n")
            print(f"E8\tR1\t{status}\t{evid}")

if __name__ == "__main__":
    main()
