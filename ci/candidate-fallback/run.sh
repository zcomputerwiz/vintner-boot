#!/bin/bash
# Candidate fallback integration test on an installed vintner system (WO-0006).
#
# Tests that a bad candidate image falls back to the known-good image after
# bounded attempts, on both UEFI and BIOS, across panic, health failure,
# hang and early kill.
#
# Deliverables:
#   ci/candidate-fallback/run.sh <uefi|bios>
#   ci/candidate-fallback/README.md
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

OUT="ci/out/candidate-fallback/$FIRMWARE"
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

VINTNER_BIN="$OUT/vintner"
if [ ! -f "$VINTNER_BIN" ]; then
	echo "Building static vintner binary on host..."
	(cd src && CGO_ENABLED=0 go build -o "../$VINTNER_BIN" ./cmd/vintner)
fi

# --- 2. Prepare golden test disk (Setup §2) ----------------------------------
echo "=================================================================="
echo "=== Preparing golden test disk for candidate fallback ($FIRMWARE) ==="
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
echo "Booting preparation step 1: install systemd-ukify and systemd-boot-efi..."
PREP_INSTALL_CMD="DEBIAN_FRONTEND=noninteractive apt-get -y install systemd-ukify systemd-boot-efi && apt-mark hold linux-image-cloud-amd64 linux-image-$KERNEL_ABI"
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
python3 ci/candidate-fallback/inspector.py verify-prep \
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

# Dummy seed for plain test boots
TEST_BOOT_SEED=$(make_seed "test-boot" "true")

# --- 3. Case Definitions and Execution ---------------------------------------

CASES=("G0" "B1" "B2" "B3" "B4")

for CASE in "${CASES[@]}"; do
	echo "=================================================================="
	echo "=== Running Case $CASE ($FIRMWARE) ==="
	echo "=================================================================="

	CASE_DISK="$OUT/${CASE}.raw"
	CASE_VARS=""
	cp --sparse=always "$PREPARED_RAW" "$CASE_DISK"
	if [ "$FIRMWARE" = "uefi" ]; then
		CASE_VARS="$OUT/${CASE}-vars.fd"
		cp "$PREPARED_VARS" "$CASE_VARS"
	fi

	# Determine guest operation script for this case (§2)
	case "$CASE" in
		G0)
			OP_SCRIPT=$(cat <<'EOF'
cat <<'SH' > /etc/initramfs-tools/conf.d/vintner-test
# harmless control file
SH
update-initramfs -u -k "$(uname -r)"
rm -f /etc/initramfs-tools/conf.d/vintner-test
EOF
)
			;;
		B1)
			OP_SCRIPT=$(cat <<'EOF'
cat <<'SH' > /etc/initramfs-tools/scripts/init-top/break-test
#!/bin/sh
PREREQ=""
prereqs() { echo "$PREREQ"; }
case $1 in prereqs) prereqs; exit 0;; esac
echo c > /proc/sysrq-trigger
SH
chmod +x /etc/initramfs-tools/scripts/init-top/break-test
update-initramfs -u -k "$(uname -r)"
rm -f /etc/initramfs-tools/scripts/init-top/break-test
EOF
)
			;;
		B2)
			OP_SCRIPT=$(cat <<'EOF'
cat <<'SH' > /etc/systemd/system/vintner-test-health.service
[Unit]
ConditionPathExists=/run/vintner-test-break
Before=boot-complete.target

[Service]
Type=oneshot
ExecStart=/bin/false

[Install]
RequiredBy=boot-complete.target
SH
mkdir -p /etc/systemd/system/boot-complete.target.requires
ln -sf /etc/systemd/system/vintner-test-health.service /etc/systemd/system/boot-complete.target.requires/vintner-test-health.service

cat <<'SH' > /etc/initramfs-tools/scripts/init-bottom/break-test
#!/bin/sh
PREREQ=""
prereqs() { echo "$PREREQ"; }
case $1 in prereqs) prereqs; exit 0;; esac
touch /run/vintner-test-break
if [ -d "${rootmnt:-}/run" ]; then
  touch "${rootmnt}/run/vintner-test-break"
fi
SH
chmod +x /etc/initramfs-tools/scripts/init-bottom/break-test
update-initramfs -u -k "$(uname -r)"
rm -f /etc/initramfs-tools/scripts/init-bottom/break-test
EOF
)
			;;
		B3)
			OP_SCRIPT=$(cat <<'EOF'
cat <<'SH' > /etc/initramfs-tools/scripts/init-top/break-test
#!/bin/sh
PREREQ=""
prereqs() { echo "$PREREQ"; }
case $1 in prereqs) prereqs; exit 0;; esac
sleep infinity
SH
chmod +x /etc/initramfs-tools/scripts/init-top/break-test
update-initramfs -u -k "$(uname -r)"
rm -f /etc/initramfs-tools/scripts/init-top/break-test
EOF
)
			;;
		B4)
			OP_SCRIPT=$(cat <<'EOF'
cat <<'SH' > /etc/initramfs-tools/conf.d/vintner-test
# harmless control file
SH
update-initramfs -u -k "$(uname -r)"
rm -f /etc/initramfs-tools/conf.d/vintner-test
EOF
)
			;;
	esac

	echo "Booting $CASE Operation Boot: publishing candidate generation 2..."
	OP_SEED=$(make_seed "${CASE}-op" "$OP_SCRIPT")
	run_qemu "$CASE_DISK" "$OP_SEED" "$OUT/${CASE}-op.log" 600 "$CASE_VARS"

	if ! grep -q "=== OP EXIT: 0 ===" "$OUT/${CASE}-op.log"; then
		echo "ERROR: Case $CASE operation failed:" >&2
		tr -d '\r' < "$OUT/${CASE}-op.log" | tail -n 40 >&2
		exit 1
	fi

	# Record initial state after candidate published
	INITIAL_JSON="$OUT/${CASE}-initial.json"
	python3 ci/candidate-fallback/inspector.py record-initial-state \
		--firmware "$FIRMWARE" \
		--case "$CASE" \
		--disk "$CASE_DISK" \
		--bs-offset "$BS_OFFSET" \
		--vintner-bin "$VINTNER_BIN" \
		--out-file "$INITIAL_JSON"

	STATE_JSON="$OUT/${CASE}-state.json"
	rm -f "$STATE_JSON"

	# Boot 1 to 6
	for B in 1 2 3 4 5 6; do
		BOOT_LOG="$OUT/${CASE}-boot${B}.log"
		META_JSON="$OUT/${CASE}-boot${B}-meta.json"
		KILL_FLAG=()
		if [ "$CASE" = "B4" ] && [ "$B" -le 3 ]; then
			KILL_FLAG=("--kill-on-cmdline")
		fi

		echo "Booting $CASE Boot $B..."
		VARS_ARG=()
		CODE_ARG=()
		if [ "$FIRMWARE" = "uefi" ]; then
			VARS_ARG=("--vars" "$CASE_VARS")
			CODE_ARG=("--code-fd" "$CODE_FD")
		fi

		python3 ci/candidate-fallback/inspector.py run-boot \
			--firmware "$FIRMWARE" \
			--disk "$CASE_DISK" \
			--seed "$TEST_BOOT_SEED" \
			--log "$BOOT_LOG" \
			--case "$CASE" \
			--boot-num "$B" \
			"${VARS_ARG[@]}" \
			"${CODE_ARG[@]}" \
			--timeout 120 \
			"${KILL_FLAG[@]}" \
			--meta-out "$META_JSON"

		python3 ci/candidate-fallback/inspector.py record-boot-state \
			--firmware "$FIRMWARE" \
			--case "$CASE" \
			--boot-num "$B" \
			--disk "$CASE_DISK" \
			--bs-offset "$BS_OFFSET" \
			--vintner-bin "$VINTNER_BIN" \
			--boot-log "$BOOT_LOG" \
			--meta-json "$META_JSON" \
			--state-file "$STATE_JSON"
	done

	# Evaluate checks for this case
	python3 ci/candidate-fallback/inspector.py evaluate-case \
		--firmware "$FIRMWARE" \
		--case "$CASE" \
		--state-file "$STATE_JSON" \
		--initial-state "$INITIAL_JSON" \
		--results-file "$RESULTS"
done

# Evaluate H8 across all cases
STATE_FILES=()
for CASE in "${CASES[@]}"; do
	STATE_FILES+=("$OUT/${CASE}-state.json")
done

python3 ci/candidate-fallback/inspector.py evaluate-h8 \
	--results-file "$RESULTS" \
	"${STATE_FILES[@]}"

echo "=================================================================="
echo "=== Test Results ($FIRMWARE) ==="
echo "=================================================================="
cat "$RESULTS"

# Fail if any non-OBSERVED check failed or missing
if grep -q -E $'\tFAIL\t' "$RESULTS"; then
	echo "FAIL: One or more checks failed" >&2
	exit 1
fi

echo "All checks passed successfully."
