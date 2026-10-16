#!/bin/bash
# Smoke test for recovery/out (run recovery/build.sh first): boots the recovery
# image as a UKI under OVMF and as kernel+initrd under SeaBIOS, with and without
# a root disk, and checks it reports the root mounted read-only, leaves the root
# byte-identical and writes logs to boot storage. Without vintner.root= it takes
# the root from the manifest on boot storage. Then it builds LVM-on-LUKS and
# md RAID1 roots with the recovery shell and checks those the same way. No root
# needed.
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
	printf 'label: gpt\ntype=%s\n' "$3" | sfdisk -q --wipe always "$1"
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
	grep -ao -m1 'VINTNER-RECOVERY-TEST .*' "$log" | tr -d '\r' | sed 's/^VINTNER-RECOVERY-TEST //' || echo "no result"
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
# Root from the manifest on boot storage, as the UEFI recovery UKIs need: they
# are shared, so their command line names no root.
manifest() { # seq uuid: one manifest copy whose line 2 is the SHA-256 of line 1
	local j="{\"format\":1,\"seq\":$1,\"root\":{\"partuuid\":\"0a1b2c3d-0000-4000-8000-000000000001\",\"uuid\":\"$2\"}}"
	printf '%s\n%s\n' "$j" "$(printf '%s' "$j" | sha256sum | cut -d' ' -f1)"
}
bs_with() { # image manifest-a manifest-b: boot storage holding those two copies
	local off
	off=$(gpt_fat "$1" 64M BC13C2FF-59E6-4262-A352-B275FD6F7172)
	mmd -i "$1@@$off" ::/vintner ::/vintner/state
	printf '%s\n' "$2" > "$T/manifest-a" && mcopy -i "$1@@$off" "$T/manifest-a" ::/vintner/state/manifest-a
	printf '%s\n' "$3" > "$T/manifest-b" && mcopy -i "$1@@$off" "$T/manifest-b" ::/vintner/state/manifest-b
}
OTHER=0e9d8c7b-6a5f-4e3d-8c2b-1a0f9e8d7c6b
# a names the root; b has a higher seq but a broken hash, so a is used.
bs_with "$T/bs-hash.img" "$(manifest 1 "$UUID")" "$(manifest 2 "$OTHER" | sed '2s/^./x/')"
check manifest-root "$(run manifest-root uefi "$TEST" "$T/root.img" "$T/bs-hash.img")" '^root=mounted ro=yes '
# Both valid: the higher seq is used, and the root it names does not exist.
bs_with "$T/bs-seq.img" "$(manifest 1 "$UUID")" "$(manifest 2 "$OTHER")"
check manifest-seq "$(run manifest-seq bios "$TEST" "$T/root.img" "$T/bs-seq.img")" '^root=failed '
check root-unchanged "$(sha256sum < "$T/root.img")" "^${ROOT_SUM%% *}"

# --- LVM on LUKS, and md RAID1 ----------------------------------------------
# The host has no device-mapper, so the recovery shell on ttyS0 builds these
# disks itself. ttyS0 is a pair of FIFOs that this script reads and writes.
serial() { # name cmdline disks...: boots under SeaBIOS in the background
	local name=$1 cmdline=$2 d
	shift 2
	rm -f "$T/$name".{in,out}
	mkfifo "$T/$name.in" "$T/$name.out"
	local q=(qemu-system-x86_64 -machine "q35,accel=$ACCEL" -m 1024 -nodefaults -display none -monitor none
		-serial "pipe:$T/$name" -no-reboot -kernel "$OUT/vmlinuz" -initrd "$OUT/initrd.img" -append "$cmdline")
	for d in "$@"; do q+=(-drive "file=$d,format=raw,if=virtio"); done
	timeout 600 "${q[@]}" </dev/null >/dev/null 2>&1 &
	cat "$T/$name.out" > "$T/$name.log" &
}
await() { # name pattern: waits up to 5 minutes for pattern on ttyS0
	for _ in $(seq 600); do
		tr -d '\r' < "$T/$1.log" | grep -aq "$2" && return 0
		sleep 0.5
	done
	echo "$1: timed out waiting for '$2'" >&2
	return 1
}
say() { printf '%s\n' "$2" > "$T/$1.in"; }
result() { tr -d '\r' < "$T/$1.log" | grep -ao -m1 'VINTNER-RECOVERY-TEST .*' | sed 's/^VINTNER-RECOVERY-TEST //' || echo "no result"; }

LVM_UUID=2b7c1e9a-5d34-4f6e-8a21-c0d9e8f7a6b5
MD_UUID=9e8d7c6b-5a49-4382-a1b0-c9d8e7f6a5b4
truncate -s 128M "$T/luks.img" "$T/md1.img" "$T/md2.img"
serial provision "console=ttyS0" "$T/luks.img" "$T/md1.img" "$T/md2.img"
await provision 'root: absent'
# The quoted markers keep the echoed command line from matching. The shell runs
# busybox applets before PATH, and busybox has its own mke2fs.
say provision "printf pw | cryptsetup luksFormat -q --pbkdf pbkdf2 --pbkdf-force-iterations 1000 --key-file=- /dev/vda \
&& printf pw | cryptsetup open --key-file=- /dev/vda c && lvm pvcreate -q /dev/mapper/c && lvm vgcreate -q vg /dev/mapper/c \
&& lvm lvcreate -q -y -L 32m -n root vg && /usr/sbin/mke2fs -q -t ext4 -U $LVM_UUID /dev/mapper/vg-root \
&& mdadm --create /dev/md0 --run -q --level=1 --raid-devices=2 --metadata=1.2 /dev/vdb /dev/vdc \
&& /usr/sbin/mke2fs -q -t ext4 -U $MD_UUID /dev/md0 && sync && echo 'PROVISION''-OK'; poweroff -f"
await provision 'PROVISION-OK' || true
wait
check provision "$(tr -d '\r' < "$T/provision.log" | grep -ac 'PROVISION-OK')" '^1$'
SUMS=$(cat "$T/luks.img" "$T/md1.img" "$T/md2.img" | sha256sum)

serial lvm-luks "$TEST vintner.root=UUID=$LVM_UUID" "$T/luks.img"
await lvm-luks 'Passphrase for /dev/vda' && say lvm-luks pw
await lvm-luks 'VINTNER-RECOVERY-TEST' || true
wait
check lvm-on-luks "$(result lvm-luks)" '^root=mounted ro=yes '
serial md "$TEST vintner.root=UUID=$MD_UUID" "$T/md1.img" "$T/md2.img"
await md 'VINTNER-RECOVERY-TEST' || true
wait
check md-raid1 "$(result md)" '^root=mounted ro=yes '
check layers-unchanged "$(cat "$T/luks.img" "$T/md1.img" "$T/md2.img" | sha256sum)" "^${SUMS%% *}"
exit $fail
