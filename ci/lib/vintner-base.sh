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
# shellcheck disable=SC2024 # the logs are written by this user into OUT; sudo is for the chroot
set -euo pipefail

if [ $# -ne 2 ] || { [ "$1" != "uefi" ] && [ "$1" != "bios" ]; }; then
	echo "Usage: $0 {uefi|bios} <out-dir>" >&2
	exit 1
fi

FIRMWARE="$1"
OUT="$2"
cd "$(dirname "$0")/../.."

DEBS="$(dirname "$OUT")/debs"
ROOT="$OUT/root"
STATE="$OUT/state"
MNT="$OUT/mnt"
DISK="$OUT/disk.raw"
BASE_RAW="$OUT/base.raw"
BASE_VARS="$OUT/base-vars.fd"
RESULTS="$OUT/results.tsv"
VERSIONS="$OUT/versions.txt"
SEED_DIR="$OUT/seed"
FS_SERIAL=1234ABCD
export MTOOLS_SKIP_CHECK=1

mkdir -p "$OUT" "$DEBS" "$ROOT" "$STATE" "$MNT" "$SEED_DIR"
: > "$VERSIONS"
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

# Ensure recovery kernel, initramfs, and UKIs exist
if [ ! -f "recovery/out/vmlinuz" ] || [ ! -f "recovery/out/initrd.img" ] || [ ! -f "recovery/out/version" ] || \
   [ ! -f "recovery/out/vintner-recovery-a.efi" ] || [ ! -f "recovery/out/vintner-recovery-b.efi" ]; then
	echo "=== Building recovery image ==="
	bash recovery/build.sh
fi

# Record recovery version
if [ -f "recovery/out/versions.txt" ]; then
	echo "=== Recovery packages ===" >> "$VERSIONS"
	cat recovery/out/versions.txt >> "$VERSIONS"
fi

# --- 1. Debian trixie packages, unpacked without apt or root -----------------
echo "=== Fetching Debian trixie packages ==="
MIRROR=https://deb.debian.org/debian
[ -f "ci/out/Packages" ] || curl -sSfL "$MIRROR/dists/trixie/main/binary-amd64/Packages.xz" | xz -dc > "ci/out/Packages"

field() {
	awk -v p="$1" -v f="$2:" '$1=="Package:"{cur=$2} cur==p && $1==f{sub(/^[^ ]+ /,""); print; exit}' "ci/out/Packages"
}

fetch() {
	local fn
	fn=$(field "$1" Filename)
	[ -n "$fn" ] || { echo "no $1 in trixie" >&2; exit 1; }
	[ -f "$DEBS/${fn##*/}" ] || curl -sSfL "$MIRROR/$fn" -o "$DEBS/${fn##*/}"
	dpkg-deb -x "$DEBS/${fn##*/}" "$ROOT"
	echo "$1 $(field "$1" Version)" >> "$VERSIONS"
}

for p in systemd-boot-efi systemd-ukify grub-pc-bin grub-common libc6 liblzma5 \
	libdevmapper1.02.1 libselinux1 libudev1 libpcre2-8-0 libcap2; do
	fetch "$p"
done

trixie() {
	"$ROOT/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2" --library-path "$ROOT/usr/lib/x86_64-linux-gnu" "$@"
}

editenv() {
	trixie "$ROOT/usr/bin/grub-editenv" "$@"
}

STUB=$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub
UKIFY=""
for u in "$ROOT/usr/lib/systemd/ukify" "$ROOT/usr/bin/ukify"; do
	if [ -e "$u" ]; then UKIFY="$u"; break; fi
done
[ -n "$UKIFY" ] || { echo "ukify not found in root" >&2; exit 1; }

# --- 2. The vintner packages --------------------------------------------------
# The guest gets vintner from the .deb files, as a product would (WO-0010 2.5).
# VINTNER_DEBS names a directory that holds them (CI passes the artifact of the
# package build); otherwise they are built here from the tree, on a Debian 13 host.
echo "=== Locating the vintner packages ==="
PKG_DIR=${VINTNER_DEBS:-ci/out/vintner-debs}
TREE_NOW=$(ci/package/build-debs.sh --tree)
have_debs() { compgen -G "$PKG_DIR/vintner-core_*.deb" >/dev/null && compgen -G "$PKG_DIR/vintner-platform_*.deb" >/dev/null; }
if [ -z "${VINTNER_DEBS:-}" ] && { ! have_debs || [ "$(cat "$PKG_DIR/SOURCE_TREE" 2>/dev/null)" != "$TREE_NOW" ]; }; then
	# The recovery kernel and initramfs were built above; the payload stage reuses them.
	VINTNER_RECOVERY_DIR=$PWD/recovery/out ci/package/build-debs.sh "$PKG_DIR"
fi
have_debs || { echo "no vintner-core and vintner-platform .deb in $PKG_DIR" >&2; exit 1; }
CORE_DEB=$(compgen -G "$PKG_DIR/vintner-core_*.deb" | head -n 1)
PLATFORM_DEB=$(compgen -G "$PKG_DIR/vintner-platform_*.deb" | head -n 1)
CORE_DEB=$(realpath "$CORE_DEB")
PLATFORM_DEB=$(realpath "$PLATFORM_DEB")
echo "vintner-core $(dpkg-deb -f "$CORE_DEB" Version) ($CORE_DEB)" | tee -a "$VERSIONS"
# The host-side copy of the binary is the package's own.
dpkg-deb --fsys-tarfile "$CORE_DEB" | tar -xO ./usr/sbin/vintner > "$OUT/vintner"
chmod 0755 "$OUT/vintner"

# --- 3. Base image and raw disk layout ---------------------------------------
echo "=== Preparing dual-firmware disk image ==="
./ci/lib/fetch-image.sh

IMAGE_CACHE="ci/out/cache/debian-13-genericcloud-amd64-20261001-2618.qcow2"
[ -f "$IMAGE_CACHE" ] || { echo "Image cache not found at $IMAGE_CACHE" >&2; exit 1; }

rm -f "$DISK" "$BASE_RAW"
qemu-img convert -O raw "$IMAGE_CACHE" "$DISK"

# Grow root partition by 2 GiB before appending boot storage
echo "Growing root partition by 2 GiB..."
truncate -s +2G "$DISK"
echo ", +" | sfdisk -N 1 "$DISK"

echo "Block devices on host before losetup (root resize):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$DISK")
sudo e2fsck -fy "${LOOP}p1" || true
sudo resize2fs "${LOOP}p1"
sudo losetup -d "$LOOP"
LOOP=""

# Append boot storage partition: type BC13C2FF-59E6-4262-A352-B275FD6F7172, 512 MiB
echo "Appending boot storage partition (512 MiB)..."
truncate -s +512M "$DISK"
echo ', 512M, BC13C2FF-59E6-4262-A352-B275FD6F7172' | sfdisk --append "$DISK"

# Read partition offsets and PARTUUIDs
PART_INFO=$(python3 - "$DISK" <<'EOF'
import json, sys, subprocess
data = json.loads(subprocess.check_output(["sfdisk", "-J", sys.argv[1]]))["partitiontable"]["partitions"]
res = {}
for p in data:
    ptype = p.get("type", "").upper()
    if ptype == "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709":
        res["root_start"] = p["start"]
        res["root_sectors"] = p["size"]
        res["root_partuuid"] = p["uuid"].lower()
    elif ptype == "21686148-6449-6E6F-744E-656564454649":
        res["bios_boot_start"] = p["start"]
        res["bios_boot_sectors"] = p["size"]
    elif ptype == "C12A7328-F81F-11D2-BA4B-00A0C93EC93B":
        res["esp_start"] = p["start"]
        res["esp_part"] = int(p["node"][len(p["node"].rstrip("0123456789")):])
        res["esp_sectors"] = p["size"]
        res["esp_partuuid"] = p["uuid"].lower()
    elif ptype == "BC13C2FF-59E6-4262-A352-B275FD6F7172":
        res["bs_start"] = p["start"]
        res["bs_part"] = int(p["node"][len(p["node"].rstrip("0123456789")):])
        res["bs_sectors"] = p["size"]
        res["bs_partuuid"] = p["uuid"].lower()
for k, v in res.items():
    print(f"{k.upper()}={v}")
EOF
)
eval "$PART_INFO"

ESP_OFFSET=$((ESP_START * 512))
BS_OFFSET=$((BS_START * 512))

echo "Root: start=$ROOT_START, sectors=$ROOT_SECTORS, partuuid=$ROOT_PARTUUID"
echo "BIOS Boot: start=$BIOS_BOOT_START, sectors=$BIOS_BOOT_SECTORS"
echo "ESP: start=$ESP_START, sectors=$ESP_SECTORS, partuuid=$ESP_PARTUUID"
echo "Boot Storage: start=$BS_START, sectors=$BS_SECTORS, partuuid=$BS_PARTUUID"

# --- 4. Format boot storage -----------------------------------------------------
# The layout the installer checks (WO-0010 3.1) is the cloud image's BIOS Boot
# Partition, ESP and root, plus the boot storage appended above. The product formats
# boot storage (D-A); everything on it, on the ESP and in the BIOS Boot Partition is
# written by `vintner install` in step 5.
echo "=== Formatting boot storage ==="
mformat -i "$DISK@@$BS_OFFSET" -F -N "$FS_SERIAL" -v BOOTSTORAGE ::

# --- 5. Mount root filesystem and configure guest ---------------------------
echo "=== Configuring guest root filesystem ==="
echo "Block devices on host before losetup (guest config):"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$DISK")
ROOT_DEV="${LOOP}p1"
ROOT_FS_UUID=$(sudo blkid -s UUID -o value "$ROOT_DEV")

sudo mount "$ROOT_DEV" "$MNT"

KERNEL_IMG=$(ls "$MNT"/boot/vmlinuz-*)
INITRD_IMG=$(ls "$MNT"/boot/initrd.img-*)
KERNEL_ABI=$(basename "$KERNEL_IMG" | sed 's/^vmlinuz-//')

echo "Guest kernel: $KERNEL_ABI"
echo "guest-kernel: $KERNEL_ABI" >> "$VERSIONS"

# os-1's .osrel per ADR-0003: the installed os-release with IMAGE_ID=vintner-os,
# IMAGE_VERSION=<generation> and the ABI appended to PRETTY_NAME, so images the
# publisher adds (generation 2 and up) sort above it.
OS_PRETTY=$(sed -n 's/^PRETTY_NAME=//p' "$MNT/etc/os-release" | tr -d '"')
{
	grep -vE '^(IMAGE_ID|IMAGE_VERSION|PRETTY_NAME)=' "$MNT/etc/os-release"
	printf 'PRETTY_NAME="%s, %s"\nIMAGE_ID=vintner-os\nIMAGE_VERSION=1\n' "$OS_PRETTY" "$KERNEL_ABI"
} > "$OUT/osrel"

# Install vintner-core from its package (WO-0010 2.5): the binary, both hooks, the
# Plymouth hook, vintner-bless.service (enabled), the two masks and
# /etc/kernel/install.conf all come from the .deb, with its dependencies from the
# Debian archive. The guest is not running, so apt runs in a chroot of the mounted
# root with the host's resolver.
sudo cp "$CORE_DEB" "$MNT/root/vintner-core.deb"
for m in proc dev dev/pts; do
	sudo mount --bind "/$m" "$MNT/$m"
	# rslave: what happens under the guest root (a busy or lazy unmount) must not
	# propagate back to the real /proc, /dev and /dev/pts; a shared bind mount can
	# break the host (it did: wsl.exe could not start processes in the distro).
	sudo mount --make-rslave "$MNT/$m"
done
sudo mv "$MNT/etc/resolv.conf" "$MNT/etc/resolv.conf.ci-save"
sudo cp --dereference /etc/resolv.conf "$MNT/etc/resolv.conf"
PKG_RC=0
sudo chroot "$MNT" env DEBIAN_FRONTEND=noninteractive LC_ALL=C sh -c \
	'apt-get update && apt-get install -y --no-install-recommends /root/vintner-core.deb' || PKG_RC=$?

# `vintner install` (WO-0010 3.6): the same command a product runs, inside a chroot of
# the guest root, as ci/install/run.sh does. It stages, publishes generation 1, takes
# booting over on both firmwares, removes the stock loaders and verifies. This
# machine's firmware does not boot the disk, so no NVRAM entry is written (--efivars
# none); the removable path carries the UEFI boot. The row's console and test-mode
# options reach the guest through the product's own inputs: /etc/kernel/cmdline for
# os-1 and the recovery command-line options for the two recovery images.
# vintner-platform conflicts with the stock loaders, so the installer takes its
# payloads from the unpacked package (architecture section 5).
INSTALL_RC=0
if [ "$PKG_RC" -eq 0 ]; then
	echo "=== vintner install ==="
	sudo mkdir -p "$MNT/boot/efi" "$MNT/mnt/bs" "$MNT/root/vp"
	sudo mount "${LOOP}p$ESP_PART" "$MNT/boot/efi"
	sudo mount "${LOOP}p$BS_PART" "$MNT/mnt/bs"
	sudo cp "$PLATFORM_DEB" "$MNT/root/vintner-platform.deb"
	sudo chroot "$MNT" dpkg-deb -x /root/vintner-platform.deb /root/vp
	echo "console=ttyS0,115200${VINTNER_EXTRA_CMDLINE:+ $VINTNER_EXTRA_CMDLINE}" | sudo tee "$MNT/etc/kernel/cmdline" >/dev/null
	RECA_EXTRA="vintner.recovery_test=1 vintner.root=UUID=$ROOT_FS_UUID"
	RECB_EXTRA="$RECA_EXTRA vintner.test_slot=b"
	sudo chroot "$MNT" /usr/sbin/vintner install --disk "$LOOP" --firmware both --target / --esp /boot/efi --boot-storage /mnt/bs \
		--root-partuuid "$ROOT_PARTUUID" --payload-dir /root/vp/usr/lib/vintner/payloads --efivars none \
		--recovery-cmdline-a "$RECA_EXTRA" --recovery-cmdline-b "$RECB_EXTRA" > "$OUT/install.log" 2>&1 || INSTALL_RC=$?
	cat "$OUT/install.log"
	sudo rm -rf "$MNT/root/vp" "$MNT/root/vintner-platform.deb"
	for m in boot/efi mnt/bs; do sudo umount "$MNT/$m" || sudo umount -l "$MNT/$m"; done
	grep -q "^installed:" "$OUT/install.log" || INSTALL_RC=1
fi
sudo rm -f "$MNT/etc/resolv.conf"
sudo mv "$MNT/etc/resolv.conf.ci-save" "$MNT/etc/resolv.conf"
# Processes that package scripts left running in the chroot (agents, daemons) keep
# the bind mounts busy: stop what has the guest root as its root, then unmount.
MNT_REAL=$(realpath "$MNT")
for p in /proc/[0-9]*; do
	if [ "$(sudo readlink "$p/root" 2>/dev/null)" = "$MNT_REAL" ]; then sudo kill "${p#/proc/}" 2>/dev/null || true; fi
done
sleep 1
for m in dev/pts dev proc; do sudo umount "$MNT/$m" || sudo umount -l "$MNT/$m"; done
sudo rm -f "$MNT/root/vintner-core.deb"
[ "$PKG_RC" -eq 0 ] || { echo "installing vintner-core into the guest failed (rc=$PKG_RC)" >&2; exit 1; }
[ "$INSTALL_RC" -eq 0 ] || { echo "vintner install failed (rc=$INSTALL_RC), see install.log" >&2; exit 1; }
sudo chroot "$MNT" dpkg-query -W -f='${Package} ${Version}\n' vintner-core systemd-ukify sbsigntool | tee -a "$VERSIONS"
# The hooks arrive armed. Rows install other packages (kernels, initramfs tools) in
# the guest before the step they test, and a row arms the hooks when it wants vintner
# to publish: until then they are left off, as they were when the rows copied them in
# by hand.
sudo chmod a-x "$MNT/etc/kernel/postinst.d/zz-vintner" "$MNT/etc/initramfs/post-update.d/zz-vintner"

# os-1 is what is in the guest now, after the package install above: installing
# vintner-core and its dependencies runs update-initramfs, so the initramfs differs
# from the one in the downloaded image, and a copy taken earlier would not match it.
INITRD_IMG=$(ls "$MNT"/boot/initrd.img-"$KERNEL_ABI")
cp "$KERNEL_IMG" "$OUT/vmlinuz"
cp "$INITRD_IMG" "$OUT/initrd.img"

# Ensure machine-id exists
MACHINE_ID=$(cat "$MNT/etc/machine-id" 2>/dev/null || true)
if [ ${#MACHINE_ID} -ne 32 ]; then
	MACHINE_ID=$(tr -d '-' < /proc/sys/kernel/random/uuid)
	echo "$MACHINE_ID" | sudo tee "$MNT/etc/machine-id" >/dev/null
fi
echo "Guest machine-id: $MACHINE_ID"

# Install report script and report service
cat <<'EOF' | sudo tee "$MNT/usr/local/bin/vintner-lifecycle-report.sh" >/dev/null
#!/bin/bash
set -u
# No getty on ttyS0 from here on, so nothing hangs up the console mid-report.
systemctl stop serial-getty@ttyS0.service 2>/dev/null || true
exec > /dev/ttyS0 2>&1

echo "=== VINTNER-REPORT-BEGIN ==="
echo "CMDLINE: $(cat /proc/cmdline)"

if [ -d /sys/firmware/efi ]; then
	mount -t efivarfs efivarfs /sys/firmware/efi/efivars 2>/dev/null || true
	ENTRY=""
	for p in /sys/firmware/efi/efivars/LoaderEntrySelected-* /sys/firmware/efi/efivars/LoaderBootCountPath-*; do
		if [ -f "$p" ]; then
			val=$(cat "$p" 2>/dev/null | tail -c +5 | tr -d '\0' | tr -cd '[:print:]' || true)
			[ -n "$val" ] && { ENTRY="$val"; break; }
		fi
	done
	ENTRY="${ENTRY##*\\}"
	ENTRY="${ENTRY##*/}"
	echo "LOADER_ENTRY_SELECTED: $ENTRY"
	echo "LOADER_VARS: $(ls /sys/firmware/efi/efivars 2>/dev/null | grep -i Loader | paste -sd ' ' || true)"
else
	echo "LOADER_ENTRY_SELECTED: none (bios)"
fi

BLESS_STATUS=$(systemctl is-active vintner-bless.service 2>&1 || true)
echo "BLESS_ACTIVE: $BLESS_STATUS"

echo "=== DPKG_QUERY_BEGIN ==="
dpkg-query -W -f='${Package} ${Version}\n' linux-image-amd64 'linux-image-*' systemd-ukify dracut \
	systemd udev systemd-boot grub-efi-amd64 shim-signed grub-pc grub-cloud-amd64 2>/dev/null || true
echo "=== DPKG_QUERY_END ==="

ROOT_DF=$(df -B1 / 2>/dev/null | tail -n 1 || true)
echo "ROOT_DF: $ROOT_DF"

echo "=== VINTNER-REPORT-END ==="
sync
systemctl poweroff -f || poweroff -f
EOF
sudo chmod 0755 "$MNT/usr/local/bin/vintner-lifecycle-report.sh"

cat <<'EOF' | sudo tee "$MNT/etc/systemd/system/vintner-lifecycle-report.service" >/dev/null
[Unit]
Description=Vintner lifecycle test report and shutdown
# After the gettys: agetty hangs up ttyS0 when it starts, and the report's
# later writes to it would be lost.
After=multi-user.target vintner-bless.service cloud-final.service getty.target serial-getty@ttyS0.service
Requires=multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/vintner-lifecycle-report.sh

[Install]
WantedBy=multi-user.target
EOF

sudo mkdir -p "$MNT/etc/systemd/system/multi-user.target.wants"
sudo ln -sf /etc/systemd/system/vintner-lifecycle-report.service "$MNT/etc/systemd/system/multi-user.target.wants/vintner-lifecycle-report.service"

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# Setup OVMF vars
CODE_FD=""
VARS_TEMPLATE=""
if [ "$FIRMWARE" = "uefi" ]; then
	CODE_FD="/usr/share/OVMF/OVMF_CODE_4M.fd"
	VARS_TEMPLATE="/usr/share/OVMF/OVMF_VARS_4M.fd"
	if [ ! -f "$CODE_FD" ]; then
		CODE_FD="/usr/share/OVMF/OVMF_CODE.fd"
		VARS_TEMPLATE="/usr/share/OVMF/OVMF_VARS.fd"
	fi
	cp "$VARS_TEMPLATE" "$BASE_VARS"
fi


source ci/lib/vintner-qemu.sh

printf 'ID\tKind\tStatus\tCommandExit\tVintnerFiles\tNVRAM\tBoot2\tEvidence\n' > "$RESULTS"

# --- 10. Operation P0: the installed system boots, and the loaders are gone -------
# `vintner install` already removed the stock loaders (ESP directory first, then the
# packages, because shim-signed's postrm would otherwise run grub-install and replace
# FIBOOTBOOTX64.EFI: the old X0 finding). P0 now boots the installed disk and runs the
# purge once more: nothing is left to purge, and vintner must still be what boots. The
# X0 scenario (purge with the ESP directory kept) cannot arise on the product path any
# more; the installer removes the directory itself, which ci/install/run.sh (I6) and the
# install unit tests check.
echo "=== Running Operation P0 (Preparation: purge stock bootloaders) ==="
PURGE_SCRIPT=$(cat <<'EOF'
BEFORE=$(dpkg-query -W -f='${db:Status-Abbrev} ${Package}=${Version}\n' 'grub*' 'shim*' 'systemd-boot*' 2>/dev/null | awk '$1=="ii"{print $2}' | paste -sd' ')
dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' | awk '$1=="ii"{print $2}' | sort > /tmp/pkgs-before
TO_PURGE=""
for p in grub-cloud-amd64 grub-pc grub-efi-amd64 grub-efi-amd64-signed shim-signed systemd-boot; do
	if dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q "ok installed"; then
		TO_PURGE="$TO_PURGE $p"
	fi
done
RC=0
if [ -n "$TO_PURGE" ]; then
	# grub-efi-amd64-signed is Protected: apt refuses to remove it without this.
	DEBIAN_FRONTEND=noninteractive apt-get -y --allow-remove-essential purge $TO_PURGE || RC=$?
fi
apt-get update || RC=$?
dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' | awk '$1=="ii"{print $2}' | sort > /tmp/pkgs-after
echo "LOADER-PACKAGES-BEFORE: $BEFORE"
echo "PURGE-REQUESTED:$TO_PURGE"
echo "REMOVED-BY-APT: $(comm -23 /tmp/pkgs-before /tmp/pkgs-after | paste -sd' ')"
exit $RC
EOF
)
P0_SCRIPT="rm -rf /boot/efi/EFI/debian
echo \"ESP-DEBIAN-REMOVED: \$([ -e /boot/efi/EFI/debian ] && echo no || echo yes)\"
$PURGE_SCRIPT"

P0_SEED=$(make_seed "P0" "$P0_SCRIPT")
P0_VARS=""
[ "$FIRMWARE" = "uefi" ] && P0_VARS="$BASE_VARS"
PRE_P0="$OUT/pre-p0.raw"
cp --sparse=always "$DISK" "$PRE_P0"

echo "Booting P0 Boot 1..."
run_qemu "$DISK" "$P0_SEED" "$OUT/P0-boot1.log" 600 "$P0_VARS"
echo "Booting P0 Boot 2..."
run_qemu "$DISK" "$P0_SEED" "$OUT/P0-boot2.log" 120 "$P0_VARS"

inspect P0 prep "$DISK" "$PRE_P0" "$P0_VARS"
rm -f "$PRE_P0"

if [ "$(awk -F'\t' '$1=="P0"{print $3}' "$RESULTS")" != "PASS" ]; then
	echo "CRITICAL: P0 failed; every other operation would run on a broken base." >&2
	exit 1
fi
mv "$DISK" "$BASE_RAW"

ROOT_DF_LINE=$(tr -d '\r' < "$OUT/P0-boot2.log" | grep -m1 '^ROOT_DF: ' || true)
echo "Root filesystem after P0: $ROOT_DF_LINE"
echo "$ROOT_DF_LINE" >> "$VERSIONS"


printf 'FIRMWARE=%q\nOUT=%q\nROOT=%q\nUKIFY=%q\nSTUB=%q\nCODE_FD=%q\nBASE_RAW=%q\nBASE_VARS=%q\nRESULTS=%q\nVERSIONS=%q\nESP_OFFSET=%q\nBS_OFFSET=%q\nBS_PARTUUID=%q\nBIOS_BOOT_START=%q\nBIOS_BOOT_SECTORS=%q\nROOT_START=%q\nROOT_SECTORS=%q\nROOT_FS_UUID=%q\nROOT_PARTUUID=%q\nKERNEL_ABI=%q\nSEED_DIR=%q\n' \
       "$FIRMWARE" "$OUT" "$ROOT" "$UKIFY" "$STUB" "$CODE_FD" "$BASE_RAW" "$BASE_VARS" "$RESULTS" "$VERSIONS" "$ESP_OFFSET" "$BS_OFFSET" "$BS_PARTUUID" "$BIOS_BOOT_START" "$BIOS_BOOT_SECTORS" "$ROOT_START" "$ROOT_SECTORS" "$ROOT_FS_UUID" "$ROOT_PARTUUID" "$KERNEL_ABI" "$SEED_DIR" > "$OUT/base.env"
