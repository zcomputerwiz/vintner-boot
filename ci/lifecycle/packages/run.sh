#!/bin/bash
# Package lifecycle test on an installed vintner system (WO-0003).
#
# Tests what normal package installs and removals do to a system that boots
# through vintner, across both UEFI and BIOS, under real APT operations.
#
# Deliverables:
#   ci/lifecycle/packages/run.sh <uefi|bios>
#   ci/lifecycle/packages/README.md
#
# Requires: root via sudo (for loop mounts and partition operations), KVM,
# mtools, qemu-system-x86, qemu-utils, cloud-image-utils, sfdisk, python3,
# python3-virt-firmware, golang-go.
set -euo pipefail

if [ $# -ne 1 ] || { [ "$1" != "uefi" ] && [ "$1" != "bios" ]; }; then
	echo "Usage: $0 {uefi|bios}" >&2
	exit 1
fi

FIRMWARE="$1"
cd "$(dirname "$0")/../../.."

OUT="ci/out/lifecycle/packages/$FIRMWARE"
# --- Cleanup handler --------------------------------------------------------
LOOP=""
INSPECT_TMP=""
cleanup() {
	local rc=$?
	if [ -n "${INSPECT_TMP:-}" ] && [ -f "$INSPECT_TMP" ]; then
		rm -f "$INSPECT_TMP" || true
	fi
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

./ci/lib/vintner-base.sh "$FIRMWARE" "$OUT"
# shellcheck source=/dev/null # written by vintner-base.sh
. "$OUT/base.env"
. ci/lib/vintner-qemu.sh

# --- 11. Run All Operations (C0, N1, K1..K4, U1..U3, X1..X6) ----------------
declare -A OP_KINDS
declare -A OP_COMMANDS

OP_KINDS["C0"]="control"
OP_COMMANDS["C0"]="true"

OP_KINDS["N1"]="neg-control"
OP_COMMANDS["N1"]=$(cat <<'EOF'
BS_DEV=$(blkid -t LABEL=BOOTSTORAGE -o device || true)
ESP_DEV=$(blkid -t LABEL=ESP -o device || true)
[ -n "$BS_DEV" ] || BS_DEV=$(blkid -t TYPE=vfat -o device | grep -v "$ESP_DEV" | head -n 1 || true)
[ -n "$ESP_DEV" ] || ESP_DEV=$(blkid -t TYPE=vfat -o device | grep -v "$BS_DEV" | head -n 1 || true)
mkdir -p /mnt/bs /boot/efi
mount -t vfat "$BS_DEV" /mnt/bs
mkdir -p /mnt/bs/vintner/bios/os-1
touch /mnt/bs/vintner/bios/os-1/lifecycle-negative-test
sync
umount /mnt/bs
if ! mountpoint -q /boot/efi; then
	mount -t vfat "$ESP_DEV" /boot/efi || true
fi
mkdir -p /boot/efi/loader
echo "" >> /boot/efi/loader/loader.conf
sync
EOF
)

OP_KINDS["K1"]="ordinary"
OP_COMMANDS["K1"]="DEBIAN_FRONTEND=noninteractive apt-get -y install linux-image-amd64"

OP_KINDS["K2"]="ordinary"
OP_COMMANDS["K2"]="update-initramfs -u -k all"

OP_KINDS["K3"]="ordinary"
OP_COMMANDS["K3"]=$(cat <<'EOF'
RC=0
DEBIAN_FRONTEND=noninteractive apt-get -y install linux-image-amd64 || RC=$?
if [ $RC -eq 0 ]; then
	NEW_PKG=$(dpkg-query -W -f='${Package}\n' 'linux-image-[0-9]*' | grep -v "$(uname -r)" || true)
	if [ -n "$NEW_PKG" ]; then
		DEBIAN_FRONTEND=noninteractive apt-get -y purge $NEW_PKG || RC=$?
	fi
fi
exit $RC
EOF
)

OP_KINDS["K4"]="ordinary"
OP_COMMANDS["K4"]="DEBIAN_FRONTEND=noninteractive apt-get -y install --reinstall \"linux-image-\$(uname -r)\""

OP_KINDS["U1"]="ordinary"
OP_COMMANDS["U1"]="DEBIAN_FRONTEND=noninteractive apt-get -y install systemd-ukify"

OP_KINDS["U2"]="ordinary"
OP_COMMANDS["U2"]="DEBIAN_FRONTEND=noninteractive apt-get -y install dracut"

OP_KINDS["U3"]="ordinary"
OP_COMMANDS["U3"]="DEBIAN_FRONTEND=noninteractive apt-get -y install --reinstall systemd udev"

OP_KINDS["X1"]="conflicting"
OP_COMMANDS["X1"]="DEBIAN_FRONTEND=noninteractive apt-get -y install systemd-boot"

OP_KINDS["X2"]="conflicting"
OP_COMMANDS["X2"]="DEBIAN_FRONTEND=noninteractive apt-get -y install grub-efi-amd64"

OP_KINDS["X3"]="conflicting"
OP_COMMANDS["X3"]="DEBIAN_FRONTEND=noninteractive apt-get -y install shim-signed"

OP_KINDS["X4"]="conflicting"
OP_COMMANDS["X4"]="DEBIAN_FRONTEND=noninteractive apt-get -y install grub-pc"

OP_KINDS["X5"]="conflicting"
OP_COMMANDS["X5"]="DEBIAN_FRONTEND=noninteractive apt-get -y install grub-cloud-amd64"

# X4 with the answer an administrator gives to the grub-pc install-device prompt.
OP_KINDS["X6"]="conflicting"
OP_COMMANDS["X6"]=$(cat <<'EOF'
echo "grub-pc grub-pc/install_devices multiselect /dev/vda" | debconf-set-selections
DEBIAN_FRONTEND=noninteractive apt-get -y install grub-pc
EOF
)

for op in C0 N1 K1 K2 K3 K4 U1 U2 U3 X1 X2 X3 X4 X5 X6; do
	kind="${OP_KINDS[$op]}"
	cmd="${OP_COMMANDS[$op]}"
	echo "=================================================================="
	echo "=== Running Operation $op ($kind) ==="
	echo "=================================================================="

	OP_QCOW2="$OUT/${op}.qcow2"
	OP_VARS=""
	if [ "$FIRMWARE" = "uefi" ]; then
		OP_VARS="$OUT/${op}-vars.fd"
		cp "$BASE_VARS" "$OP_VARS"
	fi

	rm -f "$OP_QCOW2"
	qemu-img create -f qcow2 -F raw -b "base.raw" "$OP_QCOW2"

	OP_SEED=$(make_seed "$op" "$cmd")
	B1_LOG="$OUT/${op}-boot1.log"
	B2_LOG="$OUT/${op}-boot2.log"

	echo "Booting $op Boot 1..."
	run_qemu "$OP_QCOW2" "$OP_SEED" "$B1_LOG" 600 "$OP_VARS"

	echo "Booting $op Boot 2..."
	run_qemu "$OP_QCOW2" "$OP_SEED" "$B2_LOG" 120 "$OP_VARS"

	echo "Inspecting $op overlay disk..."
	INSPECT_RAW="$OUT/${op}-inspect.raw"
	INSPECT_TMP="$INSPECT_RAW"
	qemu-img convert -O raw "$OP_QCOW2" "$INSPECT_RAW"

	inspect "$op" "$kind" "$INSPECT_RAW" "$BASE_RAW" "$OP_VARS"

	rm -f "$INSPECT_RAW"
	INSPECT_TMP=""
done

# --- 12. Print Summary & Exit Evaluation ------------------------------------
echo "=================================================================="
echo "=== Test Summary Table ($FIRMWARE) ==="
echo "=================================================================="
cat "$RESULTS"
echo "=== versions.txt ==="
cat "$VERSIONS"

FAILURES=$(awk -F'\t' '$2!="conflicting" && $3=="FAIL"{print $1}' "$RESULTS")
if [ -n "$FAILURES" ]; then
	echo "ERROR: Test failed on operations: $FAILURES" >&2
	exit 1
fi

echo "All $FIRMWARE lifecycle operations passed successfully!"
