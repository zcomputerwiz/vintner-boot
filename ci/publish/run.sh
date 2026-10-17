#!/bin/bash
# Publisher integration test on an installed vintner system (WO-0005).
#
# Tests that kernel upgrades and initramfs regenerations trigger the publisher
# and create bootable candidates, preserving known-good, under real APT operations
# across UEFI and BIOS.
#
# Deliverables:
#   ci/publish/run.sh <uefi|bios>
#   ci/publish/README.md
#
# Requires: root via sudo, KVM, loop devices with partition scanning,
# mtools, qemu-system-x86, qemu-utils, cloud-image-utils, sfdisk, python3,
# python3-virt-firmware, golang-go.
set -euo pipefail

if [ $# -ne 1 ] || { [ "$1" != "uefi" ] && [ "$1" != "bios" ]; }; then
	echo "Usage: $0 {uefi|bios}" >&2
	exit 1
fi

FIRMWARE="$1"
cd "$(dirname "$0")/../.."

OUT="ci/out/publish/$FIRMWARE"
MNT="$OUT/mnt"
RESULTS="$OUT/results.tsv"
BOOTS_SUMMARY="$OUT/boots-summary.txt"
export MTOOLS_SKIP_CHECK=1

mkdir -p "$OUT" "$MNT"
: > "$BOOTS_SUMMARY"

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
	if [ -f "src/gen_manifest.go" ]; then
		rm -f src/gen_manifest.go || true
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
./ci/lib/vintner-base.sh "$FIRMWARE" "$OUT"
# shellcheck source=/dev/null # written by vintner-base.sh
. "$OUT/base.env"
# shellcheck source=ci/lib/vintner-qemu.sh
. ci/lib/vintner-qemu.sh

VINTNER_BIN="$OUT/vintner"
if [ ! -f "$VINTNER_BIN" ]; then
	echo "Building static vintner binary on host..."
	(cd src && CGO_ENABLED=0 go build -o "../$VINTNER_BIN" ./cmd/vintner)
fi

# --- 2. Prepare golden test disk (Setup §3) ----------------------------------
echo "=================================================================="
echo "=== Preparing golden test disk for publisher ($FIRMWARE) ==="
echo "=================================================================="

PREP_RAW="$OUT/prep.raw"
PREP_VARS=""
cp --sparse=always "$BASE_RAW" "$PREP_RAW"
if [ "$FIRMWARE" = "uefi" ]; then
	PREP_VARS="$OUT/prep-vars.fd"
	cp "$BASE_VARS" "$PREP_VARS"
fi

# 2a. Install systemd-ukify and systemd-boot-efi in guest via apt-get
echo "Booting preparation step 1: install systemd-ukify and systemd-boot-efi..."
PREP_INSTALL_CMD="DEBIAN_FRONTEND=noninteractive apt-get -y install systemd-ukify systemd-boot-efi"
PREP_INSTALL_SEED=$(make_seed "prep-install" "$PREP_INSTALL_CMD")
run_qemu "$PREP_RAW" "$PREP_INSTALL_SEED" "$OUT/prep-install.log" 600 "$PREP_VARS"

if ! grep -q "=== OP EXIT: 0 ===" "$OUT/prep-install.log"; then
	echo "ERROR: Preparation package install failed:" >&2
	tr -d '\r' < "$OUT/prep-install.log" | tail -n 40 >&2
	exit 1
fi

# 2b. Copy the two hooks to guest root
echo "Copying hooks and updated vintner binary to guest root..."
echo "Block devices on host before losetup (prep hooks):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$PREP_RAW")
sudo mount "${LOOP}p1" "$MNT"

sudo cp "$VINTNER_BIN" "$MNT/usr/sbin/vintner"
sudo chmod 0755 "$MNT/usr/sbin/vintner"
sudo ln -sf /usr/sbin/vintner "$MNT/usr/bin/vintner"

sudo cp "integration/kernel-hooks/zz-vintner" "$MNT/etc/kernel/postinst.d/zz-vintner"
sudo chmod 0755 "$MNT/etc/kernel/postinst.d/zz-vintner"

sudo mkdir -p "$MNT/etc/initramfs/post-update.d"
sudo cp "integration/initramfs-hooks/zz-vintner" "$MNT/etc/initramfs/post-update.d/zz-vintner"
sudo chmod 0755 "$MNT/etc/initramfs/post-update.d/zz-vintner"

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# 2c. Boot once so that os-1 is blessed GOOD
echo "Booting preparation step 2: boot once to bless os-1..."
PREP_BLESS_SEED=$(make_seed "prep-bless" "true")
run_qemu "$PREP_RAW" "$PREP_BLESS_SEED" "$OUT/prep-bless.log" 180 "$PREP_VARS"

# 2d. Check that prepared disk has exactly one image, os-1, GOOD, and nothing under /vintner/staging/
python3 ci/publish/inspect.py verify-prep \
	--disk "$PREP_RAW" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN"

PREPARED_RAW="$OUT/prepared.raw"
PREPARED_VARS=""
cp --sparse=always "$PREP_RAW" "$PREPARED_RAW"
if [ "$FIRMWARE" = "uefi" ]; then
	PREPARED_VARS="$OUT/prepared-vars.fd"
	cp "$PREP_VARS" "$PREPARED_VARS"
fi

printf "ID\tCase\tStatus\tEvidence\n" > "$RESULTS"

# Helper to log boot image
record_boot() {
	local case_name="$1"
	local boot_num="$2"
	local log_path="$3"
	local booted
	booted=$(python3 ci/publish/inspect.py get-booted --log "$log_path" --firmware "$FIRMWARE")
	echo "$case_name(B$boot_num:$booted)" >> "$BOOTS_SUMMARY"
	echo "[$case_name] Boot $boot_num finished: booted $booted"
}

# --- 3. Cases Execution -----------------------------------------------------

# === Case C0: Control (reboot once) ===
echo "=================================================================="
echo "=== Running Case C0 (control) ==="
echo "=================================================================="
C0_DISK="$OUT/C0.raw"
C0_VARS=""
cp --sparse=always "$PREPARED_RAW" "$C0_DISK"
[ "$FIRMWARE" = "uefi" ] && { C0_VARS="$OUT/C0-vars.fd"; cp "$PREPARED_VARS" "$C0_VARS"; }

C0_SEED1=$(make_seed "C0-b1" "true")
echo "Booting C0 Boot 1..."
run_qemu "$C0_DISK" "$C0_SEED1" "$OUT/C0-boot1.log" 180 "$C0_VARS"
record_boot "C0" 1 "$OUT/C0-boot1.log"

C0_SEED2=$(make_seed "C0-b2" "true")
echo "Booting C0 Boot 2..."
run_qemu "$C0_DISK" "$C0_SEED2" "$OUT/C0-boot2.log" 180 "$C0_VARS"
record_boot "C0" 2 "$OUT/C0-boot2.log"

python3 ci/publish/inspect.py inspect-case \
	--firmware "$FIRMWARE" \
	--case "C0" \
	--disk "$C0_DISK" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN" \
	--prepared-disk "$PREPARED_RAW" \
	--boot1-log "$OUT/C0-boot1.log" \
	--boot2-log "$OUT/C0-boot2.log" \
	--results-file "$RESULTS"

# === Case K1: Second kernel with new ABI ===
echo "=================================================================="
echo "=== Running Case K1 (install second kernel) ==="
echo "=================================================================="
K1_DISK="$OUT/K1.raw"
K1_VARS=""
cp --sparse=always "$PREPARED_RAW" "$K1_DISK"
[ "$FIRMWARE" = "uefi" ] && { K1_VARS="$OUT/K1-vars.fd"; cp "$PREPARED_VARS" "$K1_VARS"; }

K1_CMD="DEBIAN_FRONTEND=noninteractive apt-get -y install linux-image-amd64"
K1_SEED1=$(make_seed "K1-b1" "$K1_CMD")
echo "Booting K1 Boot 1..."
run_qemu "$K1_DISK" "$K1_SEED1" "$OUT/K1-boot1.log" 600 "$K1_VARS"
record_boot "K1" 1 "$OUT/K1-boot1.log"

K1_B1_DISK="$OUT/K1-b1.raw"
cp --sparse=always "$K1_DISK" "$K1_B1_DISK"

K1_SEED2=$(make_seed "K1-b2" "true")
echo "Booting K1 Boot 2..."
run_qemu "$K1_DISK" "$K1_SEED2" "$OUT/K1-boot2.log" 180 "$K1_VARS"
record_boot "K1" 2 "$OUT/K1-boot2.log"

# Mount root read-only for inspection
echo "Block devices on host before losetup (K1 inspect):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$K1_DISK")
sudo mount -o ro "${LOOP}p1" "$MNT"

python3 ci/publish/inspect.py inspect-case \
	--firmware "$FIRMWARE" \
	--case "K1" \
	--disk "$K1_DISK" \
	--b1-disk "$K1_B1_DISK" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN" \
	--prepared-disk "$PREPARED_RAW" \
	--root-mount "$MNT" \
	--boot1-log "$OUT/K1-boot1.log" \
	--boot2-log "$OUT/K1-boot2.log" \
	--results-file "$RESULTS"

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# === Case K2: Add line to conf.d, update-initramfs ===
echo "=================================================================="
echo "=== Running Case K2 (update initramfs) ==="
echo "=================================================================="
K2_DISK="$OUT/K2.raw"
K2_VARS=""
cp --sparse=always "$PREPARED_RAW" "$K2_DISK"
[ "$FIRMWARE" = "uefi" ] && { K2_VARS="$OUT/K2-vars.fd"; cp "$PREPARED_VARS" "$K2_VARS"; }

# shellcheck disable=SC2016
K2_CMD='echo "COMPRESS=gzip" >> /etc/initramfs-tools/conf.d/vintner-test && update-initramfs -u -k "$(uname -r)"'
K2_SEED1=$(make_seed "K2-b1" "$K2_CMD")
echo "Booting K2 Boot 1..."
run_qemu "$K2_DISK" "$K2_SEED1" "$OUT/K2-boot1.log" 300 "$K2_VARS"
record_boot "K2" 1 "$OUT/K2-boot1.log"

K2_B1_DISK="$OUT/K2-b1.raw"
cp --sparse=always "$K2_DISK" "$K2_B1_DISK"

K2_SEED2=$(make_seed "K2-b2" "true")
echo "Booting K2 Boot 2..."
run_qemu "$K2_DISK" "$K2_SEED2" "$OUT/K2-boot2.log" 180 "$K2_VARS"
record_boot "K2" 2 "$OUT/K2-boot2.log"

echo "Block devices on host before losetup (K2 inspect):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$K2_DISK")
sudo mount -o ro "${LOOP}p1" "$MNT"

python3 ci/publish/inspect.py inspect-case \
	--firmware "$FIRMWARE" \
	--case "K2" \
	--disk "$K2_DISK" \
	--b1-disk "$K2_B1_DISK" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN" \
	--prepared-disk "$PREPARED_RAW" \
	--root-mount "$MNT" \
	--boot1-log "$OUT/K2-boot1.log" \
	--boot2-log "$OUT/K2-boot2.log" \
	--results-file "$RESULTS"

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# === Case K3: K2 twice then publish ===
echo "=================================================================="
echo "=== Running Case K3 (two changes + publish) ==="
echo "=================================================================="
K3_DISK="$OUT/K3.raw"
K3_VARS=""
cp --sparse=always "$PREPARED_RAW" "$K3_DISK"
[ "$FIRMWARE" = "uefi" ] && { K3_VARS="$OUT/K3-vars.fd"; cp "$PREPARED_VARS" "$K3_VARS"; }

K3_CMD=$(cat <<'EOF'
echo "# k3 change 1" >> /etc/initramfs-tools/conf.d/vintner-test
update-initramfs -u -k "$(uname -r)"
echo "# k3 change 2" >> /etc/initramfs-tools/conf.d/vintner-test
update-initramfs -u -k "$(uname -r)"
vintner publish --kernel "$(uname -r)" 2>&1 | tee /dev/ttyS0
EOF
)
K3_SEED1=$(make_seed "K3-b1" "$K3_CMD")
echo "Booting K3 Boot 1..."
run_qemu "$K3_DISK" "$K3_SEED1" "$OUT/K3-boot1.log" 300 "$K3_VARS"
record_boot "K3" 1 "$OUT/K3-boot1.log"

K3_SEED2=$(make_seed "K3-b2" "true")
echo "Booting K3 Boot 2..."
run_qemu "$K3_DISK" "$K3_SEED2" "$OUT/K3-boot2.log" 180 "$K3_VARS"
record_boot "K3" 2 "$OUT/K3-boot2.log"

python3 ci/publish/inspect.py inspect-case \
	--firmware "$FIRMWARE" \
	--case "K3" \
	--disk "$K3_DISK" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN" \
	--prepared-disk "$PREPARED_RAW" \
	--boot1-log "$OUT/K3-boot1.log" \
	--boot2-log "$OUT/K3-boot2.log" \
	--op-log "$OUT/K3-boot1.log" \
	--results-file "$RESULTS"

# === Case U1: Unchanged publish ===
echo "=================================================================="
echo "=== Running Case U1 (unchanged publish) ==="
echo "=================================================================="
U1_DISK="$OUT/U1.raw"
U1_VARS=""
cp --sparse=always "$PREPARED_RAW" "$U1_DISK"
[ "$FIRMWARE" = "uefi" ] && { U1_VARS="$OUT/U1-vars.fd"; cp "$PREPARED_VARS" "$U1_VARS"; }

U1_CMD=$(cat <<'EOF'
systemctl is-active --quiet vintner-bless.service || systemctl start vintner-bless.service
mkdir -p /tmp/bs
BS_DEV=$(blkid -t LABEL=BOOTSTORAGE -o device || blkid -t TYPE=vfat -o device | head -n 1)
mount -t vfat "$BS_DEV" /tmp/bs
find /tmp/bs -type f -exec sha256sum {} + | sort > /tmp/bs-before
umount /tmp/bs
vintner publish --kernel "$(uname -r)" > /tmp/pub-u1.log 2>&1
mount -t vfat "$BS_DEV" /tmp/bs
find /tmp/bs -type f -exec sha256sum {} + | sort > /tmp/bs-after
umount /tmp/bs
if ! diff -u /tmp/bs-before /tmp/bs-after; then
  echo "DIFF_FAILED" > /dev/ttyS0
fi
cat /tmp/pub-u1.log > /dev/ttyS0
EOF
)
U1_SEED1=$(make_seed "U1-b1" "$U1_CMD")
echo "Booting U1 Boot 1..."
run_qemu "$U1_DISK" "$U1_SEED1" "$OUT/U1-boot1.log" 180 "$U1_VARS"
record_boot "U1" 1 "$OUT/U1-boot1.log"

python3 ci/publish/inspect.py inspect-case \
	--firmware "$FIRMWARE" \
	--case "U1" \
	--disk "$U1_DISK" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN" \
	--prepared-disk "$PREPARED_RAW" \
	--boot1-log "$OUT/U1-boot1.log" \
	--op-log "$OUT/U1-boot1.log" \
	--results-file "$RESULTS"

# === Case G1: Garbage collection (4 rounds) ===
echo "=================================================================="
echo "=== Running Case G1 (garbage collection over budget) ==="
echo "=================================================================="
G1_DISK="$OUT/G1.raw"
G1_VARS=""
cp --sparse=always "$PREPARED_RAW" "$G1_DISK"
[ "$FIRMWARE" = "uefi" ] && { G1_VARS="$OUT/G1-vars.fd"; cp "$PREPARED_VARS" "$G1_VARS"; }

for round in 1 2 3 4; do
	echo "--- G1 Round $round: operation ---"
	G1_CMD="echo \"# g1 r$round\" >> /etc/initramfs-tools/conf.d/vintner-test && update-initramfs -u -k \"\$(uname -r)\""
	G1_SEED_OP=$(make_seed "G1-r${round}-op" "$G1_CMD")
	run_qemu "$G1_DISK" "$G1_SEED_OP" "$OUT/G1-r${round}-op.log" 300 "$G1_VARS"
	record_boot "G1" "R${round}op" "$OUT/G1-r${round}-op.log"

	echo "--- G1 Round $round: reboot ---"
	G1_SEED_RB=$(make_seed "G1-r${round}-rb" "true")
	run_qemu "$G1_DISK" "$G1_SEED_RB" "$OUT/G1-r${round}-rb.log" 180 "$G1_VARS"
	record_boot "G1" "R${round}rb" "$OUT/G1-r${round}-rb.log"
done

echo "Block devices on host before losetup (G1 inspect):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$G1_DISK")
sudo mount -o ro "${LOOP}p1" "$MNT"

python3 ci/publish/inspect.py inspect-case \
	--firmware "$FIRMWARE" \
	--case "G1" \
	--disk "$G1_DISK" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN" \
	--prepared-disk "$PREPARED_RAW" \
	--root-mount "$MNT" \
	--results-file "$RESULTS"

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# === Case R1: Replaced/purged kernel package ===
echo "=================================================================="
echo "=== Running Case R1 (replaced kernel package) ==="
echo "=================================================================="
R1_DISK="$OUT/R1.raw"
R1_VARS=""
cp --sparse=always "$PREPARED_RAW" "$R1_DISK"
[ "$FIRMWARE" = "uefi" ] && { R1_VARS="$OUT/R1-vars.fd"; cp "$PREPARED_VARS" "$R1_VARS"; }

R1_CMD=$(cat <<'EOF'
DEBIAN_FRONTEND=noninteractive apt-get -y install linux-image-amd64
NEW_PKG=$(dpkg-query -W -f='${Package}\n' 'linux-image-[0-9]*' | grep -v "$(uname -r)" | head -n 1)
DEBIAN_FRONTEND=noninteractive apt-get -y purge "$NEW_PKG"
echo "COMPRESS=gzip" >> /etc/initramfs-tools/conf.d/vintner-test
update-initramfs -u -k "$(uname -r)"
EOF
)
R1_SEED1=$(make_seed "R1-b1" "$R1_CMD")
echo "Booting R1 Boot 1..."
run_qemu "$R1_DISK" "$R1_SEED1" "$OUT/R1-boot1.log" 600 "$R1_VARS"
record_boot "R1" 1 "$OUT/R1-boot1.log"

R1_B1_DISK="$OUT/R1-b1.raw"
cp --sparse=always "$R1_DISK" "$R1_B1_DISK"

R1_SEED2=$(make_seed "R1-b2" "true")
echo "Booting R1 Boot 2..."
run_qemu "$R1_DISK" "$R1_SEED2" "$OUT/R1-boot2.log" 180 "$R1_VARS"
record_boot "R1" 2 "$OUT/R1-boot2.log"

python3 ci/publish/inspect.py inspect-case \
	--firmware "$FIRMWARE" \
	--case "R1" \
	--disk "$R1_DISK" \
	--b1-disk "$R1_B1_DISK" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$VINTNER_BIN" \
	--prepared-disk "$PREPARED_RAW" \
	--boot1-log "$OUT/R1-boot1.log" \
	--boot2-log "$OUT/R1-boot2.log" \
	--results-file "$RESULTS"

# === Check E9: Summary across all boots ===
python3 ci/publish/inspect.py record-e9 \
	--results-file "$RESULTS" \
	--summary-file "$BOOTS_SUMMARY"

# --- 4. Summary and Exit Evaluation -----------------------------------------
echo "=================================================================="
echo "=== Test Summary Table ($FIRMWARE) ==="
echo "=================================================================="
cat "$RESULTS"
echo "=== versions.txt ==="
cat "$VERSIONS"

FAILURES=$(awk -F'\t' '$3=="FAIL"{print $1 "-" $2}' "$RESULTS")
if [ -n "$FAILURES" ]; then
	echo "ERROR: Publisher test failed on checks: $FAILURES" >&2
	exit 1
fi

echo "All $FIRMWARE publisher checks passed successfully!"
