#!/bin/bash
# Builds bootable USB stick images for real-hardware checks (WO-0001 Part B).
#
# Deliverables produced under ci/out/hardware/:
#   - uefi.img: UEFI GPT disk with systemd-boot, os-2+3, os-1, recovery-a+3, recovery-b
#   - bios.img: BIOS GPT disk with GRUB, BIOS boot partition, grubenv (B1 state)
#
# Image creation needs no root: curl, xz, dpkg-deb, cpio, gzip, sfdisk, mtools, python3.
# Writing to a USB device ('write' subcommand) requires root and prints lsblk safety info.
set -euo pipefail

cd "$(dirname "$0")/../.."

OUT=ci/out/hardware
ROOT=$OUT/root
DEBS=$OUT/debs
MIRROR=https://deb.debian.org/debian
FS_SERIAL=1234ABCD # FAT volume serial for BIOS; GRUB sees fs UUID 1234-ABCD
export MTOOLS_SKIP_CHECK=1

mkdir -p "$OUT" "$DEBS" "$ROOT"

first() {
	local f
	for f; do
		[ -e "$f" ] && { echo "$f"; return 0; }
	done
	return 1
}

# --- 1. Fetch Debian trixie packages without apt or root ---------------------
fetch_packages() {
	echo "=== Fetching Debian trixie packages ==="
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

	: > "$OUT/versions.txt"
	local kernel_pkg
	kernel_pkg=$(field linux-image-amd64 Depends | sed 's/ .*//; s/,$//')

	for p in systemd-boot-efi systemd-ukify busybox-static "$kernel_pkg" \
		grub-pc-bin grub-common libc6 liblzma5 libdevmapper1.02.1 libselinux1 libudev1 \
		libpcre2-8-0 libcap2; do
		fetch "$p"
	done
}

trixie() {
	"$ROOT/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2" --library-path "$ROOT/usr/lib/x86_64-linux-gnu" "$@"
}

editenv() {
	trixie "$ROOT/usr/bin/grub-editenv" "$@"
}

# --- 2. Build test initramfs (console=tty0, 30s wait, poweroff) -------------
build_initramfs() {
	echo "=== Building test initramfs ==="
	local busybox
	busybox=$(first "$ROOT/usr/bin/busybox" "$ROOT/bin/busybox")
	[ -n "$busybox" ] || { echo "busybox not found" >&2; exit 1; }

	rm -rf "$OUT/initrd"
	mkdir -p "$OUT/initrd"/{bin,proc,dev,sys}
	cp "$busybox" "$OUT/initrd/bin/busybox"

	cat > "$OUT/initrd/init" <<'EOF'
#!/bin/busybox sh
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t devtmpfs dev /dev
/bin/busybox mount -t sysfs sys /sys 2>/dev/null || true

arg() {
	/bin/busybox sed -n "s/.*vintner.$1=\([^ ]*\).*/\1/p" /proc/cmdline
}

ID="$(arg test_id)"
[ -n "$ID" ] || ID="unknown"

# Print banner to tty0 (screen)
if [ -w /dev/tty0 ]; then
	{
		echo ""
		echo "=========================================================="
		echo "        VINTNER REAL-HARDWARE TEST BOOTED"
		echo "=========================================================="
		echo ""
		echo "  Booted image test_id : $ID"
		echo "  Kernel release       : $(/bin/busybox uname -r)"
		echo ""
		echo "  Please record this test ID on your worksheet."
		echo "  Machine will automatically power off in 30 seconds..."
		echo "=========================================================="
		echo ""
	} > /dev/tty0
fi

# Print banner to serial console if present
if [ -w /dev/ttyS0 ]; then
	echo "VINTNER-TEST-BOOTED id=$ID" > /dev/ttyS0
fi

if [ "$(arg test_action)" = "reboot" ]; then
	/bin/busybox sleep 30
	/bin/busybox reboot -f
fi

/bin/busybox sleep 30
/bin/busybox poweroff -f
EOF
	chmod +x "$OUT/initrd/init"
	(cd "$OUT/initrd" && find . | cpio -o -H newc --owner=0:0 --quiet | gzip -9) > "$OUT/initrd.img"
}

# --- 3. Build uefi.img -------------------------------------------------------
build_uefi_image() {
	echo "=== Building uefi.img ==="
	local img="$OUT/uefi.img"
	local sdboot="$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi"
	local stub="$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub"
	local ukify
	ukify=$(first "$ROOT/usr/lib/systemd/ukify" "$ROOT/usr/bin/ukify")
	local kernel
	kernel=$(ls "$ROOT"/boot/vmlinuz-*)

	rm -f "$img"
	truncate -s 400M "$img"

	printf 'label: gpt\nsize=64MiB, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B\ntype=BC13C2FF-59E6-4262-A352-B275FD6F7172\n' | sfdisk -q "$img"

	local esp_start bs_start
	esp_start=$(python3 - "$img" <<'EOF'
import json, sys, subprocess
data = json.loads(subprocess.check_output(["sfdisk", "-J", sys.argv[1]]))["partitiontable"]["partitions"]
print(data[0]["start"])
EOF
)
	bs_start=$(python3 - "$img" <<'EOF'
import json, sys, subprocess
data = json.loads(subprocess.check_output(["sfdisk", "-J", sys.argv[1]]))["partitiontable"]["partitions"]
print(data[1]["start"])
EOF
)

	local esp_off=$((esp_start * 512))
	local bs_off=$((bs_start * 512))

	mformat -i "$img@@$esp_off" -F -v ESP ::
	mformat -i "$img@@$bs_off" -F -v BOOTSTORAGE ::

	mmd -i "$img@@$esp_off" ::/EFI ::/EFI/BOOT ::/EFI/vintner ::/EFI/Linux ::/loader ::/loader/entries
	mmd -i "$img@@$bs_off" ::/EFI ::/EFI/Linux

	mcopy -i "$img@@$esp_off" "$sdboot" ::/EFI/BOOT/BOOTX64.EFI
	mcopy -i "$img@@$esp_off" "$sdboot" ::/EFI/vintner/systemd-bootx64.efi
	mcopy -i "$img@@$esp_off" firmware/uefi/loader.conf ::/loader/loader.conf

	make_uki() { # name test_id image_id image_version
		local name="$1" test_id="$2" img_id="$3" img_ver="$4"
		local osrel="$OUT/osrel-$name"
		printf 'ID=debian\nIMAGE_ID=%s\nIMAGE_VERSION=%s\nPRETTY_NAME="%s"\n' "$img_id" "$img_ver" "$name" > "$osrel"
		python3 "$ukify" build \
			--linux="$kernel" \
			--initrd="$OUT/initrd.img" \
			--stub="$stub" \
			--os-release="@$osrel" \
			--output="$OUT/$name.efi" \
			--cmdline="console=tty0 console=ttyS0 panic=-1 vintner.test_id=$test_id" >/dev/null
	}

	# Scenario S1: os-2+3, os-1, recovery A +3, recovery B
	make_uki "vintner-os-2" "os-2" "vintner-os" "2"
	make_uki "vintner-os-1" "os-1" "vintner-os" "1"
	make_uki "vintner-recovery-a" "ra" "vintner-recovery-a" "20261015"
	make_uki "vintner-recovery-b" "rb" "vintner-recovery-b" "20261015"

	mcopy -i "$img@@$bs_off" "$OUT/vintner-os-2.efi" ::/EFI/Linux/vintner-os-2+3.efi
	mcopy -i "$img@@$bs_off" "$OUT/vintner-os-1.efi" ::/EFI/Linux/vintner-os-1.efi
	mcopy -i "$img@@$bs_off" "$OUT/vintner-recovery-a.efi" ::/EFI/Linux/vintner-recovery-a+3.efi
	mcopy -i "$img@@$bs_off" "$OUT/vintner-recovery-b.efi" ::/EFI/Linux/vintner-recovery-b.efi

	echo "uefi.img built successfully at $img"
}

# --- 4. Build bios.img -------------------------------------------------------
build_bios_image() {
	echo "=== Building bios.img ==="
	local img="$OUT/bios.img"
	local kernel
	kernel=$(ls "$ROOT"/boot/vmlinuz-*)
	local grubdir="$ROOT/usr/lib/grub/i386-pc"

	rm -f "$img"
	truncate -s 160M "$img"

	printf 'label: gpt\nsize=2MiB, type=21686148-6449-6E6F-744E-656564454649\ntype=BC13C2FF-59E6-4262-A352-B275FD6F7172\n' | sfdisk -q "$img"

	local embed_lba bs_start
	embed_lba=$(python3 - "$img" <<'EOF'
import json, sys, subprocess
data = json.loads(subprocess.check_output(["sfdisk", "-J", sys.argv[1]]))["partitiontable"]["partitions"]
print(data[0]["start"])
EOF
)
	bs_start=$(python3 - "$img" <<'EOF'
import json, sys, subprocess
data = json.loads(subprocess.check_output(["sfdisk", "-J", sys.argv[1]]))["partitiontable"]["partitions"]
print(data[1]["start"])
EOF
)

	local bs_off=$((bs_start * 512))

	mformat -i "$img@@$bs_off" -F -N "$FS_SERIAL" -v BOOTSTORAGE ::
	mmd -i "$img@@$bs_off" ::/grub ::/vintner ::/vintner/bios

	mcopy -i "$img@@$bs_off" firmware/bios/grub.cfg ::/grub/grub.cfg

	slot_cfg() { # dest_img slot_dir test_id
		local slot_dir="$1" test_id="$2"
		printf 'set vintner_cmdline="console=tty0 console=ttyS0 panic=-1 vintner.test_id=%s"\n' "$test_id" > "$OUT/slot.cfg"
		mcopy -o -i "$img@@$bs_off" "$OUT/slot.cfg" "::/vintner/bios/$slot_dir/slot.cfg"
	}

	for s in os-1 os-2 recovery-a recovery-b; do
		mmd -i "$img@@$bs_off" "::/vintner/bios/$s"
		mcopy -i "$img@@$bs_off" "$kernel" "::/vintner/bios/$s/vmlinuz"
		mcopy -i "$img@@$bs_off" "$OUT/initrd.img" "::/vintner/bios/$s/initrd.img"
		slot_cfg "$s" "${s/recovery-/r}"
	done

	# Early config and core.img
	printf 'search --no-floppy --fs-uuid --set=root %s-%s\nset prefix=(%s)/grub\n' \
		"${FS_SERIAL:0:4}" "${FS_SERIAL:4}" "\$root" > "$OUT/early.cfg"

	trixie "$ROOT/usr/bin/grub-mkimage" -O i386-pc -d "$grubdir" -o "$OUT/core.img" -c "$OUT/early.cfg" -p /grub \
		biosdisk part_gpt fat search search_fs_uuid normal configfile linux echo test true regexp eval \
		loadenv keystatus serial terminal halt reboot

	# Embed core.img and MBR jump code
	python3 - "$img" "$OUT/core.img" "$grubdir/boot.img" "$embed_lba" $((2 * 1024 * 1024)) <<'EOF'
import struct, sys
img, lba, room = sys.argv[1], int(sys.argv[4]), int(sys.argv[5])
core = bytearray(open(sys.argv[2], 'rb').read())
core += bytes(-len(core) % 512)
boot = open(sys.argv[3], 'rb').read()
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

	# Initial grubenv for Scenario B1:
	# VINTNER_ORDER="2 1", VINTNER_T_2=xxx, VINTNER_T_1=xxx, VINTNER_T_RA=xxx
	editenv "$OUT/env" create
	editenv "$OUT/env" set "VINTNER_ORDER=2 1" VINTNER_T_2=xxx VINTNER_T_1=xxx VINTNER_T_RA=xxx
	mcopy -o -i "$img@@$bs_off" "$OUT/env" ::/grub/grubenv

	echo "bios.img built successfully at $img"
}

# --- 5. Inspect stick / image ------------------------------------------------
inspect_target() {
	local type="${1:-}"
	local target="${2:-}"

	if [ -z "$type" ] || [ -z "$target" ]; then
		echo "Usage: $0 inspect <uefi|bios> <path-or-device>" >&2
		exit 1
	fi

	[ -e "$target" ] || { echo "Target $target does not exist" >&2; exit 1; }

	if [ -b "$target" ]; then
		echo "Block device details:"
		lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS "$target"
	fi

	local bs_part=""
	if [ -b "$target" ]; then
		# Target is block device (e.g. /dev/sde)
		local bs_uuid="bc13c2ff-59e6-4262-a352-b275fd6f7172"
		bs_part=$(lsblk -rn -o PATH,PARTTYPE "$target" | awk -v u="$bs_uuid" 'tolower($2)==tolower(u){print $1; exit}')
		if [ -z "$bs_part" ]; then
			# Fallback: partition 2
			bs_part="${target}2"
			[ -b "$bs_part" ] || bs_part="${target}p2"
		fi
		[ -b "$bs_part" ] || { echo "Could not find boot storage partition on $target" >&2; exit 1; }
	else
		# Target is raw image file
		local bs_start
		bs_start=$(python3 - "$target" <<'EOF'
import json, sys, subprocess
data = json.loads(subprocess.check_output(["sfdisk", "-J", sys.argv[1]]))["partitiontable"]["partitions"]
print(data[1]["start"])
EOF
)
		bs_part="$target@@$((bs_start * 512))"
	fi

	case "$type" in
		uefi)
			echo "=== Inspecting UEFI boot storage ($bs_part) ==="
			echo "Entries in ::/EFI/Linux/:"
			mdir -b -i "$bs_part" ::/EFI/Linux | sed 's|.*/||'
			;;
		bios)
			echo "=== Inspecting BIOS boot storage ($bs_part) ==="
			mtype -i "$bs_part" ::/grub/grubenv > "$OUT/env.read"
			echo "grubenv variables (VINTNER_*):"
			editenv "$OUT/env.read" list | grep '^VINTNER_' | sort
			;;
		*)
			echo "Unknown type '$type'. Use 'uefi' or 'bios'." >&2
			exit 1
			;;
	esac
}

# --- 6. Write to USB stick ---------------------------------------------------
write_stick() {
	local type="${1:-}"
	local dev="${2:-}"

	if [ -z "$type" ] || [ -z "$dev" ]; then
		echo "Usage: $0 write <uefi|bios> <block-device>" >&2
		exit 1
	fi

	[ -b "$dev" ] || { echo "Error: $dev is not a block device." >&2; exit 1; }

	local img="$OUT/$type.img"
	[ -f "$img" ] || { echo "Image $img not found. Run '$0 build' first." >&2; exit 1; }

	echo "=========================================================="
	echo "  WARNING: YOU ARE ABOUT TO OVERWRITE A BLOCK DEVICE!"
	echo "=========================================================="
	echo "Block devices currently on host:"
	lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
	echo ""
	echo "Target device: $dev"
	lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS "$dev"
	echo ""

	# Check if target is mounted
	if lsblk -rn -o MOUNTPOINTS "$dev" | grep -q '[^[:space:]]'; then
		echo "Error: Partitions on $dev are currently mounted! Unmount them before proceeding." >&2
		exit 1
	fi

	echo -n "Type the exact device path '$dev' to confirm writing: "
	read -r confirm
	if [ "$confirm" != "$dev" ]; then
		echo "Confirmation mismatch. Aborting." >&2
		exit 1
	fi

	echo "Writing $img to $dev..."
	sudo dd if="$img" of="$dev" bs=4M status=progress conv=fsync
	sudo sync
	echo "Write complete."
}

# --- Entry point -------------------------------------------------------------
cmd="${1:-build}"

case "$cmd" in
	build)
		fetch_packages
		build_initramfs
		build_uefi_image
		build_bios_image
		echo ""
		echo "=== Hardware Images Ready ==="
		echo "UEFI image: $OUT/uefi.img"
		echo "BIOS image: $OUT/bios.img"
		;;
	inspect)
		shift
		inspect_target "$@"
		;;
	write)
		shift
		write_stick "$@"
		;;
	*)
		echo "Usage: $0 {build|inspect <uefi|bios> <target>|write <uefi|bios> <device>}" >&2
		exit 1
		;;
esac
