#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Installs the built vintner packages into a Debian 13 (trixie) chroot and checks
# what WO-0010 2.1 to 2.3 promise:
#   - vintner-core installs before and after plymouth with no dpkg prompt or error,
#     and never changes plymouth's /etc/plymouth/plymouthd.conf;
#   - the binary, hooks, unit, masks, /etc/kernel/install.conf and the stub are in place;
#   - removing the package takes the masks, hooks and unit away; purging removes
#     the conffiles;
#   - vintner-platform cannot be installed beside a stock loader package;
#   - vintner-platform is inert (no maintainer scripts, files only under
#     /usr/lib/vintner/payloads), carries a verifiable payload set, and removing
#     it removes the payloads.
# Needs: root (sudo), debootstrap, network access to a Debian mirror.
# Usage: ci/package/install-test.sh DIR   (DIR holds vintner-core_*.deb and vintner-platform_*.deb)
# shellcheck disable=SC2016 # dpkg-query expands ${Status}, not the shell
set -euo pipefail
cd "$(dirname "$0")/../.."

DEBS=${1:?usage: $0 DIR-with-debs}
DEBS=$(cd "$DEBS" && pwd)
OUT=${OUT:-ci/out/package}
MIRROR=${MIRROR:-https://deb.debian.org/debian}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
BASE=$OUT/base
FAILS=0
RESULTS=$OUT/results.tsv
printf 'check\tresult\tevidence\n' > "$RESULTS"

cleanup() {
	for d in "$OUT"/work/*; do
		[ -d "$d" ] || continue
		sudo umount "$d/proc" 2>/dev/null || true
	done
	sudo rm -rf "$OUT/work"
}
trap cleanup EXIT

record() { # name PASS|FAIL evidence
	printf '%s\t%s\t%s\n' "$1" "$2" "$3" | tee -a "$RESULTS"
	[ "$2" = PASS ] || FAILS=$((FAILS + 1))
}
check() { # name evidence command...
	local name=$1 ev=$2
	shift 2
	if "$@" >/dev/null 2>&1; then record "$name" PASS "$ev"; else record "$name" FAIL "$ev"; fi
}

# A trixie base, built once and copied per scenario.
if [ ! -f "$BASE/.done" ]; then
	sudo rm -rf "$BASE"
	sudo debootstrap --variant=minbase --include=ca-certificates trixie "$BASE" "$MIRROR" >&2
	sudo touch "$BASE/.done"
fi

scenario() { # name -> prints the chroot directory
	local d=$OUT/work/$1
	sudo rm -rf "$d"
	sudo mkdir -p "$OUT/work"
	sudo cp -a "$BASE" "$d"
	sudo mount -t proc proc "$d/proc"
	sudo cp "$DEBS"/vintner-*.deb "$d/root/"
	# No pseudo-terminal in the chroot: apt would fail to write its terminal log.
	echo 'Dpkg::Use-Pty "0";' | sudo tee "$d/etc/apt/apt.conf.d/99no-pty" >/dev/null
	echo "$d"
}
in_chroot() { # dir command...
	local d=$1
	shift
	sudo chroot "$d" env DEBIAN_FRONTEND=noninteractive LC_ALL=C "$@"
}
apt_install() { in_chroot "$1" apt-get install -y --no-install-recommends "${@:2}" 2>&1; }

cores=("$DEBS"/vintner-core_*.deb)
platforms=("$DEBS"/vintner-platform_*.deb)
core_deb=${cores[0]##*/}
platform_deb=${platforms[0]##*/}

check "arch" "vintner-core is Architecture: amd64" \
	test "$(dpkg-deb -f "$DEBS/$core_deb" Architecture)" = amd64
check "depends-ukify" "vintner-core Depends: systemd-ukify" \
	bash -c "dpkg-deb -f '$DEBS/$core_deb' Depends | grep -q systemd-ukify"
check "depends-stub" "vintner-core Depends: systemd-boot-efi (the stub vintner publish builds images with)" \
	bash -c "dpkg-deb -f '$DEBS/$core_deb' Depends | grep -q systemd-boot-efi"
check "no-plymouthd-conf" "vintner-core does not ship /etc/plymouth/plymouthd.conf" \
	bash -c "! dpkg-deb -c '$DEBS/$core_deb' | grep -q 'etc/plymouth/plymouthd.conf'"

# --- A: plymouth first, then vintner-core -----------------------------------
D=$(scenario plymouth-first)
apt_install "$D" plymouth > "$OUT/a-plymouth.log"
sum_before=$(in_chroot "$D" md5sum /etc/plymouth/plymouthd.conf)
apt_install "$D" "/root/$core_deb" > "$OUT/a-core.log" || true
if grep -qi 'Configuration file\|dpkg: error\|^E:' "$OUT/a-core.log"; then
	record "A-plymouth-then-core" FAIL "prompt or error, see a-core.log"
elif [ "$(in_chroot "$D" dpkg-query -W -f '${Status}' vintner-core)" != "install ok installed" ]; then
	record "A-plymouth-then-core" FAIL "vintner-core not installed"
else
	record "A-plymouth-then-core" PASS "installed with no conffile prompt or error"
fi
check "A-plymouthd-conf-untouched" "plymouthd.conf is still plymouth's, byte for byte" \
	test "$(in_chroot "$D" md5sum /etc/plymouth/plymouthd.conf)" = "$sum_before"

# Files, masks, owner.
check "files" "/usr/sbin/vintner, both hooks, the Plymouth hook, the unit" \
	sudo bash -c "cd '$D' && test -x usr/sbin/vintner && test -x etc/kernel/postinst.d/zz-vintner && test -x etc/initramfs/post-update.d/zz-vintner && test -x usr/share/initramfs-tools/hooks/plymouth-vintner && test -f usr/lib/systemd/system/vintner-bless.service"
check "stub-installed" "installing vintner-core brings the systemd-stub that vintner publish needs" \
	sudo test -s "$D/usr/lib/systemd/boot/efi/linuxx64.efi.stub"
check "install-conf" "/etc/kernel/install.conf is layout=other and owned by vintner-core" \
	bash -c "test \"\$(sudo cat '$D/etc/kernel/install.conf')\" = layout=other && sudo chroot '$D' dpkg -S /etc/kernel/install.conf | grep -q '^vintner-core:'"
check "mask-bless-boot" "systemd-bless-boot.service is masked (/dev/null)" \
	test "$(sudo readlink "$D/etc/systemd/system/systemd-bless-boot.service")" = /dev/null
check "mask-boot-update" "systemd-boot-update.service is masked (/dev/null)" \
	test "$(sudo readlink "$D/etc/systemd/system/systemd-boot-update.service")" = /dev/null
check "unit-enabled" "vintner-bless.service is enabled for basic.target" \
	sudo test -L "$D/etc/systemd/system/basic.target.wants/vintner-bless.service"
check "binary-static" "usr/sbin/vintner is a static executable" \
	bash -c "sudo chroot '$D' sh -c 'ldd /usr/sbin/vintner 2>&1' | grep -qi 'not a dynamic'"

# An administrator edit of plymouthd.conf (what vintner install does, 3.2) survives
# a reinstall of plymouth.
in_chroot "$D" sh -c 'printf "[Daemon]\nTheme=vintner-bgrt\nShowDelay=0\nDeviceTimeout=8\n" > /etc/plymouth/plymouthd.conf'
in_chroot "$D" apt-get install -y --reinstall plymouth > "$OUT/a-reinstall.log" 2>&1 || true
check "A-edit-survives-reinstall" "an edited plymouthd.conf survives a reinstall of plymouth, with no prompt" \
	bash -c "! grep -qi 'Configuration file' '$OUT/a-reinstall.log' && grep -q 'Theme=vintner-bgrt' '$D/etc/plymouth/plymouthd.conf'"

# Remove: masks, hooks, unit go; conffiles stay until purge.
in_chroot "$D" dpkg --remove vintner-core > "$OUT/a-remove.log" 2>&1 || true
check "remove-masks" "removing vintner-core removes both masks, the unit and the binary" \
	bash -c "sudo test ! -L '$D/etc/systemd/system/systemd-bless-boot.service' && sudo test ! -L '$D/etc/systemd/system/systemd-boot-update.service' && sudo test ! -e '$D/usr/lib/systemd/system/vintner-bless.service' && sudo test ! -e '$D/usr/sbin/vintner'"
check "remove-keeps-conffiles" "remove leaves the conffiles (hooks, install.conf) for purge" \
	sudo test -f "$D/etc/kernel/install.conf"
in_chroot "$D" dpkg --purge vintner-core > "$OUT/a-purge.log" 2>&1 || true
check "purge-conffiles" "purge removes the hooks and install.conf" \
	bash -c "sudo test ! -e '$D/etc/kernel/install.conf' && sudo test ! -e '$D/etc/kernel/postinst.d/zz-vintner' && sudo test ! -e '$D/etc/initramfs/post-update.d/zz-vintner'"
check "purge-leaves-plymouth" "plymouth and its conffile are intact after the purge" \
	bash -c "sudo test -f '$D/etc/plymouth/plymouthd.conf' && sudo chroot '$D' dpkg-query -W -f '\${Status}' plymouth | grep -q 'install ok installed'"
sudo umount "$D/proc"

# --- B: vintner-core first, then plymouth -----------------------------------
D=$(scenario core-first)
apt_install "$D" "/root/$core_deb" > "$OUT/b-core.log" || true
apt_install "$D" plymouth > "$OUT/b-plymouth.log" || true
if grep -qi 'Configuration file\|dpkg: error\|^E:' "$OUT/b-core.log" "$OUT/b-plymouth.log"; then
	record "B-core-then-plymouth" FAIL "prompt or error, see b-core.log and b-plymouth.log"
elif [ "$(in_chroot "$D" dpkg-query -W -f '${Status}' plymouth)" != "install ok installed" ]; then
	record "B-core-then-plymouth" FAIL "plymouth not installed"
else
	record "B-core-then-plymouth" PASS "plymouth installed after vintner-core with no prompt or error"
fi
check "B-plymouthd-conf-is-plymouths" "plymouthd.conf is plymouth's own after installing it second" \
	bash -c "sudo chroot '$D' dpkg -V plymouth | grep -q plymouthd.conf && exit 1 || exit 0"
sudo umount "$D/proc"

# --- C: vintner-platform conflicts with the stock loaders -------------------
D=$(scenario platform-conflicts)
apt_install "$D" "/root/$core_deb" "/root/$platform_deb" > "$OUT/c-platform.log" || true
for pkg in systemd-boot grub-pc grub-efi-amd64 shim-signed; do
	# apt resolves a Conflicts by removing the installed package, so the two
	# cannot stay together: the simulation must remove vintner-platform.
	in_chroot "$D" apt-get install -s "$pkg" > "$OUT/c-$pkg.log" 2>&1 || true
	if grep -q '^Remv vintner-platform' "$OUT/c-$pkg.log"; then
		record "C-conflicts-$pkg" PASS "installing $pkg would remove vintner-platform"
	else
		record "C-conflicts-$pkg" FAIL "$pkg can be installed beside vintner-platform, see c-$pkg.log"
	fi
done
sudo umount "$D/proc"

# --- D: vintner-platform is inert and carries the payloads ------------------
# Inert: no maintainer script at all, so installing, upgrading or removing it can
# only write the files it ships, and those lie under /usr/lib/vintner/payloads
# and /usr/share/doc (and /usr/share/lintian). Nothing can reach the ESP, boot storage or NVRAM.
check "D-arch" "vintner-platform is Architecture: amd64 (x86 loaders and GRUB i386-pc objects)" \
	test "$(dpkg-deb -f "$DEBS/$platform_deb" Architecture)" = amd64
# The control archive of an inert package holds control and md5sums, nothing that dpkg runs.
platform_ctl=$OUT/platform-control
rm -rf "$platform_ctl"
dpkg-deb -e "$DEBS/$platform_deb" "$platform_ctl"
check "D-no-maintainer-scripts" "the control archive of vintner-platform is control and md5sums only (no maintainer script, trigger or conffile)" \
	test "$(find "$platform_ctl" -type f -printf '%f\n' | sort | paste -sd' ')" = "control md5sums"
# Every path, directories included: the root and the parents of the payload directory
# are the only ones outside it.
platform_paths=$(dpkg-deb --fsys-tarfile "$DEBS/$platform_deb" | tar -t)
stray=$(printf '%s\n' "$platform_paths" | grep -vE '^\./($|usr/$|usr/lib/$|usr/lib/vintner/|usr/share/$|usr/share/doc/|usr/share/lintian/)' || true)
if [ -z "$stray" ]; then
	record "D-paths" PASS "every path in vintner-platform is under /usr/lib/vintner, /usr/share/doc or /usr/share/lintian"
else
	record "D-paths" FAIL "paths outside the payload directory: $(printf '%s' "$stray" | head -3 | paste -sd' ')"
fi
D=$(scenario platform)
apt_install "$D" "/root/$core_deb" "/root/$platform_deb" > "$OUT/d-platform.log" || true
if [ "$(in_chroot "$D" dpkg-query -W -f '${Status}' vintner-platform)" = "install ok installed" ] &&
	! grep -qi 'Configuration file\|dpkg: error\|^E:' "$OUT/d-platform.log"; then
	record "D-install" PASS "vintner-platform installs beside vintner-core with no prompt or error"
else
	record "D-install" FAIL "see d-platform.log"
fi
check "D-payload-files" "systemd-boot, stub, loader.conf, boot.img, modules, grub.cfg, recovery kernel and initramfs are in place" \
	sudo bash -c "cd '$D/usr/lib/vintner/payloads' && test -s uefi/systemd-bootx64.efi && test -s uefi/linuxx64.efi.stub && test -s uefi/loader.conf && test -s bios/i386-pc/boot.img && test -s bios/i386-pc/kernel.img && test -s bios/i386-pc/moddep.lst && test -s bios/grub.cfg && test -s bios/core-modules.txt && test -s recovery/vmlinuz && test -s recovery/initrd.img && test -s recovery/version && test -s VERSIONS"
check "D-core-modules-present" "every module in core-modules.txt exists in the shipped i386-pc directory" \
	sudo bash -c "cd '$D/usr/lib/vintner/payloads/bios' && for m in \$(cat core-modules.txt); do test -s i386-pc/\$m.mod || exit 1; done"
check "D-manifest" "MANIFEST lists every payload file and sha256sum -c passes" \
	sudo bash -c "cd '$D/usr/lib/vintner/payloads' && sha256sum --quiet -c MANIFEST && test \"\$(find . -type f ! -name MANIFEST | wc -l)\" = \"\$(wc -l < MANIFEST)\""
check "D-recovery-initrd-has-vintner" "the shipped recovery initramfs carries /init and the vintner binary" \
	sudo bash -c "cd '$D/usr/lib/vintner/payloads/recovery' && xz -dc initrd.img | cpio -it --quiet 2>/dev/null | grep -qx 'usr/sbin/vintner' && xz -dc initrd.img | cpio -it --quiet 2>/dev/null | grep -qx 'init'"
# The module set is enough: grub-mkimage, reading only the shipped directory, builds
# a core.img from core-modules.txt with an early config of the kind the installer
# writes. grub-common is not in Conflicts, and nothing else is installed for it.
apt_install "$D" grub-common > "$OUT/d-grub-common.log" || true
check "D-core-img-builds" "grub-mkimage builds a core.img from the shipped files and core-modules.txt alone" \
	sudo chroot "$D" sh -c 'cd /usr/lib/vintner/payloads/bios && printf "search --no-floppy --fs-uuid --set=root 0000-0000\nset prefix=(\$root)/grub\n" > /tmp/early.cfg && grub-mkimage -O i386-pc -d i386-pc -p /grub -c /tmp/early.cfg -o /tmp/core.img $(cat core-modules.txt) && test -s /tmp/core.img'
check "D-no-grub-programs" "none of GRUB's own programs (grub-bios-setup, grub-ntldr-img) is shipped" \
	sudo bash -c "cd '$D/usr/lib/vintner/payloads' && ! find . -name 'grub-*' | grep -q ."
# Remove: the payloads go, and nothing else is touched.
in_chroot "$D" dpkg --remove vintner-platform > "$OUT/d-remove.log" 2>&1 || true
check "D-remove" "removing vintner-platform removes the payload directory" \
	sudo test ! -e "$D/usr/lib/vintner/payloads"
check "D-remove-keeps-core" "removing vintner-platform leaves vintner-core installed" \
	bash -c "test \"\$(sudo chroot '$D' dpkg-query -W -f '\${Status}' vintner-core)\" = 'install ok installed'"
sudo umount "$D/proc"

# --- E: vintner-platform recommends grub-common ------------------------------
# A BIOS install needs grub-mkimage (vintner install builds core.img with it), so
# apt's default, with recommended packages, brings grub-common. That must not bring
# update-grub or its kernel hooks: they are in grub2-common (architecture §5).
check "E-recommends" "vintner-platform Recommends: grub-common" \
	bash -c "dpkg-deb -f '$DEBS/$platform_deb' Recommends | tr ',' '\n' | grep -qx ' *grub-common *'"
D=$(scenario recommends)
in_chroot "$D" apt-get install -y "/root/$platform_deb" > "$OUT/e-recommends.log" 2>&1 || true
check "E-grub-mkimage" "installing vintner-platform the default way (with recommended packages) brings grub-mkimage" \
	sudo chroot "$D" sh -c 'command -v grub-mkimage'
check "E-no-update-grub" "it brings no update-grub, no zz-update-grub kernel hook and no grub2-common" \
	sudo chroot "$D" sh -c '! command -v update-grub && ! command -v grub-install && ! ls /etc/kernel/*/zz-update-grub 2>/dev/null | grep -q . && ! dpkg-query -W -f "\${Status}" grub2-common 2>/dev/null | grep -q "ok installed"'
sudo umount "$D/proc"

echo
if [ "$FAILS" -ne 0 ]; then
	echo "FAIL: $FAILS check(s) failed, see $RESULTS" >&2
	exit 1
fi
echo "all package checks passed ($RESULTS)"
