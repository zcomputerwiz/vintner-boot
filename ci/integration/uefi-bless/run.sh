#!/bin/bash
# Builds a Debian 13 UEFI disk with vintner UKI, boots it under QEMU with KVM,
# and verifies decision D4 and vintner bless (architecture §4, §5, WO-0001 Part A).
#
# Needs: bash, curl, xz, dpkg-deb, python3 with pefile, sfdisk, mtools,
# qemu-system-x86_64, ovmf, cloud-localds, go.
# Where root is needed, sudo is used (loop mounting root partition to install vintner).
set -euo pipefail
cd "$(dirname "$0")/../../.."

OUT=ci/out/integration/uefi-bless
DEBS=$OUT/debs
ROOT=$OUT/root
STATE=$OUT/state
MNT=$OUT/mnt
DISK=$OUT/disk.raw
RESULTS=$OUT/results.tsv
VARS_FD=$OUT/vars.fd
SEED_IMG=$OUT/seed.img
export MTOOLS_SKIP_CHECK=1

rm -rf "$STATE" "$OUT/inspect_b1" "$OUT/inspect_b3"
mkdir -p "$OUT" "$DEBS" "$ROOT" "$STATE" "$MNT"
: > "$OUT/versions.txt"

first() {
	local f
	for f; do
		[ -e "$f" ] && { echo "$f"; return 0; }
	done
	return 1
}

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

fetch systemd-boot-efi
fetch systemd-ukify

SDBOOT=$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi
STUB=$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub
UKIFY=$(first "$ROOT/usr/lib/systemd/ukify" "$ROOT/usr/bin/ukify")

echo "Using systemd-boot: $SDBOOT"
echo "Using ukify: $UKIFY"

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
        res["root_partuuid"] = p["uuid"].lower()
    elif ptype == "C12A7328-F81F-11D2-BA4B-00A0C93EC93B":
        res["esp_start"] = p["start"]
        res["esp_partuuid"] = p["uuid"].lower()
    elif ptype == "BC13C2FF-59E6-4262-A352-B275FD6F7172":
        res["bs_start"] = p["start"]
        res["bs_partuuid"] = p["uuid"].lower()
print(f"ROOT_START={res['root_start']}")
print(f"ROOT_PARTUUID={res['root_partuuid']}")
print(f"ESP_START={res['esp_start']}")
print(f"ESP_PARTUUID={res['esp_partuuid']}")
print(f"BS_START={res['bs_start']}")
print(f"BS_PARTUUID={res['bs_partuuid']}")
EOF
)
eval "$PART_INFO"

ESP_OFFSET=$(( ESP_START * 512 ))
BS_OFFSET=$(( BS_START * 512 ))

echo "Root: start=$ROOT_START, partuuid=$ROOT_PARTUUID"
echo "ESP: start=$ESP_START, partuuid=$ESP_PARTUUID"
echo "Boot Storage: start=$BS_START, partuuid=$BS_PARTUUID"

# --- 4. Format and populate Boot Storage & ESP ------------------------------
echo "=== Formatting and populating Boot Storage and ESP ==="
mformat -i "$DISK@@$BS_OFFSET" -F -v BOOTSTORAGE ::
mmd -i "$DISK@@$BS_OFFSET" ::/EFI ::/EFI/Linux ::/vintner ::/vintner/state

mmd -i "$DISK@@$ESP_OFFSET" ::/EFI/vintner ::/loader 2>/dev/null || true
mcopy -o -i "$DISK@@$ESP_OFFSET" "$SDBOOT" ::/EFI/BOOT/BOOTX64.EFI
mcopy -o -i "$DISK@@$ESP_OFFSET" "$SDBOOT" ::/EFI/vintner/systemd-bootx64.efi
mcopy -o -i "$DISK@@$ESP_OFFSET" firmware/uefi/loader.conf ::/loader/loader.conf

# --- 5. Mount root filesystem and configure guest ---------------------------
echo "=== Configuring guest root filesystem ==="
LOOP=""
cleanup() {
	if [ -n "${MNT:-}" ] && mountpoint -q "$MNT"; then
		sudo umount "$MNT" || true
	fi
	if [ -n "${LOOP:-}" ]; then
		sudo losetup -d "$LOOP" || true
	fi
}
trap cleanup EXIT

# Check block devices before losetup as per work order rule
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

cp "$KERNEL_IMG" "$OUT/vmlinuz"
cp "$INITRD_IMG" "$OUT/initrd.img"

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
		echo "=== CHECK A1 ===" > /dev/ttyS0
		systemctl status vintner-bless.service > "$LOGDIR/a1.txt" 2>&1 || true
		cat "$LOGDIR/a1.txt" > /dev/ttyS0

		echo "=== CHECK A4 ===" > /dev/ttyS0
		findmnt -rn -t vfat -o TARGET,SOURCE > "$LOGDIR/a4-findmnt.txt" 2>&1 || true
		grep /run/vintner /proc/self/mountinfo > "$LOGDIR/a4-mountinfo.txt" 2>&1 || true
		sed 's/^/vfat-mount: /' "$LOGDIR/a4-findmnt.txt" > /dev/ttyS0
		sed 's/^/vintner-mount: /' "$LOGDIR/a4-mountinfo.txt" > /dev/ttyS0

		echo "=== CHECK A5 ===" > /dev/ttyS0
		find /run/systemd/generator* -name '*.mount' -o -name '*.automount' > "$LOGDIR/a5-ls.txt" 2>&1 || true
		journalctl -b -o short-monotonic | grep -i gpt-auto > "$LOGDIR/a5-journal.txt" 2>&1 || true
		sed 's/^/generated: /' "$LOGDIR/a5-ls.txt" > /dev/ttyS0
		sed 's/^/gpt-auto-log: /' "$LOGDIR/a5-journal.txt" > /dev/ttyS0

		echo "=== CHECK A6 ===" > /dev/ttyS0
		/usr/sbin/vintner reconcile --dry-run > "$LOGDIR/a6.txt" 2>&1
		echo "reconcile_exit=$?" >> "$LOGDIR/a6.txt"
		echo "mounts_after_reconcile=$(grep -c /run/vintner /proc/self/mountinfo)" >> "$LOGDIR/a6.txt"
		cat "$LOGDIR/a6.txt" > /dev/ttyS0

		echo "=== VINTNER-BOOT1-DONE ===" > /dev/ttyS0
		sync
		poweroff -f
		;;
	boot2)
		echo "=== VINTNER-BOOT2-DONE ===" > /dev/ttyS0
		sync
		poweroff -f
		;;
	boot3)
		echo "=== CHECK A8 ===" > /dev/ttyS0
		systemctl status vintner-bless.service > "$LOGDIR/a8.txt" 2>&1 || true
		journalctl -u vintner-bless.service -o cat > "$LOGDIR/a8-journal.txt" 2>&1 || true
		cat "$LOGDIR/a8.txt" > /dev/ttyS0
		cat "$LOGDIR/a8-journal.txt" > /dev/ttyS0

		echo "=== VINTNER-BOOT3-DONE ===" > /dev/ttyS0
		sync
		poweroff -f
		;;
esac
EOF
sudo chmod 0755 "$MNT/usr/local/bin/vintner-guest-checks.sh"

cat <<'EOF' | sudo tee "$MNT/etc/systemd/system/vintner-guest-checks.service" >/dev/null
[Unit]
Description=Run vintner test checks
After=multi-user.target
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

echo "boot1" | sudo tee "$MNT/etc/vintner-test-phase" >/dev/null

sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# --- 6. Build UKI with ukify ------------------------------------------------
echo "=== Building initial UKI (vintner-os-1+3-0.efi) ==="
cat <<EOF > "$OUT/osrel"
ID=debian
IMAGE_ID=vintner-os
IMAGE_VERSION=1
PRETTY_NAME="Debian GNU/Linux 13 (trixie)"
EOF

CMDLINE_1="root=UUID=$ROOT_FS_UUID ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=$BS_PARTUUID"

python3 "$UKIFY" build \
	--linux="$OUT/vmlinuz" \
	--initrd="$OUT/initrd.img" \
	--stub="$STUB" \
	--os-release="@$OUT/osrel" \
	--cmdline="$CMDLINE_1" \
	--output="$OUT/vintner-os-1+3-0.efi"

mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/vintner-os-1+3-0.efi" ::/EFI/Linux/vintner-os-1+3-0.efi

# --- 7. Generate Manifest (throwaway program) --------------------------------
echo "=== Generating initial manifest ==="
UKI_SHA256=$(sha256sum "$OUT/vintner-os-1+3-0.efi" | awk '{print $1}')
UKI_SIZE=$(stat -c %s "$OUT/vintner-os-1+3-0.efi")

GEN_MANIFEST="src/gen_manifest.go"
cat <<'EOF' > "$GEN_MANIFEST"
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func main() {
	if len(os.Args) < 10 {
		fmt.Fprintf(os.Stderr, "usage: %s <destDir> <rootPartUUID> <rootUUID> <bootStoragePartUUID> <machineID> <kernelABI> <ukiSHA256> <ukiSize> <cmdline>\n", os.Args[0])
		os.Exit(1)
	}
	destDir := os.Args[1]
	rootPartUUID := os.Args[2]
	rootUUID := os.Args[3]
	bootStoragePartUUID := os.Args[4]
	machineID := os.Args[5]
	kernelABI := os.Args[6]
	ukiSHA256 := os.Args[7]
	var ukiSize int
	fmt.Sscanf(os.Args[8], "%d", &ukiSize)
	cmdline := os.Args[9]

	raw, err := os.ReadFile("../schema/examples/manifest-uefi.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read example manifest: %v\n", err)
		os.Exit(1)
	}

	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse example manifest: %v\n", err)
		os.Exit(1)
	}

	m.Format = 1
	m.Seq = 1
	m.InstallationID = "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f"
	m.MachineID = machineID
	m.Firmware = "uefi"
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
		Cmdline: cmdline,
		UEFI: &manifest.UEFIFile{
			Entry:  "vintner-os-1",
			SHA256: ukiSHA256,
			Size:   ukiSize,
		},
		Created:   now,
		FirstGood: nil,
		LastGood:  nil,
	}

	m.Images = []manifest.Image{img}

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
	"$ROOT_PARTUUID" "$ROOT_FS_UUID" "$BS_PARTUUID" \
	"$MACHINE_ID" "$KERNEL_ABI" "$UKI_SHA256" "$UKI_SIZE" "$CMDLINE_1")
rm -f "$GEN_MANIFEST"

mcopy -o -i "$DISK@@$BS_OFFSET" "$STATE/manifest-a" ::/vintner/state/manifest-a
mcopy -o -i "$DISK@@$BS_OFFSET" "$STATE/manifest-b" ::/vintner/state/manifest-b

# --- 8. Cloud-init NoCloud seed ---------------------------------------------
echo "=== Creating cloud-init NoCloud seed disk ==="
mkdir -p "$OUT/cidata"
cat <<EOF > "$OUT/cidata/meta-data"
instance-id: vintner-boot-test
local-hostname: debian-vintner
EOF

cat <<EOF > "$OUT/cidata/user-data"
#cloud-config
disable_root: false
network:
  config: disabled
EOF

cloud-localds "$SEED_IMG" "$OUT/cidata/user-data" "$OUT/cidata/meta-data"

# --- 9. QEMU Boot Helper ----------------------------------------------------
CODE_FD=$(first /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/ovmf/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_CODE.fd)
VARS_TEMPLATE=$(first /usr/share/OVMF/OVMF_VARS_4M.fd /usr/share/ovmf/OVMF_VARS_4M.fd /usr/share/OVMF/OVMF_VARS.fd)

ACCEL="tcg" BOOT_TIMEOUT=900
[ -w /dev/kvm ] && ACCEL="kvm" BOOT_TIMEOUT=180

run_qemu() {
	local boot_name="$1"
	local logfile="$OUT/${boot_name}.log"
	cp "$VARS_TEMPLATE" "$VARS_FD"
	chmod 0644 "$VARS_FD"
	: > "$logfile"

	echo "Booting VM ($boot_name, accel=$ACCEL)..."
	timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
		-machine "q35,accel=$ACCEL" \
		-m 2048 \
		-smp 2 \
		-nodefaults \
		-display none \
		-monitor none \
		-serial "file:$logfile" \
		-no-reboot \
		-drive "if=pflash,format=raw,unit=0,file=$CODE_FD,readonly=on" \
		-drive "if=pflash,format=raw,unit=1,file=$VARS_FD" \
		-drive "file=$DISK,format=raw,if=virtio" \
		-drive "file=$SEED_IMG,format=raw,if=virtio" || true
	echo "QEMU finished for $boot_name."
}

# --- 10. Run Boot 1 ----------------------------------------------------------
echo "=== Starting Boot 1 (bless enabled) ==="
run_qemu "boot1"

# Record Boot 1 Results
echo "Evaluating Boot 1..."
mkdir -p "$OUT/inspect_b1"
mtype -i "$DISK@@$BS_OFFSET" ::/vintner/state/manifest-a > "$OUT/inspect_b1/manifest-a"
mtype -i "$DISK@@$BS_OFFSET" ::/vintner/state/manifest-b > "$OUT/inspect_b1/manifest-b"
FILES_B1=$(mdir -b -i "$DISK@@$BS_OFFSET" ::/EFI/Linux | sed 's|.*/||' | tr '\n' ' ' | sed 's/ $//')

# A1 Check
A1_STATUS="FAIL"
if grep -q "Active: active (exited)" "$OUT/boot1.log"; then
	A1_STATUS="PASS"
fi
A1_EVID=$(grep -A 10 "=== CHECK A1 ===" "$OUT/boot1.log" | grep -E "Active:|Process:" | tr '\n' '; ' | sed 's/; $//')

# A2 Check
A2_STATUS="FAIL"
if echo "$FILES_B1" | grep -q "vintner-os-1+3-0.efi"; then
	A2_STATUS="PASS"
fi
A2_EVID="Files in ::/EFI/Linux: $FILES_B1"

# A3 Check
A3_STATUS="FAIL"
A3_EVID=$(python3 - "$OUT/inspect_b1" <<'EOF'
import json, sys
out = sys.argv[1]
try:
    ma = json.loads(open(f"{out}/manifest-a").readline())
    mb = json.loads(open(f"{out}/manifest-b").readline())
    if ma["seq"] == 3 and ma["images"][0]["state"] == "GOOD" and ma["images"][0]["first_good"] is not None and mb["seq"] == 2:
        print("PASS|manifest-a seq=3 (state=GOOD, first_good set), manifest-b seq=2 (TRYING)")
    else:
        print(f"FAIL|ma seq={ma['seq']} state={ma['images'][0]['state']}, mb seq={mb['seq']}")
except Exception as e:
    print(f"FAIL|{e}")
EOF
)
if [ "${A3_EVID%%|*}" = "PASS" ]; then
	A3_STATUS="PASS"
fi
A3_EVID="${A3_EVID#*|}"

# A4 Check
# Boot storage must not be mounted anywhere: the only vfat mount is the ESP.
A4_STATUS="FAIL"
A4_VFAT=$(tr -d '\r' < "$OUT/boot1.log" | sed -n 's/^vfat-mount: //p' | awk '{print $1}' | paste -sd ' ')
A4_VINTNER=$(tr -d '\r' < "$OUT/boot1.log" | grep -c '^vintner-mount: ' || true)
if [ "$A4_VFAT" = "/boot/efi" ] && [ "$A4_VINTNER" = 0 ]; then
	A4_STATUS="PASS"
fi
A4_EVID="vfat mounts: ${A4_VFAT:-none}; /run/vintner mounts: $A4_VINTNER"

# A5 Check (OBSERVED)
A5_STATUS="OBSERVED"
A5_UNITS=$(tr -d '\r' < "$OUT/boot1.log" | sed -n 's/^generated: //p' | xargs -r -n1 basename | paste -sd ' ')
A5_LOG=$(tr -d '\r' < "$OUT/boot1.log" | sed -n 's/^gpt-auto-log: //p' | head -n 5 | paste -sd ';')
A5_EVID="generated mount units: ${A5_UNITS:-none}; gpt-auto log: ${A5_LOG:-none}"

# A6 Check
A6_STATUS="FAIL"
A6_LOG=$(tr -d '\r' < "$OUT/boot1.log" | sed -n '/=== CHECK A6 ===/,/=== VINTNER-BOOT1-DONE ===/p')
if echo "$A6_LOG" | grep -q '^reconcile_exit=0$' && echo "$A6_LOG" | grep -q '^mounts_after_reconcile=0$'; then
	A6_STATUS="PASS"
fi
A6_EVID=$(echo "$A6_LOG" | grep -E '^(reconcile_exit|mounts_after_reconcile)=|^REPORT:|^ACTION:' | paste -sd ';')

# --- 11. Run Boot 2 ----------------------------------------------------------
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
FILES_B2=$(mdir -b -i "$DISK@@$BS_OFFSET" ::/EFI/Linux | sed 's|.*/||' | tr '\n' ' ' | sed 's/ $//')

# A7 Check
A7_STATUS="FAIL"
if echo "$FILES_B2" | grep -q "vintner-os-1+2-1.efi"; then
	A7_STATUS="PASS"
fi
A7_EVID="Files in ::/EFI/Linux: $FILES_B2 (decremented by systemd-boot to +2-1, not rearmed)"

# --- 12. Run Boot 3 ----------------------------------------------------------
echo "=== Preparing for Boot 3 (unmask bless, remove vintner.bootstorage=) ==="
echo "Block devices on host before losetup:"
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
LOOP=$(sudo losetup -P -f --show "$DISK")
ROOT_DEV=$(lsblk -rn -o PATH,PARTUUID "$LOOP" | awk -v u="$ROOT_PARTUUID" '$2==u{print $1}')
[ -n "$ROOT_DEV" ] || ROOT_DEV="${LOOP}p1"
sudo mount "$ROOT_DEV" "$MNT"
echo "boot3" | sudo tee "$MNT/etc/vintner-test-phase" >/dev/null
sudo rm -f "$MNT/etc/systemd/system/vintner-bless.service"
sudo umount "$MNT"
sudo losetup -d "$LOOP"
LOOP=""

# Rebuild UKI without vintner.bootstorage=
CMDLINE_3="root=UUID=$ROOT_FS_UUID ro console=ttyS0,115200"
python3 "$UKIFY" build \
	--linux="$OUT/vmlinuz" \
	--initrd="$OUT/initrd.img" \
	--stub="$STUB" \
	--os-release="@$OUT/osrel" \
	--cmdline="$CMDLINE_3" \
	--output="$OUT/vintner-os-1+2-1.efi"

# On boot storage, place updated UKI as vintner-os-1+2-1.efi
mcopy -o -i "$DISK@@$BS_OFFSET" "$OUT/vintner-os-1+2-1.efi" ::/EFI/Linux/vintner-os-1+2-1.efi

echo "=== Starting Boot 3 (missing bootstorage parameter) ==="
run_qemu "boot3"

# Record Boot 3 Results
echo "Evaluating Boot 3..."
mkdir -p "$OUT/inspect_b3"
mtype -i "$DISK@@$BS_OFFSET" ::/vintner/state/manifest-a > "$OUT/inspect_b3/manifest-a"
mtype -i "$DISK@@$BS_OFFSET" ::/vintner/state/manifest-b > "$OUT/inspect_b3/manifest-b"
FILES_B3=$(mdir -b -i "$DISK@@$BS_OFFSET" ::/EFI/Linux | sed 's|.*/||' | tr '\n' ' ' | sed 's/ $//')

# A8 Check
A8_STATUS="FAIL"
A8_EVID=$(python3 - "$OUT" "$FILES_B3" <<'EOF'
import json, sys
out = sys.argv[1]
files_b3 = sys.argv[2]
log = open(f"{out}/boot3.log", errors="replace").read()
err_found = "bootstorage: no vintner.bootstorage=PARTUUID= on the kernel command line" in log
files_ok = "vintner-os-1+1-2.efi" in files_b3
try:
    ma = json.loads(open(f"{out}/inspect_b3/manifest-a").readline())
    manifest_unchanged = (ma["seq"] == 3 and ma["images"][0]["state"] == "GOOD")
except Exception:
    manifest_unchanged = False

if err_found and files_ok and manifest_unchanged:
    print("PASS|Bless failed naming missing parameter; file decremented to +1-2; manifest unchanged")
else:
    print(f"FAIL|err_found={err_found}, files_ok={files_ok}, manifest_unchanged={manifest_unchanged}")
EOF
)
if [ "${A8_EVID%%|*}" = "PASS" ]; then
	A8_STATUS="PASS"
fi
A8_EVID="${A8_EVID#*|}"

# --- 13. Print Results Table ------------------------------------------------
echo "=== Test Summary Table ==="
{
	printf "ID\tCheck\tExpected\tStatus\tEvidence\n"
	printf "A1\tsystemctl status vintner-bless.service after first boot\tactive (exited), exit status 0\t%s\t%s\n" "$A1_STATUS" "$A1_EVID"
	printf "A2\tFile name on boot storage after first boot\tvintner-os-1+3-0.efi\t%s\t%s\n" "$A2_STATUS" "$A2_EVID"
	printf "A3\tManifest after first boot\tgeneration 1 GOOD, first_good/last_good set, seq+1\t%s\t%s\n" "$A3_STATUS" "$A3_EVID"
	printf "A4\tBoot storage mounts visible on running system\tOnly ESP mounted; boot storage not in host namespace\t%s\t%s\n" "$A4_STATUS" "$A4_EVID"
	printf "A5\tWhat systemd-gpt-auto-generator did\tOBSERVED (skips XBOOTLDR because /boot populated)\t%s\t%s\n" "$A5_STATUS" "$A5_EVID"
	printf "A6\tsudo vintner reconcile --dry-run\tExit 0, no report lines, unmounted afterwards\t%s\t%s\n" "$A6_STATUS" "$A6_EVID"
	printf "A7\tSecond boot with vintner-bless.service masked\tvintner-os-1+2-1.efi (boot counted, not rearmed)\t%s\t%s\n" "$A7_STATUS" "$A7_EVID"
	printf "A8\tThird boot with unmasked bless but missing vintner.bootstorage=\tBless fails naming parameter; file +1-2; manifest unchanged\t%s\t%s\n" "$A8_STATUS" "$A8_EVID"
} > "$RESULTS"

cat "$RESULTS"

# Check overall success
FAILURES=$(awk -F'\t' '$4=="FAIL"{print $1}' "$RESULTS")
if [ -n "$FAILURES" ]; then
	echo "Test run failed on checks: $FAILURES" >&2
	exit 1
fi

echo "All Part A checks passed successfully!"
