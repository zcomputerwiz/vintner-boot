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

def read_log(path):
    return clean_ansi(open(path, errors="replace").read()) if path and os.path.isfile(path) else ""

def block(text, name):
    # Lines between "=== <name>-BEGIN ===" and "=== <name>-END ===", or None unless each marker occurs once, in order.
    begin, end = f"=== {name}-BEGIN ===", f"=== {name}-END ==="
    if text.count(begin) != 1 or text.count(end) != 1 or text.index(begin) > text.index(end):
        return None
    return [l for l in text.split(begin)[1].split(end)[0].splitlines() if l]

def serial_value(text, key):
    # The value of the one "<key>=<value>" the guest printed, or None if it printed none or several.
    vals = re.findall(rf"{key}=(\S*)", text)
    return vals[0] if len(vals) == 1 else None

def emit(results_file, check, case, ok, evid):
    row = f"{check}\t{case}\t{'PASS' if ok else 'FAIL'}\t{'; '.join(evid)}"
    with open(results_file, "a") as f:
        f.write(row + "\n")
    print(row)

def mtype(disk, offset, path):
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    if r.returncode == 0:
        return r.stdout
    if b"not found" in r.stderr:
        return None
    raise RuntimeError(f"mtype {path} on {disk}: {r.stderr.decode(errors='replace').strip()}")

def mdir(disk, offset, path="::/", dirs=False):
    # Sorted paths under path; directories (ending in "/") only when dirs is set.
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
    # None when grubenv is missing or not a GRUB environment block: every check that reads it then FAILs.
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

def get_booted(log_path, firmware):
    if not os.path.isfile(log_path):
        return "none"
    text = clean_ansi(open(log_path, errors="replace").read())
    if firmware == "uefi":
        m = re.search(r"^LOADER_ENTRY_SELECTED:[ \t]*(.+)$", text, re.M)
        if m:
            entry = m.group(1).strip()
            entry = entry.replace("\\", "/").split("/")[-1]
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

def compute_module_tree(root_mnt, abi):
    vmlinuz_path = os.path.join(root_mnt, "boot", f"vmlinuz-{abi}")
    if not os.path.isfile(vmlinuz_path):
        return None
    with open(vmlinuz_path, "rb") as f:
        k_sha = hashlib.sha256(f.read()).hexdigest()

    dep_path = os.path.join(root_mnt, "lib", "modules", abi, "modules.dep")
    if not os.path.isfile(dep_path):
        return None
    with open(dep_path, "rb") as f:
        dep_sha = hashlib.sha256(f.read()).hexdigest()

    dkms_dir = os.path.join(root_mnt, "lib", "modules", abi, "updates", "dkms")
    lines = []
    if os.path.isdir(dkms_dir):
        for root, _, files in os.walk(dkms_dir):
            for file in files:
                p = os.path.join(root, file)
                rel = os.path.relpath(p, dkms_dir).replace("\\", "/")
                with open(p, "rb") as f:
                    lines.append(f"{rel} {hashlib.sha256(f.read()).hexdigest()}\n")
    if not lines:
        h = hashlib.sha256(b"")
        dkms_sha = h.hexdigest()
    else:
        lines.sort()
        dkms_sha = hashlib.sha256("".join(lines).encode("utf-8")).hexdigest()

    return {
        "kernel_sha256": k_sha,
        "modules_dep_sha256": dep_sha,
        "dkms_sha256": dkms_sha
    }

def retained_entries(root_mnt):
    # Every quoted string in APT::NeverAutoRemove { ... };, raw (escapes kept), sorted; None if missing or unparseable.
    conf_path = os.path.join(root_mnt, "etc", "apt", "apt.conf.d", "50vintner-retained-kernels")
    if not os.path.isfile(conf_path):
        return None
    content = open(conf_path, "r", errors="replace").read()
    m = re.search(r"APT::NeverAutoRemove\s*\{(.*?)\};", content, re.DOTALL)
    if not m or not re.fullmatch(r'(\s*"[^"]*";)*\s*', m.group(1)):
        return None
    return sorted(re.findall(r'"([^"]*)"', m.group(1)))

def quote_meta(s):
    # Go's regexp.QuoteMeta: a backslash before each of \.+*?()|[]{}^$
    return re.sub(r"([\\.+*?()|\[\]{}^$])", r"\\\1", s)

assert quote_meta("linux-image-6.12.111+deb13-cloud-amd64") == r"linux-image-6\.12\.111\+deb13-cloud-amd64"

def expected_retained(pkgs):
    # The raw NeverAutoRemove entries the publisher must write for pkgs; None if a package is unknown.
    return None if None in pkgs else sorted({"^" + quote_meta(p) + "$" for p in pkgs})

def dpkg_owners(root_mnt, path):
    # Packages whose dpkg file list on the guest root has exactly this path as a line.
    info = os.path.join(root_mnt, "var", "lib", "dpkg", "info")
    return sorted(f[:-len(".list")] for f in os.listdir(info) if f.endswith(".list")
                  and path in open(os.path.join(info, f), errors="replace").read().splitlines())

def dpkg_version(root_mnt, pkg):
    # Version from pkg's stanza in the guest's dpkg status file, or None.
    with open(os.path.join(root_mnt, "var", "lib", "dpkg", "status"), errors="replace") as f:
        for stanza in f.read().split("\n\n"):
            fields = dict(l.split(": ", 1) for l in stanza.splitlines() if l and not l[0].isspace() and ": " in l)
            if fields.get("Package") == pkg:
                return fields.get("Version")
    return None

def is_uki(name, g):
    # /EFI/Linux/vintner-os-<g>.efi with or without a boot counter
    return re.fullmatch(rf"/EFI/Linux/vintner-os-{g}(\+.*)?\.efi", name) is not None

def expected_bs(prep_efi, prep_bios, prep_gens, kept, removed):
    # Whole /EFI/Linux and /vintner/bios listings: the prepared disk's without the removed generations, plus new kept ones.
    new = [g for g in kept if g not in prep_gens]
    efi = [e for e in prep_efi if not any(is_uki(e, g) for g in removed)] + \
          [f"/EFI/Linux/vintner-os-{g}+3-0.efi" for g in new]
    bios = [e for e in prep_bios if not any(e.startswith(f"/vintner/bios/os-{g}/") for g in removed)] + \
           [f"/vintner/bios/os-{g}/{f}" for g in new for f in ("", "vmlinuz", "initrd.img", "slot.cfg")]
    return sorted(efi), sorted(bios)

def verify_prep(disk, bs_offset, vintner_bin):
    m = load_manifest_via_vintner(vintner_bin, disk, bs_offset)
    images = m.get("images", [])
    if len(images) != 1:
        sys.exit(f"FAIL: expected 1 image on prepared disk, found {len(images)}")
    img = images[0]
    if img.get("generation") != 1:
        sys.exit(f"FAIL: expected generation 1, got {img.get('generation')}")
    if img.get("state") != "GOOD":
        sys.exit(f"FAIL: expected state GOOD for image 1, got {img.get('state')}")
    staging_files = mdir(disk, bs_offset, "::/vintner/staging", dirs=True)
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
    p_e9.add_argument("--firmware", required=True)

    p_gb = sub.add_parser("get-booted")
    p_gb.add_argument("--log", required=True)
    p_gb.add_argument("--firmware", required=True)

    args = parser.parse_args()

    if args.cmd == "get-booted":
        print(get_booted(args.log, args.firmware))
        return

    if args.cmd == "verify-prep":
        verify_prep(args.disk, args.bs_offset, args.vintner_bin)
        return

    if args.cmd == "record-e9":
        # One entry per boot, in boot order: "<label>\t<serial log>\t<op>", op set when the boot ran a case operation.
        ok = True
        evid_parts = []
        for line in open(args.summary_file).read().splitlines():
            label, log, op = (line.split("\t") + ["", ""])[:3]
            text = read_log(log)
            booted = get_booted(log, args.firmware)
            end = "=== VINTNER-REPORT-END ===" in text
            bless = re.search(r"^BLESS_ACTIVE: active$", text, re.M) is not None
            boot_ok = re.fullmatch(r"os-[0-9]+", booted) is not None and end and bless
            parts = [booted, "report end seen" if end else "report end NOT seen",
                     "bless active" if bless else "bless NOT active"]
            if op:
                m_exit = re.search(r"=== OP EXIT: (-?\d+) ===", text)
                code = m_exit.group(1) if m_exit else "missing"
                boot_ok = boot_ok and code == "0"
                parts.append(f"op exit {code}")
            ok = ok and boot_ok
            evid_parts.append(f"{label}:{','.join(parts)}")
        if not evid_parts:
            ok = False
            evid_parts.append("no boots recorded")
        emit(args.results_file, "E9", "all", ok, evid_parts)
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

        m_prep = load_manifest_via_vintner(vintner_bin, prep_disk, bs_offset)
        m_curr = load_manifest_via_vintner(vintner_bin, disk, bs_offset)

        grubenv_bytes = mtype(disk, bs_offset, "/grub/grubenv")
        env = parse_grubenv(grubenv_bytes)
        prep_env = parse_grubenv(mtype(prep_disk, bs_offset, "/grub/grubenv"))

        m_b1, env_b1 = None, None
        b1_disk = args.b1_disk or disk
        if args.b1_disk:
            m_b1 = load_manifest_via_vintner(vintner_bin, args.b1_disk, bs_offset)
            env_b1 = parse_grubenv(mtype(args.b1_disk, bs_offset, "/grub/grubenv"))
        else:
            m_b1, env_b1 = m_curr, env

        curr_images = m_curr.get("images", [])
        curr_gens = [img["generation"] for img in curr_images]

        # os-1 and boot storage as the prepared disk had them, before any operation
        img1_prep = next(img for img in m_prep["images"] if img["generation"] == 1)
        os1_abi, os1_pkg = img1_prep["kernel"]["abi"], img1_prep["kernel"]["package"]
        prep_gens = [img["generation"] for img in m_prep["images"]]
        prep_efi = mdir(prep_disk, bs_offset, "::/EFI/Linux")
        prep_bios = mdir(prep_disk, bs_offset, "::/vintner/bios", dirs=True)

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
            if curr_uefi != prep_efi:
                ok = False
                evid_parts.append(f"EFI/Linux changed: {curr_uefi} vs {prep_efi}")
            else:
                evid_parts.append(f"EFI/Linux unchanged ({len(curr_uefi)} files)")

            curr_bios = mdir(disk, bs_offset, "::/vintner/bios")
            prep_bios_files = mdir(prep_disk, bs_offset, "::/vintner/bios")
            if curr_bios != prep_bios_files:
                ok = False
                evid_parts.append(f"vintner/bios changed: {curr_bios} vs {prep_bios_files}")
            else:
                evid_parts.append(f"vintner/bios unchanged ({len(curr_bios)} entries)")

            staging = mdir(disk, bs_offset, "::/vintner/staging", dirs=True)
            if staging:
                ok = False
                evid_parts.append(f"staging not empty: {staging}")
            else:
                evid_parts.append("staging empty")

            if env is None or prep_env is None or "VINTNER_ORDER" not in prep_env:
                ok = False
                evid_parts.append("grubenv or its VINTNER_ORDER missing or unreadable")
            elif env.get("VINTNER_ORDER") != prep_env["VINTNER_ORDER"]:
                ok = False
                evid_parts.append(f"VINTNER_ORDER changed: {env.get('VINTNER_ORDER')}")
            else:
                evid_parts.append(f"VINTNER_ORDER={env.get('VINTNER_ORDER')}")

            if b2_booted != "os-1":
                ok = False
                evid_parts.append(f"boot2 booted {b2_booted}, expected os-1")
            else:
                evid_parts.append(f"booted={b2_booted}")

            emit(results_file, "E1", "C0", ok, evid_parts)

        elif case in ("K1", "K2"):
            # Check E2, E3, E4
            b1_booted = get_booted(b1_log, fw)
            b2_booted = get_booted(b2_log, fw)

            # E2 evaluation on b1_disk (after the operation):
            e2_ok = True
            e2_evid = []
            b1_images = m_b1.get("images", [])
            b1_gens = sorted(img["generation"] for img in b1_images)
            if b1_gens != [1, 2] or m_b1.get("next_generation") != 3:
                e2_ok = False
                e2_evid.append(f"generations={b1_gens} next_generation={m_b1.get('next_generation')}, expected [1, 2] and 3")
            else:
                e2_evid.append("generations=[1, 2] next_generation=3")

            # Expected kernel from the guest root, not from the manifest under test
            exp_abi = exp_pkg = exp_ver = None
            if not mnt:
                e2_ok = False
                e2_evid.append("guest root not mounted")
            else:
                if case == "K1":
                    new_abis = sorted(f[len("vmlinuz-"):] for f in os.listdir(os.path.join(mnt, "boot"))
                                      if f.startswith("vmlinuz-") and f != f"vmlinuz-{os1_abi}")
                    exp_abi = new_abis[0] if len(new_abis) == 1 else None
                    e2_evid.append(f"guest /boot/vmlinuz-* other than os-1's {os1_abi}: {new_abis}")
                else:
                    exp_abi = os1_abi
                owners = dpkg_owners(mnt, f"/boot/vmlinuz-{exp_abi}") if exp_abi else []
                exp_pkg = owners[0] if len(owners) == 1 else None
                exp_ver = dpkg_version(mnt, exp_pkg) if exp_pkg else None
                e2_evid.append(f"guest: owners of /boot/vmlinuz-{exp_abi}={owners} version={exp_ver}")

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

                k2 = img2_b1["kernel"]
                actual_kernel = (k2.get("abi"), k2.get("package"), k2.get("version"))
                if None in (exp_abi, exp_pkg, exp_ver) or actual_kernel != (exp_abi, exp_pkg, exp_ver):
                    e2_ok = False
                    e2_evid.append(f"kernel abi/package/version={actual_kernel}, expected {(exp_abi, exp_pkg, exp_ver)}")
                else:
                    e2_evid.append(f"abi={exp_abi} pkg={exp_pkg} version={exp_ver}")

                # Check module_tree
                expected_modtree = compute_module_tree(mnt, exp_abi) if mnt and exp_abi else None
                actual_modtree = img2_b1.get("module_tree")
                if expected_modtree is None or actual_modtree != expected_modtree:
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

                # Whole listings: os-1's and the recovery images' as before the operation, plus generation 2
                exp_efi, exp_bios = expected_bs(prep_efi, prep_bios, prep_gens, [1, 2], [])
                b1_efi = mdir(b1_disk, bs_offset, "::/EFI/Linux")
                b1_bios = mdir(b1_disk, bs_offset, "::/vintner/bios", dirs=True)
                if b1_efi != exp_efi:
                    e2_ok = False
                    e2_evid.append(f"EFI/Linux={b1_efi}, expected {exp_efi}")
                else:
                    e2_evid.append(f"EFI/Linux={b1_efi}")
                if b1_bios != exp_bios:
                    e2_ok = False
                    e2_evid.append(f"vintner/bios={b1_bios}, expected {exp_bios}")
                else:
                    e2_evid.append(f"vintner/bios as expected ({len(b1_bios)} entries)")

                # grubenv
                if env_b1 is None:
                    e2_ok = False
                    e2_evid.append("grubenv missing or unreadable")
                else:
                    if env_b1.get("VINTNER_ORDER") != "2 1":
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
                staging = mdir(b1_disk, bs_offset, "::/vintner/staging", dirs=True)
                if staging:
                    e2_ok = False
                    e2_evid.append(f"staging not empty: {staging}")
                else:
                    e2_evid.append("staging empty")

                # NeverAutoRemove: os-1's package before the operation and the owner of the new kernel
                retained = retained_entries(mnt) if mnt else None
                expected_entries = expected_retained([os1_pkg, exp_pkg])
                if retained is None or expected_entries is None or retained != expected_entries:
                    e2_ok = False
                    e2_evid.append(f"NeverAutoRemove lists {retained}, expected {expected_entries}")
                else:
                    e2_evid.append(f"NeverAutoRemove verified: {retained}")

            emit(results_file, "E2", case, e2_ok, e2_evid)

            # Check E3:
            e3_ok = True
            e3_evid = []
            if b2_booted != "os-2":
                e3_ok = False
                e3_evid.append(f"boot 2 ran {b2_booted}, expected os-2")
            else:
                e3_evid.append(f"next boot ran {b2_booted}")

            img2 = next((img for img in curr_images if img["generation"] == 2), None)
            if img2 and img2.get("state") == "GOOD" and img2.get("first_good"):
                e3_evid.append(f"image 2 GOOD (first_good={img2.get('first_good')})")
            else:
                e3_ok = False
                e3_evid.append(f"image 2 state={img2.get('state') if img2 else 'none'}, first_good={img2.get('first_good') if img2 else 'none'}")

            emit(results_file, "E3", case, e3_ok, e3_evid)

            # Check E4:
            e4_ok = True
            e4_evid = []
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

                # Whole record against the one before the operation, apart from last_good
                for when, m in (("after op", m_b1), ("after reboot", m_curr)):
                    rec = next((img for img in m.get("images", []) if img["generation"] == 1), {})
                    changed = sorted(k for k in set(rec) | set(img1_prep) if k != "last_good" and rec.get(k) != img1_prep.get(k))
                    if not rec or changed:
                        e4_ok = False
                        e4_evid.append(f"os-1 record {when} differs from before the op in {changed or 'all keys (missing)'}")
                    else:
                        e4_evid.append(f"os-1 record {when} unchanged apart from last_good")

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

                if env is None:
                    e4_ok = False
                    e4_evid.append("grubenv missing or unreadable")
                else:
                    if "1" not in env.get("VINTNER_ORDER", "").split():
                        e4_ok = False
                        e4_evid.append(f"os-1 missing from VINTNER_ORDER ({env.get('VINTNER_ORDER')})")
                    else:
                        e4_evid.append(f"os-1 in VINTNER_ORDER ({env.get('VINTNER_ORDER')})")

                    if env.get("VINTNER_T_1") != "xxx":
                        e4_ok = False
                        e4_evid.append(f"VINTNER_T_1 is {env.get('VINTNER_T_1')}")
                    else:
                        e4_evid.append("VINTNER_T_1=xxx")

            emit(results_file, "E4", case, e4_ok, e4_evid)

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

            # The extra publish's own stdout, between the guest's markers
            op_text = read_log(op_log)
            pub = block(op_text, "PUBLISH")
            pub_exit = serial_value(op_text, "PUBLISH_EXIT")
            if pub_exit != "0" or pub is None or "unchanged" not in pub:
                ok = False
                evid_parts.append(f"extra publish: PUBLISH_EXIT={pub_exit}, stdout={pub}, expected exit 0 and the line 'unchanged'")
            else:
                evid_parts.append(f"extra publish: PUBLISH_EXIT=0, stdout={pub}")

            emit(results_file, "E5", "K3", ok, evid_parts)

        elif case == "U1":
            # Check E6
            ok = True
            evid_parts = []
            b1_text = read_log(b1_log)

            # Check op exit
            m_exit = re.search(r"=== OP EXIT: (-?\d+) ===", b1_text)
            exit_code = int(m_exit.group(1)) if m_exit else -1
            if exit_code != 0:
                ok = False
                evid_parts.append(f"op exit code {exit_code}")
            else:
                evid_parts.append("op exit=0")

            pub = block(b1_text, "PUBLISH")
            pub_exit = serial_value(b1_text, "PUBLISH_EXIT")
            if pub_exit != "0":
                ok = False
                evid_parts.append(f"PUBLISH_EXIT={pub_exit}, expected 0")
            else:
                evid_parts.append("PUBLISH_EXIT=0")

            if pub is None or "unchanged" not in pub:
                ok = False
                evid_parts.append(f"publish stdout={pub}, expected the line 'unchanged'")
            else:
                evid_parts.append(f"publish stdout={pub}")

            # Listings: the sha256sum lines between each pair of markers
            listings = [[l for l in block(b1_text, f"LISTING-{w}") or [] if re.fullmatch(r"[0-9a-f]{64}  /tmp/bs/.+", l)]
                        for w in ("BEFORE", "AFTER")]
            before, after = listings
            if not before or before != after:
                ok = False
                evid_parts.append(f"listings: {len(before)} files before, {len(after)} after; "
                                  f"only before={sorted(set(before) - set(after))}; only after={sorted(set(after) - set(before))}")
            else:
                evid_parts.append(f"listings identical ({len(before)} files)")

            emit(results_file, "E6", "U1", ok, evid_parts)

        elif case == "G1":
            # Check E7 after round args.round: garbage collection with retain 3, os-1 GOOD, each new image blessed GOOD
            rnd = args.round
            kept = {1: [1, 2], 2: [1, 2, 3], 3: [2, 3, 4], 4: [3, 4, 5]}[rnd]
            removed = [g for g in range(1, rnd + 2) if g not in kept]
            ok = True
            evid_parts = []
            if sorted(curr_gens) != kept:
                ok = False
                evid_parts.append(f"kept generations={sorted(curr_gens)}, expected {kept}")
            else:
                evid_parts.append(f"kept generations={kept}")

            # Removed generations must have no records, UKIs, BIOS dirs, order entries or counters
            curr_efi = mdir(disk, bs_offset, "::/EFI/Linux")
            curr_bios = mdir(disk, bs_offset, "::/vintner/bios", dirs=True)
            if env is None or "VINTNER_ORDER" not in env:
                ok = False
                evid_parts.append("grubenv or its VINTNER_ORDER missing or unreadable")
            for rem_gen in removed:
                left = ["record" for img in curr_images if img["generation"] == rem_gen]
                left += [e for e in curr_efi if is_uki(e, rem_gen)]
                left += [e for e in curr_bios if e.startswith(f"/vintner/bios/os-{rem_gen}/")]
                if env is not None and str(rem_gen) in env.get("VINTNER_ORDER", "").split():
                    left.append("VINTNER_ORDER entry")
                if env is not None and f"VINTNER_T_{rem_gen}" in env:
                    left.append(f"VINTNER_T_{rem_gen}")
                if left:
                    ok = False
                    evid_parts.append(f"gen {rem_gen} left behind: {left}")
                else:
                    evid_parts.append(f"gen {rem_gen} record/UKI/BIOS dir/order entry/counter gone")
            if env is not None:
                evid_parts.append(f"VINTNER_ORDER={env.get('VINTNER_ORDER')}")

            exp_efi, exp_bios = expected_bs(prep_efi, prep_bios, prep_gens, kept, removed)
            if curr_efi != exp_efi:
                ok = False
                evid_parts.append(f"EFI/Linux={curr_efi}, expected {exp_efi}")
            else:
                evid_parts.append(f"EFI/Linux={curr_efi}")
            if curr_bios != exp_bios:
                ok = False
                evid_parts.append(f"vintner/bios={curr_bios}, expected {exp_bios}")
            else:
                evid_parts.append(f"vintner/bios as expected ({len(curr_bios)} entries)")

            # Every G1 image republishes os-1's kernel, so the kept images' packages are os-1's package
            retained = retained_entries(mnt) if mnt else None
            expected_entries = expected_retained([os1_pkg])
            if retained is None or expected_entries is None or retained != expected_entries:
                ok = False
                evid_parts.append(f"NeverAutoRemove is {retained}, expected {expected_entries}")
            else:
                evid_parts.append(f"NeverAutoRemove lists {retained}")

            emit(results_file, "E7", f"G1r{rnd}", ok, evid_parts)

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
            purged = serial_value(read_log(b1_log), "PURGED_PACKAGE")

            if not img2:
                ok = False
                evid_parts.append("gen 2 missing from manifest")
            else:
                if img2.get("stale") is not True:
                    ok = False
                    evid_parts.append(f"gen 2 stale={img2.get('stale')}, expected true")
                else:
                    evid_parts.append("gen 2 stale=true")

                if purged is None or img2["kernel"].get("package") != purged:
                    ok = False
                    evid_parts.append(f"gen 2 kernel.package={img2['kernel'].get('package')}, guest purged {purged}")
                else:
                    evid_parts.append(f"gen 2 kernel.package={purged} = purged package")

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

                if img3["kernel"].get("abi") != os1_abi:
                    ok = False
                    evid_parts.append(f"gen 3 kernel.abi={img3['kernel'].get('abi')}, expected os-1's {os1_abi}")
                else:
                    evid_parts.append(f"gen 3 kernel.abi={os1_abi}")

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

            emit(results_file, "E8", "R1", ok, evid_parts)

if __name__ == "__main__":
    main()
