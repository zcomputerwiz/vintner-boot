#!/bin/bash
# End-to-end integration test for vintner on BIOS / SeaBIOS (WO-0002).
#
# Builds a real Debian 13 system booting under SeaBIOS through vintner's static
# GRUB configuration (firmware/bios/grub.cfg), verifies grubenv boot counting,
# vintner bless re-arming, on-demand boot storage mounting (D4), the 512-byte
# first-sector rule, the 3-try failure degradation, and fallback to recovery A.
#
# Requires: root via sudo for loop mounts and disk inspection, KVM, mtools,
# qemu-system-x86, qemu-utils, cloud-image-utils, sfdisk, python3, golang-go.
set -euo pipefail

cd "$(dirname "$0")/../../.."

OUT=ci/out/integration/bios-bless
ROOT=$OUT/root
DEBS=$OUT/debs
STATE=$OUT/state
MNT=$OUT/mnt
DISK=$OUT/disk.raw
RESULTS=$OUT/results.tsv
SEED_IMG=$OUT/seed.img
FS_SERIAL=1234ABCD # FAT volume serial; GRUB sees fs UUID 1234-ABCD
export MTOOLS_SKIP_CHECK=1

rm -rf "$STATE" "$OUT/inspect_b"*
mkdir -p "$OUT" "$DEBS" "$ROOT" "$STATE" "$MNT"
: > "$OUT/versions.txt"

# has <list> <NAME=value>: true if the ;-separated list holds exactly that item.
has() {
	case ";$1;" in *";$2;"*) return 0 ;; esac
	return 1
}

# Ensure cleanup on exit
LOOP=""
GEN_MANIFEST=src/gen_manifest.go
cleanup() {
	local rc=$?
	if [ "$rc" != 0 ]; then
		for l in "$OUT"/boot*.log; do
			[ -f "$l" ] && { echo "--- last 60 lines of $l ---"; tr -d '\r' < "$l" | tail -n 60; }
		done
	fi
	if [ -n "${MNT:-}" ] && mountpoint -q "$MNT"; then
		sudo umount "$MNT" || true
	fi
	if [ -n "${LOOP:-}" ]; then
		sudo losetup -d "$LOOP" || true
	fi
	rm -f "$GEN_MANIFEST"
}
trap cleanup EXIT

# Ensure recovery kernel and initramfs exist
if [ ! -f "recovery/out/vmlinuz" ] || [ ! -f "recovery/out/initrd.img" ]; then
	echo "=== Building recovery image ==="
	bash recovery/build.sh
fi

# --- 1. Debian trixie packages, unpacked without apt or root -------------------
echo "=== Fetching Debian trixie packages ==="
MIRROR=https://deb.debian.org/debian
[ -f "$OUT/Packages" ] || curl -sSfL "$MIRROR/dists/trixie/main/binary-amd64/Packages.xz" | xz -dc > "$OUT/Packages"

field() {
	awk -v p="$1" -v f="$2:" '$1=="Package:"{cur=$2} cur==p && $1==f{sub(/^[^ ]+ /,""); print; exit}' "$OUT/Packages"
}

fetch() {
	local fn
	fn=$(field "$1" Filename)
	[ -n "$fn" ] || { echo "no $1 in trixie" >&2; exit 1; }
	[ -f "$DEBS/${fn##*/}" ] || curl -sSfL "$MIRROR/$fn" -o "$DEBS/${fn##*/}"
	dpkg-deb -x "$DEBS/${fn##*/}" "$ROOT"
	echo "$1 $(field "$1" Version)" >> "$OUT/versions.txt"
}

for p in grub-pc-bin grub-common libc6 liblzma5 libdevmapper1.02.1 libselinux1 libudev1 \
	libpcre2-8-0 libcap2; do
	fetch "$p"
done

trixie() {
	"$ROOT/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2" --library-path "$ROOT/usr/lib/x86_64-linux-gnu" "$@"
}

editenv() {
	trixie "$ROOT/usr/bin/grub-editenv" "$@"
}

# --- 2. Build static vintner binary -----------------------------------------
echo "=== Building static vintner binary ==="
(cd src && CGO_ENABLED=0 go build -o "../$OUT/vintner" ./cmd/vintner)

# --- 3. Base image and raw disk layout ---------------------------------------
echo "=== Preparing disk image ==="
./ci/lib/fetch-image.sh

IMAGE_CACHE="ci/out/cache/debian-13-genericcloud-amd64-20261001-2618.qcow2"
[ -f "$IMAGE_CACHE" ] || { echo "Image cache not found at $IMAGE_CACHE" >&2; exit 1; }

rm -f "$DISK"
qemu-img convert -O raw "$IMAGE_CACHE" "$DISK"
truncate -s +1G "$DISK"

# Add boot storage partition: type bc13c2ff-59e6-4262-a352-b275fd6f7172, ~512 MiB
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
    elif ptype == "BC13C2FF-59E6-4262-A352-B275FD6F7172":
        res["bs_start"] = p["start"]
        res["bs_sectors"] = p["size"]
        res["bs_partuuid"] = p["uuid"].lower()
for k, v in res.items():
    print(f"{k.upper()}={v}")
EOF
)
eval "$PART_INFO"

BS_OFFSET=$((BS_START * 512))
EMBED_LBA=$BIOS_BOOT_START
EMBED_ROOM=$((BIOS_BOOT_SECTORS * 512))

echo "Root: start=$ROOT_START, sectors=$ROOT_SECTORS, partuuid=$ROOT_PARTUUID"
echo "BIOS Boot: start=$BIOS_BOOT_START, sectors=$BIOS_BOOT_SECTORS"
echo "Boot Storage: start=$BS_START, sectors=$BS_SECTORS, partuuid=$BS_PARTUUID"

# --- 4. Format and populate Boot Storage & GRUB ------------------------------
echo "=== Formatting and populating Boot Storage ==="
mformat -i "$DISK@@$BS_OFFSET" -F -N "$FS_SERIAL" -v BOOTSTORAGE ::
mmd -i "$DISK@@$BS_OFFSET" ::/grub ::/vintner ::/vintner/bios ::/vintner/bios/os-1 \
	::/vintner/bios/recovery-a ::/vintner/bios/recovery-b ::/vintner/state

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
    f.seek(0)
    f.write(mbr)
    f.seek(lba * 512)
    f.write(core)
EOF

# Initial grubenv: VINTNER_ORDER=1, VINTNER_T_1=xxx, VINTNER_T_RA=xxx
editenv "$OUT/env" create
editenv "$OUT/env" set "VINTNER_ORDER=1" "VINTNER_T_1=xxx" "VINTNER_T_RA=xxx"
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/env" ::/grub/grubenv

# Copy recovery files to boot storage
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/vmlinuz ::/vintner/bios/recovery-a/vmlinuz
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/initrd.img ::/vintner/bios/recovery-a/initrd.img
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/vmlinuz ::/vintner/bios/recovery-b/vmlinuz
mcopy -o -i "$DISK@@$BS_OFFSET" recovery/out/initrd.img ::/vintner/bios/recovery-b/initrd.img

# --- 5. Mount root filesystem and configure guest ---------------------------
echo "=== Configuring guest root filesystem ==="
echo "Block devices on host before losetup:"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS

LOOP=$(sudo losetup -P -f --show "$DISK")
echo "Created loop device: $LOOP"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS "$LOOP"

ROOT_DEV=$(lsblk -rn -o PATH,PARTUUID "$LOOP" | awk -v u="$ROOT_PARTUUID" '$2==u{print $1}')
if [ -z "$ROOT_DEV" ]; then
	ROOT_DEV="${LOOP}p1"
fi

ROOT_FS_UUID=$(sudo blkid -s UUID -o value "$ROOT_DEV")
echo "Root device: $ROOT_DEV, UUID: $ROOT_FS_UUID"

sudo mount "$ROOT_DEV" "$MNT"

KERNEL_IMG=$(ls "$MNT"/boot/vmlinuz-*)
INITRD_IMG=$(ls "$MNT"/boot/initrd.img-*)
KERNEL_ABI=$(basename "$KERNEL_IMG" | sed 's/^vmlinuz-//')
echo "guest-kernel $KERNEL_ABI" >> "$OUT/versions.txt"
sed 's/^/recovery: /' recovery/out/versions.txt >> "$OUT/versions.txt"

cp "$KERNEL_IMG" "$OUT/vmlinuz"
cp "$INITRD_IMG" "$OUT/initrd.img"

# Install kernel and initrd into os-1 slot on boot storage
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/vmlinuz" ::/vintner/bios/os-1/vmlinuz
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/initrd.img" ::/vintner/bios/os-1/initrd.img

# Create slot.cfg for os-1
OS1_CMDLINE="root=UUID=$ROOT_FS_UUID ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=$BS_PARTUUID"
cat <<EOF > "$OUT/os1-slot.cfg"
set vintner_cmdline="$OS1_CMDLINE"
EOF
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/os1-slot.cfg" ::/vintner/bios/os-1/slot.cfg

# Create slot.cfg for recovery A and B
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

# Install static vintner binary
sudo cp "$OUT/vintner" "$MNT/usr/sbin/vintner"
sudo chmod 0755 "$MNT/usr/sbin/vintner"

# Install vintner-bless.service
sudo cp integration/systemd-units/vintner-bless.service "$MNT/usr/lib/systemd/system/vintner-bless.service"
sudo chmod 0644 "$MNT/usr/lib/systemd/system/vintner-bless.service"
sudo mkdir -p "$MNT/etc/systemd/system/basic.target.wants"
sudo ln -sf /usr/lib/systemd/system/vintner-bless.service "$MNT/etc/systemd/system/basic.target.wants/vintner-bless.service"

# Mask systemd-bless-boot.service and systemd-networkd-wait-online.service
sudo ln -sf /dev/null "$MNT/etc/systemd/system/systemd-bless-boot.service"
sudo ln -sf /dev/null "$MNT/etc/systemd/system/systemd-networkd-wait-online.service"

# Ensure machine-id exists and is valid 32-hex
MACHINE_ID=$(cat "$MNT/etc/machine-id" 2>/dev/null || true)
if [ ${#MACHINE_ID} -ne 32 ]; then
	MACHINE_ID=$(tr -d '-' < /proc/sys/kernel/random/uuid)
	echo "$MACHINE_ID" | sudo tee "$MNT/etc/machine-id" >/dev/null
fi
echo "Guest machine-id: $MACHINE_ID"

# Verify /etc/fstab does not contain boot storage
if grep -q "$BS_PARTUUID" "$MNT/etc/fstab"; then
	echo "ERROR: Boot storage found in /etc/fstab!" >&2
	exit 1
fi

# Install guest check runner script & service
cat <<'EOF' | sudo tee "$MNT/usr/local/bin/vintner-guest-checks.sh" >/dev/null
#!/bin/bash
set -u
LOGDIR=/var/log/vintner-checks
mkdir -p "$LOGDIR"
PHASE=$(cat /etc/vintner-test-phase 2>/dev/null || echo "boot1")
echo "=== VINTNER-TEST-PHASE: $PHASE ===" > /dev/ttyS0

case "$PHASE" in
	boot1)
		echo "=== CHECK C1 ===" > /dev/ttyS0
		systemctl status vintner-bless.service > "$LOGDIR/c1.txt" 2>&1 || true
		cat "$LOGDIR/c1.txt" > /dev/ttyS0

		echo "=== CHECK C4 ===" > /dev/ttyS0
		findmnt -rn -t vfat -o TARGET,SOURCE | while read -r target source; do
			echo "vfat-mount: $target $source" > /dev/ttyS0
		done
		grep /run/vintner /proc/self/mountinfo | while read -r line; do
			echo "vintner-mount: $line" > /dev/ttyS0
		done || true

		echo "=== VINTNER-BOOT1-DONE ===" > /dev/ttyS0
		sync
		poweroff -f
		;;
	boot2)
		echo "=== VINTNER-BOOT2-DONE ===" > /dev/ttyS0
		sync
		poweroff -f
		;;
esac
EOF
sudo chmod 0755 "$MNT/usr/local/bin/vintner-guest-checks.sh"

cat <<'EOF' | sudo tee "$MNT/etc/systemd/system/vintner-guest-checks.service" >/dev/null
[Unit]
Description=Run vintner test checks
After=multi-user.target vintner-bless.service
Requires=multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/vintner-guest-checks.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

sudo mkdir -p "$MNT/etc/systemd/system/multi-user.target.wants"
sudo ln -sf /etc/systemd/system/vintner-guest-checks.service "$MNT/etc/systemd/system/multi-user.target.wants/vintner-guest-checks.service"

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# --- 6. Initial Manifests ----------------------------------------------------
echo "=== Generating initial manifest ==="
OS1_VMLINUZ_SHA=$(sha256sum "$OUT/vmlinuz" | awk '{print $1}')
OS1_INITRD_SHA=$(sha256sum "$OUT/initrd.img" | awk '{print $1}')
OS1_SLOT_SHA=$(sha256sum "$OUT/os1-slot.cfg" | awk '{print $1}')

REC_VMLINUZ_SHA=$(sha256sum recovery/out/vmlinuz | awk '{print $1}')
REC_INITRD_SHA=$(sha256sum recovery/out/initrd.img | awk '{print $1}')
RECA_SLOT_SHA=$(sha256sum "$OUT/reca-slot.cfg" | awk '{print $1}')
RECB_SLOT_SHA=$(sha256sum "$OUT/recb-slot.cfg" | awk '{print $1}')

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

	os1VmlinuzSHA := os.Args[7]
	os1InitrdSHA := os.Args[8]
	os1SlotCfgSHA := os.Args[9]
	os1Cmdline := os.Args[10]

	recVmlinuzSHA := os.Args[11]
	recInitrdSHA := os.Args[12]
	recASlotCfgSHA := os.Args[13]
	recBSlotCfgSHA := os.Args[14]

	var m manifest.Manifest
	m.Format = 1
	m.Seq = 1
	m.InstallationID = "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f"
	m.MachineID = machineID
	m.Firmware = "bios"
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

	// Writes manifest-a with seq 1
	m.Seq = 0
	if err := manifest.Save(destDir, &m); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save manifest-a: %v\n", err)
		os.Exit(1)
	}

	// Writes manifest-b with seq 2
	if err := manifest.Save(destDir, &m); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save manifest-b: %v\n", err)
		os.Exit(1)
	}
}
EOF

(cd src && go run gen_manifest.go "../$STATE" \
	"$ROOT_PARTUUID" "$ROOT_FS_UUID" "$BS_PARTUUID" "$MACHINE_ID" "$KERNEL_ABI" \
	"$OS1_VMLINUZ_SHA" "$OS1_INITRD_SHA" "$OS1_SLOT_SHA" "$OS1_CMDLINE" \
	"$REC_VMLINUZ_SHA" "$REC_INITRD_SHA" "$RECA_SLOT_SHA" "$RECB_SLOT_SHA")
rm -f "$GEN_MANIFEST"

mcopy -o -i "$DISK@@$BS_OFFSET" "$STATE/manifest-a" ::/vintner/state/manifest-a
mcopy -o -i "$DISK@@$BS_OFFSET" "$STATE/manifest-b" ::/vintner/state/manifest-b

# --- 7. Cloud-init NoCloud seed ---------------------------------------------
echo "=== Creating cloud-init NoCloud seed disk ==="
mkdir -p "$OUT/cidata"
cat <<EOF > "$OUT/cidata/meta-data"
instance-id: vintner-bios-test
local-hostname: debian-vintner-bios
EOF

cat <<EOF > "$OUT/cidata/user-data"
#cloud-config
disable_root: false
network:
  config: disabled
EOF

cloud-localds "$SEED_IMG" "$OUT/cidata/user-data" "$OUT/cidata/meta-data"

# --- 8. QEMU Boot Helper ----------------------------------------------------
ACCEL="tcg"
[ -w /dev/kvm ] && ACCEL="kvm"
BOOT_TIMEOUT=180
[ "$ACCEL" = "tcg" ] && BOOT_TIMEOUT=900

run_qemu() {
	local boot_name="$1"
	local logfile="$OUT/${boot_name}.log"
	: > "$logfile"

	echo "Booting VM ($boot_name, accel=$ACCEL)..."
	timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
		-machine "q35,accel=$ACCEL" \
		-m 1024 -nodefaults -display none \
		-monitor none -serial "file:$logfile" -no-reboot \
		-drive "file=$DISK,format=raw,if=virtio" \
		-drive "file=$SEED_IMG,format=raw,if=virtio" || true
	echo "QEMU finished for $boot_name."
}

# --- 9. Run Boot 1 ----------------------------------------------------------
echo "=== Starting Boot 1 (bless enabled) ==="
run_qemu "boot1"

# Record Boot 1 Results
echo "Evaluating Boot 1..."
mkdir -p "$OUT/inspect_b1"
mtype -i "$DISK@@$BS_OFFSET" ::/grub/grubenv > "$OUT/inspect_b1/grubenv"
mtype -i "$DISK@@$BS_OFFSET" ::/vintner/state/manifest-a > "$OUT/inspect_b1/manifest-a"
mtype -i "$DISK@@$BS_OFFSET" ::/vintner/state/manifest-b > "$OUT/inspect_b1/manifest-b"

# C1 Check: systemctl status vintner-bless.service
C1_STATUS="FAIL"
C1_BLOCK=$(tr -d '\r' < "$OUT/boot1.log" | sed -n '/=== CHECK C1 ===/,/=== CHECK C4 ===/p')
if echo "$C1_BLOCK" | grep -q "Active: active (exited)" && echo "$C1_BLOCK" | grep -q "ExecStart=/usr/sbin/vintner bless .*status=0/SUCCESS"; then
	C1_STATUS="PASS"
fi
C1_EVID=$(echo "$C1_BLOCK" | grep -E "Active:|Process:" | paste -sd ';' || true)

# C2 Check: grubenv re-armed (VINTNER_T_1=xxx and no VINTNER_BOOTED)
C2_STATUS="FAIL"
C2_VARS=$(editenv "$OUT/inspect_b1/grubenv" list | grep '^VINTNER_' | sort | paste -sd ';')
if has "$C2_VARS" VINTNER_T_1=xxx && ! echo "$C2_VARS" | grep -q "VINTNER_BOOTED="; then
	C2_STATUS="PASS"
fi
C2_EVID="$C2_VARS"

# C3 Check: manifest after boot 1
C3_STATUS="FAIL"
C3_EVID=$(python3 - "$STATE" "$OUT/inspect_b1" <<'EOF'
import json, sys
def load(d, c):
    return json.loads(open(f"{d}/manifest-{c}").readline())
try:
    before = {c: load(sys.argv[1], c)["seq"] for c in "ab"}
    after = {c: load(sys.argv[2], c) for c in "ab"}
    new = max("ab", key=lambda c: after[c]["seq"])
    img = after[new]["images"][0]
    ok = (after[new]["seq"] == max(before.values()) + 1 and before[new] == min(before.values())
          and img["state"] == "GOOD" and img.get("first_good") and img.get("last_good"))
    print(f"{'PASS' if ok else 'FAIL'}|seq before a={before['a']} b={before['b']}, after a={after['a']['seq']} b={after['b']['seq']}; "
          f"manifest-{new}: state={img['state']} first_good={img.get('first_good')} last_good={img.get('last_good')}")
except Exception as e:
    print(f"FAIL|{e}")
EOF
)
if [ "${C3_EVID%%|*}" = "PASS" ]; then
	C3_STATUS="PASS"
fi
C3_EVID="${C3_EVID#*|}"

# C4 Check: findmnt and /run/vintner
C4_STATUS="FAIL"
C4_VFAT=$(tr -d '\r' < "$OUT/boot1.log" | sed -n 's/^vfat-mount: //p' | paste -sd ';' || true)
C4_VINTNER=$(tr -d '\r' < "$OUT/boot1.log" | sed -n 's/^vintner-mount: //p' | paste -sd ';' || true)
# findmnt prints device paths, not PARTUUIDs: the ESP must be the only vfat mount.
if [ "$(echo "$C4_VFAT" | awk -F';' '{for (i = 1; i <= NF; i++) {split($i, f, " "); print f[1]}}')" = "/boot/efi" ] && [ -z "$C4_VINTNER" ]; then
	C4_STATUS="PASS"
fi
C4_EVID="vfat mounts: ${C4_VFAT:-none}; /run/vintner mounts: ${C4_VINTNER:-none}"

# C5 Check: grubenv file on host exactly 1024 bytes, second 512 bytes all '#'
C5_STATUS="FAIL"
C5_EVID=$(python3 - "$OUT/inspect_b1/grubenv" <<'EOF'
import sys
f = sys.argv[1]
try:
    data = open(f, "rb").read()
    size = len(data)
    second_half_all_hashes = (data[512:] == b'#' * 512)
    if size == 1024 and second_half_all_hashes:
        print(f"PASS|size={size} bytes; bytes 512..1023 all '#' padding")
    else:
        print(f"FAIL|size={size}, second_half_hashes={second_half_all_hashes}")
except Exception as e:
    print(f"FAIL|{e}")
EOF
)
if [ "${C5_EVID%%|*}" = "PASS" ]; then
	C5_STATUS="PASS"
fi
C5_EVID="${C5_EVID#*|}"

# --- 10. Run Boot 2 ----------------------------------------------------------
echo "=== Preparing for Boot 2 (mask vintner-bless.service) ==="
echo "Block devices on host before losetup:"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$DISK")
ROOT_DEV=$(lsblk -rn -o PATH,PARTUUID "$LOOP" | awk -v u="$ROOT_PARTUUID" '$2==u{print $1}')
[ -n "$ROOT_DEV" ] || ROOT_DEV="${LOOP}p1"
sudo mount "$ROOT_DEV" "$MNT"
echo "boot2" | sudo tee "$MNT/etc/vintner-test-phase" >/dev/null
sudo ln -sf /dev/null "$MNT/etc/systemd/system/vintner-bless.service"
sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

echo "=== Starting Boot 2 (bless masked) ==="
run_qemu "boot2"

# Record Boot 2 Results
echo "Evaluating Boot 2..."
mkdir -p "$OUT/inspect_b2"
mtype -i "$DISK@@$BS_OFFSET" ::/grub/grubenv > "$OUT/inspect_b2/grubenv"

# C6 Check: grubenv after boot 2 (counted, not rearmed)
C6_STATUS="FAIL"
C6_VARS=$(editenv "$OUT/inspect_b2/grubenv" list | grep '^VINTNER_' | sort | paste -sd ';')
if has "$C6_VARS" VINTNER_T_1=xx && has "$C6_VARS" VINTNER_BOOTED=1; then
	C6_STATUS="PASS"
fi
C6_EVID="$C6_VARS"

# --- 11. Run Boots 3 to 5 (broken image degradation) -------------------------
echo "=== Preparing for Boots 3 to 5 (unmask bless, set VINTNER_T_1=xxx, make root fail) ==="
echo "Block devices on host before losetup:"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$DISK")
ROOT_DEV=$(lsblk -rn -o PATH,PARTUUID "$LOOP" | awk -v u="$ROOT_PARTUUID" '$2==u{print $1}')
[ -n "$ROOT_DEV" ] || ROOT_DEV="${LOOP}p1"
sudo mount "$ROOT_DEV" "$MNT"
sudo rm -f "$MNT/etc/systemd/system/vintner-bless.service"
sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# Reset grubenv to VINTNER_T_1=xxx and unset VINTNER_BOOTED
editenv "$OUT/inspect_b2/grubenv" set "VINTNER_T_1=xxx"
editenv "$OUT/inspect_b2/grubenv" unset VINTNER_BOOTED
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/inspect_b2/grubenv" ::/grub/grubenv

# Update os-1 slot.cfg with invalid root UUID and rootdelay=5 panic=5
cat <<EOF > "$OUT/bad-slot.cfg"
set vintner_cmdline="root=UUID=00000000-0000-0000-0000-000000000000 ro rootdelay=5 panic=5 console=ttyS0,115200 vintner.bootstorage=PARTUUID=$BS_PARTUUID"
EOF
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/bad-slot.cfg" ::/vintner/bios/os-1/slot.cfg

echo "=== Starting Boot 3 (first failure, expect T_1=xx) ==="
run_qemu "boot3"
mkdir -p "$OUT/inspect_b3"
mtype -i "$DISK@@$BS_OFFSET" ::/grub/grubenv > "$OUT/inspect_b3/grubenv"
B3_VARS=$(editenv "$OUT/inspect_b3/grubenv" list | grep -E '^VINTNER_(T_1|BOOTED)=' | sort | paste -sd ';')
B3_FAIL=$(tr -d '\r' < "$OUT/boot3.log" | grep -m1 -E '(Gave up waiting|ALERT!|Kernel panic|not syncing|reboot:)' || true)

echo "=== Starting Boot 4 (second failure, expect T_1=x) ==="
run_qemu "boot4"
mkdir -p "$OUT/inspect_b4"
mtype -i "$DISK@@$BS_OFFSET" ::/grub/grubenv > "$OUT/inspect_b4/grubenv"
B4_VARS=$(editenv "$OUT/inspect_b4/grubenv" list | grep -E '^VINTNER_(T_1|BOOTED)=' | sort | paste -sd ';')
B4_FAIL=$(tr -d '\r' < "$OUT/boot4.log" | grep -m1 -E '(Gave up waiting|ALERT!|Kernel panic|not syncing|reboot:)' || true)

echo "=== Starting Boot 5 (third failure, expect T_1=0) ==="
run_qemu "boot5"
mkdir -p "$OUT/inspect_b5"
mtype -i "$DISK@@$BS_OFFSET" ::/grub/grubenv > "$OUT/inspect_b5/grubenv"
B5_VARS=$(editenv "$OUT/inspect_b5/grubenv" list | grep -E '^VINTNER_(T_1|BOOTED)=' | sort | paste -sd ';')
B5_FAIL=$(tr -d '\r' < "$OUT/boot5.log" | grep -m1 -E '(Gave up waiting|ALERT!|Kernel panic|not syncing|reboot:)' || true)

# C7 Check: degradation sequence xx -> x -> 0 with BOOTED=1 each time
C7_STATUS="FAIL"
if has "$B3_VARS" VINTNER_BOOTED=1 && has "$B3_VARS" VINTNER_T_1=xx && \
   has "$B4_VARS" VINTNER_BOOTED=1 && has "$B4_VARS" VINTNER_T_1=x && \
   has "$B5_VARS" VINTNER_BOOTED=1 && has "$B5_VARS" VINTNER_T_1=0 && \
   [ -n "$B3_FAIL" ] && [ -n "$B4_FAIL" ] && [ -n "$B5_FAIL" ]; then
	C7_STATUS="PASS"
fi
C7_EVID="B3: $B3_VARS ($B3_FAIL); B4: $B4_VARS ($B4_FAIL); B5: $B5_VARS ($B5_FAIL)"

# --- 12. Run Boot 6 (Recovery fallback) ---------------------------------------
echo "=== Calculating root partition hash before Boot 6 ==="
ROOT_SHA_BEFORE=$(dd if="$DISK" skip="$ROOT_START" count="$ROOT_SECTORS" bs=512 status=none | sha256sum | awk '{print $1}')
echo "Root SHA256 before boot 6: $ROOT_SHA_BEFORE"

echo "=== Starting Boot 6 (expect fallback to recovery A) ==="
run_qemu "boot6"

# Record Boot 6 Results
echo "Evaluating Boot 6..."
mkdir -p "$OUT/inspect_b6"
mtype -i "$DISK@@$BS_OFFSET" ::/grub/grubenv > "$OUT/inspect_b6/grubenv"
B6_VARS=$(editenv "$OUT/inspect_b6/grubenv" list | grep '^VINTNER_' | sort | paste -sd ';')

C8_TRYING=$(tr -d '\r' < "$OUT/boot6.log" | sed -r 's/\x1b\[[0-9;]*[a-zA-Z]//g' | grep -m1 'vintner: trying recovery-a' || true)
C8_REC_LOG=$(tr -d '\r' < "$OUT/boot6.log" | sed -r 's/\x1b\[[0-9;]*[a-zA-Z]//g' | grep -m1 'VINTNER-RECOVERY-TEST' || true)
C8_OS1=$(tr -d '\r' < "$OUT/boot6.log" | grep -c 'vintner: trying os-1' || true)

# C8 Check: GRUB skipped os-1, booted recovery A, counted T_RA=xx and BOOTED=RA
C8_STATUS="FAIL"
if [ "$C8_OS1" = 0 ] && [ -n "$C8_TRYING" ] && echo "$C8_REC_LOG" | grep -q "root=mounted ro=yes" && \
   has "$B6_VARS" VINTNER_BOOTED=RA && has "$B6_VARS" VINTNER_T_RA=xx; then
	C8_STATUS="PASS"
fi
C8_EVID="'vintner: trying os-1' lines: $C8_OS1; ${C8_TRYING:-none}; ${C8_REC_LOG:-none}; grubenv: $B6_VARS"

# C9 Check: root filesystem byte-identical after recovery boot
echo "=== Calculating root partition hash after Boot 6 ==="
ROOT_SHA_AFTER=$(dd if="$DISK" skip="$ROOT_START" count="$ROOT_SECTORS" bs=512 status=none | sha256sum | awk '{print $1}')
echo "Root SHA256 after boot 6: $ROOT_SHA_AFTER"

C9_STATUS="FAIL"
if [ "$ROOT_SHA_BEFORE" = "$ROOT_SHA_AFTER" ] && [ -n "$ROOT_SHA_BEFORE" ]; then
	C9_STATUS="PASS"
fi
C9_EVID="before: $ROOT_SHA_BEFORE; after: $ROOT_SHA_AFTER"

# --- 13. Print Results Table ------------------------------------------------
echo "=== Test Summary Table ==="
{
	printf "ID\tAfter\tCheck\tExpected\tStatus\tEvidence\n"
	printf "C1\tboot 1\tsystemctl status vintner-bless.service\tactive (exited), status 0\t%s\t%s\n" "$C1_STATUS" "$C1_EVID"
	printf "C2\tboot 1\tgrubenv\tVINTNER_T_1=xxx and no VINTNER_BOOTED (re-armed)\t%s\t%s\n" "$C2_STATUS" "$C2_EVID"
	printf "C3\tboot 1\tmanifest\tgeneration 1 GOOD, first_good/last_good set, seq one higher, written to the older copy\t%s\t%s\n" "$C3_STATUS" "$C3_EVID"
	printf "C4\tboot 1\tfindmnt -rn -t vfat; grep /run/vintner mountinfo\tOnly the ESP at /boot/efi; boot storage not mounted anywhere\t%s\t%s\n" "$C4_STATUS" "$C4_EVID"
	printf "C5\tboot 1\tgrubenv file on host\texactly 1024 bytes, second 512 bytes all '#'\t%s\t%s\n" "$C5_STATUS" "$C5_EVID"
	printf "C6\tboot 2\tgrubenv\tVINTNER_T_1=xx, VINTNER_BOOTED=1 (counted, not rearmed)\t%s\t%s\n" "$C6_STATUS" "$C6_EVID"
	printf "C7\tboots 3 to 5\tgrubenv after each\tVINTNER_T_1 goes xx, then x, then 0; BOOTED=1\t%s\t%s\n" "$C7_STATUS" "$C7_EVID"
	printf "C8\tboot 6\tserial log and grubenv\tSkips os-1, boots recovery A, root=mounted ro=yes; T_RA=xx, BOOTED=RA\t%s\t%s\n" "$C8_STATUS" "$C8_EVID"
	printf "C9\tall\troot filesystem\tRecovery boot left root partition unchanged (sha256)\t%s\t%s\n" "$C9_STATUS" "$C9_EVID"
} > "$RESULTS"

cat "$RESULTS"
echo "=== versions.txt ==="
cat "$OUT/versions.txt"

# Check overall success
FAILURES=$(awk -F'\t' '$5=="FAIL"{print $1}' "$RESULTS")
if [ -n "$FAILURES" ]; then
	echo "Test run failed on checks: $FAILURES" >&2
	exit 1
fi

echo "All WO-0002 BIOS checks passed successfully!"
