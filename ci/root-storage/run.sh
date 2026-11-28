#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Root storage lanes (WO-0010 6.4): the normal boot path with an ext4 root inside
# LUKS, LVM or md RAID. A Debian 13 system is installed onto a disk whose root
# partition holds the container, `vintner install --root-fs-uuid` turns it into a
# vintner system from inside a chroot of the target (the installer cannot open the
# container, so it is told the UUID of the ext4 filesystem inside), and the disk is
# booted under SeaBIOS and under OVMF:
#   boot a: generation 1 starts, the initramfs opens the container, vintner-bless marks
#           generation 1 GOOD; the guest then rebuilds its initramfs, which publishes
#           generation 2 through the package hooks;
#   boot b: generation 2 starts from the same root and is blessed GOOD.
# BIOS trial state lives in grubenv on boot storage, never on the root, so it must
# work whatever the root is (architecture section 10).
#
# Needs: root (sudo), debootstrap, a network and a Debian mirror, sfdisk, mkfs.vfat,
# mkfs.ext4, losetup, qemu-system-x86_64, OVMF, python3, and for the layout cryptsetup
# (luks), lvm2 (lvm) or mdadm (md).
# Usage: ci/root-storage/run.sh <luks|lvm|md>   (writes ci/out/root-storage-<layout>/results.tsv)
# shellcheck disable=SC2002,SC2024 # the pipe reads efivarfs, which cannot seek; the logs are written by this user into OUT
set -euo pipefail
cd "$(dirname "$0")/../.."

LAYOUT=${1:-}
case "$LAYOUT" in luks | lvm | md) ;; *)
	echo "usage: $0 <luks|lvm|md>" >&2
	exit 2
	;;
esac

OUT=${OUT:-ci/out/root-storage-$LAYOUT}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
MIRROR=${MIRROR:-https://deb.debian.org/debian}
PKG_DIR=${VINTNER_DEBS:-ci/out/vintner-debs}
DISK=$OUT/disk.raw
MNT=$OUT/mnt
RESULTS=$OUT/results.tsv
FAILS=0
LOOP=""
# Names of the host mappings this lane makes: nothing else is ever touched.
MAP=vbrs$LAYOUT
VG=vbrsvg
MDDEV=/dev/md/vbrs
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
		for m in sys dev/pts dev proc boot/efi mnt/bs; do
			mountpoint -q "$MNT/$m" && { sudo umount "$MNT/$m" || sudo umount -l "$MNT/$m"; }
		done
		mountpoint -q "$MNT" && sudo umount "$MNT"
	fi
	close_container
	if [ -n "$LOOP" ]; then
		sudo losetup -d "$LOOP" || true
		LOOP=""
	fi
}

# close_container: closes the LUKS volume, deactivates the volume group or stops the
# array, whichever this lane opened. Safe to call when nothing is open.
close_container() {
	case "$LAYOUT" in
		luks) [ -e "/dev/mapper/$MAP" ] && { sudo cryptsetup close "$MAP" || true; } ;;
		lvm) sudo lvm vgs "$VG" >/dev/null 2>&1 && { sudo lvm vgchange -an "$VG" >/dev/null || true; } ;;
		md) [ -e "$MDDEV" ] && { sudo mdadm --stop "$MDDEV" >/dev/null 2>&1 || true; } ;;
	esac
	return 0
}
trap unmount_all EXIT

# A run that died earlier may have left its own mappings behind.
close_container

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

# --- The disk: BIOS Boot, ESP, boot storage, root (examples/disk-layouts, layout 3) ---
# md has a second member after the root; it has the Linux RAID type so that the root is
# still the one partition of the root type.
rm -f "$DISK"
truncate -s 6G "$DISK"
{
	cat <<'EOF'
label: gpt
start=2048, size=4096, type=21686148-6449-6E6F-744E-656564454649
size=128M, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B
size=1G, type=BC13C2FF-59E6-4262-A352-B275FD6F7172
EOF
	if [ "$LAYOUT" = md ]; then
		echo "size=2G, type=4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709"
		echo "size=2G, type=A19D880F-05FC-4D3B-A006-743F0F84911E"
	else
		echo "type=4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709"
	fi
} | sfdisk -q "$DISK"
LOOP=$(sudo losetup -P -f --show "$DISK")
sudo mkfs.vfat -F 32 -n ESP "${LOOP}p2" >/dev/null
sudo mkfs.vfat -F 32 -n BOOTSTORAGE "${LOOP}p3" >/dev/null

# --- The container and the ext4 inside it ------------------------------------
KEYFILE=$OUT/root.key
case "$LAYOUT" in
	luks)
		head -c 64 /dev/urandom > "$KEYFILE"
		# A low iteration count keeps the test quick; the key is a file, not a passphrase.
		sudo cryptsetup luksFormat --batch-mode --type luks2 --pbkdf pbkdf2 --pbkdf-force-iterations 1000 \
			--key-file "$KEYFILE" "${LOOP}p4"
		sudo cryptsetup open --key-file "$KEYFILE" "${LOOP}p4" "$MAP"
		ROOT_DEV=/dev/mapper/$MAP
		LUKS_UUID=$(sudo blkid -s UUID -o value "${LOOP}p4")
		;;
	lvm)
		sudo lvm pvcreate -ff -y "${LOOP}p4" >/dev/null
		sudo lvm vgcreate "$VG" "${LOOP}p4" >/dev/null
		sudo lvm lvcreate -y -l 100%FREE -n root "$VG" >/dev/null
		ROOT_DEV=/dev/$VG/root
		;;
	md)
		sudo mdadm --create "$MDDEV" --run --assume-clean --level=1 --raid-devices=2 --metadata=1.2 --name=vbrs \
			"${LOOP}p4" "${LOOP}p5" >/dev/null 2>&1
		ROOT_DEV=$MDDEV
		;;
esac
sudo mkfs.ext4 -q "$ROOT_DEV"
INNER_UUID=$(sudo blkid -s UUID -o value "$ROOT_DEV")
echo "root container: $LAYOUT; ext4 UUID inside: $INNER_UUID"

# --- The Debian root --------------------------------------------------------
mkdir -p "$MNT"
sudo mount "$ROOT_DEV" "$MNT"
case "$LAYOUT" in
	luks) EXTRA=cryptsetup,cryptsetup-initramfs ;;
	lvm) EXTRA=lvm2 ;;
	md) EXTRA=mdadm ;;
esac
echo "=== debootstrap Debian 13 ==="
sudo debootstrap --variant=minbase \
	--include=systemd-sysv,udev,dbus,kmod,initramfs-tools,linux-image-cloud-amd64,e2fsprogs,grub-common,ca-certificates,util-linux,"$EXTRA" \
	trixie "$MNT" "$MIRROR" > "$OUT/debootstrap.log" 2>&1 || { tail -n 30 "$OUT/debootstrap.log" >&2; exit 1; }

sudo chroot "$MNT" systemd-machine-id-setup >/dev/null
sudo mkdir -p "$MNT/etc/kernel"
echo "console=ttyS0,115200" | sudo tee "$MNT/etc/kernel/cmdline" >/dev/null

# What the initramfs has to open, and how the target names its root. The kernel command
# line vintner install builds says root=UUID=<the ext4 inside>: the initramfs opens the
# container first, and udev then makes the by-uuid link of the filesystem inside.
case "$LAYOUT" in
	luks)
		echo "$MAP UUID=$LUKS_UUID /etc/$MAP.key luks,initramfs" | sudo tee "$MNT/etc/crypttab" >/dev/null
		sudo install -m 0400 "$KEYFILE" "$MNT/etc/$MAP.key"
		echo "KEYFILE_PATTERN=\"/etc/$MAP.key\"" | sudo tee "$MNT/etc/cryptsetup-initramfs/conf-hook" >/dev/null
		echo "UMASK=0077" | sudo tee -a "$MNT/etc/initramfs-tools/initramfs.conf" >/dev/null
		echo "/dev/mapper/$MAP / ext4 errors=remount-ro 0 1" | sudo tee "$MNT/etc/fstab" >/dev/null
		;;
	lvm)
		echo "/dev/mapper/$VG-root / ext4 errors=remount-ro 0 1" | sudo tee "$MNT/etc/fstab" >/dev/null
		;;
	md)
		sudo mkdir -p "$MNT/etc/mdadm"
		sudo mdadm --detail --scan "$MDDEV" | sudo tee "$MNT/etc/mdadm/mdadm.conf" >/dev/null
		echo "UUID=$INNER_UUID / ext4 errors=remount-ro 0 1" | sudo tee "$MNT/etc/fstab" >/dev/null
		;;
esac

# What the guest tells the serial console once it is up, and then it powers off. On the
# first boot it also rebuilds its initramfs, which publishes generation 2 through the
# package hooks; the second boot starts that generation.
sudo tee "$MNT/usr/local/bin/vintner-rs-report" >/dev/null <<'EOF'
#!/bin/bash
exec > /dev/ttyS0 2>&1
echo "=== VINTNER-RS-REPORT-BEGIN ==="
echo "CMDLINE: $(cat /proc/cmdline)"
if [ -d /sys/firmware/efi ]; then
	mountpoint -q /sys/firmware/efi/efivars || mount -t efivarfs efivarfs /sys/firmware/efi/efivars 2>/dev/null
	f=/sys/firmware/efi/efivars/LoaderEntrySelected-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
	# efivarfs cannot seek, so the variable is read through a pipe; the first 4 bytes are its attributes.
	[ -r "$f" ] && echo "LOADER_ENTRY_SELECTED: $(cat "$f" | tail -c +5 | tr -d '\0')"
fi
src=$(findmnt -no SOURCE /)
echo "ROOT_SOURCE: $src"
echo "ROOT_TYPE: $(lsblk -no TYPE "$src" 2>/dev/null | head -n 1)"
echo "BLESS_ACTIVE: $(systemctl is-active vintner-bless.service)"
if [ ! -e /var/lib/vintner-rs-phase ]; then
	echo "PHASE: a"
	touch /var/lib/vintner-rs-phase
	update-initramfs -u -k "$(uname -r)" > /var/log/vintner-rs-update.log 2>&1
	echo "PUBLISH_RC: $?"
	tail -n 5 /var/log/vintner-rs-update.log
else
	echo "PHASE: b"
fi
echo "=== VINTNER-RS-REPORT-END ==="
sync
systemctl poweroff
EOF
sudo chmod 0755 "$MNT/usr/local/bin/vintner-rs-report"
sudo tee "$MNT/etc/systemd/system/vintner-rs-report.service" >/dev/null <<'EOF'
[Unit]
Description=Report the boot to the serial console
After=multi-user.target vintner-bless.service
Wants=vintner-bless.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/vintner-rs-report

[Install]
WantedBy=multi-user.target
EOF
sudo mkdir -p "$MNT/etc/systemd/system/multi-user.target.wants"
sudo ln -sf /etc/systemd/system/vintner-rs-report.service "$MNT/etc/systemd/system/multi-user.target.wants/vintner-rs-report.service"
sudo passwd -d -R "$MNT" root >/dev/null 2>&1 || true

# --- vintner install, from inside a chroot of the target ----------------------
sudo mkdir -p "$MNT/boot/efi" "$MNT/mnt/bs" "$MNT/root"
sudo mount "${LOOP}p2" "$MNT/boot/efi"
sudo mount "${LOOP}p3" "$MNT/mnt/bs"
for m in proc sys dev dev/pts; do
	sudo mount --bind "/$m" "$MNT/$m"
	sudo mount --make-rslave "$MNT/$m" # nothing under the guest root may propagate back to the host
done
sudo cp "$CORE_DEB" "$MNT/root/vintner-core.deb"
sudo cp "$PLATFORM_DEB" "$MNT/root/vintner-platform.deb"

# The initramfs the kernel package built did not have the crypttab, fstab and mdadm.conf
# above to read: build it again, so that it holds what opens the container.
echo "=== update-initramfs: the container's hooks ==="
sudo chroot "$MNT" update-initramfs -u -k all > "$OUT/initramfs.log" 2>&1 || { tail -n 30 "$OUT/initramfs.log" >&2; exit 1; }

echo "=== installing vintner-core and unpacking the payloads ==="
sudo chroot "$MNT" env DEBIAN_FRONTEND=noninteractive LC_ALL=C \
	apt-get install -y --no-install-recommends /root/vintner-core.deb > "$OUT/packages.log" 2>&1 \
	|| { tail -n 30 "$OUT/packages.log" >&2; exit 1; }
sudo mkdir -p "$MNT/root/vp"
sudo chroot "$MNT" dpkg-deb -x /root/vintner-platform.deb /root/vp

echo "=== vintner install --root-fs-uuid ==="
INSTALL_RC=0
sudo chroot "$MNT" /usr/sbin/vintner install --disk "$LOOP" --firmware both --target / --esp /boot/efi --boot-storage /mnt/bs \
	--root-fs-uuid "$INNER_UUID" --payload-dir /root/vp/usr/lib/vintner/payloads > "$OUT/install.log" 2>&1 || INSTALL_RC=$?
cat "$OUT/install.log"
if [ "$INSTALL_RC" -eq 0 ] && grep -q '^installed:' "$OUT/install.log"; then
	record "R1-install" PASS "vintner install exited 0 on a $LAYOUT root and reported the installation"
else
	record "R1-install" FAIL "vintner install exited $INSTALL_RC, see install.log"
	exit 1
fi

# The initramfs generation 1 starts must be able to open the container.
ABI=$(sudo chroot "$MNT" sh -c 'ls /boot | sed -n "s/^vmlinuz-//p" | sort -V | tail -n 1')
sudo chroot "$MNT" sh -c "rm -rf /tmp/ird && unmkinitramfs /boot/initrd.img-$ABI /tmp/ird"
case "$LAYOUT" in
	luks) want_ird='*/cryptroot/crypttab' ;;
	lvm) want_ird='*/sbin/lvm' ;;
	md) want_ird='*/sbin/mdadm' ;;
esac
if [ -n "$(sudo find "$MNT/tmp/ird" -path "$want_ird" | head -n 1)" ]; then
	record "R2-initramfs-opens-container" PASS "initrd.img-$ABI holds ${want_ird#\*}"
else
	record "R2-initramfs-opens-container" FAIL "initrd.img-$ABI has no ${want_ird#\*}"
fi
sudo rm -rf "$MNT/tmp/ird"

sudo rm -rf "$MNT/root/vintner-core.deb" "$MNT/root/vintner-platform.deb" "$MNT/root/vp"
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

# boot FIRMWARE PHASE: boots the firmware's copy of the disk until the guest has reported and powered off.
boot() {
	local fw=$1 phase=$2 disk=$OUT/disk-$1.raw log=$OUT/boot-$1-$2.log
	local fwargs=()
	if [ "$fw" = uefi ]; then
		fwargs=(-drive "if=pflash,format=raw,unit=0,file=$CODE_FD,readonly=on" -drive "if=pflash,format=raw,unit=1,file=$OUT/vars-$fw.fd")
	fi
	: > "$log"
	timeout 900 qemu-system-x86_64 -machine "q35,accel=$ACCEL" -m 2048 -smp 2 -nodefaults -display none \
		-monitor none -serial "file:$log" -no-reboot "${fwargs[@]}" \
		-drive "file=$disk,format=raw,if=virtio" &
	local qpid=$!
	while kill -0 "$qpid" 2>/dev/null; do
		if grep -aq 'VINTNER-RS-REPORT-END' "$log"; then
			sleep 5
			kill "$qpid" 2>/dev/null || true
			break
		fi
		sleep 2
	done
	wait "$qpid" || true
}

# states FIRMWARE: "1=GOOD 2=GOOD" - every generation in the manifest on the disk the guest ran on.
states() {
	local L out
	L=$(sudo losetup -P -f --show "$OUT/disk-$1.raw")
	mkdir -p "$OUT/bs-ro"
	sudo mount -o ro "${L}p3" "$OUT/bs-ro"
	out=$(sudo "$OUT/host/vintner" manifest show --state-dir "$OUT/bs-ro/vintner/state" \
		| python3 -c 'import json,sys; m=json.load(sys.stdin); print(" ".join("%s=%s" % (i["generation"], i["state"]) for i in sorted(m["images"], key=lambda i: i["generation"])))')
	sudo umount "$OUT/bs-ro"
	sudo losetup -d "$L"
	echo "$out"
}

clean_log() { tr -d '\r' < "$1" | sed 's/\x1b\[[0-9;?]*[A-Za-z]//g'; }

case "$LAYOUT" in luks) WANT_TYPE=crypt ;; lvm) WANT_TYPE=lvm ;; md) WANT_TYPE=raid1 ;; esac

for fw in bios uefi; do
	echo "=== booting $fw ==="
	cp --sparse=always "$DISK" "$OUT/disk-$fw.raw"
	[ "$fw" = uefi ] && cp "$VARS_TEMPLATE" "$OUT/vars-$fw.fd"
	for phase in a b; do
		boot "$fw" "$phase"
		clean=$(clean_log "$OUT/boot-$fw-$phase.log")
		want_gen=1
		[ "$phase" = b ] && want_gen=2
		n="R-$fw-gen$want_gen"

		if [ "$fw" = bios ]; then
			first=$(sed -n 's/.*vintner: trying \([^ ]*\).*/\1/p' <<<"$clean" | head -n 1 || true)
		else
			first=$(sed -n 's/^LOADER_ENTRY_SELECTED: \(.*\)$/\1/p' <<<"$clean" | head -n 1 || true)
			first=${first#vintner-}
		fi
		cmdline=$(sed -n 's/^CMDLINE: \(.*\)$/\1/p' <<<"$clean" | head -n 1 || true)
		rtype=$(sed -n 's/^ROOT_TYPE: \(.*\)$/\1/p' <<<"$clean" | head -n 1 || true)
		phase_seen=$(sed -n 's/^PHASE: \(.*\)$/\1/p' <<<"$clean" | head -n 1 || true)

		# Here-strings, not pipes: grep -q ends early, and under pipefail the printf feeding it then fails (SIGPIPE).
		if [[ "$first" == "os-$want_gen"* ]]; then
			if grep -q "root=UUID=$INNER_UUID" <<<"$cmdline" && [ "$rtype" = "$WANT_TYPE" ] && [ "$phase_seen" = "$phase" ]; then
				record "$n-boots" PASS "$fw started os-$want_gen with root=UUID=<the ext4 inside the $LAYOUT container>, the root is a $rtype device"
			else
				record "$n-boots" FAIL "cmdline '$cmdline', root device type '${rtype:-none}' (want $WANT_TYPE), phase '${phase_seen:-none}' (want $phase); see boot-$fw-$phase.log"
			fi
		else
			record "$n-boots" FAIL "$fw started '${first:-nothing}', not os-$want_gen; see boot-$fw-$phase.log"
		fi

		if grep -q '^BLESS_ACTIVE: active$' <<<"$clean"; then
			st=$(states "$fw")
			want="1=GOOD"
			[ "$phase" = b ] && want="1=GOOD 2=GOOD"
			# After boot a the guest has already published generation 2, which is still on trial.
			if [ "$phase" = a ]; then
				case "$st" in "1=GOOD 2=STAGED" | "1=GOOD 2=TRYING") record "R-$fw-publish-gen2" PASS "the guest's initramfs rebuild published generation 2 ($st)" ;; *)
					rc=$(sed -n 's/^PUBLISH_RC: \(.*\)$/\1/p' <<<"$clean" | head -n 1 || true)
					record "R-$fw-publish-gen2" FAIL "manifest '$st', update-initramfs rc '${rc:-none}'; see boot-$fw-a.log"
					;;
				esac
				case "$st" in 1=GOOD*) record "R-$fw-gen1-blessed" PASS "the manifest says generation 1 is GOOD ($st)" ;; *) record "R-$fw-gen1-blessed" FAIL "manifest '$st'" ;; esac
			elif [ "$st" = "$want" ]; then
				record "R-$fw-gen2-blessed" PASS "generation 2 was blessed from the $LAYOUT root ($st)"
			else
				record "R-$fw-gen2-blessed" FAIL "manifest '$st', want '$want'"
			fi
		else
			record "R-$fw-$phase-blessed" FAIL "vintner-bless.service was not active; see boot-$fw-$phase.log"
		fi
	done
done

echo
if [ "$FAILS" -ne 0 ]; then
	echo "FAIL: $FAILS row(s) failed, see $RESULTS" >&2
	exit 1
fi
echo "all root-storage ($LAYOUT) rows passed ($RESULTS)"
