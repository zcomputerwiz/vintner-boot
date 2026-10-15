#!/bin/bash
# Proves on Debian 13's own systemd-boot binary how vintner UKI entries are
# ordered, counted and selected (architecture §4.1 and §10, ADR-0003).
# Needs: curl, xz, dpkg-deb, cpio, gzip, sfdisk, mtools, python3 with pefile,
# python3-virt-firmware, qemu-system-x86_64 and the OVMF 4M firmware. No root.
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=ci/out/selection-policy
ROOT=$OUT/root
MIRROR=https://deb.debian.org/debian
CODE=/usr/share/OVMF/OVMF_CODE_4M.fd
VARS=/usr/share/OVMF/OVMF_VARS_4M.fd
LOADER_GUID=4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
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
for p in systemd-boot-efi systemd-ukify busybox-static "$kernel_pkg"; do fetch "$p"; done
KERNEL=$(ls "$ROOT"/boot/vmlinuz-*)
STUB=$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub
SDBOOT=$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi
first() { local f; for f; do [ -e "$f" ] && { echo "$f"; return; }; done; return 1; }
UKIFY=$(first "$ROOT"/usr/lib/systemd/ukify "$ROOT"/usr/bin/ukify)
BUSYBOX=$(first "$ROOT"/usr/bin/busybox "$ROOT"/bin/busybox)

# --- test initramfs: print the test id, then power off or reboot -----------
rm -rf "$OUT/initrd" && mkdir -p "$OUT/initrd"/{bin,proc,dev}
cp "$BUSYBOX" "$OUT/initrd/bin/busybox"
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

# --- UKIs, with .osrel per ADR-0003 ----------------------------------------
uki() { # name test_id action image_id image_version
	local osrel=$OUT/osrel-$1
	if [ -n "$4" ]; then
		printf 'ID=debian\nIMAGE_ID=%s\nIMAGE_VERSION=%s\nPRETTY_NAME="%s"\n' "$4" "$5" "$1" > "$osrel"
	else
		printf 'ID=debian\nVERSION_ID=13\nPRETTY_NAME="Foreign Debian"\n' > "$osrel"
	fi
	python3 "$UKIFY" build --linux="$KERNEL" --initrd="$OUT/initrd.img" \
		--stub="$STUB" --os-release="@$osrel" --output="$OUT/uki-$1.efi" \
		--cmdline="console=ttyS0 panic=-1 vintner.test_id=$2 vintner.test_action=$3" >/dev/null
}
uki os-1 os-1 poweroff vintner-os 1
uki os-1-fail os-1 reboot vintner-os 1
uki os-2 os-2 poweroff vintner-os 2
uki os-2-fail os-2 reboot vintner-os 2
uki ra ra poweroff vintner-recovery-a 20261015
uki rb rb poweroff vintner-recovery-b 20261015
uki foreign foreign poweroff "" ""

# --- disk: GPT with ESP (loader) and XBOOTLDR (UKIs) -----------------------
IMG=$OUT/disk.img
new_disk() { # "uki-src:name" ... (names land in /EFI/Linux on XBOOTLDR)
	rm -f "$IMG" && truncate -s 400M "$IMG"
	printf 'label: gpt\nsize=64MiB, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B\ntype=BC13C2FF-59E6-4262-A352-B275FD6F7172\n' | sfdisk -q "$IMG"
	ESP="$IMG@@$(( $(sfdisk -J "$IMG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["partitiontable"]["partitions"][0]["start"])') * 512 ))"
	XBL="$IMG@@$(( $(sfdisk -J "$IMG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["partitiontable"]["partitions"][1]["start"])') * 512 ))"
	mformat -i "$ESP" -F -v ESP ::
	mformat -i "$XBL" -F -v XBOOTLDR ::
	mmd -i "$ESP" ::/EFI ::/EFI/BOOT ::/EFI/vintner ::/EFI/Linux ::/loader ::/loader/entries
	mmd -i "$XBL" ::/EFI ::/EFI/Linux
	mcopy -i "$ESP" "$SDBOOT" ::/EFI/BOOT/BOOTX64.EFI
	mcopy -i "$ESP" "$SDBOOT" ::/EFI/vintner/systemd-bootx64.efi
	mcopy -i "$ESP" firmware/uefi/loader.conf ::/loader/loader.conf
	local e
	for e in "$@"; do mcopy -i "$XBL" "$OUT/uki-${e%%:*}.efi" "::/EFI/Linux/${e#*:}"; done
}
listing() { mdir -b -i "$XBL" ::/EFI/Linux | sed 's|.*/||' | sort | tr '\n' ' '; }

new_vars() { # [name value]...: LoaderInfo string variables (UTF-16LE, NUL-terminated)
	cp "$VARS" "$OUT/vars.fd" && chmod u+w "$OUT/vars.fd"
	[ $# -gt 0 ] || return 0
	python3 - "$LOADER_GUID" "$@" > "$OUT/vars.json" <<'EOF'
import json, sys
guid, kv = sys.argv[1], sys.argv[2:]
print(json.dumps({"version": 2, "variables": [
    {"name": kv[i], "guid": guid, "attr": 7, "data": (kv[i + 1] + "\0").encode("utf-16-le").hex()}
    for i in range(0, len(kv), 2)]}))
EOF
	virt-fw-vars -i "$OUT/vars.fd" -o "$OUT/vars.fd" --set-json "$OUT/vars.json" >/dev/null
}

ACCEL=tcg
[ -w /dev/kvm ] && ACCEL=kvm
boot() { # one boot; prints the test id that booted, or NONE
	local log=$OUT/$SCEN-boot$1.log
	timeout 300 qemu-system-x86_64 -machine "q35,accel=$ACCEL" -m 512 -nodefaults -display none \
		-monitor none -serial "file:$log" -no-reboot \
		-drive "if=pflash,format=raw,unit=0,file=$CODE,readonly=on" \
		-drive "if=pflash,format=raw,unit=1,file=$OUT/vars.fd" \
		-drive "file=$IMG,format=raw,if=virtio" </dev/null >/dev/null 2>&1 || true
	grep -ao -m1 'VINTNER-TEST-BOOTED id=[A-Za-z0-9-]*' "$log" | sed 's/.*id=//' || echo NONE
}

fail=0
SUMMARY=$OUT/summary.tsv
printf 'scenario\tbooted\tfinal /EFI/Linux on XBOOTLDR\tresult\n' > "$SUMMARY"
scenario() { # name expected-ids(space separated) [OBSERVE]
	SCEN=$1
	local got="" i=1
	for _ in $2; do got="$got $(boot $i)"; i=$((i + 1)); done
	got=${got# }
	local result=PASS
	if [ "${3:-}" = OBSERVE ]; then result=OBSERVED
	elif [ "$got" != "$2" ]; then result=FAIL; fail=1; fi
	printf '%s\t%s\t%s\t%s\n' "$SCEN" "$got" "$(listing)" "$result" | tee -a "$SUMMARY"
}
S1=(os-2:vintner-os-2+3.efi os-1:vintner-os-1.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi)

new_disk "${S1[@]}"; new_vars
scenario S1-order "os-2"
new_disk os-2-fail:vintner-os-2+1-2.efi os-1:vintner-os-1.efi; new_vars
scenario S2-failing-candidate "os-2 os-1"
new_disk os-2:vintner-os-2+0-3.efi os-1:vintner-os-1+0-3.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi; new_vars
scenario S3-all-ordinary-bad "ra"
new_disk os-2:vintner-os-2+0-3.efi os-1:vintner-os-1+0-3.efi ra:vintner-recovery-a+0-3.efi rb:vintner-recovery-b.efi; new_vars
scenario S4-recovery-a-bad "rb"
new_disk "${S1[@]}" foreign:debian-6.12.efi; new_vars
mcopy -i "$ESP" "$OUT/uki-foreign.efi" ::/EFI/Linux/debian-6.12.efi
printf 'title Foreign\nefi /EFI/Linux/debian-6.12.efi\n' > "$OUT/aaa.conf" && mcopy -i "$ESP" "$OUT/aaa.conf" ::/loader/entries/aaa.conf
scenario S5-foreign-entries "os-2"
new_disk os-1-fail:vintner-os-1+3.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi; new_vars
scenario S6-good-image-fails-repeatedly "os-1 os-1 os-1 ra"
new_disk "${S1[@]}" foreign:debian-6.12.efi; new_vars LoaderEntryDefault debian-6.12.efi
scenario S7-LoaderEntryDefault-overrides "foreign"
new_disk "${S1[@]}"; new_vars LoaderEntryOneShot 'vintner-recovery-*'
scenario S8-one-shot-recovery "ra os-2"
new_disk os-2:vintner-os-2+3.efi os-1:vintner-os-1.efi ra:vintner-recovery-a+0-3.efi rb:vintner-recovery-b.efi; new_vars LoaderEntryOneShot 'vintner-recovery-*'
scenario S9-one-shot-recovery-a-bad "rb"
head -c 4096 "$OUT/uki-os-2.efi" > "$OUT/uki-trunc.efi"
new_disk trunc:vintner-os-2+3.efi os-1:vintner-os-1.efi; new_vars
scenario S10-image-fails-to-load "x" OBSERVE

echo "trixie packages:"; cat "$OUT/versions.txt"
exit $fail
