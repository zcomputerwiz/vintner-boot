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
DEBS="ci/out/lifecycle/packages/debs"
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
	loadenv keystatus serial terminal halt reboot

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
cat "$MNT/etc/os-release" > "$OUT/osrel"

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
After=multi-user.target vintner-bless.service cloud-final.service
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

OS1_CMDLINE="root=UUID=$ROOT_FS_UUID ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=$BS_PARTUUID"
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

# --- 8. QEMU VM Runner Helper ------------------------------------------------
ACCEL="tcg"
[ -w /dev/kvm ] && ACCEL="kvm"

run_qemu() {
	local disk_path="$1"
	local seed_path="$2"
	local logfile="$3"
	local timeout_secs="$4"
	local vars_path="${5:-}"

	: > "$logfile"
	local disk_fmt="raw"
	[[ "$disk_path" == *.qcow2 ]] && disk_fmt="qcow2"

	if [ "$FIRMWARE" = "uefi" ]; then
		timeout "$timeout_secs" qemu-system-x86_64 \
			-machine "q35,accel=$ACCEL" \
			-m 2048 -smp 2 -nodefaults -display none \
			-monitor none -serial "file:$logfile" -no-reboot \
			-drive "if=pflash,format=raw,unit=0,file=$CODE_FD,readonly=on" \
			-drive "if=pflash,format=raw,unit=1,file=$vars_path" \
			-drive "file=$disk_path,format=$disk_fmt,if=virtio" \
			-drive "file=$seed_path,format=raw,if=virtio" \
			-nic "user,model=virtio-net-pci" || true
	else
		timeout "$timeout_secs" qemu-system-x86_64 \
			-machine "q35,accel=$ACCEL" \
			-m 2048 -smp 2 -nodefaults -display none \
			-monitor none -serial "file:$logfile" -no-reboot \
			-drive "file=$disk_path,format=$disk_fmt,if=virtio" \
			-drive "file=$seed_path,format=raw,if=virtio" \
			-nic "user,model=virtio-net-pci" || true
	fi
}

make_seed() {
	local op_id="$1"
	local op_script="$2"
	local seed_img="$SEED_DIR/${op_id}-seed.img"
	local ud="$SEED_DIR/${op_id}-user-data"
	local md="$SEED_DIR/${op_id}-meta-data"

	cat <<EOF > "$md"
instance-id: lifecycle-${op_id}
local-hostname: debian-vintner-lifecycle
EOF

	# shellcheck disable=SC2001
INDENTED_SCRIPT=$(echo "$op_script" | sed 's/^/      /')
cat <<EOF > "$ud"
#cloud-config
disable_root: false
write_files:
  - path: /tmp/run_op.sh
    permissions: '0755'
    content: |
      #!/bin/bash
      set -u
      exec > /tmp/op.log 2>&1
$INDENTED_SCRIPT
  - path: /tmp/run_runner.sh
    permissions: '0755'
    content: |
      #!/bin/bash
      /tmp/run_op.sh
      RC=\$?
      echo "=== OP EXIT: \$RC ===" > /dev/ttyS0
      tail -n 20 /tmp/op.log | sed 's/^/OPLOG: /' > /dev/ttyS0
runcmd:
  - /tmp/run_runner.sh
EOF

	cloud-localds "$seed_img" "$ud" "$md"
	echo "$seed_img"
}

# --- 9. Inspection helper ---------------------------------------------------
# Compares a disk after an operation with the disk before it. Every read that
# should succeed fails the run instead of reporting "no change".
cat <<'EOF' > "$OUT/inspect.py"
import sys, re, os, subprocess, hashlib

(raw_disk, base_raw, firmware, op_id, op_kind, esp_offset, bs_offset, bs_partuuid,
 boot1_log, boot2_log, changes_out, c0_nvram_file, vars_file,
 bbp_start, bbp_sectors) = sys.argv[1:16]
esp_offset, bs_offset = int(esp_offset), int(bs_offset)
bbp_offset, bbp_size = int(bbp_start) * 512, int(bbp_sectors) * 512

mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")
VINTNER_ESP = ["/EFI/BOOT/BOOTX64.EFI", "/EFI/vintner/systemd-bootx64.efi", "/loader/loader.conf"]

def sha256_bytes(b):
    return hashlib.sha256(b).hexdigest()

def mtype_read(disk, offset, path):
    # None only when the file does not exist; other errors stop the run.
    r = subprocess.run(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, capture_output=True)
    if r.returncode == 0:
        return r.stdout
    if b"not found" in r.stderr:
        return None
    sys.exit(f"mtype {path} on {disk}: {r.stderr.decode(errors='replace').strip()}")

def mdir_list(disk, offset):
    out = subprocess.check_output(["mdir", "-s", "-b", "-i", f"{disk}@@{offset}", "::/"], env=mtools_env)
    lines = []
    for l in out.decode("utf-8", errors="replace").splitlines():
        l = l.strip()
        if l.startswith("::"): l = l[2:]
        if l and not l.endswith("/"):
            lines.append(l if l.startswith("/") else "/" + l)
    if not lines:
        sys.exit(f"no files listed on {disk} at offset {offset}")
    return sorted(lines)

def read_at(path, offset, size):
    with open(path, "rb") as f:
        f.seek(offset)
        return f.read(size)

def clean(text):
    return re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", text).replace("\r", "")

# 1. Boot 1: exit status and the command's last lines
b1_text = clean(open(boot1_log, errors="replace").read())
# Console output can share a line with these markers, so they are not anchored.
m_exit = re.search(r"=== OP EXIT: (-?\d+) ===", b1_text)
cmd_exit = int(m_exit.group(1)) if m_exit else -1
cmd_tail = re.findall(r"OPLOG: (.*)$", b1_text, re.M)

# 2. Vintner loader files, for both firmwares: the disk boots on both
changed_vintner_files = []
for p in VINTNER_ESP:
    base_b = mtype_read(base_raw, esp_offset, p)
    if base_b is None:
        sys.exit(f"{p} missing from the disk before the operation")
    curr_b = mtype_read(raw_disk, esp_offset, p)
    if curr_b is None:
        changed_vintner_files.append(f"removed {p}")
    elif curr_b != base_b:
        changed_vintner_files.append(f"modified {p}")
if read_at(raw_disk, 0, 440) != read_at(base_raw, 0, 440):
    changed_vintner_files.append("modified MBR boot code (bytes 0-439)")
if sha256_bytes(read_at(raw_disk, bbp_offset, bbp_size)) != sha256_bytes(read_at(base_raw, bbp_offset, bbp_size)):
    changed_vintner_files.append("modified BIOS Boot Partition")

# 3. Boot storage files, UKIs compared without their +N-M counter
def normalize_bs_path(p):
    return re.sub(r"^(.*vintner-[^+]+)\+\d+(-\d+)?(\.efi)$", r"\1\3", p)

def get_bs_files(disk):
    res = {}
    for f in mdir_list(disk, bs_offset):
        if f == "/grub/grubenv" or f.startswith("/vintner/state/") or f.startswith("/vintner/recovery-logs/"):
            continue
        content = mtype_read(disk, bs_offset, f)
        if content is None:
            sys.exit(f"{f} listed but unreadable on {disk}")
        res[normalize_bs_path(f)] = sha256_bytes(content)
    return res

curr_bs = get_bs_files(raw_disk)
base_bs = get_bs_files(base_raw)
changed_vintner_files += [f"added {k}" for k in sorted(curr_bs) if k not in base_bs]
changed_vintner_files += [f"removed {k}" for k in sorted(base_bs) if k not in curr_bs]
changed_vintner_files += [f"modified {k}" for k in sorted(curr_bs) if k in base_bs and curr_bs[k] != base_bs[k]]

# 4. Rest of the ESP (OBSERVED only)
def get_esp_rest(disk):
    res = {}
    for f in mdir_list(disk, esp_offset):
        if f in VINTNER_ESP:
            continue
        content = mtype_read(disk, esp_offset, f)
        if content is None:
            sys.exit(f"{f} listed but unreadable on {disk}")
        res[f] = sha256_bytes(content)
    return res

curr_esp = get_esp_rest(raw_disk)
base_esp = get_esp_rest(base_raw)
esp_added = [k for k in sorted(curr_esp) if k not in base_esp]
esp_removed = [k for k in sorted(base_esp) if k not in curr_esp]
esp_modified = [k for k in sorted(curr_esp) if k in base_esp and curr_esp[k] != base_esp[k]]

# 5. NVRAM, UEFI only, against the C0 run
changed_nvram = []
if firmware != "uefi":
    nvram_col = "n/a (BIOS)"
elif op_kind == "prep":
    nvram_col = "not compared (no control run yet)"
else:
    out = subprocess.check_output(["virt-fw-vars", "-i", vars_file, "-p"])
    curr_entries = sorted(re.sub(r"\s+", " ", l.strip()) for l in out.decode("utf-8", errors="replace").splitlines()
                          if re.match(r"^(Boot[0-9A-Fa-f]{4}|BootOrder)\s*:", l.strip()))
    if not curr_entries:
        sys.exit(f"no Boot#### or BootOrder variables read from {vars_file}")
    if op_id == "C0":
        with open(c0_nvram_file, "w") as f:
            f.write("\n".join(curr_entries) + "\n")
    c0_entries = [l for l in open(c0_nvram_file).read().splitlines() if l]
    changed_nvram = [f"+{e}" for e in curr_entries if e not in c0_entries] + [f"-{e}" for e in c0_entries if e not in curr_entries]
    nvram_col = "; ".join(changed_nvram) if changed_nvram else "none"

# 6. What booted boot 2
b2_text = clean(open(boot2_log, errors="replace").read())
m_cmd = re.search(r"^CMDLINE: (.*)$", b2_text, re.M)
b2_cmdline = m_cmd.group(1).strip() if m_cmd else ""
m_ent = re.search(r"^LOADER_ENTRY_SELECTED: (.*)$", b2_text, re.M)
b2_entry = m_ent.group(1).strip() if m_ent else ""
b2_bless = re.search(r"^BLESS_ACTIVE: active$", b2_text, re.M) is not None
cmdline_has_bs = f"vintner.bootstorage=PARTUUID={bs_partuuid}" in b2_cmdline.split()
grub_tries = re.findall(r"vintner: trying (\S+)", b2_text)

if firmware == "uefi":
    loader_says = re.sub(r"(\+\d+(-\d+)?)?\.efi$", "", b2_entry) == "vintner-os-1"
else:
    loader_says = grub_tries[:1] == ["os-1"]
boot2_vintner = cmdline_has_bs and b2_bless and loader_says
boot2_str = "vintner os-1" if boot2_vintner else \
    f"other (entry={b2_entry or 'none'}, grub_tries={','.join(grub_tries) or 'none'}, cmdline_bs={cmdline_has_bs}, bless_active={b2_bless})"

# 7. Changes file
vintner_col = ", ".join(changed_vintner_files) if changed_vintner_files else "none"
with open(changes_out, "w") as f:
    f.write(f"Operation: {op_id} ({op_kind}), firmware {firmware}\n")
    f.write(f"Command exit: {cmd_exit}\n")
    f.write("Command output, last lines:\n")
    for l in cmd_tail[-20:] or ["(none)"]:
        f.write(f"  | {l}\n")
    f.write(f"Changed vintner files: {vintner_col}\n")
    f.write(f"Rest of the ESP: added={len(esp_added)}, modified={len(esp_modified)}, removed={len(esp_removed)}\n")
    for a in esp_added: f.write(f"  + {a}\n")
    for m in esp_modified: f.write(f"  * {m}\n")
    for r in esp_removed: f.write(f"  - {r}\n")
    f.write(f"Changed NVRAM: {nvram_col}\n")
    f.write(f"Boot 2: {boot2_str}\n")
    f.write(f"Boot 2 cmdline: {b2_cmdline or '(none)'}\n")

# 8. Status
if op_id == "N1":
    status = "PASS" if ("added /vintner/bios/os-1/lifecycle-negative-test" in changed_vintner_files
                        and "modified /loader/loader.conf" in changed_vintner_files) else "FAIL"
elif op_kind in ("prep", "control", "ordinary"):
    ok = cmd_exit == 0 and not changed_vintner_files and not changed_nvram and boot2_vintner
    status = "PASS" if ok else "FAIL"
else:
    status = "OBSERVED"

evidence = f"exit={cmd_exit}; vintner_files={vintner_col}; nvram={nvram_col}; boot2={boot2_str}"
print(f"{op_id}\t{op_kind}\t{status}\t{cmd_exit}\t{vintner_col}\t{nvram_col}\t{boot2_str}\t{evidence}")
EOF

# inspect <op> <kind> <disk after> <disk before> <vars>: appends the row and prints the changes.
inspect() {
	local row
	row=$(python3 "$OUT/inspect.py" "$3" "$4" "$FIRMWARE" "$1" "$2" \
		"$ESP_OFFSET" "$BS_OFFSET" "$BS_PARTUUID" "$OUT/$1-boot1.log" "$OUT/$1-boot2.log" "$OUT/$1-changes.txt" \
		"$OUT/c0-nvram.txt" "$5" "$BIOS_BOOT_START" "$BIOS_BOOT_SECTORS")
	printf '%s\n' "$row" >> "$RESULTS"
	echo "Result: $row"
	cat "$OUT/$1-changes.txt"
	echo "=== $1 dpkg-query -W ===" >> "$VERSIONS"
	sed -n '/=== DPKG_QUERY_BEGIN ===/,/=== DPKG_QUERY_END ===/{ /=== DPKG_QUERY_/d; p; }' "$OUT/$1-boot1.log" | tr -d '\r' >> "$VERSIONS"
}

printf 'ID\tKind\tStatus\tCommandExit\tVintnerFiles\tNVRAM\tBoot2\tEvidence\n' > "$RESULTS"

# --- 10. Preparation (Operation P0) ------------------------------------------
echo "=== Running Operation P0 (Preparation: purge stock bootloaders) ==="
P0_SCRIPT=$(cat <<'EOF'
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
if [ "$(tail -n 1 "$RESULTS" | cut -f3)" != "PASS" ]; then
	echo "CRITICAL: P0 failed; every other operation would run on a broken base." >&2
	exit 1
fi
mv "$DISK" "$BASE_RAW"

ROOT_DF_LINE=$(tr -d '\r' < "$OUT/P0-boot2.log" | grep -m1 '^ROOT_DF: ' || true)
echo "Root filesystem after P0: $ROOT_DF_LINE"
echo "$ROOT_DF_LINE" >> "$VERSIONS"

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
