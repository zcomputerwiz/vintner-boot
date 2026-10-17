#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Publication interruption integration test on an installed vintner system (WO-0007).
#
# Cuts the power at every durable step of a publish, on both UEFI and BIOS,
# verifying fallback boot, manifest reconciliation, and subsequent publication.
#
# Deliverables:
#   ci/publish-interrupt/run.sh <uefi|bios>
#   ci/publish-interrupt/README.md
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

OUT="ci/out/publish-interrupt/$FIRMWARE"
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

TAGGED_BIN="$OUT/vintner-tagged"
DEFAULT_BIN="$OUT/vintner-default"

echo "Building tagged and default vintner binaries on host..."
(cd src && CGO_ENABLED=0 go build -tags vintner_testpoints -o "../$TAGGED_BIN" ./cmd/vintner)
(cd src && CGO_ENABLED=0 go build -o "../$DEFAULT_BIN" ./cmd/vintner)

# Pre-case check 2 (§2): default build contains neither VINTNER_TEST_INTERRUPT nor vintner-testpoint
if strings "$DEFAULT_BIN" | grep -q -E "VINTNER_TEST_INTERRUPT|vintner-testpoint"; then
	echo "FAIL: default binary contains testpoint strings!" >&2
	exit 1
fi
echo "Pre-check passed: default build contains no testpoint strings."

# read_disk <disk> <prefix>
read_disk() {
	python3 ci/publish-interrupt/inspector.py read \
		--disk "$1" \
		--bs-offset "$BS_OFFSET" \
		--vintner-bin "$TAGGED_BIN" \
		--root "$ROOT" \
		--out-prefix "$OUT/$2"
}

# --- 2. Prepare Base Golden Test Disk (§2) -----------------------------------
echo "=================================================================="
echo "=== Preparing Base Golden Test Disk ($FIRMWARE) ==="
echo "=================================================================="

PREP_RAW="$OUT/prep.raw"
PREP_VARS=""
cp --sparse=always "$BASE_RAW" "$PREP_RAW"
if [ "$FIRMWARE" = "uefi" ]; then
	PREP_VARS="$OUT/prep-vars.fd"
	# shellcheck disable=SC2153
	cp "$BASE_VARS" "$PREP_VARS"
fi

# 2a. Install systemd-ukify and systemd-boot-efi in guest via apt-get
echo "Booting preparation step 1: install packages in guest..."
PREP_INSTALL_CMD="DEBIAN_FRONTEND=noninteractive apt-get -y install systemd-ukify systemd-boot-efi && apt-mark hold linux-image-cloud-amd64 linux-image-$KERNEL_ABI"
PREP_INSTALL_SEED=$(make_seed "prep-install" "$PREP_INSTALL_CMD")
run_qemu "$PREP_RAW" "$PREP_INSTALL_SEED" "$OUT/prep-install.log" 600 "$PREP_VARS"

if ! grep -q "=== OP EXIT: 0 ===" "$OUT/prep-install.log"; then
	echo "ERROR: Preparation package install failed:" >&2
	tr -d '\r' < "$OUT/prep-install.log" | tail -n 40 >&2
	exit 1
fi

# 2b. Copy hooks and tagged vintner binary to guest root
echo "Copying hooks and tagged vintner binary to guest root..."
echo "Block devices on host before losetup (prep hooks):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$PREP_RAW")
sudo mount "${LOOP}p1" "$MNT"

sudo cp "$TAGGED_BIN" "$MNT/usr/sbin/vintner"
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

# Pre-case check 1 (§2): guest's /usr/sbin/vintner matches sha256 of tagged binary
echo "Checking guest /usr/sbin/vintner sha256 hash..."
echo "Block devices on host before losetup (check hash):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -r -f --show "$PREP_RAW")
sudo mount -o ro "${LOOP}p1" "$MNT"
GUEST_HASH=$(sha256sum "$MNT/usr/sbin/vintner" | awk '{print $1}')
HOST_HASH=$(sha256sum "$TAGGED_BIN" | awk '{print $1}')
sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

if [ "$GUEST_HASH" != "$HOST_HASH" ]; then
	echo "FAIL: guest /usr/sbin/vintner hash ($GUEST_HASH) does not match host tagged binary ($HOST_HASH)" >&2
	exit 1
fi
echo "Pre-check passed: guest binary hash matches host tagged binary ($HOST_HASH)."

# 2d. Verify base prepared disk
python3 ci/publish-interrupt/inspector.py verify-prep-base \
	--disk "$PREP_RAW" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$TAGGED_BIN"

BASE_PREPARED_RAW="$OUT/base-prepared.raw"
BASE_PREPARED_VARS=""
cp --sparse=always "$PREP_RAW" "$BASE_PREPARED_RAW"
if [ "$FIRMWARE" = "uefi" ]; then
	BASE_PREPARED_VARS="$OUT/base-prepared-vars.fd"
	cp "$PREP_VARS" "$BASE_PREPARED_VARS"
fi

# --- 3. Prepare Garbage Collection Test Disk (§2) ----------------------------
echo "=================================================================="
echo "=== Preparing Garbage Collection Test Disk ($FIRMWARE) ==="
echo "=================================================================="

GC_RAW="$OUT/gc-prep.raw"
GC_VARS=""
cp --sparse=always "$BASE_PREPARED_RAW" "$GC_RAW"
if [ "$FIRMWARE" = "uefi" ]; then
	GC_VARS="$OUT/gc-prep-vars.fd"
	cp "$BASE_PREPARED_VARS" "$GC_VARS"
fi

# Round 1: publish generation 2 and bless
echo "GC prep round 1: publishing generation 2..."
# shellcheck disable=SC2016
GC_R1_CMD='echo "# gc round 1" > /etc/initramfs-tools/conf.d/vintner-gc.conf && update-initramfs -u -k "$(uname -r)"'
run_qemu "$GC_RAW" "$(make_seed "gc-r1-op" "$GC_R1_CMD")" "$OUT/gc-r1-op.log" 300 "$GC_VARS"
echo "GC prep round 1: booting to bless generation 2..."
run_qemu "$GC_RAW" "$(make_seed "gc-r1-bless" "true")" "$OUT/gc-r1-bless.log" 180 "$GC_VARS"

# Round 2: publish generation 3 and bless
echo "GC prep round 2: publishing generation 3..."
# shellcheck disable=SC2016
GC_R2_CMD='echo "# gc round 2" > /etc/initramfs-tools/conf.d/vintner-gc.conf && update-initramfs -u -k "$(uname -r)"'
run_qemu "$GC_RAW" "$(make_seed "gc-r2-op" "$GC_R2_CMD")" "$OUT/gc-r2-op.log" 300 "$GC_VARS"
echo "GC prep round 2: booting to bless generation 3..."
run_qemu "$GC_RAW" "$(make_seed "gc-r2-bless" "true")" "$OUT/gc-r2-bless.log" 180 "$GC_VARS"

# Verify GC prepared disk holds generations 1, 2, 3 all GOOD
python3 ci/publish-interrupt/inspector.py verify-prep-gc \
	--disk "$GC_RAW" \
	--bs-offset "$BS_OFFSET" \
	--vintner-bin "$TAGGED_BIN"

GC_PREPARED_RAW="$OUT/gc-prepared.raw"
GC_PREPARED_VARS=""
cp --sparse=always "$GC_RAW" "$GC_PREPARED_RAW"
if [ "$FIRMWARE" = "uefi" ]; then
	GC_PREPARED_VARS="$OUT/gc-prepared-vars.fd"
	cp "$GC_VARS" "$GC_PREPARED_VARS"
fi

# Dummy seed for plain test boots
DUMMY_SEED=$(make_seed "dummy-boot" "true")

# --- 4. Cases Execution (§3) -------------------------------------------------

CASES=("C0" "T1" "T2" "T3" "T4" "T5" "T6" "M1" "M2" "G1" "G2")

for CASE in "${CASES[@]}"; do
	echo "=================================================================="
	echo "=== Running Case $CASE ($FIRMWARE) ==="
	echo "=================================================================="

	CASE_DISK="$OUT/${CASE}.raw"
	CASE_VARS=""

	if [ "$CASE" = "G1" ] || [ "$CASE" = "G2" ]; then
		cp --sparse=always "$GC_PREPARED_RAW" "$CASE_DISK"
		if [ "$FIRMWARE" = "uefi" ]; then
			CASE_VARS="$OUT/${CASE}-vars.fd"
			cp "$GC_PREPARED_VARS" "$CASE_VARS"
		fi
	else
		cp --sparse=always "$BASE_PREPARED_RAW" "$CASE_DISK"
		if [ "$FIRMWARE" = "uefi" ]; then
			CASE_VARS="$OUT/${CASE}-vars.fd"
			cp "$BASE_PREPARED_VARS" "$CASE_VARS"
		fi
	fi

	# Host baseline read before case
	read_disk "$CASE_DISK" "${CASE}-read-pre"

	# Determine testpoint for operation boot
	TESTPOINT=""
	case "$CASE" in
		C0) TESTPOINT="" ;;
		T1) TESTPOINT="staged-recorded" ;;
		T2) TESTPOINT="staging-written" ;;
		T3) TESTPOINT="staging-verified" ;;
		T4) TESTPOINT="renamed" ;;
		T5) TESTPOINT="grubenv-written" ;;
		T6) TESTPOINT="trying-recorded" ;;
		M1) TESTPOINT="manifest-written#1" ;;
		M2) TESTPOINT="manifest-written#2" ;;
		G1) TESTPOINT="gc-record-removed" ;;
		G2) TESTPOINT="gc-grubenv-written" ;;
	esac

	# Step 1: Operation Boot
	echo "[$CASE] Step 1: Operation boot (testpoint: '${TESTPOINT:-none}')..."
	if [ -n "$TESTPOINT" ]; then
		OP_SCRIPT=$(cat <<EOF
set -e
exec > /dev/ttyS0 2>&1
cat <<'SH' > /etc/initramfs-tools/conf.d/vintner-case-${CASE}
# harmless conf.d change for ${CASE} op boot
SH
VINTNER_TEST_INTERRUPT="${TESTPOINT}" update-initramfs -u -k "\$(uname -r)"
EOF
)
	else
		OP_SCRIPT=$(cat <<EOF
set -e
exec > /dev/ttyS0 2>&1
cat <<'SH' > /etc/initramfs-tools/conf.d/vintner-case-${CASE}
# harmless conf.d change for ${CASE} op boot
SH
update-initramfs -u -k "\$(uname -r)"
EOF
)
	fi

	OP_SEED=$(make_seed "${CASE}-op" "$OP_SCRIPT")
	VARS_ARG=()
	CODE_ARG=()
	if [ "$FIRMWARE" = "uefi" ]; then
		VARS_ARG=("--vars" "$CASE_VARS")
		CODE_ARG=("--code-fd" "$CODE_FD")
	fi

	python3 ci/publish-interrupt/inspector.py run-boot \
		--firmware "$FIRMWARE" \
		--disk "$CASE_DISK" \
		--seed "$OP_SEED" \
		--log "$OUT/${CASE}-op.log" \
		--case "$CASE" \
		--boot-num "op" \
		"${VARS_ARG[@]}" \
		"${CODE_ARG[@]}" \
		--timeout 300 \
		--meta-out "$OUT/${CASE}-op-meta.json"

	read_disk "$CASE_DISK" "${CASE}-read-op"

	# Step 2: Boot 1
	echo "[$CASE] Step 2: Boot 1..."
	python3 ci/publish-interrupt/inspector.py run-boot \
		--firmware "$FIRMWARE" \
		--disk "$CASE_DISK" \
		--seed "$DUMMY_SEED" \
		--log "$OUT/${CASE}-boot1.log" \
		--case "$CASE" \
		--boot-num "1" \
		"${VARS_ARG[@]}" \
		"${CODE_ARG[@]}" \
		--timeout 120 \
		--meta-out "$OUT/${CASE}-boot1-meta.json"

	read_disk "$CASE_DISK" "${CASE}-read-boot1"

	# Step 3: Recovery publish
	echo "[$CASE] Step 3: Recovery publish..."
	REC_SCRIPT=$(cat <<EOF
set -e
exec > /dev/ttyS0 2>&1
cat <<'SH' > /etc/initramfs-tools/conf.d/vintner-case-${CASE}
# harmless conf.d change for ${CASE} recovery publish (distinct)
SH
update-initramfs -u -k "\$(uname -r)"
EOF
)
	REC_SEED=$(make_seed "${CASE}-rec-op" "$REC_SCRIPT")

	python3 ci/publish-interrupt/inspector.py run-boot \
		--firmware "$FIRMWARE" \
		--disk "$CASE_DISK" \
		--seed "$REC_SEED" \
		--log "$OUT/${CASE}-rec-op.log" \
		--case "$CASE" \
		--boot-num "rec-op" \
		"${VARS_ARG[@]}" \
		"${CODE_ARG[@]}" \
		--timeout 300 \
		--meta-out "$OUT/${CASE}-rec-op-meta.json"

	read_disk "$CASE_DISK" "${CASE}-read-rec-op"

	# Read 50vintner-retained-kernels after recovery publish (for check P7)
	echo "Block devices on host before losetup (${CASE} retained kernels):"
	lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
	LOOP=$(sudo losetup -P -r -f --show "$CASE_DISK")
	sudo mount -o ro "${LOOP}p1" "$MNT"
	cp "$MNT/etc/apt/apt.conf.d/50vintner-retained-kernels" "$OUT/${CASE}-retained-kernels.txt" 2>/dev/null || true
	sudo umount "$MNT"
	sudo losetup -d "$LOOP"
	LOOP=""

	# Step 4: Boot 2
	echo "[$CASE] Step 4: Boot 2..."
	python3 ci/publish-interrupt/inspector.py run-boot \
		--firmware "$FIRMWARE" \
		--disk "$CASE_DISK" \
		--seed "$DUMMY_SEED" \
		--log "$OUT/${CASE}-boot2.log" \
		--case "$CASE" \
		--boot-num "2" \
		"${VARS_ARG[@]}" \
		"${CODE_ARG[@]}" \
		--timeout 120 \
		--meta-out "$OUT/${CASE}-boot2-meta.json"

	read_disk "$CASE_DISK" "${CASE}-read-boot2"
done

# --- 5. Evaluation (§5) ------------------------------------------------------
echo "=================================================================="
echo "=== Evaluating Checks P1 to P9 ($FIRMWARE) ==="
echo "=================================================================="

python3 ci/publish-interrupt/inspector.py evaluate \
	--firmware "$FIRMWARE" \
	--out-dir "$OUT" \
	--results-file "$RESULTS"

echo "=== Results Table ($FIRMWARE) ==="
cat "$RESULTS"

echo "=== versions.txt ==="
cat "$VERSIONS"

# Verification: all P1-P7, P9 must be PASS, P8 must be OBSERVED
BAD=$(awk -F'\t' 'NR > 1 && $1 ~ /^P[1-79]$/ && $3 != "PASS" {print $1 ":" $2 ":" $3}' "$RESULTS" | paste -sd ' ')
BAD_OBS=$(awk -F'\t' 'NR > 1 && $1 == "P8" && $3 != "OBSERVED" {print $1 ":" $2 ":" $3}' "$RESULTS" | paste -sd ' ')

EXPECTED_COUNT=$((11 * 9))
ACTUAL_COUNT=$(awk 'NR > 1' "$RESULTS" | wc -l)

if [ "$ACTUAL_COUNT" -ne "$EXPECTED_COUNT" ]; then
	echo "ERROR: Expected $EXPECTED_COUNT result rows, got $ACTUAL_COUNT" >&2
	exit 1
fi

if [ -n "$BAD" ] || [ -n "$BAD_OBS" ]; then
	echo "ERROR: One or more checks did not pass:" >&2
	[ -n "$BAD" ] && echo "  Failing checks: $BAD" >&2
	[ -n "$BAD_OBS" ] && echo "  Failing observed checks: $BAD_OBS" >&2
	exit 1
fi

echo "All $FIRMWARE publish interruption checks passed successfully ($ACTUAL_COUNT/$EXPECTED_COUNT rows)."
