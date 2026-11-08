#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# The product path (WO-0010 3.2 and 3.6): a Debian 13 system is installed onto a
# disk and `vintner install` turns it into a vintner system, from inside a chroot
# of the target, as an installer would. Nothing is copied into the guest by hand:
# the packages come from the .deb files (VINTNER_DEBS, as for the other rows) and
# `vintner install` does the rest. The disk is then booted under SeaBIOS and under
# OVMF, and each must boot generation 1 and bless it GOOD.
#
# Needs: root (sudo), debootstrap, a network and a Debian mirror, sfdisk, mkfs.vfat,
# mkfs.ext4, losetup, qemu-system-x86_64, OVMF.
# Usage: ci/install/run.sh   (writes ci/out/install/results.tsv)
# shellcheck disable=SC2002,SC2024 # the pipe reads efivarfs, which cannot seek; the logs are written by this user into OUT
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=${OUT:-ci/out/install}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
MIRROR=${MIRROR:-https://deb.debian.org/debian}
PKG_DIR=${VINTNER_DEBS:-ci/out/vintner-debs}
DISK=$OUT/disk.raw
MNT=$OUT/mnt
RESULTS=$OUT/results.tsv
FAILS=0
LOOP=""
printf 'row\tresult\tevidence\n' > "$RESULTS"

record() { # row PASS|FAIL evidence
	printf '%s\t%s\t%s\n' "$1" "$2" "$3" | tee -a "$RESULTS"
	[ "$2" = PASS ] || FAILS=$((FAILS + 1))
}

unmount_all() {
	if [ -d "$MNT" ]; then
		# Processes left in the chroot keep its mounts busy: stop them first.
		local real p
		real=$(realpath "$MNT")
		for p in /proc/[0-9]*; do
			if [ "$(sudo readlink "$p/root" 2>/dev/null)" = "$real" ]; then sudo kill "${p#/proc/}" 2>/dev/null || true; fi
		done
		sleep 1
		for m in dev/pts dev proc boot/efi mnt/bs; do
			mountpoint -q "$MNT/$m" && { sudo umount "$MNT/$m" || sudo umount -l "$MNT/$m"; }
		done
		mountpoint -q "$MNT" && sudo umount "$MNT"
	fi
	if [ -n "$LOOP" ]; then
		sudo losetup -d "$LOOP" || true
		LOOP=""
	fi
}
trap unmount_all EXIT

# --- The packages -----------------------------------------------------------
TREE_NOW=$(ci/package/build-debs.sh --tree)
have_debs() { compgen -G "$PKG_DIR/vintner-core_*.deb" >/dev/null && compgen -G "$PKG_DIR/vintner-platform_*.deb" >/dev/null; }
if [ -z "${VINTNER_DEBS:-}" ] && { ! have_debs || [ "$(cat "$PKG_DIR/SOURCE_TREE" 2>/dev/null)" != "$TREE_NOW" ]; }; then
	ci/package/build-debs.sh "$PKG_DIR"
fi
have_debs || { echo "no vintner .deb in $PKG_DIR" >&2; exit 1; }
CORE_DEB=$(realpath "$(compgen -G "$PKG_DIR/vintner-core_*.deb" | head -n 1)")
PLATFORM_DEB=$(realpath "$(compgen -G "$PKG_DIR/vintner-platform_*.deb" | head -n 1)")
mkdir -p "$OUT/host"
dpkg-deb --fsys-tarfile "$CORE_DEB" | tar -xO ./usr/sbin/vintner > "$OUT/host/vintner"
chmod 0755 "$OUT/host/vintner"

# --- The disk: BIOS Boot, ESP, boot storage, root (examples/disk-layouts, layout 3)
rm -f "$DISK"
truncate -s 5G "$DISK"
sfdisk -q "$DISK" <<'EOF'
label: gpt
start=2048, size=4096, type=21686148-6449-6E6F-744E-656564454649
size=128M, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B
size=1G, type=BC13C2FF-59E6-4262-A352-B275FD6F7172
type=4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709
EOF
LOOP=$(sudo losetup -P -f --show "$DISK")
sudo mkfs.vfat -F 32 -n ESP "${LOOP}p2" >/dev/null
sudo mkfs.vfat -F 32 -n BOOTSTORAGE "${LOOP}p3" >/dev/null
sudo mkfs.ext4 -q "${LOOP}p4"
ROOT_UUID=$(sudo blkid -s UUID -o value "${LOOP}p4")

# --- The Debian root --------------------------------------------------------
mkdir -p "$MNT"
sudo mount "${LOOP}p4" "$MNT"
echo "=== debootstrap Debian 13 ==="
sudo debootstrap --variant=minbase \
	--include=systemd-sysv,udev,dbus,kmod,initramfs-tools,linux-image-cloud-amd64,e2fsprogs,grub-common,ca-certificates \
	trixie "$MNT" "$MIRROR" > "$OUT/debootstrap.log" 2>&1 || { tail -n 30 "$OUT/debootstrap.log" >&2; exit 1; }

sudo chroot "$MNT" systemd-machine-id-setup >/dev/null
echo "UUID=$ROOT_UUID / ext4 errors=remount-ro 0 1" | sudo tee "$MNT/etc/fstab" >/dev/null
sudo mkdir -p "$MNT/etc/kernel"
echo "console=ttyS0,115200" | sudo tee "$MNT/etc/kernel/cmdline" >/dev/null

# What the guest tells the serial console once it is up, and then it powers off.
sudo tee "$MNT/usr/local/bin/vintner-install-report" >/dev/null <<'EOF'
#!/bin/bash
exec > /dev/ttyS0 2>&1
echo "=== VINTNER-INSTALL-REPORT-BEGIN ==="
echo "CMDLINE: $(cat /proc/cmdline)"
if [ -d /sys/firmware/efi ]; then
	mountpoint -q /sys/firmware/efi/efivars || mount -t efivarfs efivarfs /sys/firmware/efi/efivars 2>/dev/null
	f=/sys/firmware/efi/efivars/LoaderEntrySelected-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
		# efivarfs cannot seek, so the variable is read through a pipe; the first 4 bytes are its attributes.
		[ -r "$f" ] && echo "LOADER_ENTRY_SELECTED: $(cat "$f" | tail -c +5 | tr -d '\0')"
fi
echo "BLESS_ACTIVE: $(systemctl is-active vintner-bless.service)"
echo "=== VINTNER-INSTALL-REPORT-END ==="
systemctl poweroff
EOF
sudo chmod 0755 "$MNT/usr/local/bin/vintner-install-report"
sudo tee "$MNT/etc/systemd/system/vintner-install-report.service" >/dev/null <<'EOF'
[Unit]
Description=Report the boot to the serial console
After=multi-user.target vintner-bless.service
Wants=vintner-bless.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/vintner-install-report

[Install]
WantedBy=multi-user.target
EOF
sudo mkdir -p "$MNT/etc/systemd/system/multi-user.target.wants"
sudo ln -sf /etc/systemd/system/vintner-install-report.service "$MNT/etc/systemd/system/multi-user.target.wants/vintner-install-report.service"
sudo passwd -d -R "$MNT" root >/dev/null 2>&1 || true

# --- vintner install, from inside a chroot of the target ----------------------
sudo mkdir -p "$MNT/boot/efi" "$MNT/mnt/bs" "$MNT/root"
sudo mount "${LOOP}p2" "$MNT/boot/efi"
sudo mount "${LOOP}p3" "$MNT/mnt/bs"
for m in proc dev dev/pts; do
	sudo mount --bind "/$m" "$MNT/$m"
	sudo mount --make-rslave "$MNT/$m" # nothing under the guest root may propagate back to the host
done
sudo cp "$CORE_DEB" "$MNT/root/vintner-core.deb"
sudo cp "$PLATFORM_DEB" "$MNT/root/vintner-platform.deb"

echo "=== installing the packages ==="
sudo chroot "$MNT" env DEBIAN_FRONTEND=noninteractive LC_ALL=C \
	apt-get install -y --no-install-recommends /root/vintner-core.deb /root/vintner-platform.deb > "$OUT/packages.log" 2>&1 \
	|| { tail -n 30 "$OUT/packages.log" >&2; exit 1; }

echo "=== vintner install ==="
INSTALL_RC=0
sudo chroot "$MNT" /usr/sbin/vintner install --disk "$LOOP" --firmware both --target / --esp /boot/efi --boot-storage /mnt/bs \
	> "$OUT/install.log" 2>&1 || INSTALL_RC=$?
cat "$OUT/install.log"
if [ "$INSTALL_RC" -eq 0 ] && grep -q '^installed:' "$OUT/install.log"; then
	record "I1-install" PASS "vintner install exited 0 and reported the installation"
else
	record "I1-install" FAIL "vintner install exited $INSTALL_RC, see install.log"
	exit 1
fi

sudo rm -f "$MNT/root/vintner-core.deb" "$MNT/root/vintner-platform.deb"
unmount_all

# --- Boot it ----------------------------------------------------------------
CODE_FD=/usr/share/OVMF/OVMF_CODE_4M.fd
VARS_TEMPLATE=/usr/share/OVMF/OVMF_VARS_4M.fd
if [ ! -f "$CODE_FD" ]; then
	CODE_FD=/usr/share/OVMF/OVMF_CODE.fd
	VARS_TEMPLATE=/usr/share/OVMF/OVMF_VARS.fd
fi
ACCEL=tcg
[ -w /dev/kvm ] && ACCEL=kvm

# boot FIRMWARE: boots a copy of the disk until the guest has reported and powered off.
boot() {
	local fw=$1 disk=$OUT/disk-$1.raw log=$OUT/boot-$1.log
	cp --sparse=always "$DISK" "$disk"
	local fwargs=()
	if [ "$fw" = uefi ]; then
		cp "$VARS_TEMPLATE" "$OUT/vars-$fw.fd"
		fwargs=(-drive "if=pflash,format=raw,unit=0,file=$CODE_FD,readonly=on" -drive "if=pflash,format=raw,unit=1,file=$OUT/vars-$fw.fd")
	fi
	: > "$log"
	timeout 600 qemu-system-x86_64 -machine "q35,accel=$ACCEL" -m 2048 -smp 2 -nodefaults -display none \
		-monitor none -serial "file:$log" -no-reboot "${fwargs[@]}" \
		-drive "file=$disk,format=raw,if=virtio" &
	local qpid=$!
	while kill -0 "$qpid" 2>/dev/null; do
		if grep -aq 'VINTNER-INSTALL-REPORT-END' "$log"; then
			sleep 5
			kill "$qpid" 2>/dev/null || true
			break
		fi
		sleep 2
	done
	wait "$qpid" || true
}

# image_state FIRMWARE: the state of generation 1 in the manifest on the disk the guest ran on.
image_state() {
	local L state
	L=$(sudo losetup -P -f --show "$OUT/disk-$1.raw")
	mkdir -p "$OUT/bs-ro"
	sudo mount -o ro "${L}p3" "$OUT/bs-ro"
	state=$(sudo "$OUT/host/vintner" manifest show --state-dir "$OUT/bs-ro/vintner/state" | tr -d ' \n' | sed -n 's/.*"images":\[{[^]]*"state":"\([A-Z]*\)".*/\1/p')
	sudo umount "$OUT/bs-ro"
	sudo losetup -d "$L"
	echo "$state"
}

for fw in bios uefi; do
	echo "=== booting $fw ==="
	boot "$fw"
	log=$OUT/boot-$fw.log
	clean=$(tr -d '\r' < "$log" | sed 's/\x1b\[[0-9;?]*[A-Za-z]//g')
	if [ "$fw" = bios ]; then
		first=$(printf '%s\n' "$clean" | sed -n 's/.*vintner: trying \([^ ]*\).*/\1/p' | head -n 1)
		if [ "$first" = os-1 ]; then
			record "I2-bios-boots-generation-1" PASS "GRUB tried os-1 first"
		else
			record "I2-bios-boots-generation-1" FAIL "GRUB tried '${first:-nothing}' first, see boot-bios.log"
		fi
	else
		entry=$(printf '%s\n' "$clean" | sed -n 's/^LOADER_ENTRY_SELECTED: \(.*\)$/\1/p' | head -n 1)
		case "$entry" in
			vintner-os-1*) record "I4-uefi-boots-generation-1" PASS "systemd-boot selected $entry" ;;
			*) record "I4-uefi-boots-generation-1" FAIL "systemd-boot selected '${entry:-nothing}', see boot-uefi.log" ;;
		esac
	fi
	cmdline=$(printf '%s\n' "$clean" | sed -n 's/^CMDLINE: \(.*\)$/\1/p' | head -n 1)
	if printf '%s' "$cmdline" | grep -q 'vintner.bootstorage=PARTUUID=' && printf '%s\n' "$clean" | grep -q '^BLESS_ACTIVE: active$'; then
		st=$(image_state "$fw")
		n=3; [ "$fw" = uefi ] && n=5
		if [ "$st" = GOOD ]; then
			record "I$n-$fw-blessed-good" PASS "the guest reached vintner-bless.service and the manifest says GOOD"
		else
			record "I$n-$fw-blessed-good" FAIL "the manifest says '${st:-nothing}' for generation 1"
		fi
	else
		n=3; [ "$fw" = uefi ] && n=5
		record "I$n-$fw-blessed-good" FAIL "no bootstorage on the command line or vintner-bless.service not active: '$cmdline'"
	fi
done

echo
if [ "$FAILS" -ne 0 ]; then
	echo "FAIL: $FAILS row(s) failed, see $RESULTS" >&2
	exit 1
fi
echo "all install rows passed ($RESULTS)"
