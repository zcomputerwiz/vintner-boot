#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# run.sh - QEMU graphics & Plymouth BGRT theme boot test runner (WO-0008a)

set -euo pipefail

if [ $# -lt 1 ]; then
	echo "Usage: $0 <uefi|bios>" >&2
	exit 1
fi

FIRMWARE="$1"
if [ "$FIRMWARE" != "uefi" ] && [ "$FIRMWARE" != "bios" ]; then
	echo "Invalid firmware: $FIRMWARE (must be uefi or bios)" >&2
	exit 1
fi

cd "$(dirname "$0")/../.."

OUT="ci/out/graphics/$FIRMWARE"
MNT="$OUT/mnt"
RESULTS="$OUT/results.tsv"
export MTOOLS_SKIP_CHECK=1

# Clean any leftover files from previous runs while preserving mountpoint
if [ -d "$OUT" ]; then
	find "$OUT" -maxdepth 1 ! -name "$FIRMWARE" ! -name "mnt" -exec rm -rf {} +
fi
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
echo "=== Step 1: Building base disk with splash cmdline ($FIRMWARE) ==="
export VINTNER_EXTRA_CMDLINE='quiet splash vt.global_cursor_default=0 bgrt_disable plymouth.ignore-serial-consoles plymouth.debug=stream:/run/plymouth-debug.log'
./ci/lib/vintner-base.sh "$FIRMWARE" "$OUT"
# shellcheck source=/dev/null # written by vintner-base.sh
. "$OUT/base.env"
VINTNER_BIN="$OUT/vintner"
# shellcheck source=ci/lib/vintner-qemu.sh
. ci/lib/vintner-qemu.sh

# QEMU runner with display and QMP socket for screendump capture
run_qemu_gfx() {
	local disk_path="$1"
	local seed_path="$2"
	local logfile="$3"
	local timeout_secs="$4"
	local vars_path="${5:-}"
	local qmp_sock="$6"
	local extra_qemu_args="${7:-}"

	: > "$logfile"
	local disk_fmt="raw"
	[[ "$disk_path" == *.qcow2 ]] && disk_fmt="qcow2"

	local pflash_args=()
	if [ "$FIRMWARE" = "uefi" ]; then
		pflash_args=(
			-drive "if=pflash,format=raw,unit=0,file=$CODE_FD,readonly=on"
			-drive "if=pflash,format=raw,unit=1,file=$vars_path"
		)
	fi

	# shellcheck disable=SC2086
	timeout "$timeout_secs" qemu-system-x86_64 \
		-machine "q35,accel=$ACCEL" \
		-m 2048 -smp 2 -nodefaults \
		-display none \
		-qmp "unix:$qmp_sock,server,nowait" \
		-serial "file:$logfile" -no-reboot \
		"${pflash_args[@]}" \
		-drive "file=$disk_path,format=$disk_fmt,if=virtio" \
		-drive "file=$seed_path,format=raw,if=virtio" \
		-nic "user,model=virtio-net-pci" \
		-vga none -device VGA,addr=0x9.0 \
		$extra_qemu_args || true
}

# --- 2. Prepare test disk & install theme files ------------------------------
echo "=== Step 2: Preparing test disk & staging Plymouth theme ($FIRMWARE) ==="
TEST_DISK="$OUT/test.raw"
cp --sparse=always "$BASE_RAW" "$TEST_DISK"
TEST_VARS=""
[ "$FIRMWARE" = "uefi" ] && { TEST_VARS="$OUT/test-vars.fd"; cp "$BASE_VARS" "$TEST_VARS"; }

echo "Block devices on host before losetup:"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$TEST_DISK")
sudo mount "${LOOP}p1" "$MNT"

# Install theme assets into root filesystem
sudo mkdir -p "$MNT/usr/share/plymouth/themes/vintner-bgrt"
sudo cp -a graphics/plymouth-theme/. "$MNT/usr/share/plymouth/themes/vintner-bgrt/"

# Install plymouthd.conf
sudo mkdir -p "$MNT/etc/plymouth" "$MNT/tmp"
sudo cp integration/plymouth/plymouthd.conf "$MNT/tmp/repo-plymouthd.conf"
sudo cp integration/plymouth/plymouthd.conf "$MNT/etc/plymouth/plymouthd.conf"

# Install initramfs hook
sudo mkdir -p "$MNT/etc/initramfs-tools/hooks"
sudo cp integration/initramfs-hooks/plymouth-vintner "$MNT/etc/initramfs-tools/hooks/plymouth-vintner"
sudo chmod 755 "$MNT/etc/initramfs-tools/hooks/plymouth-vintner"

# Stage zz-vintner hooks in /root to be installed in guest prep after apt
sudo mkdir -p "$MNT/root"
sudo cp "integration/kernel-hooks/zz-vintner" "$MNT/root/zz-vintner-kernel"
sudo chmod 0755 "$MNT/root/zz-vintner-kernel"
sudo cp "integration/initramfs-hooks/zz-vintner" "$MNT/root/zz-vintner-initramfs"
sudo chmod 0755 "$MNT/root/zz-vintner-initramfs"
sudo cp "$VINTNER_BIN" "$MNT/usr/sbin/vintner"
sudo chmod 0755 "$MNT/usr/sbin/vintner"
sudo ln -sf /usr/sbin/vintner "$MNT/usr/bin/vintner"

# Install systemd drop-in override for Plymouth debug logging without wrappers
sudo mkdir -p "$MNT/etc/systemd/system/plymouth-start.service.d"
sudo tee "$MNT/etc/systemd/system/plymouth-start.service.d/override.conf" > /dev/null << 'EOF'
[Service]
ExecStart=
ExecStart=/usr/sbin/plymouthd --mode=boot --pid-file=/run/plymouth/pid --attach-to-session --debug --debug-file=/var/log/plymouth-debug.log
EOF

# Install vintner-test-splash.service & helper script
sudo mkdir -p "$MNT/etc/systemd/system/sysinit.target.wants"
sudo tee "$MNT/etc/systemd/system/vintner-test-splash.service" > /dev/null << 'EOF'
[Unit]
Description=Vintner Test Splash Service
DefaultDependencies=no
After=local-fs.target plymouth-start.service
Before=plymouth-quit.service plymouth-quit-wait.service multi-user.target
Conflicts=shutdown.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/vintner-test-splash.sh
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=sysinit.target
EOF

sudo ln -sf /etc/systemd/system/vintner-test-splash.service "$MNT/etc/systemd/system/sysinit.target.wants/vintner-test-splash.service"

sudo tee "$MNT/usr/local/bin/vintner-test-splash.sh" > /dev/null << 'EOF'
#!/bin/bash
# Only run during splash boot when plymouth is installed
if [ ! -f /usr/sbin/plymouthd ] && [ ! -f /sbin/plymouthd ]; then
	exit 0
fi

# Emit err (<3>) and warning (<4>) messages to kmsg for check G2 (no direct write to ttyS0)
echo '<3>vintner-test-error' > /dev/kmsg
echo '<4>vintner-test-warning' > /dev/kmsg

# Signal that test splash unit is active and Plymouth is displayed
echo "=== VINTNER-TEST-SPLASH-START ===" > /dev/ttyS0

# Check plymouthd running status while it is active
PLYMOUTHD_RUNNING=0
if pidof plymouthd >/dev/null 2>&1 || plymouth --ping 2>/dev/null; then
	PLYMOUTHD_RUNNING=1
fi

# Sleep 25 seconds per WO-0008a
sleep 25

CMDLINE=$(cat /proc/cmdline)
DEFAULT_THEME=$(plymouth-set-default-theme 2>/dev/null || true)
PLYMOUTHD_CONF=$(cat /etc/plymouth/plymouthd.conf 2>/dev/null || true)
PLYMOUTHD_CONF_SHA256=$(sha256sum /etc/plymouth/plymouthd.conf 2>/dev/null | awk '{print $1}' || echo "none")

DEBUG_LOG=""
for f in /var/log/plymouth/plymouth-debug.log /var/log/plymouth-debug.log /run/plymouth-debug.log /var/log/plymouth/debug.log /run/plymouth/debug.log; do
	if [ -f "$f" ]; then
		DEBUG_LOG=$(cat "$f")
		break
	fi
done

FB_NAME="none"
FB_VIRTUAL_SIZE="none"
if [ -r /sys/class/graphics/fb0/name ]; then
	FB_NAME=$(cat /sys/class/graphics/fb0/name 2>/dev/null || echo "none")
fi
if [ -r /sys/class/graphics/fb0/virtual_size ]; then
	FB_VIRTUAL_SIZE=$(cat /sys/class/graphics/fb0/virtual_size 2>/dev/null || echo "none")
fi

BGRT_PRESENT=0
BGRT_DETAILS="none"
if [ -d /sys/firmware/acpi/bgrt ]; then
	BGRT_PRESENT=1
	BGRT_STATUS=$(cat /sys/firmware/acpi/bgrt/status 2>/dev/null || echo "unknown")
	BGRT_DETAILS="status=$BGRT_STATUS"
fi

KERNEL_VER="$(uname -r)"
BOOTED_INITRD="none"
INITRAMFS_FILES=""
if [ -f "/boot/initrd.img-$KERNEL_VER" ]; then
	BOOTED_INITRD="initrd.img-$KERNEL_VER"
	INITRAMFS_FILES=$(lsinitramfs "/boot/initrd.img-$KERNEL_VER" 2>/dev/null || true)
fi

KERNEL_CONFIG_LINES="none"
if [ -r "/boot/config-$KERNEL_VER" ]; then
	KERNEL_CONFIG_LINES=$(grep -E '^(CONFIG_DRM|# CONFIG_DRM is|CONFIG_FB_VESA|# CONFIG_FB_VESA is|CONFIG_FB_SIMPLE|# CONFIG_FB_SIMPLE is)' "/boot/config-$KERNEL_VER" 2>/dev/null | tr '\n' '; ' || echo "none")
fi

KERNEL_DRIVERS_PRESENT=""
for drv in simpledrm bochs bochs-drm virtio-gpu; do
	if [ -d "/lib/modules/$KERNEL_VER" ] && find "/lib/modules/$KERNEL_VER" -name "${drv}*.ko*" 2>/dev/null | grep -q .; then
		KERNEL_DRIVERS_PRESENT="${KERNEL_DRIVERS_PRESENT:+$KERNEL_DRIVERS_PRESENT, }$drv"
	fi
done

# Print evidence between markers to ttyS0
{
	echo "VINTNER-GFX-BEGIN"
	echo "CMDLINE=$CMDLINE"
	echo "DEFAULT_THEME=$DEFAULT_THEME"
	echo "PLYMOUTHD_CONF_SHA256=$PLYMOUTHD_CONF_SHA256"
	echo "PLYMOUTHD_RUNNING=$PLYMOUTHD_RUNNING"
	echo "FB_NAME=$FB_NAME"
	echo "FB_VIRTUAL_SIZE=$FB_VIRTUAL_SIZE"
	echo "BGRT_PRESENT=$BGRT_PRESENT"
	echo "BGRT_DETAILS=$BGRT_DETAILS"
	echo "BOOTED_INITRD=$BOOTED_INITRD"
	echo "KERNEL_CONFIG_LINES=$KERNEL_CONFIG_LINES"
	echo "KERNEL_DRIVERS_PRESENT=$KERNEL_DRIVERS_PRESENT"
	echo "PLYMOUTHD_CONF<<VINTNER_EOF"
	echo "$PLYMOUTHD_CONF"
	echo "VINTNER_EOF"
	echo "PLYMOUTH_DEBUG_LOG<<VINTNER_EOF"
	echo "$DEBUG_LOG"
	echo "VINTNER_EOF"
	echo "INITRAMFS_FILES<<VINTNER_EOF"
	echo "$INITRAMFS_FILES"
	echo "VINTNER_EOF"
	echo "VINTNER-GFX-END"
} > /dev/ttyS0

# Keep splash active
sleep 30
EOF
sudo chmod 755 "$MNT/usr/local/bin/vintner-test-splash.sh"

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# --- 3. Guest preparation operation -----------------------------------------
echo "=== Step 3: Installing generic kernel & plymouth in guest ($FIRMWARE) ==="
# shellcheck disable=SC2016
PREP_CMD='export DEBIAN_FRONTEND=noninteractive
apt-mark hold linux-image-cloud-amd64 "linux-image-$(uname -r)"
apt-get update
apt-get install -y -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" --no-install-recommends plymouth plymouth-themes fontconfig-config fonts-dejavu-core systemd-ukify systemd-boot-efi linux-image-amd64
[ -f /tmp/repo-plymouthd.conf ] && cp /tmp/repo-plymouthd.conf /etc/plymouth/plymouthd.conf
plymouth-set-default-theme vintner-bgrt

mkdir -p /etc/kernel/postinst.d /etc/initramfs/post-update.d
cp /root/zz-vintner-kernel /etc/kernel/postinst.d/zz-vintner && chmod 755 /etc/kernel/postinst.d/zz-vintner
cp /root/zz-vintner-initramfs /etc/initramfs/post-update.d/zz-vintner && chmod 755 /etc/initramfs/post-update.d/zz-vintner

GENERIC_KVER=$(ls -d /lib/modules/*-amd64 | grep -v cloud | sort -V | tail -n 1 | xargs -n1 basename)
echo "Generic kernel is $GENERIC_KVER"
update-initramfs -u -k "$GENERIC_KVER"
'
PREP_SEED=$(make_seed "prep" "$PREP_CMD")
run_qemu "$TEST_DISK" "$PREP_SEED" "$OUT/prep.log" 600 "$TEST_VARS"

# --- 4. Boot generation 2 with display on and QMP screendump ----------------
echo "=== Step 4: Booting image with splash and taking screendumps ==="
SPLASH_SEED=$(make_seed "splash" "true")
QMP_SOCK="$OUT/qmp.sock"
rm -f "$QMP_SOCK" "$OUT/screendump-active.ppm"

python3 ci/graphics/inspector.py capture-dump \
	--qmp-socket "$QMP_SOCK" \
	--out-ppm "$OUT/screendump-active.ppm" \
	--ready-log "$OUT/splash-boot.log" \
	--timeout 60 &
DUMP_PID=$!

run_qemu_gfx "$TEST_DISK" "$SPLASH_SEED" "$OUT/splash-boot.log" 180 "$TEST_VARS" "$QMP_SOCK"
wait "$DUMP_PID" || true

# --- 5. Control run for G7 (Solid Red Logo) ---------------------------------
echo "=== Step 5: Running G7 control run with solid red logo ==="
CTRL_DISK="$OUT/control.raw"
cp --sparse=always "$TEST_DISK" "$CTRL_DISK"
CTRL_VARS=""
[ "$FIRMWARE" = "uefi" ] && { CTRL_VARS="$OUT/control-vars.fd"; cp "$TEST_VARS" "$CTRL_VARS"; }

RED_PNG="$OUT/red-fallback.png"
python3 ci/graphics/inspector.py create-red-png --out-png "$RED_PNG"

echo "Block devices on host before control losetup:"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$CTRL_DISK")
sudo mount "${LOOP}p1" "$MNT"
sudo cp "$RED_PNG" "$MNT/usr/share/plymouth/themes/vintner-bgrt/bgrt-fallback.png"
sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# Rebuild initramfs with red logo (publishes os-3)
# shellcheck disable=SC2016
CTRL_PREP_SEED=$(make_seed "ctrl-prep" '
GENERIC_KVER=$(ls -d /lib/modules/*-amd64 | grep -v cloud | sort -V | tail -n 1 | xargs -n1 basename)
update-initramfs -u -k "$GENERIC_KVER"
')
run_qemu "$CTRL_DISK" "$CTRL_PREP_SEED" "$OUT/ctrl-prep.log" 300 "$CTRL_VARS"

# Boot control run and capture screendump
CTRL_QMP_SOCK="$OUT/ctrl-qmp.sock"
rm -f "$CTRL_QMP_SOCK" "$OUT/screendump-control.ppm"

python3 ci/graphics/inspector.py capture-dump \
	--qmp-socket "$CTRL_QMP_SOCK" \
	--out-ppm "$OUT/screendump-control.ppm" \
	--ready-log "$OUT/ctrl-boot.log" \
	--timeout 60 &
CTRL_DUMP_PID=$!

CTRL_BOOT_SEED=$(make_seed "ctrl-boot" "true")
run_qemu_gfx "$CTRL_DISK" "$CTRL_BOOT_SEED" "$OUT/ctrl-boot.log" 180 "$CTRL_VARS" "$CTRL_QMP_SOCK"
wait "$CTRL_DUMP_PID" || true

# --- 6. G9 UEFI-only Splash observation run ---------------------------------
if [ "$FIRMWARE" = "uefi" ]; then
	echo "=== Step 6: Testing UEFI BGRT with QEMU boot splash ==="
	SPLASH_BMP="$OUT/uefi-splash.bmp"
	python3 ci/graphics/inspector.py create-splash-bmp --out-bmp "$SPLASH_BMP"
	UEFI_SPLASH_SEED=$(make_seed "uefi-splash" "sleep 15; poweroff")
	run_qemu_gfx "$TEST_DISK" "$UEFI_SPLASH_SEED" "$OUT/uefi-bgrt-boot.log" 120 "$TEST_VARS" "$OUT/dummy-qmp.sock" "-boot splash=$SPLASH_BMP"
fi

# --- 7. Evaluation and Gate Verification -------------------------------------
echo "=== Step 7: Evaluating checks G0 to G9 ($FIRMWARE) ==="
python3 ci/graphics/inspector.py evaluate \
	--firmware "$FIRMWARE" \
	--out-dir "$OUT" \
	--results-file "$RESULTS"

echo "=== Results Table ($FIRMWARE) ==="
cat "$RESULTS"

python3 ci/graphics/inspector.py check-results \
	--firmware "$FIRMWARE" \
	--results-file "$RESULTS"

echo "All $FIRMWARE graphics boot tests completed successfully."
