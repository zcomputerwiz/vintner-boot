#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Checks that this Linux machine can run the vintner-boot tests: the Go unit
# tests, shellcheck, and the QEMU suites of .github/workflows/qemu-tests.yml.
# Written for a Debian 13 WSL2 distribution (docs/local-testing.md); a native
# Debian 13 workstation passes the same checks.
#
# Usage: ci/local/check-env.sh [--loop]
#   --loop  also attach a scratch image with "losetup -P" and check that its
#           partition appears, as the suites do (needs sudo).
#
# Exits 1 if something required is missing. Warnings do not fail.
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

loop_check=no
case "${1:-}" in
	"") ;;
	--loop) loop_check=yes ;;
	*) echo "usage: $0 [--loop]" >&2; exit 2 ;;
esac

failed=0
ok() { printf 'ok    %s\n' "$*"; }
warn() { printf 'WARN  %s\n' "$*"; }
bad() { printf 'FAIL  %s\n' "$*"; failed=1; }

# --- Host ---------------------------------------------------------------------
if [ "$(uname -s)" != Linux ]; then
	bad "not Linux ($(uname -s)): run this inside WSL or on a Linux workstation"
	exit 1
fi
if grep -qi microsoft /proc/sys/kernel/osrelease 2>/dev/null; then
	ok "WSL2 kernel $(uname -r)"
else
	ok "Linux kernel $(uname -r)"
fi
if [ -r /etc/debian_version ]; then
	ok "Debian $(cat /etc/debian_version)"
else
	warn "not Debian: the package names below are Debian's (CI uses Ubuntu 24.04)"
fi
if [ "$(id -u)" = 0 ]; then
	warn "running as root: run the tests as your own user (they call sudo where needed), and never commit as root"
fi
case "$PWD" in
	/mnt/[a-z]/*)
		warn "this checkout is on a Windows drive ($PWD): the QEMU suites are slow there and loop devices on drvfs files are unreliable; run them from a clone in the Linux filesystem (docs/local-testing.md)"
		;;
	*) ok "checkout in the Linux filesystem: $PWD" ;;
esac

# --- Commands, by the Debian package that provides them -----------------------
# The union of what .github/workflows/lint.yml and qemu-tests.yml install.
missing_pkgs=()
for entry in \
	go:golang-go shellcheck:shellcheck curl:curl \
	qemu-system-x86_64:qemu-system-x86 qemu-img:qemu-utils \
	mtype:mtools mcopy:mtools mdir:mtools sfdisk:fdisk losetup:mount \
	mkfs.ext4:e2fsprogs debugfs:e2fsprogs modprobe:kmod cpio:cpio xz:xz-utils \
	objcopy:binutils cloud-localds:cloud-image-utils \
	sbsign:sbsigntool sbverify:sbsigntool ukify:systemd-ukify \
	grub-editenv:grub-common dpkg-deb:dpkg python3:python3 sudo:sudo \
	debootstrap:debootstrap mkfs.vfat:dosfstools; do
	cmd=${entry%%:*} pkg=${entry#*:}
	if command -v "$cmd" >/dev/null 2>&1; then
		ok "$cmd"
	else
		bad "$cmd missing (package $pkg)"
		missing_pkgs+=("$pkg")
	fi
done
for entry in pefile:python3-pefile virt.firmware:python3-virt-firmware; do
	mod=${entry%%:*} pkg=${entry#*:}
	if python3 -c "import $mod" >/dev/null 2>&1; then
		ok "python3 module $mod"
	else
		bad "python3 module $mod missing (package $pkg)"
		missing_pkgs+=("$pkg")
	fi
done
for entry in dpkg-buildpackage:dpkg-dev dh:debhelper lintian:lintian; do
	cmd=${entry%%:*} pkg=${entry#*:}
	if command -v "$cmd" >/dev/null 2>&1; then
		ok "$cmd"
	else
		warn "$cmd missing (package $pkg): needed only to build the .deb as lint.yml does"
	fi
done

# --- Firmware and stubs the suites read from the host -------------------------
for entry in \
	/usr/share/OVMF/OVMF_CODE_4M.fd:ovmf /usr/share/OVMF/OVMF_VARS_4M.fd:ovmf \
	/usr/share/OVMF/OVMF_CODE_4M.secboot.fd:ovmf /usr/share/OVMF/OVMF_VARS_4M.ms.fd:ovmf \
	/usr/share/seabios/bios-256k.bin:seabios \
	/usr/lib/systemd/boot/efi/linuxx64.efi.stub:systemd-boot-efi \
	/usr/lib/systemd/boot/efi/systemd-bootx64.efi:systemd-boot-efi; do
	f=${entry%%:*} pkg=${entry#*:}
	if [ -f "$f" ]; then
		ok "$f"
	else
		bad "$f missing (package $pkg)"
		missing_pkgs+=("$pkg")
	fi
done
if [ ${#missing_pkgs[@]} -gt 0 ]; then
	printf 'Install with:\n  sudo apt-get install -y %s\n' "$(printf '%s\n' "${missing_pkgs[@]}" | sort -u | paste -sd' ')"
fi

# --- Go version against src/go.mod --------------------------------------------
if command -v go >/dev/null 2>&1; then
	want=$(sed -n 's/^go \([0-9.]*\)$/\1/p' src/go.mod)
	have=$(go env GOVERSION | sed 's/^go//')
	if [ -n "$want" ] && [ "$(printf '%s\n%s\n' "$want" "$have" | sort -V | head -n 1)" = "$want" ]; then
		ok "go $have (src/go.mod wants $want)"
	else
		bad "go $have is older than src/go.mod's go $want"
	fi
fi

# --- KVM, sudo, kernel filesystems --------------------------------------------
if [ ! -e /dev/kvm ]; then
	bad "/dev/kvm missing: enable nested virtualization for WSL2 (docs/local-testing.md); without it the boots run under TCG and the suites' timeouts expire"
elif [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
	ok "/dev/kvm usable"
else
	bad "/dev/kvm not usable by $(id -un): sudo usermod -aG kvm $(id -un), then restart WSL (wsl --shutdown)"
fi
if sudo -n true 2>/dev/null; then
	ok "sudo without a password"
	have_sudo=yes
else
	warn "sudo asks for a password: the suites call sudo mid-run (losetup, mount), so run 'sudo -v' just before starting one"
	have_sudo=no
fi
for fs in vfat ext4; do
	if grep -qw "$fs" /proc/filesystems || modinfo "$fs" >/dev/null 2>&1; then
		ok "kernel filesystem $fs"
	else
		bad "kernel has no $fs support"
	fi
done

# --- Git identity ---------------------------------------------------------------
name=$(git config user.name || true)
email=$(git config user.email || true)
if [ -n "$name" ] && [ -n "$email" ]; then
	ok "git identity: $name <$email>"
else
	warn "no git user.name/user.email here: commits made from this checkout would be signed $(id -un)@$(hostname -f 2>/dev/null || hostname); set them with git config --global"
fi

# --- Disk space -----------------------------------------------------------------
mkdir -p ci/out
avail_kb=$(df -Pk ci/out | awk 'NR == 2 {print $4}')
if [ "$avail_kb" -ge $((40 * 1024 * 1024)) ]; then
	ok "$((avail_kb / 1024 / 1024)) GiB free for ci/out"
else
	warn "only $((avail_kb / 1024 / 1024)) GiB free for ci/out: one QEMU suite can write 10 to 30 GiB of disk images"
fi

# --- Optional: loop device with partition scanning ------------------------------
if [ "$loop_check" = yes ]; then
	if [ "$have_sudo" = no ] && ! sudo -v; then
		bad "loop check needs sudo"
	else
		img=$(mktemp -p ci/out check-env-loop.XXXXXX)
		dev=""
		truncate -s 8M "$img"
		echo 'label: gpt
start=2048, size=8192, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4' | sfdisk -q "$img"
		if dev=$(sudo losetup -P -f --show "$img"); then
			for _ in 1 2 3 4 5; do [ -b "${dev}p1" ] && break; sleep 0.2; done
			if [ -b "${dev}p1" ]; then
				ok "losetup -P: ${dev}p1 appeared"
			else
				bad "losetup -P attached $dev but ${dev}p1 did not appear"
			fi
			sudo losetup -d "$dev"
		else
			bad "losetup -P failed"
		fi
		rm -f "$img"
	fi
fi

if [ "$failed" = 0 ]; then
	echo "Environment ready for the vintner-boot tests."
else
	echo "Environment incomplete: fix the FAIL lines above." >&2
fi
exit "$failed"
