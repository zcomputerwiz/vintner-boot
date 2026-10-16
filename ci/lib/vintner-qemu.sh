#!/bin/bash
# --- 8. QEMU VM Runner Helper ------------------------------------------------
ACCEL="tcg"
[ -w /dev/kvm ] && ACCEL="kvm"

run_qemu() {
	local disk_path="$1"
	local seed_path="$2"
	local logfile="$3"
	local timeout_secs="$4"
	local vars_path="${5:-}"

	: > "$logfile"
	local disk_fmt="raw"
	[[ "$disk_path" == *.qcow2 ]] && disk_fmt="qcow2"

	if [ "$FIRMWARE" = "uefi" ]; then
		timeout "$timeout_secs" qemu-system-x86_64 \
			-machine "q35,accel=$ACCEL" \
			-m 2048 -smp 2 -nodefaults -display none \
			-monitor none -serial "file:$logfile" -no-reboot \
			-drive "if=pflash,format=raw,unit=0,file=$CODE_FD,readonly=on" \
			-drive "if=pflash,format=raw,unit=1,file=$vars_path" \
			-drive "file=$disk_path,format=$disk_fmt,if=virtio" \
			-drive "file=$seed_path,format=raw,if=virtio" \
			-nic "user,model=virtio-net-pci" || true
	else
		timeout "$timeout_secs" qemu-system-x86_64 \
			-machine "q35,accel=$ACCEL" \
			-m 2048 -smp 2 -nodefaults -display none \
			-monitor none -serial "file:$logfile" -no-reboot \
			-drive "file=$disk_path,format=$disk_fmt,if=virtio" \
			-drive "file=$seed_path,format=raw,if=virtio" \
			-nic "user,model=virtio-net-pci" || true
	fi
}

make_seed() {
	local op_id="$1"
	local op_script="$2"
	local seed_img="$SEED_DIR/${op_id}-seed.img"
	local ud="$SEED_DIR/${op_id}-user-data"
	local md="$SEED_DIR/${op_id}-meta-data"

	cat <<EOF > "$md"
instance-id: lifecycle-${op_id}
local-hostname: debian-vintner-lifecycle
EOF

	# shellcheck disable=SC2001
INDENTED_SCRIPT=$(echo "$op_script" | sed 's/^/      /')
cat <<EOF > "$ud"
#cloud-config
disable_root: false
write_files:
  - path: /tmp/run_op.sh
    permissions: '0755'
    content: |
      #!/bin/bash
      set -u
      exec > /tmp/op.log 2>&1
$INDENTED_SCRIPT
  - path: /tmp/run_runner.sh
    permissions: '0755'
    content: |
      #!/bin/bash
      /tmp/run_op.sh
      RC=\$?
      echo "=== OP EXIT: \$RC ===" > /dev/ttyS0
      tail -n 20 /tmp/op.log | sed 's/^/OPLOG: /' > /dev/ttyS0
runcmd:
  - /tmp/run_runner.sh
EOF

	cloud-localds "$seed_img" "$ud" "$md"
	echo "$seed_img"
}

# --- 9. Inspection helper ---------------------------------------------------
# Compares a disk after an operation with the disk before it. Every read that
# should succeed fails the run instead of reporting "no change".
cat <<'EOF' > "$OUT/inspect.py"
import sys, re, os, subprocess, hashlib

(raw_disk, base_raw, firmware, op_id, op_kind, esp_offset, bs_offset, bs_partuuid,
 boot1_log, boot2_log, changes_out, c0_nvram_file, vars_file,
 bbp_start, bbp_sectors) = sys.argv[1:16]
esp_offset, bs_offset = int(esp_offset), int(bs_offset)
bbp_offset, bbp_size = int(bbp_start) * 512, int(bbp_sectors) * 512

mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")
VINTNER_ESP = ["/EFI/BOOT/BOOTX64.EFI", "/EFI/vintner/systemd-bootx64.efi", "/loader/loader.conf"]

def sha256_bytes(b):
    return hashlib.sha256(b).hexdigest()

def mtype_read(disk, offset, path):
    # None only when the file does not exist; other errors stop the run.
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    if r.returncode == 0:
        return r.stdout
    if b"not found" in r.stderr:
        return None
    sys.exit(f"mtype {path} on {disk}: {r.stderr.decode(errors='replace').strip()}")

def mdir_list(disk, offset):
    out = subprocess.check_output(["mdir", "-s", "-b", "-i", f"{disk}@@{offset}", "::/"], env=mtools_env)
    lines = []
    for l in out.decode("utf-8", errors="replace").splitlines():
        l = l.strip()
        if l.startswith("::"): l = l[2:]
        if l and not l.endswith("/"):
            lines.append(l if l.startswith("/") else "/" + l)
    if not lines:
        sys.exit(f"no files listed on {disk} at offset {offset}")
    return sorted(lines)

def read_at(path, offset, size):
    with open(path, "rb") as f:
        f.seek(offset)
        return f.read(size)

def clean(text):
    return re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", text).replace("\r", "")

# 1. Boot 1: exit status and the command's last lines
b1_text = clean(open(boot1_log, errors="replace").read())
# Console output can share a line with these markers, so they are not anchored.
m_exit = re.search(r"=== OP EXIT: (-?\d+) ===", b1_text)
cmd_exit = int(m_exit.group(1)) if m_exit else -1
cmd_tail = re.findall(r"OPLOG: (.*)$", b1_text, re.M)

# 2. Vintner loader files, for both firmwares: the disk boots on both
changed_vintner_files = []
for p in VINTNER_ESP:
    base_b = mtype_read(base_raw, esp_offset, p)
    if base_b is None:
        sys.exit(f"{p} missing from the disk before the operation")
    curr_b = mtype_read(raw_disk, esp_offset, p)
    if curr_b is None:
        changed_vintner_files.append(f"removed {p}")
    elif curr_b != base_b:
        changed_vintner_files.append(f"modified {p}")
if read_at(raw_disk, 0, 440) != read_at(base_raw, 0, 440):
    changed_vintner_files.append("modified MBR boot code (bytes 0-439)")
if sha256_bytes(read_at(raw_disk, bbp_offset, bbp_size)) != sha256_bytes(read_at(base_raw, bbp_offset, bbp_size)):
    changed_vintner_files.append("modified BIOS Boot Partition")

# 3. Boot storage files, UKIs compared without their +N-M counter
def normalize_bs_path(p):
    return re.sub(r"^(.*vintner-[^+]+)\+\d+(-\d+)?(\.efi)$", r"\1\3", p)

def get_bs_files(disk):
    res = {}
    for f in mdir_list(disk, bs_offset):
        if f == "/grub/grubenv" or f.startswith("/vintner/state/") or f.startswith("/vintner/recovery-logs/"):
            continue
        content = mtype_read(disk, bs_offset, f)
        if content is None:
            sys.exit(f"{f} listed but unreadable on {disk}")
        res[normalize_bs_path(f)] = sha256_bytes(content)
    return res

curr_bs = get_bs_files(raw_disk)
base_bs = get_bs_files(base_raw)
changed_vintner_files += [f"added {k}" for k in sorted(curr_bs) if k not in base_bs]
changed_vintner_files += [f"removed {k}" for k in sorted(base_bs) if k not in curr_bs]
changed_vintner_files += [f"modified {k}" for k in sorted(curr_bs) if k in base_bs and curr_bs[k] != base_bs[k]]

# 4. Rest of the ESP (OBSERVED only)
def get_esp_rest(disk):
    res = {}
    for f in mdir_list(disk, esp_offset):
        if f in VINTNER_ESP:
            continue
        content = mtype_read(disk, esp_offset, f)
        if content is None:
            sys.exit(f"{f} listed but unreadable on {disk}")
        res[f] = sha256_bytes(content)
    return res

curr_esp = get_esp_rest(raw_disk)
base_esp = get_esp_rest(base_raw)
esp_added = [k for k in sorted(curr_esp) if k not in base_esp]
esp_removed = [k for k in sorted(base_esp) if k not in curr_esp]
esp_modified = [k for k in sorted(curr_esp) if k in base_esp and curr_esp[k] != base_esp[k]]

# 5. NVRAM, UEFI only, against the C0 run
changed_nvram = []
if firmware != "uefi":
    nvram_col = "n/a (BIOS)"
elif op_kind == "prep" or op_id == "X0":
    nvram_col = "not compared (no control run yet)"
else:
    out = subprocess.check_output(["virt-fw-vars", "-i", vars_file, "-p"])
    curr_entries = sorted(re.sub(r"\s+", " ", l.strip()) for l in out.decode("utf-8", errors="replace").splitlines()
                          if re.match(r"^(Boot[0-9A-Fa-f]{4}|BootOrder)\s*:", l.strip()))
    if not curr_entries:
        sys.exit(f"no Boot#### or BootOrder variables read from {vars_file}")
    if op_id == "C0":
        with open(c0_nvram_file, "w") as f:
            f.write("\n".join(curr_entries) + "\n")
    c0_entries = [l for l in open(c0_nvram_file).read().splitlines() if l]
    changed_nvram = [f"+{e}" for e in curr_entries if e not in c0_entries] + [f"-{e}" for e in c0_entries if e not in curr_entries]
    nvram_col = "; ".join(changed_nvram) if changed_nvram else "none"

# 6. What booted boot 2
b2_text = clean(open(boot2_log, errors="replace").read())
m_cmd = re.search(r"^CMDLINE: (.*)$", b2_text, re.M)
b2_cmdline = m_cmd.group(1).strip() if m_cmd else ""
m_ent = re.search(r"^LOADER_ENTRY_SELECTED: (.*)$", b2_text, re.M)
b2_entry = m_ent.group(1).strip() if m_ent else ""
b2_bless = re.search(r"^BLESS_ACTIVE: active$", b2_text, re.M) is not None
cmdline_has_bs = f"vintner.bootstorage=PARTUUID={bs_partuuid}" in b2_cmdline.split()
grub_tries = re.findall(r"vintner: trying (\S+)", b2_text)

if firmware == "uefi":
    loader_says = re.sub(r"(\+\d+(-\d+)?)?\.efi$", "", b2_entry) == "vintner-os-1"
else:
    loader_says = grub_tries[:1] == ["os-1"]
boot2_vintner = cmdline_has_bs and b2_bless and loader_says
boot2_str = "vintner os-1" if boot2_vintner else \
    f"other (entry={b2_entry or 'none'}, grub_tries={','.join(grub_tries) or 'none'}, cmdline_bs={cmdline_has_bs}, bless_active={b2_bless})"

# 7. Changes file
vintner_col = ", ".join(changed_vintner_files) if changed_vintner_files else "none"
with open(changes_out, "w") as f:
    f.write(f"Operation: {op_id} ({op_kind}), firmware {firmware}\n")
    f.write(f"Command exit: {cmd_exit}\n")
    f.write("Command output, last lines:\n")
    for l in cmd_tail[-20:] or ["(none)"]:
        f.write(f"  | {l}\n")
    f.write(f"Changed vintner files: {vintner_col}\n")
    f.write(f"Rest of the ESP: added={len(esp_added)}, modified={len(esp_modified)}, removed={len(esp_removed)}\n")
    for a in esp_added: f.write(f"  + {a}\n")
    for m in esp_modified: f.write(f"  * {m}\n")
    for r in esp_removed: f.write(f"  - {r}\n")
    f.write(f"Changed NVRAM: {nvram_col}\n")
    f.write(f"Boot 2: {boot2_str}\n")
    f.write(f"Boot 2 cmdline: {b2_cmdline or '(none)'}\n")

# 8. Status
if op_id == "N1":
    status = "PASS" if ("added /vintner/bios/os-1/lifecycle-negative-test" in changed_vintner_files
                        and "modified /loader/loader.conf" in changed_vintner_files) else "FAIL"
elif op_kind in ("prep", "control", "ordinary"):
    ok = cmd_exit == 0 and not changed_vintner_files and not changed_nvram and boot2_vintner
    status = "PASS" if ok else "FAIL"
else:
    status = "OBSERVED"

evidence = f"exit={cmd_exit}; vintner_files={vintner_col}; nvram={nvram_col}; boot2={boot2_str}"
print(f"{op_id}\t{op_kind}\t{status}\t{cmd_exit}\t{vintner_col}\t{nvram_col}\t{boot2_str}\t{evidence}")
EOF

# inspect <op> <kind> <disk after> <disk before> <vars>: appends the row and prints the changes.
inspect() {
	local row
	row=$(python3 "$OUT/inspect.py" "$3" "$4" "$FIRMWARE" "$1" "$2" \
		"$ESP_OFFSET" "$BS_OFFSET" "$BS_PARTUUID" "$OUT/$1-boot1.log" "$OUT/$1-boot2.log" "$OUT/$1-changes.txt" \
		"$OUT/c0-nvram.txt" "$5" "$BIOS_BOOT_START" "$BIOS_BOOT_SECTORS")
	printf '%s\n' "$row" >> "$RESULTS"
	echo "Result: $row"
	cat "$OUT/$1-changes.txt"
	echo "=== $1 dpkg-query -W ===" >> "$VERSIONS"
	sed -n '/=== DPKG_QUERY_BEGIN ===/,/=== DPKG_QUERY_END ===/{ /=== DPKG_QUERY_/d; p; }' "$OUT/$1-boot1.log" | tr -d '\r' >> "$VERSIONS"
}
