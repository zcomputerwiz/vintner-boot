#!/bin/bash
# Builds the minimal vintner recovery image (WP-006 phase 1) from Debian trixie
# packages, independent of any installed system (architecture §4, R06):
# recovery/out/{vmlinuz,initrd.img,vintner-recovery-a.efi,vintner-recovery-b.efi}.
# Needs: curl, xz, dpkg-deb, cpio, kmod (depmod, modprobe), binutils (objdump),
# python3 with pefile. No root.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=recovery/out
ROOT=$OUT/root
INITRD=$OUT/initrd
MIRROR=https://deb.debian.org/debian
mkdir -p "$OUT/debs" "$ROOT"
: > "$OUT/versions.txt"

[ -f "$OUT/Packages" ] || curl -sSfL "$MIRROR/dists/trixie/main/binary-amd64/Packages.xz" | xz -dc > "$OUT/Packages"
field() { awk -v p="$1" -v f="$2:" '$1=="Package:"{cur=$2} cur==p && $1==f{sub(/^[^ ]+ /,""); print; exit}' "$OUT/Packages"; }
fetch() {
	local fn
	fn=$(field "$1" Filename)
	[ -n "$fn" ] || { echo "no $1 in trixie" >&2; exit 1; }
	[ -f "$OUT/debs/${fn##*/}" ] || curl -sSfL "$MIRROR/$fn" -o "$OUT/debs/${fn##*/}"
	dpkg-deb -x "$OUT/debs/${fn##*/}" "$ROOT"
	echo "$1 $(field "$1" Version)" >> "$OUT/versions.txt"
}
kernel_pkg=$(field linux-image-amd64 Depends | sed 's/ .*//; s/,$//')
while read -r p; do fetch "${p/@KERNEL@/$kernel_pkg}"; done < <(grep -v '^#' recovery/packages.list)
ABI=$(basename "$(ls -d "$ROOT"/usr/lib/modules/*)")
[ -e "$ROOT/lib" ] || ln -s usr/lib "$ROOT/lib"  # depmod looks in <base>/lib/modules
depmod -b "$ROOT" "$ABI"

# Initramfs: static busybox, the init script and the dependency closure of the
# module list, decompressed so busybox modprobe can load them.
rm -rf "$INITRD" && mkdir -p "$INITRD"/{bin,proc,sys,dev,run,mnt,etc/vintner,lib/modules/"$ABI"}
cp "$ROOT/usr/bin/busybox" "$INITRD/bin/busybox"
# Storage tools with their shared-library closure and trixie's own loader.
mkdir -p "$INITRD/usr/sbin" "$INITRD/usr/lib/x86_64-linux-gnu" "$INITRD/lib64"
cp "$ROOT/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2" "$INITRD/lib64/"
libs=()
for t in cryptsetup lvm mdadm e2fsck mke2fs; do
	cp "$ROOT/usr/sbin/$t" "$INITRD/usr/sbin/$t"
	libs+=("$INITRD/usr/sbin/$t")
done
while [ ${#libs[@]} -gt 0 ]; do
	f=${libs[0]} libs=("${libs[@]:1}")
	for l in $(objdump -p "$f" | awk '$1=="NEEDED"{print $2}'); do
		[ -e "$INITRD/usr/lib/x86_64-linux-gnu/$l" ] && continue
		cp -L "$ROOT/usr/lib/x86_64-linux-gnu/$l" "$INITRD/usr/lib/x86_64-linux-gnu/$l" || { echo "missing $l (needed by $f)" >&2; exit 1; }
		libs+=("$INITRD/usr/lib/x86_64-linux-gnu/$l")
	done
done
install -m 0755 recovery/initramfs/init "$INITRD/init"
VERSION=$(date -u +%Y%m%d)-$(git rev-parse --short HEAD)
echo "$VERSION" > "$INITRD/etc/vintner/version"
grep -v '^#' recovery/initramfs/modules.list > "$INITRD/etc/vintner/modules"
cp "$ROOT/usr/lib/modules/$ABI"/modules.{order,builtin,builtin.modinfo} "$INITRD/lib/modules/$ABI/"
while read -r m; do
	modprobe -d "$ROOT" -S "$ABI" --show-depends "$m" 2>/dev/null | awk '$1=="insmod"{print $2}'
done < "$INITRD/etc/vintner/modules" | sort -u | while read -r ko; do
	rel=${ko#*/lib/modules/"$ABI"/} # modprobe prints absolute paths
	mkdir -p "$INITRD/lib/modules/$ABI/$(dirname "$rel")"
	xz -dc "$ko" > "$INITRD/lib/modules/$ABI/${rel%.xz}"
done
depmod -b "$INITRD" "$ABI"
echo "modules: $(find "$INITRD/lib/modules" -name '*.ko' | wc -l), $(du -sh "$INITRD/lib/modules" | cut -f1)" >> "$OUT/versions.txt"
(cd "$INITRD" && find . | LC_ALL=C sort | cpio -o -H newc --owner=0:0 --quiet | xz -9 --check=crc32 -T1) > "$OUT/initrd.img" # the kernel needs crc32 checks
cp "$ROOT/boot/vmlinuz-$ABI" "$OUT/vmlinuz"

# UKIs per ADR-0003: IMAGE_ID sorts recovery A above B; recovery B never carries a counter.
for slot in a b; do
	S=${slot^^}
	printf 'ID=debian\nIMAGE_ID=vintner-recovery-%s\nIMAGE_VERSION=%s\nPRETTY_NAME="vintner recovery %s (%s)"\n' \
		"$slot" "$VERSION" "$S" "$VERSION" > "$OUT/osrel-$slot"
	python3 "$ROOT/usr/lib/systemd/ukify" build --linux="$OUT/vmlinuz" --initrd="$OUT/initrd.img" \
		--stub="$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub" --os-release="@$OUT/osrel-$slot" \
		--cmdline="console=tty0 console=ttyS0,115200 vintner.recovery=1" \
		--output="$OUT/vintner-recovery-$slot.efi" >/dev/null 2>&1
done

cat "$OUT/versions.txt"
stat -c '%n %s' "$OUT"/vmlinuz "$OUT"/initrd.img "$OUT"/vintner-recovery-*.efi | awk '{printf "%-40s %6.1f MiB\n", $1, $2/1048576}'
