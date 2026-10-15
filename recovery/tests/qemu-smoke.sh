#!/bin/bash
# Smoke test for recovery/out (run recovery/build.sh first): boots the recovery
# image as a UKI under OVMF and as kernel+initrd under SeaBIOS, with and without
# a root disk, and checks it reports the root mounted read-only, leaves the root
# byte-identical and writes logs to boot storage. No root needed.
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=recovery/out
T=$OUT/smoke
ROOT=$OUT/root
CODE=/usr/share/OVMF/OVMF_CODE_4M.fd
VARS=/usr/share/OVMF/OVMF_VARS_4M.fd
UUID=6f1c7a52-3b0e-4d8f-9a61-2c4e5d7b8a90
export MTOOLS_SKIP_CHECK=1
rm -rf "$T" && mkdir -p "$T/rootfs/etc"
ACCEL=tcg
[ -w /dev/kvm ] && ACCEL=kvm

echo "vintner recovery smoke test root" > "$T/rootfs/etc/hostname"
mkfs.ext4 -q -F -U "$UUID" -d "$T/rootfs" "$T/root.img" 64M
ROOT_SUM=$(sha256sum < "$T/root.img")

gpt_fat() { # image size type-guid: one FAT partition, prints its byte offset
	truncate -s "$2" "$1"
	printf 'label: gpt\ntype=%s\n' "$3" | sfdisk -q "$1"
	local off=$(( $(sfdisk -J "$1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["partitiontable"]["partitions"][0]["start"])') * 512 ))
	mformat -i "$1@@$off" -F ::
	echo "$off"
}
BS_OFF=$(gpt_fat "$T/bootstorage.img" 64M BC13C2FF-59E6-4262-A352-B275FD6F7172)
mmd -i "$T/bootstorage.img@@$BS_OFF" ::/vintner

esp_with_uki() { # cmdline: ESP disk whose removable-media loader is a test UKI
	python3 "$ROOT/usr/lib/systemd/ukify" build --linux="$OUT/vmlinuz" --initrd="$OUT/initrd.img" \
		--stub="$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub" --cmdline="$1" \
		--output="$T/test.efi" >/dev/null 2>&1
	local off
	off=$(gpt_fat "$T/esp.img" 64M C12A7328-F81F-11D2-BA4B-00A0C93EC93B)
	mmd -i "$T/esp.img@@$off" ::/EFI ::/EFI/BOOT
	mcopy -i "$T/esp.img@@$off" "$T/test.efi" ::/EFI/BOOT/BOOTX64.EFI
}

run() { # name firmware(uefi|bios) cmdline extra-disks...
	local fw=$2 cmdline=$3 log=$T/$1.log
	shift 3
	local q=(qemu-system-x86_64 -machine "q35,accel=$ACCEL" -m 1024 -nodefaults -display none
		-monitor none -serial "file:$log" -no-reboot)
	if [ "$fw" = uefi ]; then
		esp_with_uki "$cmdline"
		cp "$VARS" "$T/vars.fd" && chmod u+w "$T/vars.fd"
		q+=(-drive "if=pflash,format=raw,unit=0,file=$CODE,readonly=on"
			-drive "if=pflash,format=raw,unit=1,file=$T/vars.fd" -drive "file=$T/esp.img,format=raw,if=virtio")
	else
		q+=(-kernel "$OUT/vmlinuz" -initrd "$OUT/initrd.img" -append "$cmdline")
	fi
	local d
	for d in "$@"; do q+=(-drive "file=$d,format=raw,if=virtio"); done
	timeout 300 "${q[@]}" </dev/null >/dev/null 2>&1 || true
	grep -ao -m1 'VINTNER-RECOVERY-TEST [^\r]*' "$log" | sed 's/^VINTNER-RECOVERY-TEST //' || echo "no result"
}

fail=0
check() { # name actual expected-regex
	local r=PASS
	echo "$2" | grep -Eq "$3" || { r=FAIL; fail=1; }
	printf '%-14s %-4s %s\n' "$1" "$r" "$2"
}
TEST="console=ttyS0 vintner.recovery_test=1"
WITH_ROOT="$TEST vintner.root=UUID=$UUID"

check uefi-no-root "$(run uefi-no-root uefi "$TEST")" '^root=absent '
check uefi-root "$(run uefi-root uefi "$WITH_ROOT" "$T/root.img")" '^root=mounted ro=yes '
check bios-no-root "$(run bios-no-root bios "$TEST")" '^root=absent '
check bios-root "$(run bios-root bios "$WITH_ROOT" "$T/root.img")" '^root=mounted ro=yes '
check bios-logs "$(run bios-logs bios "$WITH_ROOT" "$T/root.img" "$T/bootstorage.img")" 'logs=/dev/vd[a-z][0-9]+:/vintner/recovery-logs/'
check logs-on-disk "$(mdir -b -i "$T/bootstorage.img@@$BS_OFF" ::/vintner/recovery-logs 2>/dev/null | head -1)" 'recovery-logs/'
check root-unchanged "$(sha256sum < "$T/root.img")" "^${ROOT_SUM%% *}"
exit $fail
