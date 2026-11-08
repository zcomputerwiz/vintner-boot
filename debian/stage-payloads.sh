#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Stages the inert payloads of vintner-platform (WO-0010 2.4) into
# debian/build/payloads/, which debian/vintner-platform.install installs under
# /usr/lib/vintner/payloads/. Run from the source root by debian/rules.
#
# Nothing here is executed on the target. The loader binaries and GRUB files come
# from the Debian packages named in Build-Depends (systemd-boot-efi, grub-pc-bin)
# as installed on the build host. The recovery kernel and initramfs come from
# recovery/build.sh, which downloads the Debian
# trixie packages it unpacks (see docs/packaging.md); set VINTNER_RECOVERY_DIR to
# a directory that already holds vmlinuz, initrd.img and versions.txt to use
# those instead.
set -eu

P=debian/build/payloads
rm -rf "$P"
mkdir -p "$P/uefi" "$P/bios" "$P/recovery"

SDB=/usr/lib/systemd/boot/efi
GRUBDIR=/usr/lib/grub/i386-pc
for f in "$SDB/systemd-bootx64.efi" "$SDB/linuxx64.efi.stub" "$GRUBDIR/boot.img" "$GRUBDIR/diskboot.img" "$GRUBDIR/kernel.img" "$GRUBDIR/lzma_decompress.img" "$GRUBDIR/moddep.lst"; do
	[ -f "$f" ] || { echo "stage-payloads: $f not found (Build-Depends: systemd-boot-efi, grub-pc-bin)" >&2; exit 1; }
done

# UEFI: the loader, the stub that ukify puts in front of a kernel, and our loader.conf.
cp "$SDB/systemd-bootx64.efi" "$SDB/linuxx64.efi.stub" "$P/uefi/"
cp firmware/uefi/loader.conf "$P/uefi/"

# BIOS: what `grub-mkimage -d` reads to build a core.img whose early config names
# the boot storage UUID (unknowable here): the three images core.img is made of,
# boot.img for the boot sector, moddep.lst and the modules; plus the static
# grub.cfg and the module list the core image is built with. None of GRUB's own
# programs (grub-bios-setup and friends) is shipped.
mkdir -p "$P/bios/i386-pc"
for f in boot.img diskboot.img kernel.img lzma_decompress.img moddep.lst; do
	cp "$GRUBDIR/$f" "$P/bios/i386-pc/"
done
cp "$GRUBDIR"/*.mod "$P/bios/i386-pc/"
cp firmware/bios/grub.cfg firmware/bios/core-modules.txt "$P/bios/"

# Recovery: the kernel and initramfs. The UEFI recovery UKIs are assembled from
# these and the stub above at deploy time, because tier A signs them with the
# owner's key; the BIOS slots use the kernel and initramfs as they are.
if [ -n "${VINTNER_RECOVERY_DIR:-}" ]; then
	R=$VINTNER_RECOVERY_DIR
else
	bash recovery/build.sh --no-uki
	R=recovery/out
fi
for f in vmlinuz initrd.img versions.txt; do
	[ -f "$R/$f" ] || { echo "stage-payloads: $R/$f not found" >&2; exit 1; }
	cp "$R/$f" "$P/recovery/"
done

{
	for pkg in systemd-boot-efi grub-pc-bin; do
		echo "$pkg $(dpkg-query -W -f='${Version}' "$pkg")"
	done
	echo "recovery:"
	sed 's/^/  /' "$P/recovery/versions.txt"
} > "$P/VERSIONS"

# What "deploy and verify" checks against.
(cd "$P" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) > "$P.manifest"
mv "$P.manifest" "$P/MANIFEST"
echo "stage-payloads: $(wc -l < "$P/MANIFEST") files, $(du -sh "$P" | cut -f1)"
