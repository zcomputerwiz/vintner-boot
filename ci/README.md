# Phase 0 Boot Baseline

This directory contains scripts to boot an unmodified Debian 13 genericcloud image under QEMU to establish a phase-0 baseline for `vintner-boot`.

## Required packages

For Debian/Ubuntu hosts:
```bash
sudo apt-get install qemu-system-x86 ovmf qemu-utils curl
```

## Running tests locally

Each test will automatically download the pinned Debian 13 image into `ci/out/cache/` (if it is not already cached) and then boot an overlay. Ensure KVM is enabled (e.g., your user is in the `kvm` group) to avoid slow TCG fallback.

### SeaBIOS
```bash
./ci/qemu-seabios/boot-baseline.sh
```

### OVMF (Secure Boot off)
```bash
./ci/qemu-ovmf/boot-baseline.sh --secure-boot off
```

### OVMF (Secure Boot on)
```bash
./ci/qemu-ovmf/boot-baseline.sh --secure-boot on
```

## Recorded output

Output is stored in `ci/out/<run-name>/` and includes:
- `serial.log`: the full serial log from the boot process
- `summary.txt`: metadata including QEMU version, firmware hashes, image hashes, the accelerator used (kvm or tcg), and boot time
- `overlay.qcow2`: the modified VM disk state
- `vars.fd` (for OVMF runs): the copy of the NVRAM variables file used for the boot
