#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
#
# VirtualBox dry run of the real-hardware checks (ci/hardware/README.md):
# boots ci/out/hardware/<uefi|bios>.img once in a VirtualBox VM, then reads the
# disk back and checks what the firmware and loader wrote:
#   uefi (B1, B2): serial shows os-2 booted; /EFI/Linux has vintner-os-2+2-1.efi
#   bios (B3, B4): serial shows os-2 booted; grubenv has VINTNER_BOOTED=2 and
#                  VINTNER_T_2=xx
# VirtualBox's firmware is one more implementation, still virtual: these runs
# check the images and the procedure, not real hardware.
#
# Runs in WSL on the Windows machine that has VirtualBox, and drives
# VBoxManage.exe through WSL interop. No root needed.
#
# Usage: ci/hardware/vbox-dry-run.sh <uefi|bios> [--keep]
#   Build the images first: ci/hardware/make-usb.sh build
#   --keep  leave the VM registered (to open it in the VirtualBox GUI);
#           by default it is deleted with its disk afterwards.
# Environment:
#   VBOXMANAGE       path to VBoxManage.exe
#                    (default /mnt/c/Program Files/Oracle/VirtualBox/VBoxManage.exe)
#   VINTNER_VBOX_DIR working directory on a Windows drive, which VirtualBox can
#                    read (default: %TEMP%\vintner-vbox on Windows)
set -euo pipefail

FW="${1:-}"
KEEP="${2:-}"
if { [ "$FW" != uefi ] && [ "$FW" != bios ]; } || { [ -n "$KEEP" ] && [ "$KEEP" != --keep ]; }; then
	echo "Usage: $0 <uefi|bios> [--keep]" >&2
	exit 1
fi
cd "$(dirname "$0")/../.."

VBM="${VBOXMANAGE:-/mnt/c/Program Files/Oracle/VirtualBox/VBoxManage.exe}"
[ -x "$VBM" ] || { echo "VBoxManage.exe not found at $VBM (set VBOXMANAGE)" >&2; exit 1; }
command -v wslpath >/dev/null || { echo "wslpath not found: run this in WSL" >&2; exit 1; }

IMG="ci/out/hardware/$FW.img"
[ -f "$IMG" ] || { echo "$IMG missing: run ci/hardware/make-usb.sh build first" >&2; exit 1; }

if [ -z "${VINTNER_VBOX_DIR:-}" ]; then
	win_temp=$(cmd.exe /d /c "echo %TEMP%" 2>/dev/null | tr -d '\r')
	VINTNER_VBOX_DIR="$(wslpath -u "$win_temp")/vintner-vbox"
fi
case "$VINTNER_VBOX_DIR" in
	/mnt/[a-z]/*) ;;
	*) echo "VINTNER_VBOX_DIR must be on a Windows drive (/mnt/<letter>/...): $VINTNER_VBOX_DIR" >&2; exit 1 ;;
esac
WORK="$VINTNER_VBOX_DIR/$FW"
VM="vintner-dry-run-$FW"
win() { wslpath -w "$1"; }
vbm() { "$VBM" "$@"; }

delete_vm() {
	if vbm showvminfo "$VM" >/dev/null 2>&1; then
		vbm controlvm "$VM" poweroff >/dev/null 2>&1 || true
		# The session lock can outlive poweroff briefly.
		for _ in 1 2 3 4 5; do
			vbm unregistervm "$VM" --delete >/dev/null 2>&1 && return 0
			sleep 1
		done
		echo "warning: could not delete VM $VM" >&2
	fi
}

# A VM of this name is ours from an earlier run: start clean.
delete_vm
rm -rf "$WORK"
mkdir -p "$WORK"
# shellcheck disable=SC2317 # called by the EXIT trap
cleanup() {
	if [ "$KEEP" != --keep ]; then
		delete_vm
		rm -f "$WORK/disk.img" "$WORK/after.img"
	fi
}
trap cleanup EXIT

echo "=== VirtualBox $(vbm --version | tr -d '\r'), $FW dry run in $(win "$WORK") ==="
cp --sparse=always "$IMG" "$WORK/disk.img"
vbm convertfromraw "$(win "$WORK/disk.img")" "$(win "$WORK/disk.vdi")" --format VDI >/dev/null
rm -f "$WORK/disk.img"

firmware=bios
[ "$FW" = uefi ] && firmware=efi64
vbm createvm --name "$VM" --ostype Debian_64 --basefolder "$(win "$WORK")" --register >/dev/null
vbm modifyvm "$VM" --firmware "$firmware" --memory 1024 --cpus 1 \
	--graphicscontroller vmsvga --audio-enabled off --usb-ohci off --nic1 none \
	--boot1 disk --boot2 none --boot3 none --boot4 none \
	--uart1 0x3F8 4 --uart-mode1 file "$(win "$WORK/serial.log")"
vbm storagectl "$VM" --name SATA --add sata --controller IntelAhci --portcount 1
vbm storageattach "$VM" --storagectl SATA --port 0 --device 0 --type hdd --medium "$(win "$WORK/disk.vdi")"

echo "Booting $VM headless; the test initramfs powers off 30 s after it boots..."
vbm startvm "$VM" --type headless >/dev/null
state=""
for _ in $(seq 1 90); do
	state=$(vbm showvminfo "$VM" --machinereadable | tr -d '\r' | sed -n 's/^VMState="\(.*\)"$/\1/p')
	[ "$state" = poweroff ] && break
	sleep 2
done
if [ "$state" != poweroff ]; then
	echo "FAIL: VM still '$state' after 180 s; serial log follows" >&2
	tr -d '\r' < "$WORK/serial.log" | tail -n 40 >&2 || true
	exit 1
fi

vbm clonemedium disk "$(win "$WORK/disk.vdi")" "$(win "$WORK/after.img")" --format RAW >/dev/null

fail=0
booted=$(tr -d '\r' < "$WORK/serial.log" | sed -n 's/.*VINTNER-TEST-BOOTED id=\([^ ]*\).*/\1/p' | tail -n 1)
if [ "$booted" = os-2 ]; then
	echo "PASS boot: serial shows VINTNER-TEST-BOOTED id=os-2"
else
	echo "FAIL boot: expected id=os-2 on serial, got '${booted:-nothing}'"
	fail=1
fi

inspect=$(ci/hardware/make-usb.sh inspect "$FW" "$WORK/after.img")
echo "$inspect"
if [ "$FW" = uefi ]; then
	if grep -qx 'vintner-os-2+2-1.efi' <<<"$inspect"; then
		echo "PASS B2: firmware FAT write, vintner-os-2+3.efi became vintner-os-2+2-1.efi"
	else
		echo "FAIL B2: vintner-os-2+2-1.efi not in /EFI/Linux"
		fail=1
	fi
else
	if grep -qx 'VINTNER_BOOTED=2' <<<"$inspect" && grep -qx 'VINTNER_T_2=xx' <<<"$inspect"; then
		echo "PASS B4: save_env wrote VINTNER_BOOTED=2 and VINTNER_T_2=xx"
	else
		echo "FAIL B4: expected VINTNER_BOOTED=2 and VINTNER_T_2=xx in grubenv"
		fail=1
	fi
fi
echo "Serial log: $(win "$WORK/serial.log")"
[ "$KEEP" = --keep ] && echo "VM $VM kept; delete it with: VBoxManage unregistervm $VM --delete"
exit "$fail"
