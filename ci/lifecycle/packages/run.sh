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
      tail -n 20 /tmp/op.log > /dev/ttyS0
runcmd:
  - /tmp/run_runner.sh
EOF

	cloud-localds "$seed_img" "$ud" "$md"
	echo "$seed_img"
}

# --- 9. Preparation (Operation P0) -------------------------------------------
echo "=== Running Operation P0 (Preparation: purge stock bootloaders) ==="
P0_SCRIPT=$(cat <<'EOF'
dpkg -l 'grub*' 'shim*' 'systemd-boot*' > /var/log/vintner-p0-dpkg-before.txt || true
TO_PURGE=""
for p in grub-cloud-amd64 grub-pc grub-efi-amd64 grub-efi-amd64-signed shim-signed systemd-boot; do
	if dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q "ok installed"; then
		TO_PURGE="$TO_PURGE $p"
	fi
done
echo "Packages to purge: $TO_PURGE"
if [ -n "$TO_PURGE" ]; then
	DEBIAN_FRONTEND=noninteractive apt-get -y purge $TO_PURGE
fi
apt-get update
EOF
)

P0_SEED=$(make_seed "p0" "$P0_SCRIPT")

echo "Booting P0 Boot 1..."
P0_VARS=""
[ "$FIRMWARE" = "uefi" ] && P0_VARS="$BASE_VARS"
run_qemu "$DISK" "$P0_SEED" "$OUT/p0-boot1.log" 600 "$P0_VARS"

echo "Booting P0 Boot 2..."
run_qemu "$DISK" "$P0_SEED" "$OUT/p0-boot2.log" 120 "$P0_VARS"

# Inspect P0 result
echo "Evaluating P0 against clean installation..."
P0_EVAL=$(python3 - <<EOF
import sys, re, subprocess, hashlib

def get_hash(data):
    return hashlib.sha256(data).hexdigest()

# Check boot1 exit
b1 = open("$OUT/p0-boot1.log", errors="replace").read()
m_exit = re.search(r"=== OP EXIT: (-?\d+) ===", b1)
exit_code = int(m_exit.group(1)) if m_exit else -1

# Check boot2 report
b2 = open("$OUT/p0-boot2.log", errors="replace").read()
cmdline = ""
m_cmd = re.search(r"^CMDLINE: (.*)$", b2, re.M)
if m_cmd: cmdline = m_cmd.group(1)

entry = ""
m_entry = re.search(r"^LOADER_ENTRY_SELECTED: (.*)$", b2, re.M)
if m_entry: entry = m_entry.group(1)

bless_ok = ("BLESS_ACTIVE: active" in b2)

# Vintner boot check
booted_vintner = ("vintner.bootstorage=PARTUUID=$BS_PARTUUID" in cmdline) and bless_ok
if "$FIRMWARE" == "uefi":
    booted_vintner = booted_vintner and ("vintner-os-1" in entry)

status = "PASS" if (exit_code == 0 and booted_vintner) else "FAIL"
print(f"{status}|exit={exit_code}; booted_vintner={booted_vintner}; entry={entry}")
EOF
)

P0_STATUS="${P0_EVAL%%|*}"
P0_DETAILS="${P0_EVAL#*|}"
echo "P0 Status: $P0_STATUS ($P0_DETAILS)"

if [ "$P0_STATUS" != "PASS" ]; then
	echo "CRITICAL: P0 failed! Stock loader package purge broke vintner boot." >&2
	echo "P0 details: $P0_DETAILS" >&2
	exit 1
fi

# Move post-P0 disk to base.raw
mv "$DISK" "$BASE_RAW"
echo "base.raw created successfully."

# Record root filesystem free space after initial boot
ROOT_DF_LINE=$(grep -m1 '^ROOT_DF: ' "$OUT/p0-boot1.log" || true)
echo "Root filesystem space after initial boot: $ROOT_DF_LINE"
echo "$ROOT_DF_LINE" >> "$VERSIONS"

# Initialize results.tsv
cat <<EOF > "$RESULTS"
ID	Kind	Status	CommandExit	VintnerFiles	NVRAM	Boot2	Evidence
P0	prep	PASS	0	none	none	vintner os-1	exit=0; stock loaders purged; booted vintner os-1
EOF

# --- 10. Operations Framework -----------------------------------------------
# Python inspection helper
cat <<'EOF' > "$OUT/inspect.py"
import sys, re, os, subprocess, hashlib

raw_disk = sys.argv[1]
base_raw = sys.argv[2]
firmware = sys.argv[3]
op_id = sys.argv[4]
op_kind = sys.argv[5]
esp_offset = int(sys.argv[6])
bs_offset = int(sys.argv[7])
bs_partuuid = sys.argv[8]
boot1_log = sys.argv[9]
boot2_log = sys.argv[10]
changes_out = sys.argv[11]
c0_nvram_file = sys.argv[12]
vars_file = sys.argv[13]

mtools_env = dict(os.environ, MTOOLS_SKIP_CHECK="1")

def sha256_bytes(b):
    return hashlib.sha256(b).hexdigest()

def mtype_read(disk, offset, path):
    try:
        return subprocess.check_output(["mtype", "-i", f"{disk}@@{offset}", f"::{path}"], env=mtools_env, stderr=subprocess.DEVNULL)
    except Exception:
        return None

def mdir_list(disk, offset):
    try:
        out = subprocess.check_output(["mdir", "-s", "-b", "-i", f"{disk}@@{offset}", "::/"], env=mtools_env, stderr=subprocess.DEVNULL)
        lines = []
        for l in out.decode("utf-8", errors="replace").splitlines():
            l = l.strip()
            if l.startswith("::"): l = l[2:]
            if l and not l.endswith("/"):
                lines.append(l if l.startswith("/") else "/" + l)
        return sorted(lines)
    except Exception:
        return []

# 1. Check Boot 1 exit status
b1_text = open(boot1_log, errors="replace").read() if os.path.exists(boot1_log) else ""
m_exit = re.search(r"=== OP EXIT: (-?\d+) ===", b1_text)
cmd_exit = int(m_exit.group(1)) if m_exit else -1

# Extract last 20 lines of command output from boot1 log
cmd_tail = []
in_exit = False
for line in b1_text.splitlines():
    if "=== OP EXIT: " in line:
        in_exit = True
        continue
    if in_exit:
        if "=== VINTNER-REPORT-BEGIN ===" in line:
            break
        cmd_tail.append(line)
cmd_tail_str = "; ".join(cmd_tail[-5:]) if cmd_tail else "none"

# 2. Vintner Loader Files Check
changed_vintner_files = []
# On UEFI: \EFI\BOOT\BOOTX64.EFI, \EFI\vintner\systemd-bootx64.efi, \loader\loader.conf
if firmware == "uefi":
    for p in ["/EFI/BOOT/BOOTX64.EFI", "/EFI/vintner/systemd-bootx64.efi", "/loader/loader.conf"]:
        curr_b = mtype_read(raw_disk, esp_offset, p)
        base_b = mtype_read(base_raw, esp_offset, p)
        if curr_b is None and base_b is not None:
            changed_vintner_files.append(f"removed {p}")
        elif curr_b is not None and base_b is None:
            changed_vintner_files.append(f"created {p}")
        elif curr_b != base_b:
            changed_vintner_files.append(f"modified {p}")

# On BIOS: MBR bytes 0..439 and BIOS Boot Partition
if firmware == "bios":
    with open(raw_disk, "rb") as fc, open(base_raw, "rb") as fb:
        fc.seek(0); curr_mbr = fc.read(440)
        fb.seek(0); base_mbr = fb.read(440)
        if curr_mbr != base_mbr:
            changed_vintner_files.append("modified MBR(0..439)")
        
        # Also check loader.conf on ESP for negative test N1
        curr_lc = mtype_read(raw_disk, esp_offset, "/loader/loader.conf")
        base_lc = mtype_read(base_raw, esp_offset, "/loader/loader.conf")
        if curr_lc != base_lc:
            changed_vintner_files.append("modified /loader/loader.conf")

# 3. Boot Storage Files Check
# Normalize UKI names: strip +N-M counter
def normalize_bs_path(p):
    return re.sub(r"^(.*vintner-[^+-]+)\+\d+-\d+(\.efi)$", r"\1\2", p)

def get_bs_files(disk):
    all_f = mdir_list(disk, bs_offset)
    res = {}
    for f in all_f:
        if f == "/grub/grubenv" or f.startswith("/vintner/state/") or f.startswith("/vintner/recovery-logs/"):
            continue
        content = mtype_read(disk, bs_offset, f)
        norm_p = normalize_bs_path(f)
        res[norm_p] = sha256_bytes(content) if content is not None else ""
    return res

curr_bs = get_bs_files(raw_disk)
base_bs = get_bs_files(base_raw)

bs_added = [k for k in curr_bs if k not in base_bs]
bs_removed = [k for k in base_bs if k not in curr_bs]
bs_modified = [k for k in curr_bs if k in base_bs and curr_bs[k] != base_bs[k]]

for a in bs_added:
    changed_vintner_files.append(f"added {a}")
for r in bs_removed:
    changed_vintner_files.append(f"removed {r}")
for m in bs_modified:
    changed_vintner_files.append(f"modified {m}")

# 4. Rest of ESP Check
def get_esp_rest(disk):
    all_f = mdir_list(disk, esp_offset)
    res = {}
    for f in all_f:
        if f in ["/EFI/BOOT/BOOTX64.EFI", "/EFI/vintner/systemd-bootx64.efi", "/loader/loader.conf"]:
            continue
        content = mtype_read(disk, esp_offset, f)
        res[f] = sha256_bytes(content) if content is not None else ""
    return res

curr_esp = get_esp_rest(raw_disk)
base_esp = get_esp_rest(base_raw)
esp_added = [k for k in curr_esp if k not in base_esp]
esp_removed = [k for k in base_esp if k not in curr_esp]
esp_modified = [k for k in curr_esp if k in base_esp and curr_esp[k] != base_esp[k]]

# 5. NVRAM Check (UEFI only)
changed_nvram = []
if firmware == "uefi" and vars_file and os.path.exists(vars_file):
    try:
        out = subprocess.check_output(["virt-fw-vars", "-i", vars_file, "-p"], stderr=subprocess.DEVNULL)
        curr_entries = sorted([l.strip() for l in out.decode("utf-8", errors="replace").splitlines() if re.match(r"^(Boot[0-9A-Fa-f]{4}|BootOrder)\s*:", l.strip())])
    except Exception:
        curr_entries = []
    
    if op_id == "C0":
        # Save C0 NVRAM baseline
        with open(c0_nvram_file, "w") as f:
            f.write("\n".join(curr_entries) + "\n")
    else:
        c0_entries = [l.strip() for l in open(c0_nvram_file).read().splitlines() if l.strip()] if os.path.exists(c0_nvram_file) else []
        if curr_entries != c0_entries:
            added_nv = [e for e in curr_entries if e not in c0_entries]
            rem_nv = [e for e in c0_entries if e not in curr_entries]
            for a in added_nv: changed_nvram.append(f"+{a}")
            for r in rem_nv: changed_nvram.append(f"-{r}")

# 6. What Booted Boot 2
b2_text = open(boot2_log, errors="replace").read() if os.path.exists(boot2_log) else ""
b2_cmdline = ""
m_cmd = re.search(r"^CMDLINE: (.*)$", b2_text, re.M)
if m_cmd: b2_cmdline = m_cmd.group(1)

b2_entry = ""
m_ent = re.search(r"^LOADER_ENTRY_SELECTED: (.*)$", b2_text, re.M)
if m_ent: b2_entry = m_ent.group(1)

b2_bless = ("BLESS_ACTIVE: active" in b2_text)
cmdline_has_bs = (f"vintner.bootstorage=PARTUUID={bs_partuuid}" in b2_cmdline)

boot2_vintner = cmdline_has_bs and b2_bless
if firmware == "uefi":
    boot2_vintner = boot2_vintner and ("vintner-os-1" in b2_entry)
else:
    # Check grubenv on disk
    env_b = mtype_read(raw_disk, bs_offset, "/grub/grubenv")
    env_str = env_b.decode("utf-8", errors="replace") if env_b else ""
    booted_val = ""
    for l in env_str.splitlines():
        if l.startswith("VINTNER_BOOTED="):
            booted_val = l.split("=", 1)[1]
    boot2_vintner = boot2_vintner and (booted_val == "1" or "vintner: trying os-1" in b2_text)

boot2_str = "vintner os-1" if boot2_vintner else f"other (entry={b2_entry or 'none'}, cmdline_bs={cmdline_has_bs})"

# 7. Write changes file
with open(changes_out, "w") as f:
    f.write(f"Operation: {op_id} ({op_kind})\n")
    f.write(f"Command exit: {cmd_exit}\n")
    f.write(f"Command tail: {cmd_tail_str}\n\n")
    f.write(f"Changed vintner loader/bootstorage files: {', '.join(changed_vintner_files) or 'none'}\n")
    f.write(f"Rest of ESP changes: added={len(esp_added)}, modified={len(esp_modified)}, removed={len(esp_removed)}\n")
    for a in esp_added: f.write(f"  + {a}\n")
    for m in esp_modified: f.write(f"  * {m}\n")
    for r in esp_removed: f.write(f"  - {r}\n")
    f.write(f"\nChanged NVRAM: {', '.join(changed_nvram) or 'none'}\n")
    f.write(f"Boot 2: {boot2_str}\n")
    f.write(f"Boot 2 cmdline: {b2_cmdline}\n")

# 8. Evaluate Status
status = "FAIL"
if op_id == "N1":
    # N1 passes if it reported both changes made: negative test file on bootstorage AND modified loader.conf
    saw_bs = any("lifecycle-negative-test" in x for x in changed_vintner_files)
    saw_lc = any("loader.conf" in x for x in changed_vintner_files)
    if saw_bs and saw_lc:
        status = "PASS"
elif op_kind in ("control", "ordinary"):
    no_vintner_changes = (len(changed_vintner_files) == 0)
    no_nvram_changes = (len(changed_nvram) == 0)
    if cmd_exit == 0 and no_vintner_changes and no_nvram_changes and boot2_vintner:
        status = "PASS"
elif op_kind == "conflicting":
    status = "OBSERVED"

vintner_col = ", ".join(changed_vintner_files) if changed_vintner_files else "none"
nvram_col = ", ".join(changed_nvram) if changed_nvram else "none"
evidence = f"exit={cmd_exit}; vintner_files={vintner_col}; nvram={nvram_col}; boot2={boot2_str}"

print(f"{op_id}\t{op_kind}\t{status}\t{cmd_exit}\t{vintner_col}\t{nvram_col}\t{boot2_str}\t{evidence}")
EOF

# --- 11. Run All Operations (C0, N1, K1..K4, U1..U3, X1..X5) ----------------
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

C0_NVRAM="$OUT/c0-nvram.txt"

for op in C0 N1 K1 K2 K3 K4 U1 U2 U3 X1 X2 X3 X4 X5; do
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

	CHANGES_TXT="$OUT/${op}-changes.txt"
	ROW=$(python3 "$OUT/inspect.py" "$INSPECT_RAW" "$BASE_RAW" "$FIRMWARE" "$op" "$kind" \
		"$ESP_OFFSET" "$BS_OFFSET" "$BS_PARTUUID" "$B1_LOG" "$B2_LOG" "$CHANGES_TXT" \
		"$C0_NVRAM" "$OP_VARS")

	echo "$ROW" >> "$RESULTS"
	echo "Result: $ROW"

	# Append dpkg-query output from boot 1 to versions.txt
	echo "=== $op dpkg-query -W ===" >> "$VERSIONS"
	sed -n '/=== DPKG_QUERY_BEGIN ===/,/=== DPKG_QUERY_END ===/{ /=== DPKG_QUERY_/d; p; }' "$B1_LOG" >> "$VERSIONS" || true

	rm -f "$INSPECT_RAW"
	INSPECT_TMP=""
done

# --- 12. Print Summary & Exit Evaluation ------------------------------------
echo "=================================================================="
echo "=== Test Summary Table ($FIRMWARE) ==="
echo "=================================================================="
cat "$RESULTS"

FAILURES=$(awk -F'\t' '$2!="conflicting" && $3=="FAIL"{print $1}' "$RESULTS")
if [ -n "$FAILURES" ]; then
	echo "ERROR: Test failed on operations: $FAILURES" >&2
	exit 1
fi

echo "All $FIRMWARE lifecycle operations passed successfully!"
