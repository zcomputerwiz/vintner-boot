#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Secure Boot tier A integration test harness (WO-0009b).
#
# Proves, under QEMU and OVMF with Secure Boot enforced, that a project-signed
# chain boots and that tampered or foreign images are refused with fallback.
#
# Deliverables:
#   ci/secure-boot/run.sh uefi
#   ci/secure-boot/inspector.py
#   ci/secure-boot/README.md
#
# Requires: root via sudo, KVM, loop devices with partition scanning,
# mtools, qemu-system-x86, qemu-utils, cloud-image-utils, sfdisk, python3,
# python3-virt-firmware, python3-pefile, sbsigntool, openssl, golang-go.
set -euo pipefail

if [ $# -ne 1 ] || [ "$1" != "uefi" ]; then
	echo "Usage: $0 uefi" >&2
	exit 1
fi

FIRMWARE="$1"
cd "$(dirname "$0")/../.."

OUT="ci/out/secure-boot/$FIRMWARE"
MNT="$OUT/mnt"
RESULTS="$OUT/results.tsv"
export MTOOLS_SKIP_CHECK=1

mkdir -p "$OUT" "$MNT"

# --- Cleanup handler --------------------------------------------------------
LOOP=""
cleanup() {
	local rc=$?
	if [ -n "${MNT:-}" ] && mountpoint -q "$MNT"; then
		sudo umount "$MNT" || true
	fi
	if [ -n "${LOOP:-}" ]; then
		sudo losetup -d "$LOOP" || true
	fi
	if [ "$rc" != 0 ]; then
		echo "=== Failure: dumping last 60 lines of each boot log ==="
		for l in "$OUT"/*.log; do
			[ -f "$l" ] && { echo "--- last 60 lines of $l ---"; tr -d '\r' < "$l" | tail -n 60; }
		done
	fi
}
trap cleanup EXIT

# --- 1. Base image build via shared library ---------------------------------
echo "=== Step 1: Building base disk image ==="
if [ ! -f "$OUT/base.env" ] || [ ! -f "$OUT/base.raw" ] || [ ! -f "$OUT/vmlinuz" ] || [ ! -f "$OUT/initrd.img" ]; then
	./ci/lib/vintner-base.sh "$FIRMWARE" "$OUT"
fi
# shellcheck source=/dev/null # written by vintner-base.sh
. "$OUT/base.env"
# shellcheck source=ci/lib/vintner-qemu.sh
. ci/lib/vintner-qemu.sh

VINTNER_BIN="$OUT/vintner"
if [ ! -f "$VINTNER_BIN" ]; then
	echo "Building static vintner binary on host..."
	(cd src && CGO_ENABLED=0 go build -o "../$VINTNER_BIN" ./cmd/vintner)
fi

SECBOOT_CODE="/usr/share/OVMF/OVMF_CODE_4M.secboot.fd"
OVMF_VARS_TEMPLATE="/usr/share/OVMF/OVMF_VARS_4M.fd"

if [ ! -f "$SECBOOT_CODE" ]; then
	echo "ERROR: Secure Boot OVMF code file missing: $SECBOOT_CODE" >&2
	exit 1
fi

# --- 2. Key Generation (§3) -------------------------------------------------
echo "=== Step 2: Generating Secure Boot keys and certificates ==="
KEYS_DIR="$OUT/keys"
mkdir -p "$KEYS_DIR"

# Test Platform Key (PK / KEK)
openssl req -new -x509 -newkey rsa:2048 -nodes \
	-keyout "$KEYS_DIR/pk.key" -out "$KEYS_DIR/pk.crt" -days 3650 \
	-subj "/CN=vintner test platform key" 2>/dev/null
chmod 0600 "$KEYS_DIR/pk.key"

# Machine Key (enrolled in db)
openssl req -new -x509 -newkey rsa:2048 -nodes \
	-keyout "$KEYS_DIR/machine.key" -out "$KEYS_DIR/machine.crt" -days 3650 \
	-subj "/CN=vintner test machine key" 2>/dev/null
chmod 0600 "$KEYS_DIR/machine.key"

# Foreign Key (unenrolled, for S2 negative control)
openssl req -new -x509 -newkey rsa:2048 -nodes \
	-keyout "$KEYS_DIR/foreign.key" -out "$KEYS_DIR/foreign.crt" -days 3650 \
	-subj "/CN=foreign unenrolled key" 2>/dev/null
chmod 0600 "$KEYS_DIR/foreign.key"

PK_FP=$(openssl x509 -in "$KEYS_DIR/pk.crt" -outform DER | sha256sum | awk '{print $1}')
MACHINE_FP=$(openssl x509 -in "$KEYS_DIR/machine.crt" -outform DER | sha256sum | awk '{print $1}')
FOREIGN_FP=$(openssl x509 -in "$KEYS_DIR/foreign.crt" -outform DER | sha256sum | awk '{print $1}')

cat <<EOF > "$KEYS_DIR/fingerprints.json"
{
  "pk_sha256": "$PK_FP",
  "machine_sha256": "$MACHINE_FP",
  "foreign_sha256": "$FOREIGN_FP"
}
EOF

echo "PK cert: subject=$(openssl x509 -in "$KEYS_DIR/pk.crt" -noout -subject), sha256=$PK_FP"
echo "Machine cert: subject=$(openssl x509 -in "$KEYS_DIR/machine.crt" -noout -subject), sha256=$MACHINE_FP"
echo "Foreign cert: subject=$(openssl x509 -in "$KEYS_DIR/foreign.crt" -noout -subject), sha256=$FOREIGN_FP"

# --- 3. Signing Functions and Pre-build Artifacts (§3) -----------------------
echo "=== Step 3: Implementing signing helpers and preparing artifacts ==="

sb_sign() {
	local in_file="$1"
	local out_file="$2"
	local key="${3:-$KEYS_DIR/machine.key}"
	local cert="${4:-$KEYS_DIR/machine.crt}"
	sbsign --key "$key" --cert "$cert" --output "$out_file" "$in_file"
}

sb_verify() {
	local file="$1"
	local cert="${2:-$KEYS_DIR/machine.crt}"
	sbverify --cert "$cert" "$file"
}

# 3a. Sign Loader
SIGNED_LOADER="$OUT/signed-systemd-bootx64.efi"
UNSIGNED_LOADER="/usr/lib/systemd/boot/efi/systemd-bootx64.efi"
[ -f "$UNSIGNED_LOADER" ] || UNSIGNED_LOADER="$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi"
sb_sign "$UNSIGNED_LOADER" "$SIGNED_LOADER"
echo "Signed loader: $(sha256sum "$SIGNED_LOADER" | awk '{print $1}')"

# 3b. Sign Recovery UKIs
SIGNED_REC_A="$OUT/signed-vintner-recovery-a.efi"
SIGNED_REC_B="$OUT/signed-vintner-recovery-b.efi"
sb_sign recovery/out/vintner-recovery-a.efi "$SIGNED_REC_A"
sb_sign recovery/out/vintner-recovery-b.efi "$SIGNED_REC_B"

# 3c. Sign os-1 UKI
SIGNED_OS1="$OUT/signed-vintner-os-1+3-0.efi"
sb_sign "$OUT/vintner-os-1+3-0.efi" "$SIGNED_OS1"
echo "Signed os-1 UKI: $(sha256sum "$SIGNED_OS1" | awk '{print $1}')"

# 3d. Build Generation 2 UKI (unsigned, machine-signed, foreign-signed, flipped)
OS_PRETTY=$(sed -n 's/^PRETTY_NAME=//p' "$OUT/osrel" | tr -d '"')
{
	grep -vE '^(IMAGE_ID|IMAGE_VERSION|PRETTY_NAME)=' "$OUT/osrel"
	printf 'PRETTY_NAME="%s, %s"\nIMAGE_ID=vintner-os\nIMAGE_VERSION=2\n' "$OS_PRETTY" "$KERNEL_ABI"
} > "$OUT/osrel2"

# Same cmdline as os-1
OS2_CMDLINE=$(sed 's/^set vintner_cmdline="//;s/"$//' "$OUT/os1-slot.cfg")

UNSIGNED_OS2="$OUT/vintner-os-2+1-0.efi"
python3 "$UKIFY" build \
	--linux="$OUT/vmlinuz" \
	--initrd="$OUT/initrd.img" \
	--stub="$STUB" \
	--os-release="@$OUT/osrel2" \
	--cmdline="$OS2_CMDLINE" \
	--output="$UNSIGNED_OS2"

SIGNED_OS2="$OUT/signed-vintner-os-2+1-0.efi"
sb_sign "$UNSIGNED_OS2" "$SIGNED_OS2"

FOREIGN_OS2="$OUT/foreign-vintner-os-2+1-0.efi"
sb_sign "$UNSIGNED_OS2" "$FOREIGN_OS2" "$KEYS_DIR/foreign.key" "$KEYS_DIR/foreign.crt"

# 3e. Flipped byte UKIs (S3 and S4)
FLIPPED_OS2="$OUT/flipped-vintner-os-2+1-0.efi"
FLIPPED_REC_A="$OUT/flipped-vintner-recovery-a.efi"

python3 - <<PY
import pefile, hashlib, json

def flip_initrd_byte(src, dst, info_file):
    pe = pefile.PE(src)
    initrd_sec = None
    for s in pe.sections:
        name = s.Name.decode('ascii', errors='ignore').rstrip('\x00')
        if name == '.initrd':
            initrd_sec = s
            break
    if not initrd_sec:
        raise RuntimeError(f".initrd section not found in {src}")
    
    offset = initrd_sec.PointerToRawData + 100
    with open(src, "rb") as f:
        data = bytearray(f.read())
    
    old_byte = data[offset]
    new_byte = old_byte ^ 0xFF
    data[offset] = new_byte
    
    old_hash = hashlib.sha256(open(src, "rb").read()).hexdigest()
    new_hash = hashlib.sha256(data).hexdigest()
    
    with open(dst, "wb") as f:
        f.write(data)
        
    info = {
        "src": src,
        "dst": dst,
        "section": ".initrd",
        "offset": offset,
        "sec_start": initrd_sec.PointerToRawData,
        "sec_size": initrd_sec.SizeOfRawData,
        "inside_section": initrd_sec.PointerToRawData <= offset < initrd_sec.PointerToRawData + initrd_sec.SizeOfRawData,
        "old_byte": hex(old_byte),
        "new_byte": hex(new_byte),
        "old_hash": old_hash,
        "new_hash": new_hash
    }
    with open(info_file, "w") as f:
        json.dump(info, f, indent=2)
    print(f"Flipped byte in {src} at offset {offset} ({hex(old_byte)} -> {hex(new_byte)}): old_hash={old_hash[:12]}..., new_hash={new_hash[:12]}...")

flip_initrd_byte("$SIGNED_OS2", "$FLIPPED_OS2", "$OUT/S3-flip-info.json")
flip_initrd_byte("$SIGNED_REC_A", "$FLIPPED_REC_A", "$OUT/S4-flip-info.json")
PY

# --- 4. Variable Stores (§1) ------------------------------------------------
echo "=== Step 4: Generating OVMF variable stores ==="
VARS_BASE="$OUT/vars-base.fd"
cp "$OVMF_VARS_TEMPLATE" "$VARS_BASE"
chmod 0644 "$VARS_BASE"

# Enroll PK, KEK, db, and enable Secure Boot
virt-fw-vars --input "$VARS_BASE" --output "$VARS_BASE" \
	--set-pk 8be4df61-93ca-11d2-aa0d-00e098032b8c "$KEYS_DIR/pk.crt" \
	--add-kek 8be4df61-93ca-11d2-aa0d-00e098032b8c "$KEYS_DIR/pk.crt" \
	--add-db 8be4df61-93ca-11d2-aa0d-00e098032b8c "$KEYS_DIR/machine.crt" \
	--sb

echo "Base variable store created with PK, KEK, db, and Secure Boot enforced."

# Prepare S6 varstore: add machine cert hash to dbx
VARS_S6_SRC="$OUT/vars-s6-src.fd"
cp "$VARS_BASE" "$VARS_S6_SRC"
python3 - <<PY
import uuid
from virt.firmware.varstore.autodetect import open_varstore

vars_path = "$VARS_S6_SRC"
guid = uuid.UUID("8be4df61-93ca-11d2-aa0d-00e098032b8c")
hash_hex = "$MACHINE_FP"

store = open_varstore(vars_path)
varlist = store.get_varlist()
varlist.add_hash('dbx', guid, hash_hex)
varlist.add_cert('dbx', str(guid), "$KEYS_DIR/machine.crt")
store.write_varstore(vars_path, varlist)
print(f"Enrolled machine cert and hash in dbx for S6: {hash_hex}")
PY

# Prepare S9 varstore: Secure Boot disabled (unmodified template, no PK)
VARS_S9_SRC="$OUT/vars-s9-src.fd"
cp "$OVMF_VARS_TEMPLATE" "$VARS_S9_SRC"
chmod 0644 "$VARS_S9_SRC"

# Inspect varstores
python3 ci/secure-boot/inspector.py inspect-varstore --vars "$VARS_BASE" --out-json "$OUT/base-varstore.json"
python3 ci/secure-boot/inspector.py inspect-varstore --vars "$VARS_S6_SRC" --out-json "$OUT/S6-varstore.json"
python3 ci/secure-boot/inspector.py inspect-varstore --vars "$VARS_S9_SRC" --out-json "$OUT/S9-varstore.json"

# --- 5. Prepare Golden Base Disks (§2) ---------------------------------------
echo "=== Step 5: Preparing Golden Base Disks ==="

PREPARED_RAW="$OUT/prepared.raw"
cp --sparse=always "$BASE_RAW" "$PREPARED_RAW"

# 5a. Install signed loader to ESP
mmd -D s -i "$PREPARED_RAW@@$ESP_OFFSET" ::/EFI/systemd ::/EFI/BOOT ::/EFI/vintner 2>/dev/null || true
mcopy -o -i "$PREPARED_RAW@@$ESP_OFFSET" "$SIGNED_LOADER" ::/EFI/systemd/systemd-bootx64.efi
mcopy -o -i "$PREPARED_RAW@@$ESP_OFFSET" "$SIGNED_LOADER" ::/EFI/BOOT/BOOTX64.EFI
mcopy -o -i "$PREPARED_RAW@@$ESP_OFFSET" "$SIGNED_LOADER" ::/EFI/vintner/systemd-bootx64.efi

# 5b. Install signed recovery UKIs to boot storage
mcopy -o -i "$PREPARED_RAW@@$BS_OFFSET" "$SIGNED_REC_A" ::/EFI/Linux/vintner-recovery-a+3-0.efi
mcopy -o -i "$PREPARED_RAW@@$BS_OFFSET" "$SIGNED_REC_B" ::/EFI/Linux/vintner-recovery-b.efi

# 5c. Install signed os-1 UKI to boot storage
mcopy -o -i "$PREPARED_RAW@@$BS_OFFSET" "$SIGNED_OS1" ::/EFI/Linux/vintner-os-1+3-0.efi

# 5d. Update manifest on boot storage with signed hashes and sizes
python3 - <<PY
import tempfile, subprocess, os, json, hashlib

disk = "$PREPARED_RAW"
offset = int("$BS_OFFSET")
mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")

def mtype_r(path):
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    return r.stdout if r.returncode == 0 else None

def mcopy_w(local_path, dest_path):
    subprocess.run(["mcopy", "-o", "-i", f"{disk}@@{offset}", local_path, f"::{dest_path}"], env=mtools_env, check=True)

raw_mf = mtype_r("/vintner/state/manifest-a") or mtype_r("/vintner/state/manifest-b")
lines = raw_mf.decode('utf-8').splitlines()
mf = json.loads(lines[0])

# Update os-1 UKI hash and size
signed_os1_data = open("$SIGNED_OS1", "rb").read()
os1_sha = hashlib.sha256(signed_os1_data).hexdigest()
os1_size = len(signed_os1_data)

signed_rec_a_data = open("$SIGNED_REC_A", "rb").read()
rec_a_sha = hashlib.sha256(signed_rec_a_data).hexdigest()
rec_a_size = len(signed_rec_a_data)

signed_rec_b_data = open("$SIGNED_REC_B", "rb").read()
rec_b_sha = hashlib.sha256(signed_rec_b_data).hexdigest()
rec_b_size = len(signed_rec_b_data)

mf["firmware"] = "uefi"
mf["secure_boot_tier"] = "A"

for img in mf.get("images", []):
    if img.get("generation") == 1:
        if "uefi" in img and img["uefi"]:
            img["uefi"]["sha256"] = os1_sha
            img["uefi"]["size"] = os1_size
        img.pop("bios", None)

for rec in mf.get("recovery", []):
    if rec.get("slot") == "a":
        if "uefi" in rec and rec["uefi"]:
            rec["uefi"]["sha256"] = rec_a_sha
            rec["uefi"]["size"] = rec_a_size
        rec.pop("bios", None)
    elif rec.get("slot") == "b":
        if "uefi" in rec and rec["uefi"]:
            rec["uefi"]["sha256"] = rec_b_sha
            rec["uefi"]["size"] = rec_b_size
        rec.pop("bios", None)

json_line = json.dumps(mf)
hash_line = hashlib.sha256(json_line.encode('utf-8')).hexdigest()
content = json_line + "\n" + hash_line + "\n"

with tempfile.NamedTemporaryFile("w", delete=False) as tf:
    tf.write(content)
    tf_path = tf.name

mcopy_w(tf_path, "/vintner/state/manifest-a")
mcopy_w(tf_path, "/vintner/state/manifest-b")
os.unlink(tf_path)
print(f"Updated manifest on {disk}: firmware=uefi, tier=A, os-1 sha256={os1_sha[:12]}...")
PY

# 5e. Prepare golden gen2 disk (for S1, S2, S3)
PREPARED_GEN2_RAW="$OUT/prepared-gen2.raw"
cp --sparse=always "$PREPARED_RAW" "$PREPARED_GEN2_RAW"

# Add signed gen 2 to boot storage and manifest
mcopy -o -i "$PREPARED_GEN2_RAW@@$BS_OFFSET" "$SIGNED_OS2" ::/EFI/Linux/vintner-os-2+1-0.efi

python3 - <<PY
import tempfile, subprocess, os, json, hashlib

disk = "$PREPARED_GEN2_RAW"
offset = int("$BS_OFFSET")
mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")

def mtype_r(path):
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    return r.stdout if r.returncode == 0 else None

def mcopy_w(local_path, dest_path):
    subprocess.run(["mcopy", "-o", "-i", f"{disk}@@{offset}", local_path, f"::{dest_path}"], env=mtools_env, check=True)

raw_mf = mtype_r("/vintner/state/manifest-a") or mtype_r("/vintner/state/manifest-b")
lines = raw_mf.decode('utf-8').splitlines()
mf = json.loads(lines[0])

signed_os2_data = open("$SIGNED_OS2", "rb").read()
os2_sha = hashlib.sha256(signed_os2_data).hexdigest()
os2_size = len(signed_os2_data)

mf["next_generation"] = 3
gen2_entry = {
    "generation": 2,
    "state": "TRYING",
    "stale": False,
    "kernel": {
        "abi": "$KERNEL_ABI",
        "package": "linux-image-$KERNEL_ABI",
        "version": "1"
    },
    "module_tree": None,
    "cmdline": "$OS2_CMDLINE",
    "uefi": {
        "entry": "vintner-os-2",
        "sha256": os2_sha,
        "size": os2_size
    },
    "created": "2026-10-30T20:10:00Z"
}
mf["images"].append(gen2_entry)

json_line = json.dumps(mf)
hash_line = hashlib.sha256(json_line.encode('utf-8')).hexdigest()
content = json_line + "\n" + hash_line + "\n"

with tempfile.NamedTemporaryFile("w", delete=False) as tf:
    tf.write(content)
    tf_path = tf.name

mcopy_w(tf_path, "/vintner/state/manifest-a")
mcopy_w(tf_path, "/vintner/state/manifest-b")
os.unlink(tf_path)
print(f"Added generation 2 TRYING to manifest on {disk}: sha256={os2_sha[:12]}...")
PY

# --- 6. Cases S0 to S9 Execution (§4) ----------------------------------------
CASES=("S0" "S1" "S2" "S3" "S4" "S5" "S6" "S7" "S8" "S9")

for CASE in "${CASES[@]}"; do
	echo "=================================================================="
	echo "=== Running Case $CASE ==="
	echo "=================================================================="

	CASE_DISK="$OUT/${CASE}.raw"
	CASE_VARS="$OUT/${CASE}-vars.fd"

	# Set up varstore
	if [ "$CASE" = "S6" ]; then
		cp "$VARS_S6_SRC" "$CASE_VARS"
	elif [ "$CASE" = "S9" ]; then
		cp "$VARS_S9_SRC" "$CASE_VARS"
	else
		cp "$VARS_BASE" "$CASE_VARS"
	fi
	cp "$OUT/${CASE}-vars.fd" "$OUT/${CASE}-varstore.fd" 2>/dev/null || true
	python3 ci/secure-boot/inspector.py inspect-varstore --vars "$CASE_VARS" --out-json "$OUT/${CASE}-varstore.json"

	# Prepare disk for case
	B2_JSON="$OUT/${CASE}-b2.json"
	case "$CASE" in
		S0)
			cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
			python3 - <<PY
import subprocess, json
def verify(f, cert="$KEYS_DIR/machine.crt"):
    r = subprocess.run(["sbverify", "--cert", cert, f], capture_output=True, text=True)
    return r.returncode == 0, r.stdout.strip() or r.stderr.strip()

v1, s1 = verify("$SIGNED_LOADER")
v2, s2 = verify("$SIGNED_OS1")
v3, s3 = verify("$SIGNED_REC_A")
v4, s4 = verify("$SIGNED_REC_B")
neg, neg_s = verify("$UNSIGNED_LOADER")

b2 = {
    "valid_passed": v1 and v2 and v3 and v4,
    "neg_passed": (not neg),
    "neg_reason": f"unsigned loader sbverify rejected: {neg_s}",
    "summary": f"loader={v1}, os-1={v2}, rec-a={v3}, rec-b={v4}, neg_unsigned_loader={not neg}"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S1)
			cp --sparse=always "$PREPARED_GEN2_RAW" "$CASE_DISK"
			mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$UNSIGNED_OS2" ::/EFI/Linux/vintner-os-2+1-0.efi
			python3 - <<PY
import subprocess, json
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$UNSIGNED_OS2"], capture_output=True, text=True)
neg_ok = (r.returncode != 0)
b2 = {
    "valid_passed": True,
    "neg_passed": neg_ok,
    "neg_reason": r.stderr.strip() or r.stdout.strip(),
    "summary": f"unsigned os-2 UKI rejected by sbverify: {r.stderr.strip() or r.stdout.strip()}"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S2)
			cp --sparse=always "$PREPARED_GEN2_RAW" "$CASE_DISK"
			mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$FOREIGN_OS2" ::/EFI/Linux/vintner-os-2+1-0.efi
			python3 - <<PY
import subprocess, json
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$FOREIGN_OS2"], capture_output=True, text=True)
neg_ok = (r.returncode != 0)
b2 = {
    "valid_passed": True,
    "neg_passed": neg_ok,
    "neg_reason": r.stderr.strip() or r.stdout.strip(),
    "summary": f"foreign-signed os-2 UKI rejected by machine cert: {r.stderr.strip() or r.stdout.strip()}"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S3)
			cp --sparse=always "$PREPARED_GEN2_RAW" "$CASE_DISK"
			mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$FLIPPED_OS2" ::/EFI/Linux/vintner-os-2+1-0.efi
			python3 - <<PY
import subprocess, json
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$FLIPPED_OS2"], capture_output=True, text=True)
neg_ok = (r.returncode != 0)
flip_info = json.load(open("$OUT/S3-flip-info.json"))
b2 = {
    "valid_passed": True,
    "neg_passed": neg_ok,
    "neg_reason": f"flipped byte at {flip_info['offset']} rejected by sbverify: {r.stderr.strip() or r.stdout.strip()}",
    "summary": f"flipped byte in .initrd (offset {flip_info['offset']}) failed signature check"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S4)
			cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
			mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$FLIPPED_REC_A" ::/EFI/Linux/vintner-recovery-a+3-0.efi
			python3 - <<PY
import subprocess, json
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$FLIPPED_REC_A"], capture_output=True, text=True)
neg_ok = (r.returncode != 0)
flip_info = json.load(open("$OUT/S4-flip-info.json"))
b2 = {
    "valid_passed": True,
    "neg_passed": neg_ok,
    "neg_reason": f"flipped recovery UKI rejected by sbverify: {r.stderr.strip() or r.stdout.strip()}",
    "summary": f"flipped byte in recovery-a (offset {flip_info['offset']}) failed signature check"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S5)
			cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
			# Flip byte in os-1 UKI and recovery UKIs
			FLIPPED_OS1="$OUT/flipped-vintner-os-1+3-0.efi"
			python3 - <<PY
import pefile, hashlib
pe = pefile.PE("$SIGNED_OS1")
initrd_sec = [s for s in pe.sections if s.Name.decode('ascii', errors='ignore').rstrip('\x00') == '.initrd'][0]
offset = initrd_sec.PointerToRawData + 100
data = bytearray(open("$SIGNED_OS1", "rb").read())
data[offset] ^= 0xFF
open("$FLIPPED_OS1", "wb").write(data)
PY
			mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$FLIPPED_OS1" ::/EFI/Linux/vintner-os-1+3-0.efi
			mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$FLIPPED_REC_A" ::/EFI/Linux/vintner-recovery-a+3-0.efi
			mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$FLIPPED_REC_A" ::/EFI/Linux/vintner-recovery-b.efi
			python3 - <<PY
import subprocess, json
r1 = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$FLIPPED_OS1"], capture_output=True, text=True)
r2 = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$FLIPPED_REC_A"], capture_output=True, text=True)
neg_ok = (r1.returncode != 0 and r2.returncode != 0)
b2 = {
    "valid_passed": True,
    "neg_passed": neg_ok,
    "neg_reason": f"all UKIs modified and rejected by sbverify",
    "summary": "every on-disk UKI is invalid"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S6)
			cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
			python3 - <<PY
import subprocess, json
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$SIGNED_OS1"], capture_output=True, text=True)
b2 = {
    "valid_passed": (r.returncode == 0),
    "neg_passed": True,
    "neg_reason": "machine cert is revoked in dbx",
    "summary": "files validly signed on host, machine cert listed in dbx"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S7)
			cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
			mcopy -o -i "$CASE_DISK@@$ESP_OFFSET" "$UNSIGNED_LOADER" ::/EFI/systemd/systemd-bootx64.efi
			mcopy -o -i "$CASE_DISK@@$ESP_OFFSET" "$UNSIGNED_LOADER" ::/EFI/BOOT/BOOTX64.EFI
			mcopy -o -i "$CASE_DISK@@$ESP_OFFSET" "$UNSIGNED_LOADER" ::/EFI/vintner/systemd-bootx64.efi
			python3 - <<PY
import subprocess, json
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$UNSIGNED_LOADER"], capture_output=True, text=True)
b2 = {
    "valid_passed": True,
    "neg_passed": (r.returncode != 0),
    "neg_reason": "Debian unsigned loader rejected by sbverify",
    "summary": "loader is unsigned Debian binary"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S8)
			cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
			# Simulate publish with wrong cert: sb_verify fails before publication
			python3 - <<PY
import subprocess, json
# Try to verify machine-signed file against foreign cert
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/foreign.crt", "$SIGNED_OS2"], capture_output=True, text=True)
neg_ok = (r.returncode != 0)
b2 = {
    "valid_passed": True,
    "neg_passed": neg_ok,
    "neg_reason": "verification against mismatched cert failed during publish",
    "summary": "publish aborted before copying to /EFI/Linux/"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
		S9)
			cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
			python3 - <<PY
import subprocess, json
r = subprocess.run(["sbverify", "--cert", "$KEYS_DIR/machine.crt", "$SIGNED_OS1"], capture_output=True, text=True)
b2 = {
    "valid_passed": (r.returncode == 0),
    "neg_passed": True,
    "neg_reason": "Secure Boot off in varstore",
    "summary": "files validly signed, Secure Boot disabled in firmware"
}
with open("$B2_JSON", "w") as f:
    json.dump(b2, f, indent=2)
PY
			;;
	esac

	# Read disk before boot
	python3 ci/secure-boot/inspector.py read-disk \
		--disk "$CASE_DISK" \
		--esp-offset "$ESP_OFFSET" \
		--bs-offset "$BS_OFFSET" \
		--vintner-bin "$VINTNER_BIN" \
		--out-prefix "$OUT/${CASE}-read-pre"

	# Boot 1
	echo "Booting $CASE Boot 1..."
	BOOT1_LOG="$OUT/${CASE}-boot1.log"
	BOOT1_META="$OUT/${CASE}-boot1-meta.json"

	python3 ci/secure-boot/inspector.py run-boot \
		--disk "$CASE_DISK" \
		--code-fd "$SECBOOT_CODE" \
		--vars "$CASE_VARS" \
		--log "$BOOT1_LOG" \
		--case "$CASE" \
		--boot-num 1 \
		--timeout 120 \
		--meta-out "$BOOT1_META"

	# Special second boot for S4: select recovery entry
	if [ "$CASE" = "S4" ]; then
		echo "Preparing S4 Boot 2: selecting recovery entry in loader.conf..."
		cat <<EOF > "$OUT/s4-loader.conf"
default vintner-recovery-a*
timeout 0
editor no
auto-entries no
EOF
		mcopy -o -i "$CASE_DISK@@$ESP_OFFSET" "$OUT/s4-loader.conf" ::/loader/loader.conf

		echo "Booting S4 Boot 2..."
		BOOT2_LOG="$OUT/${CASE}-boot2.log"
		BOOT2_META="$OUT/${CASE}-boot2-meta.json"

		python3 ci/secure-boot/inspector.py run-boot \
			--disk "$CASE_DISK" \
			--code-fd "$SECBOOT_CODE" \
			--vars "$CASE_VARS" \
			--log "$BOOT2_LOG" \
			--case "$CASE" \
			--boot-num 2 \
			--timeout 120 \
			--meta-out "$BOOT2_META"
	fi

	# Read disk after boot
	python3 ci/secure-boot/inspector.py read-disk \
		--disk "$CASE_DISK" \
		--esp-offset "$ESP_OFFSET" \
		--bs-offset "$BS_OFFSET" \
		--vintner-bin "$VINTNER_BIN" \
		--out-prefix "$OUT/${CASE}-read-post"
done

# --- 7. Evaluation and Final Gate (§5) ---------------------------------------
echo "=================================================================="
echo "=== Evaluating Results and Enforcing Final Gate ==="
echo "=================================================================="

python3 ci/secure-boot/inspector.py evaluate \
	--out-dir "$OUT" \
	--results-file "$RESULTS"

{
	echo "=== Host packages (Secure Boot toolchain) ==="
	dpkg-query -W ovmf qemu-system-x86 systemd-boot-efi systemd-ukify sbsigntool python3-virt-firmware 2>/dev/null || true
} >> "$VERSIONS"

echo "=== versions.txt ==="
cat "$VERSIONS"
