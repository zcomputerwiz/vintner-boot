#!/bin/bash
# Proves the static BIOS configuration (firmware/bios/grub.cfg) on Debian 13's
# own GRUB under SeaBIOS: grubenv boot counting, re-arm and fallback to
# recovery (architecture §4, ADR-0003 option B, firmware/bios/INTERFACE.md).
# Needs: curl, xz, dpkg-deb, cpio, gzip, sfdisk, mtools, python3 and
# qemu-system-x86_64 with SeaBIOS. No root.
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=ci/out/bios-trial
ROOT=$OUT/root
MIRROR=https://deb.debian.org/debian
FS_SERIAL=1234ABCD # FAT volume serial; GRUB sees it as fs UUID 1234-ABCD
export MTOOLS_SKIP_CHECK=1
mkdir -p "$OUT/debs" "$ROOT"
: > "$OUT/versions.txt"

# --- Debian trixie packages, unpacked without apt or root -------------------
[ -f "$OUT/Packages" ] || curl -sSfL "$MIRROR/dists/trixie/main/binary-amd64/Packages.xz" | xz -dc > "$OUT/Packages"
field() { # package field
	awk -v p="$1" -v f="$2:" '$1=="Package:"{cur=$2} cur==p && $1==f{sub(/^[^ ]+ /,""); print; exit}' "$OUT/Packages"
}
fetch() {
	local fn
	fn=$(field "$1" Filename)
	[ -n "$fn" ] || { echo "no $1 in trixie" >&2; exit 1; }
	[ -f "$OUT/debs/${fn##*/}" ] || curl -sSfL "$MIRROR/$fn" -o "$OUT/debs/${fn##*/}"
	dpkg-deb -x "$OUT/debs/${fn##*/}" "$ROOT"
	echo "$1 $(field "$1" Version)" >> "$OUT/versions.txt"
}
kernel_pkg=$(field linux-image-amd64 Depends | sed 's/ .*//; s/,$//')
# GRUB's host tools run on trixie's own C library and its dependencies.
for p in grub-pc-bin grub-common libc6 liblzma5 libdevmapper1.02.1 libselinux1 libudev1 \
	libpcre2-8-0 libcap2 busybox-static "$kernel_pkg"; do fetch "$p"; done
trixie() { "$ROOT/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2" --library-path "$ROOT/usr/lib/x86_64-linux-gnu" "$@"; }
editenv() { trixie "$ROOT/usr/bin/grub-editenv" "$@"; }
KERNEL=$(ls "$ROOT"/boot/vmlinuz-*)
GRUBDIR=$ROOT/usr/lib/grub/i386-pc

# --- test initramfs: print the test id, then power off or reboot -----------
rm -rf "$OUT/initrd" && mkdir -p "$OUT/initrd"/{bin,proc,dev}
cp "$ROOT/usr/bin/busybox" "$OUT/initrd/bin/busybox"
cat > "$OUT/initrd/init" <<'EOF'
#!/bin/busybox sh
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t devtmpfs dev /dev
arg() { /bin/busybox sed -n "s/.*vintner.$1=\([^ ]*\).*/\1/p" /proc/cmdline; }
echo "VINTNER-TEST-BOOTED id=$(arg test_id)" > /dev/ttyS0
if [ "$(arg test_action)" = reboot ]; then /bin/busybox reboot -f; fi
/bin/busybox poweroff -f
EOF
chmod +x "$OUT/initrd/init"
(cd "$OUT/initrd" && find . | cpio -o -H newc --owner=0:0 --quiet | gzip -9) > "$OUT/initrd.img"

# --- template disk: GPT, BIOS Boot Partition, boot storage ------------------
TEMPLATE=$OUT/template.img
rm -f "$TEMPLATE" && truncate -s 160M "$TEMPLATE"
printf 'label: gpt\nsize=2MiB, type=21686148-6449-6E6F-744E-656564454649\ntype=BC13C2FF-59E6-4262-A352-B275FD6F7172\n' | sfdisk -q "$TEMPLATE"
start() { sfdisk -J "$TEMPLATE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["partitiontable"]["partitions"][int(sys.argv[1])]["start"])' "$1"; }
EMBED_LBA=$(start 0)
BS_OFF=$(( $(start 1) * 512 ))
mformat -i "$TEMPLATE@@$BS_OFF" -F -N "$FS_SERIAL" -v BOOTSTORAGE ::
mmd -i "$TEMPLATE@@$BS_OFF" ::/grub ::/vintner ::/vintner/bios
mcopy -i "$TEMPLATE@@$BS_OFF" firmware/bios/grub.cfg ::/grub/grub.cfg
slot_cfg() { # image dir test-id action: writes /vintner/bios/<dir>/slot.cfg
	printf 'set vintner_cmdline="console=ttyS0 panic=-1 vintner.test_id=%s vintner.test_action=%s"\n' "$3" "$4" > "$OUT/slot.cfg"
	mcopy -o -i "$1" "$OUT/slot.cfg" "::/vintner/bios/$2/slot.cfg"
}
for s in os-1 os-2 recovery-a recovery-b; do
	mmd -i "$TEMPLATE@@$BS_OFF" "::/vintner/bios/$s"
	mcopy -i "$TEMPLATE@@$BS_OFF" "$KERNEL" "::/vintner/bios/$s/vmlinuz"
	mcopy -i "$TEMPLATE@@$BS_OFF" "$OUT/initrd.img" "::/vintner/bios/$s/initrd.img"
	slot_cfg "$TEMPLATE@@$BS_OFF" "$s" "${s/recovery-/r}" poweroff
done

# core.img holds every module grub.cfg needs, so nothing is loaded from the
# boot storage partition and core.img and modules can never mismatch.
# The early config is per machine (the fs UUID); grub.cfg stays static.
printf 'search --no-floppy --fs-uuid --set=root %s-%s\nset prefix=(%s)/grub\n' "${FS_SERIAL:0:4}" "${FS_SERIAL:4}" "\$root" > "$OUT/early.cfg"
trixie "$ROOT/usr/bin/grub-mkimage" -O i386-pc -d "$GRUBDIR" -o "$OUT/core.img" -c "$OUT/early.cfg" -p /grub \
	biosdisk part_gpt fat search search_fs_uuid normal configfile linux echo test true regexp eval \
	loadenv keystatus serial terminal halt reboot
# What grub-bios-setup does for GPT with a BIOS Boot Partition (util/setup.c,
# GRUB 2.12), minus Reed-Solomon codes. It cannot run here: it insists on
# opening the host's root device.
python3 - "$TEMPLATE" "$OUT/core.img" "$GRUBDIR/boot.img" "$EMBED_LBA" $((2 * 1024 * 1024)) <<'EOF'
import struct, sys
img, lba, room = sys.argv[1], int(sys.argv[4]), int(sys.argv[5])
core = bytearray(open(sys.argv[2], 'rb').read())
core += bytes(-len(core) % 512)
boot = open(sys.argv[3], 'rb').read()
assert len(boot) == 512 and len(core) <= room
struct.pack_into('<QH', core, 0x1f4, lba + 1, len(core) // 512 - 1)  # diskboot.img: rest of core.img
with open(img, 'r+b') as f:
    mbr = bytearray(f.read(512))
    mbr[0:3] = boot[0:3]               # jump; 0x03-0x59 (BPB) stays as on disk
    mbr[0x5a:0x1b8] = boot[0x5a:0x1b8] # code; disk signature and partition table stay
    struct.pack_into('<Q', mbr, 0x5c, lba)  # GRUB_BOOT_MACHINE_KERNEL_SECTOR
    mbr[0x66:0x68] = b'\x90\x90'       # GRUB_BOOT_MACHINE_DRIVE_CHECK, as for hard disks
    f.seek(0); f.write(mbr)
    f.seek(lba * 512); f.write(core)
EOF

# --- scenarios --------------------------------------------------------------
IMG=$OUT/disk.img
BS="$IMG@@$BS_OFF"
ACCEL=tcg
[ -w /dev/kvm ] && ACCEL=kvm
QEMU=(qemu-system-x86_64 -machine "q35,accel=$ACCEL" -m 512 -nodefaults -display none
	-serial "file:$OUT/log" -no-reboot -drive "file=$IMG,format=raw,if=virtio")

get_env() { mcopy -o -i "$BS" ::/grub/grubenv "$OUT/env"; }
put_env() { mcopy -o -i "$BS" "$OUT/env" ::/grub/grubenv; }
vars() { get_env; editenv "$OUT/env" list 2>/dev/null | grep '^VINTNER_' | LC_ALL=C sort | paste -sd ';'; }
set_env() { editenv "$OUT/env" create; editenv "$OUT/env" set "$@"; put_env; }

SCEN="" BOOTED="" OK=1 N=0
begin() {
	SCEN=$1 BOOTED="" OK=1 N=0
	cp --sparse=always "$TEMPLATE" "$IMG"
}
boot() { # [qmp]: one boot, QMP commands from stdin; appends the booted test id to BOOTED
	N=$((N + 1))
	rm -f "$OUT/log"
	if [ "${1:-}" = qmp ]; then
		timeout 120 "${QEMU[@]}" -monitor none -qmp stdio >/dev/null 2>&1 || true
	else
		timeout 300 "${QEMU[@]}" -monitor none </dev/null >/dev/null 2>&1 || true
	fi
	cp "$OUT/log" "$OUT/$SCEN-boot$N.log"
	BOOTED="$BOOTED $(grep -ao -m1 'VINTNER-TEST-BOOTED id=[A-Za-z0-9-]*' "$OUT/log" | sed 's/.*id=//' || echo NONE)"
}
expect() { # what actual expected
	[ "$2" = "$3" ] || { OK=0; echo "$SCEN: $1 '$2', expected '$3'" >&2; }
}
bless() { # host-side re-arm, as the vintner bless service will do after boot-complete.target
	local b
	b=$(vars | tr ';' '\n' | sed -n 's/^VINTNER_BOOTED=//p')
	editenv "$OUT/env" set "VINTNER_T_$b=xxx"
	editenv "$OUT/env" unset VINTNER_BOOTED
	put_env
}
fail=0
SUMMARY=$OUT/summary.tsv
printf 'scenario\tbooted\tfinal grubenv (VINTNER_*)\tresult\n' > "$SUMMARY"
end() { # [expected boots | OBSERVE]
	local result=PASS
	BOOTED=${BOOTED# }
	if [ "${1:-}" = OBSERVE ]; then result=OBSERVED
	else
		expect boots "$BOOTED" "$1"
		[ "$OK" = 1 ] || { result=FAIL; fail=1; }
	fi
	printf '%s\t%s\t%s\t%s\n' "$SCEN" "$BOOTED" "$(vars)" "$result" | tee -a "$SUMMARY"
}
O='VINTNER_ORDER=2 1'

begin B1-newest-boots
set_env "VINTNER_ORDER=2 1" VINTNER_T_2=xxx VINTNER_T_1=xxx VINTNER_T_RA=xxx
boot
expect grubenv "$(vars)" "VINTNER_BOOTED=2;$O;VINTNER_T_1=xxx;VINTNER_T_2=xx;VINTNER_T_RA=xxx"
bless
expect grubenv "$(vars)" "$O;VINTNER_T_1=xxx;VINTNER_T_2=xxx;VINTNER_T_RA=xxx"
end "os-2"

begin B2-failing-candidate
set_env "VINTNER_ORDER=2 1" VINTNER_T_2=xxx VINTNER_T_1=xxx VINTNER_T_RA=xxx
slot_cfg "$BS" os-2 os-2 reboot
boot; boot; boot; boot
expect grubenv "$(vars)" "VINTNER_BOOTED=1;$O;VINTNER_T_1=xx;VINTNER_T_2=0;VINTNER_T_RA=xxx"
end "os-2 os-2 os-2 os-1"

begin B3-good-image-starts-failing
set_env "VINTNER_ORDER=2 1" VINTNER_T_2=xxx VINTNER_T_1=xxx VINTNER_T_RA=xxx
boot; bless; boot; bless
slot_cfg "$BS" os-2 os-2 reboot
boot; boot; boot; boot
expect grubenv "$(vars)" "VINTNER_BOOTED=1;$O;VINTNER_T_1=xx;VINTNER_T_2=0;VINTNER_T_RA=xxx"
end "os-2 os-2 os-2 os-2 os-2 os-1"

begin B4-all-ordinary-bad
set_env "VINTNER_ORDER=2 1" VINTNER_T_2=0 VINTNER_T_1=0 VINTNER_T_RA=xxx
boot
expect grubenv "$(vars)" "VINTNER_BOOTED=RA;$O;VINTNER_T_1=0;VINTNER_T_2=0;VINTNER_T_RA=xx"
end "ra"

begin B5-recovery-a-bad
set_env "VINTNER_ORDER=2 1" VINTNER_T_2=0 VINTNER_T_1=0 VINTNER_T_RA=0
before=$(get_env && sha256sum < "$OUT/env")
boot
expect "grubenv sha256" "$(get_env && sha256sum < "$OUT/env")" "$before"
end "rb"

begin B6-corrupt-grubenv
head -c 1024 /dev/zero | tr '\0' '?' > "$OUT/env" && put_env
before=$(get_env && sha256sum < "$OUT/env")
boot
expect "grubenv sha256" "$(get_env && sha256sum < "$OUT/env")" "$before"
end "ra"

begin B7-missing-kernel
set_env "VINTNER_ORDER=2 1" VINTNER_T_2=xxx VINTNER_T_1=xxx VINTNER_T_RA=xxx
mdel -i "$BS" ::/vintner/bios/os-2/vmlinuz
boot
expect grubenv "$(vars)" "VINTNER_BOOTED=1;$O;VINTNER_T_1=xx;VINTNER_T_2=xx;VINTNER_T_RA=xxx"
end "os-1"

# Shift held down through the first 20 s (repeated make codes, no break code,
# as a held key's typematic repeat), then released, then Enter on the first entry.
begin B8-shift-menu
set_env "VINTNER_ORDER=2 1" VINTNER_T_2=xxx VINTNER_T_1=xxx VINTNER_T_RA=xxx
key() { # qcode down(true|false)
	printf '{"execute":"input-send-event","arguments":{"events":[{"type":"key","data":{"down":%s,"key":{"type":"qcode","data":"%s"}}}]}}\n' "$2" "$1"
}
keys() {
	echo '{"execute":"qmp_capabilities"}'
	for _ in $(seq 100); do key shift true; sleep 0.2; done
	key shift false; sleep 1; key ret true; key ret false; sleep 60
}
boot qmp < <(keys)
grep -aq 'vintner recovery B' "$OUT/B8-shift-menu-boot1.log" && menu=shown || menu="not shown"
SCEN="B8-shift-menu (menu $menu)"
end OBSERVE

# What a power cut during grub-editenv (O_TRUNC) can leave behind.
begin B9-empty-grubenv
: > "$OUT/env" && put_env
boot
expect "grubenv size" "$(get_env && stat -c %s "$OUT/env")" 0
end "ra"

# --- torn writes (host only) ------------------------------------------------
# A power cut during save_env can leave one 512-byte sector of the block old and
# the other new. load_env and grub-editenv share one parser
# (grub-core/lib/envblk.c), so grub-editenv shows what load_env reads.
block() { # file var=value...: a fresh environment block
	local f=$1
	shift
	editenv "$f" create
	editenv "$f" set "$@"
}
short() { sed -E 's/=(.{4}).{8,}(.{4})$/=\1...\2/' | paste -sd ';'; }
block "$OUT/old" GEN=old "PAYLOAD=$(printf 'a%.0s' $(seq 600))"
block "$OUT/new" GEN=new "PAYLOAD=$(printf 'b%.0s' $(seq 600))"
for c in old:new new:old; do
	{ head -c 512 "$OUT/${c%:*}"; tail -c 512 "$OUT/${c#*:}"; } > "$OUT/torn"
	printf 'T1-torn-%s\t-\t%s\tOBSERVED\n' "${c/:/-}" "$(editenv "$OUT/torn" list | short)" | tee -a "$SUMMARY"
done
# INTERFACE.md keeps every vintner variable in the first sector, so a torn
# block reads as entirely old or entirely new. Check a generous worst case:
# 10 generations with 6-digit numbers and 5 tries each. Debian's grub-editenv
# adds a 69-byte warning line to the header.
gens=$(seq -s ' ' 999990 999999)
# shellcheck disable=SC2046,SC2086 # one argument per generation
block "$OUT/max" "VINTNER_ORDER=$gens" $(printf 'VINTNER_T_%s=xxxxx ' $gens) VINTNER_T_RA=xxxxx VINTNER_BOOTED=999999
SCEN=T2-vintner-variables-in-first-sector OK=1 BOOTED=-
expect "second sector" "$(tail -c 512 "$OUT/max" | tr -d '#')" ""
r=PASS; [ "$OK" = 1 ] || { r=FAIL; fail=1; }
printf '%s\t-\t%s bytes of 1024 used\t%s\n' "$SCEN" "$(sed '$s/#*$//' "$OUT/max" | wc -c)" "$r" | tee -a "$SUMMARY"

echo "trixie packages:"; cat "$OUT/versions.txt"
exit $fail
