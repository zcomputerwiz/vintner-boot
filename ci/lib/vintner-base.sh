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
if [ ! -f "recovery/out/vmlinuz" ] || [ ! -f "recovery/out/initrd.img" ] || \
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

SDBOOT=$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi
STUB=$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub
UKIFY=""
for u in "$ROOT/usr/lib/systemd/ukify" "$ROOT/usr/bin/ukify"; do
	if [ -e "$u" ]; then UKIFY="$u"; break; fi
done
[ -n "$UKIFY" ] || { echo "ukify not found in root" >&2; exit 1; }

# --- 2. Build static vintner binary -----------------------------------------
echo "=== Building static vintner binary ==="
(cd src && CGO_ENABLED=0 go build -o "../$OUT/vintner" ./cmd/vintner)

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
        res["esp_sectors"] = p["size"]
        res["esp_partuuid"] = p["uuid"].lower()
    elif ptype == "BC13C2FF-59E6-4262-A352-B275FD6F7172":
        res["bs_start"] = p["start"]
        res["bs_sectors"] = p["size"]
        res["bs_partuuid"] = p["uuid"].lower()
for k, v in res.items():
    print(f"{k.upper()}={v}")
EOF
)
eval "$PART_INFO"

ESP_OFFSET=$((ESP_START * 512))
BS_OFFSET=$((BS_START * 512))
EMBED_LBA=$BIOS_BOOT_START
EMBED_ROOM=$((BIOS_BOOT_SECTORS * 512))

echo "Root: start=$ROOT_START, sectors=$ROOT_SECTORS, partuuid=$ROOT_PARTUUID"
echo "BIOS Boot: start=$BIOS_BOOT_START, sectors=$BIOS_BOOT_SECTORS"
echo "ESP: start=$ESP_START, sectors=$ESP_SECTORS, partuuid=$ESP_PARTUUID"
echo "Boot Storage: start=$BS_START, sectors=$BS_SECTORS, partuuid=$BS_PARTUUID"

# --- 4. Format and populate Boot Storage, ESP, and MBR ------------------------
echo "=== Formatting and populating Boot Storage and ESP ==="
mformat -i "$DISK@@$BS_OFFSET" -F -N "$FS_SERIAL" -v BOOTSTORAGE ::
mmd -D s -i "$DISK@@$BS_OFFSET" ::/EFI ::/EFI/Linux ::/grub ::/vintner ::/vintner/bios \
	::/vintner/bios/os-1 ::/vintner/bios/recovery-a ::/vintner/bios/recovery-b ::/vintner/state

mcopy -o -i "$DISK@@$BS_OFFSET" firmware/bios/grub.cfg ::/grub/grub.cfg

# Build early config and core.img
printf 'search --no-floppy --fs-uuid --set=root %s-%s\nset prefix=(%s)/grub\n' \
	"${FS_SERIAL:0:4}" "${FS_SERIAL:4}" "\$root" > "$OUT/early.cfg"

trixie "$ROOT/usr/bin/grub-mkimage" -O i386-pc -d "$ROOT/usr/lib/grub/i386-pc" -o "$OUT/core.img" -c "$OUT/early.cfg" -p /grub \
	biosdisk part_gpt fat search search_fs_uuid normal configfile linux echo test true regexp eval \
	loadenv keystatus serial terminal halt reboot all_video gfxterm font

# Embed core.img and MBR jump code
python3 - "$DISK" "$EMBED_LBA" "$EMBED_ROOM" "$OUT/core.img" "$ROOT/usr/lib/grub/i386-pc/boot.img" <<'EOF'
import struct, sys
img, lba, room = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
core = bytearray(open(sys.argv[4], 'rb').read())
core += bytes(-len(core) % 512)
boot = open(sys.argv[5], 'rb').read()
assert len(boot) == 512 and len(core) <= room
struct.pack_into('<QH', core, 0x1f4, lba + 1, len(core) // 512 - 1)
with open(img, 'r+b') as f:
    mbr = bytearray(f.read(512))
    mbr[0:3] = boot[0:3]
    mbr[0x5a:0x1b8] = boot[0x5a:0x1b8]
    struct.pack_into('<Q', mbr, 0x5c, lba)
    mbr[0x66:0x68] = b'\x90\x90'
    f.seek(0); f.write(mbr)
    f.seek(lba * 512); f.write(core)
EOF

# Initial grubenv: VINTNER_ORDER=1, VINTNER_T_1=xxx, VINTNER_T_RA=xxx
editenv "$OUT/env" create
editenv "$OUT/env" set "VINTNER_ORDER=1" "VINTNER_T_1=xxx" "VINTNER_T_RA=xxx"
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/env" ::/grub/grubenv

# Populate ESP
mmd -D s -i "$DISK@@$ESP_OFFSET" ::/EFI/vintner ::/loader 2>/dev/null || true
mcopy -o -i "$DISK@@$ESP_OFFSET" "$SDBOOT" ::/EFI/BOOT/BOOTX64.EFI
mcopy -o -i "$DISK@@$ESP_OFFSET" "$SDBOOT" ::/EFI/vintner/systemd-bootx64.efi
mcopy -o -i "$DISK@@$ESP_OFFSET" firmware/uefi/loader.conf ::/loader/loader.conf

# Copy recovery kernel/initrd to boot storage for BIOS
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/vmlinuz ::/vintner/bios/recovery-a/vmlinuz
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/initrd.img ::/vintner/bios/recovery-a/initrd.img
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/vmlinuz ::/vintner/bios/recovery-b/vmlinuz
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/initrd.img ::/vintner/bios/recovery-b/initrd.img

# Copy recovery UKIs to boot storage for UEFI
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/vintner-recovery-a.efi ::/EFI/Linux/vintner-recovery-a+3-0.efi
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/vintner-recovery-b.efi ::/EFI/Linux/vintner-recovery-b.efi

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

cp "$KERNEL_IMG" "$OUT/vmlinuz"
cp "$INITRD_IMG" "$OUT/initrd.img"
# os-1's .osrel per ADR-0003: the installed os-release with IMAGE_ID=vintner-os,
# IMAGE_VERSION=<generation> and the ABI appended to PRETTY_NAME, so images the
# publisher adds (generation 2 and up) sort above it.
OS_PRETTY=$(sed -n 's/^PRETTY_NAME=//p' "$MNT/etc/os-release" | tr -d '"')
{
	grep -vE '^(IMAGE_ID|IMAGE_VERSION|PRETTY_NAME)=' "$MNT/etc/os-release"
	printf 'PRETTY_NAME="%s, %s"\nIMAGE_ID=vintner-os\nIMAGE_VERSION=1\n' "$OS_PRETTY" "$KERNEL_ABI"
} > "$OUT/osrel"

# Install static vintner binary
sudo cp "$OUT/vintner" "$MNT/usr/sbin/vintner"
sudo chmod 0755 "$MNT/usr/sbin/vintner"

# Install vintner-bless.service
sudo cp integration/systemd-units/vintner-bless.service "$MNT/usr/lib/systemd/system/vintner-bless.service"
sudo chmod 0644 "$MNT/usr/lib/systemd/system/vintner-bless.service"
sudo mkdir -p "$MNT/etc/systemd/system/basic.target.wants"
sudo ln -sf /usr/lib/systemd/system/vintner-bless.service "$MNT/etc/systemd/system/basic.target.wants/vintner-bless.service"

# Mask systemd-bless-boot.service and systemd-boot-update.service
sudo ln -sf /dev/null "$MNT/etc/systemd/system/systemd-bless-boot.service"
sudo ln -sf /dev/null "$MNT/etc/systemd/system/systemd-boot-update.service"

# Configure /etc/kernel/install.conf with layout=other
sudo mkdir -p "$MNT/etc/kernel"
echo "layout=other" | sudo tee "$MNT/etc/kernel/install.conf" >/dev/null

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

# --- 6. Populate Boot Storage slots and UKIs --------------------------------
echo "=== Populating ordinary image slots and UKIs ==="
# BIOS os-1
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/vmlinuz" ::/vintner/bios/os-1/vmlinuz
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/initrd.img" ::/vintner/bios/os-1/initrd.img

if [ -n "${VINTNER_EXTRA_CMDLINE:-}" ]; then
	OS1_CMDLINE="root=UUID=$ROOT_FS_UUID ro vintner.bootstorage=PARTUUID=$BS_PARTUUID $VINTNER_EXTRA_CMDLINE console=ttyS0,115200"
else
	OS1_CMDLINE="root=UUID=$ROOT_FS_UUID ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=$BS_PARTUUID"
fi
cat <<EOF > "$OUT/os1-slot.cfg"
set vintner_cmdline="$OS1_CMDLINE"
EOF
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/os1-slot.cfg" ::/vintner/bios/os-1/slot.cfg

# BIOS recovery slots
RECA_CMDLINE="console=ttyS0 vintner.recovery_test=1 vintner.root=UUID=$ROOT_FS_UUID"
cat <<EOF > "$OUT/reca-slot.cfg"
set vintner_cmdline="$RECA_CMDLINE"
EOF
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/reca-slot.cfg" ::/vintner/bios/recovery-a/slot.cfg

RECB_CMDLINE="console=ttyS0 vintner.recovery_test=1 vintner.root=UUID=$ROOT_FS_UUID vintner.test_slot=b"
cat <<EOF > "$OUT/recb-slot.cfg"
set vintner_cmdline="$RECB_CMDLINE"
EOF
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/recb-slot.cfg" ::/vintner/bios/recovery-b/slot.cfg

# UEFI UKI for os-1
echo "Building UKI vintner-os-1+3-0.efi with ukify..."
python3 "$UKIFY" build \
	--linux="$OUT/vmlinuz" \
	--initrd="$OUT/initrd.img" \
	--stub="$STUB" \
	--os-release="@$OUT/osrel" \
	--cmdline="$OS1_CMDLINE" \
	--output="$OUT/vintner-os-1+3-0.efi"

mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/vintner-os-1+3-0.efi" ::/EFI/Linux/vintner-os-1+3-0.efi

# --- 7. Generate Initial Manifests (firmware: both) -------------------------
echo "=== Generating initial dual-firmware manifest ==="
OS1_UKI_SHA=$(sha256sum "$OUT/vintner-os-1+3-0.efi" | awk '{print $1}')
OS1_UKI_SIZE=$(stat -c %s "$OUT/vintner-os-1+3-0.efi")

OS1_VMLINUZ_SHA=$(sha256sum "$OUT/vmlinuz" | awk '{print $1}')
OS1_INITRD_SHA=$(sha256sum "$OUT/initrd.img" | awk '{print $1}')
OS1_SLOT_SHA=$(sha256sum "$OUT/os1-slot.cfg" | awk '{print $1}')

REC_VMLINUZ_SHA=$(sha256sum recovery/out/vmlinuz | awk '{print $1}')
REC_INITRD_SHA=$(sha256sum recovery/out/initrd.img | awk '{print $1}')
RECA_SLOT_SHA=$(sha256sum "$OUT/reca-slot.cfg" | awk '{print $1}')
RECB_SLOT_SHA=$(sha256sum "$OUT/recb-slot.cfg" | awk '{print $1}')

RECA_UKI_SHA=$(sha256sum recovery/out/vintner-recovery-a.efi | awk '{print $1}')
RECA_UKI_SIZE=$(stat -c %s recovery/out/vintner-recovery-a.efi)
RECB_UKI_SHA=$(sha256sum recovery/out/vintner-recovery-b.efi | awk '{print $1}')
RECB_UKI_SIZE=$(stat -c %s recovery/out/vintner-recovery-b.efi)

GEN_MANIFEST=src/gen_manifest.go
cat <<'EOF' > "$GEN_MANIFEST"
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func main() {
	destDir := os.Args[1]
	rootPartUUID := os.Args[2]
	rootUUID := os.Args[3]
	bootStoragePartUUID := os.Args[4]
	machineID := os.Args[5]
	kernelABI := os.Args[6]

	os1UKISHA := os.Args[7]
	var os1UKISize int
	fmt.Sscanf(os.Args[8], "%d", &os1UKISize)
	os1Cmdline := os.Args[9]

	os1VmlinuzSHA := os.Args[10]
	os1InitrdSHA := os.Args[11]
	os1SlotCfgSHA := os.Args[12]

	recVmlinuzSHA := os.Args[13]
	recInitrdSHA := os.Args[14]
	recASlotCfgSHA := os.Args[15]
	recBSlotCfgSHA := os.Args[16]

	recAUKISHA := os.Args[17]
	var recAUKISize int
	fmt.Sscanf(os.Args[18], "%d", &recAUKISize)

	recBUKISHA := os.Args[19]
	var recBUKISize int
	fmt.Sscanf(os.Args[20], "%d", &recBUKISize)

	var m manifest.Manifest
	m.Format = 1
	m.Seq = 1
	m.InstallationID = "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f"
	m.MachineID = machineID
	m.Firmware = "both"
	m.SecureBootTier = "none"
	m.Root = manifest.Root{
		PartUUID: rootPartUUID,
		UUID:     rootUUID,
	}
	m.BootStorage = manifest.BootStorage{
		PartUUID: bootStoragePartUUID,
	}
	m.Tries = 3
	m.Retain = 3
	m.NextGeneration = 2

	now := time.Now().UTC().Format(time.RFC3339)
	pkg := fmt.Sprintf("linux-image-%s", kernelABI)
	ver := "1"

	img := manifest.Image{
		Generation: 1,
		State:      "TRYING",
		Stale:      false,
		Kernel: manifest.Kernel{
			ABI:     kernelABI,
			Package: &pkg,
			Version: &ver,
		},
		Cmdline: os1Cmdline,
		UEFI: &manifest.UEFIFile{
			Entry:  "vintner-os-1",
			SHA256: os1UKISHA,
			Size:   os1UKISize,
		},
		BIOS: &manifest.BIOSFiles{
			Dir: "/vintner/bios/os-1",
			Files: manifest.BIOSFilesMap{
				Vmlinuz:   os1VmlinuzSHA,
				InitrdImg: os1InitrdSHA,
				SlotCfg:   os1SlotCfgSHA,
			},
		},
		Created:   now,
		FirstGood: nil,
		LastGood:  nil,
	}
	m.Images = []manifest.Image{img}

	recA := manifest.Recovery{
		Slot:    "a",
		Version: "20261015",
		UEFI: &manifest.UEFIFile{
			Entry:  "vintner-recovery-a",
			SHA256: recAUKISHA,
			Size:   recAUKISize,
		},
		BIOS: &manifest.BIOSFiles{
			Dir: "/vintner/bios/recovery-a",
			Files: manifest.BIOSFilesMap{
				Vmlinuz:   recVmlinuzSHA,
				InitrdImg: recInitrdSHA,
				SlotCfg:   recASlotCfgSHA,
			},
		},
	}
	recB := manifest.Recovery{
		Slot:    "b",
		Version: "20261015",
		UEFI: &manifest.UEFIFile{
			Entry:  "vintner-recovery-b",
			SHA256: recBUKISHA,
			Size:   recBUKISize,
		},
		BIOS: &manifest.BIOSFiles{
			Dir: "/vintner/bios/recovery-b",
			Files: manifest.BIOSFilesMap{
				Vmlinuz:   recVmlinuzSHA,
				InitrdImg: recInitrdSHA,
				SlotCfg:   recBSlotCfgSHA,
			},
		},
	}
	m.Recovery = []manifest.Recovery{recA, recB}

	if err := m.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "manifest validation failed: %v\n", err)
		os.Exit(1)
	}

	_ = os.RemoveAll(destDir)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create destDir: %v\n", err)
		os.Exit(1)
	}

	m.Seq = 0
	if err := manifest.Save(destDir, &m); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save manifest-a: %v\n", err)
		os.Exit(1)
	}

	if err := manifest.Save(destDir, &m); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save manifest-b: %v\n", err)
		os.Exit(1)
	}
}
EOF

(cd src && go run gen_manifest.go "../$STATE" \
	"$ROOT_PARTUUID" "$ROOT_FS_UUID" "$BS_PARTUUID" "$MACHINE_ID" "$KERNEL_ABI" \
	"$OS1_UKI_SHA" "$OS1_UKI_SIZE" "$OS1_CMDLINE" \
	"$OS1_VMLINUZ_SHA" "$OS1_INITRD_SHA" "$OS1_SLOT_SHA" \
	"$REC_VMLINUZ_SHA" "$REC_INITRD_SHA" "$RECA_SLOT_SHA" "$RECB_SLOT_SHA" \
	"$RECA_UKI_SHA" "$RECA_UKI_SIZE" "$RECB_UKI_SHA" "$RECB_UKI_SIZE")
rm -f "$GEN_MANIFEST"

mcopy -o -i "$DISK@@$BS_OFFSET" "$STATE/manifest-a" ::/vintner/state/manifest-a
mcopy -o -i "$DISK@@$BS_OFFSET" "$STATE/manifest-b" ::/vintner/state/manifest-b

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

# --- 10. Preparation (Operation P0) ------------------------------------------
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
# On an EFI-booted system, shim-signed's postrm runs grub-install when /boot/efi/EFI/debian
# exists, with --force-extra-removable if grub2/force_efi_extra_removable is set, as on the
# cloud image: that replaces \EFI\BOOT\BOOTX64.EFI (X0). The installer therefore removes the
# stock loader's ESP directory before it purges the packages.
P0_SCRIPT="rm -rf /boot/efi/EFI/debian
echo \"ESP-DEBIAN-REMOVED: \$([ -e /boot/efi/EFI/debian ] && echo no || echo yes)\"
$PURGE_SCRIPT"

P0_SEED=$(make_seed "P0" "$P0_SCRIPT")
P0_VARS=""
[ "$FIRMWARE" = "uefi" ] && P0_VARS="$BASE_VARS"
PRE_P0="$OUT/pre-p0.raw"
cp --sparse=always "$DISK" "$PRE_P0"
X0_VARS=""
if [ "$FIRMWARE" = "uefi" ]; then
	X0_VARS="$OUT/X0-vars.fd"
	cp "$BASE_VARS" "$X0_VARS"
fi

echo "Booting P0 Boot 1..."
run_qemu "$DISK" "$P0_SEED" "$OUT/P0-boot1.log" 600 "$P0_VARS"
echo "Booting P0 Boot 2..."
run_qemu "$DISK" "$P0_SEED" "$OUT/P0-boot2.log" 120 "$P0_VARS"

inspect P0 prep "$DISK" "$PRE_P0" "$P0_VARS"

# X0: the same removal without first removing \EFI\debian, from the disk before P0.
echo "=== Running Operation X0 (conflicting: purge stock bootloaders, ESP directory kept) ==="
X0_SEED=$(make_seed "X0" "$PURGE_SCRIPT")
qemu-img create -q -f qcow2 -F raw -b pre-p0.raw "$OUT/X0.qcow2"
run_qemu "$OUT/X0.qcow2" "$X0_SEED" "$OUT/X0-boot1.log" 600 "$X0_VARS"
run_qemu "$OUT/X0.qcow2" "$X0_SEED" "$OUT/X0-boot2.log" 120 "$X0_VARS"
INSPECT_TMP="$OUT/X0-inspect.raw"
qemu-img convert -O raw "$OUT/X0.qcow2" "$INSPECT_TMP"
inspect X0 conflicting "$INSPECT_TMP" "$PRE_P0" "$X0_VARS"
rm -f "$INSPECT_TMP" "$OUT/X0.qcow2" "$PRE_P0"
INSPECT_TMP=""
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
