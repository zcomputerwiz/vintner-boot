# Package Lifecycle Test on an Installed Vintner System

`ci/lifecycle/packages/run.sh` proves what real `apt-get` installs, upgrades, and removals do to a system that boots through vintner under both UEFI and BIOS ([architecture §5](../../docs/architecture.md), [§9 phase 1](../../docs/architecture.md), [§10](../../docs/architecture.md), [WO-0003](../../docs/work-orders/WO-0003-package-lifecycle.md)).

Phase 1 completes when "ordinary package operations, including the §5 package-relation lifecycle test, leave loader and recovery hashes unchanged".

```bash
# Run under UEFI
ci/lifecycle/packages/run.sh uefi

# Run under BIOS
ci/lifecycle/packages/run.sh bios
```

**Requirements:** Local Linux workstation with root via `sudo`, KVM (or TCG fallback), loop devices with partition scanning (`losetup -P`), QEMU user networking (`-nic user,model=virtio-net-pci`), Debian 13 packages (`systemd-boot-efi`, `systemd-ukify`, `grub-pc-bin`, `grub-common`), host utilities (`qemu-system-x86_64`, `qemu-utils`, `ovmf`, `mtools`, `dosfstools`, `e2fsprogs`, `cloud-image-utils`, `golang-go`, `python3`, `python3-virt-firmware`, `shellcheck`, `sfdisk`). Output is written to `ci/out/lifecycle/packages/<firmware>/`.

---

## Method

### 1. Dual-Firmware Base Disk
The test builds one Debian 13 disk that boots through vintner on both firmwares, sharing one boot storage partition:
- **Base image:** Starts from the pinned Debian 13 genericcloud raw image (`debian-13-genericcloud-amd64-20261001-2618.qcow2`).
- **Partition expansion:** The root partition and its ext4 filesystem are grown by 2 GiB before appending boot storage to give ample room for kernel and package installations.
- **ESP (Partition 15):** Populated with Debian trixie `systemd-bootx64.efi` (as `\EFI\BOOT\BOOTX64.EFI` and `\EFI\vintner\systemd-bootx64.efi`) and `firmware/uefi/loader.conf` (`default vintner-*`, `timeout 0`, `editor no`, `auto-entries no`).
- **BIOS Boot (Partition 14) & MBR:** MBR populated with GRUB `boot.img` and BIOS boot partition with `core.img` (built with embedded early configuration searching for boot storage by FAT UUID).
- **Boot storage (Partition 2, type `BC13C2FF-59E6-4262-A352-B275FD6F7172`):**
  - UEFI UKIs: `/EFI/Linux/vintner-os-1+3-0.efi`, `vintner-recovery-a+3-0.efi`, and `vintner-recovery-b.efi`.
  - BIOS slots: `/vintner/bios/os-1/`, `/vintner/bios/recovery-a/`, `/vintner/bios/recovery-b/` with `vmlinuz`, `initrd.img`, and `slot.cfg`.
  - GRUB config & environment: `/grub/grub.cfg` and `/grub/grubenv` (`VINTNER_ORDER=1`, `VINTNER_T_1=xxx`, `VINTNER_T_RA=xxx`).
  - State manifests: `manifest-a` and `manifest-b` generated with `firmware: "both"` and records for both UEFI and BIOS for os-1 and both recovery slots.
- **Guest configuration:**
  - Static `vintner` binary installed as `/usr/sbin/vintner`.
  - `vintner-bless.service` installed and enabled under `basic.target.wants/`.
  - `systemd-bless-boot.service` and `systemd-boot-update.service` masked to `/dev/null`.
  - `/etc/kernel/install.conf` set with `layout=other` (as shipped by `vintner-core`).
  - `vintner-lifecycle-report.service` ordered `After=multi-user.target vintner-bless.service cloud-final.service`. It queries `/proc/cmdline`, reads `LoaderEntrySelected` / `LoaderBootCountPath` from efivars, verifies `systemctl is-active vintner-bless.service`, records `dpkg-query -W`, and powers down cleanly.

### 2. Preparation (Operation P0)
As the vintner installer does after deploying vintner (§5), P0 removes the stock loader's ESP directory (`\EFI\debian`), then purges Debian's stock boot loader packages (whichever of `grub-cloud-amd64`, `grub-pc`, `grub-efi-amd64`, `grub-efi-amd64-signed`, `shim-signed`, `systemd-boot` are installed) with `apt-get --allow-remove-essential purge`, and runs `apt-get update`. Both steps are needed; see findings 1 and 2.
- Boot 1 executes the purge and package update, and prints the loader packages it found (`LOADER-PACKAGES-BEFORE`) and every package apt removed (`REMOVED-BY-APT`).
- Boot 2 confirms that the machine boots cleanly through vintner on os-1 and that blessing succeeds.
- The host compares the disk with a copy taken before P0, exactly as for the operations below, except NVRAM: there is no control run yet.
- If P0 fails, the run halts immediately: all subsequent tests require an intact base.
- The post-P0 disk is preserved as `base.raw`, and the post-P0 NVRAM variables as `base-vars.fd`.
- **X0** runs the same purge from the disk before P0, with `\EFI\debian` kept, as an observed operation.

### 3. Operation Execution
Each of the other 15 operations starts from a fresh overlay (`qemu-img create -f qcow2 -F raw -b base.raw <op>.qcow2`) and, for UEFI, a fresh copy of `base-vars.fd`.
- **Boot 1:** Runs the designated operation via a NoCloud seed (`user-data` with `instance-id: lifecycle-<op>`) using QEMU user networking (`-nic user,model=virtio-net-pci`). The script records the command exit code and the last 20 lines of output to `ttyS0`.
- **Boot 2:** Boots the machine untouched to observe which loader executes and whether vintner remains in control.
- **Host evaluation:** The host reads the disk back via `qemu-img convert` to raw, examining partition offsets using `mtools`, `python3-virt-firmware`, and SHA-256 digests. The disk boots on both firmwares, so every run checks the loader files of both. A read that should succeed (a file system, a listed file, the variable store) stops the run rather than reading as "no change". The change list for each operation, with the last 20 lines of its command output, is printed in the log and written to `<op>-changes.txt`.

### 4. Evaluation Criteria & Failure Conditions

| Category | Operations | What constitutes FAIL |
|---|---|---|
| **Preparation** | P0 | As for everyday operations, except NVRAM, against the disk before P0. |
| **Control** | C0 | Non-zero exit code; any difference in vintner loader files, boot storage UKIs/slots, or NVRAM; or if Boot 2 fails to boot vintner os-1. |
| **Negative Control** | N1 | FAIL if the test misses either of the two intentional changes: the added negative test file on boot storage (`/vintner/bios/os-1/lifecycle-negative-test`) or the appended line in `/loader/loader.conf`. This proves the inspection engine can detect modifications and fails when changes occur. |
| **Everyday Operations** | K1, K2, K3, K4, U1, U2, U3 | Non-zero command exit; any modified or removed vintner loader file, for both firmwares in every run (UEFI: `\EFI\BOOT\BOOTX64.EFI`, `\EFI\vintner\systemd-bootx64.efi`, `\loader\loader.conf`; BIOS: MBR bytes 0..439, the whole BIOS Boot Partition); any modified, added, or removed boot storage file (excluding normal state/logs and counter renames); any `Boot####` or `BootOrder` variable differing from C0; or if Boot 2 does not come up through vintner os-1: the kernel command line has `vintner.bootstorage=PARTUUID=<boot storage>`, `vintner-bless.service` is active, and the loader chose os-1 (UEFI `LoaderEntrySelected`, BIOS the first `vintner: trying` line). |
| **Competing Packages** | X0, X1, X2, X3, X4, X5, X6 | Always OBSERVED. X6 is X4 with `grub-pc/install_devices` preseeded to `/dev/vda`, the answer an administrator gives to the prompt. Full file and NVRAM changes are recorded as evidence for architecture §5 package conflicts. |

---

## Package versions

From the run's `versions.txt` (job `lifecycle-uefi` and `lifecycle-bios` in run 38825066828, commit `e9f7096`):

- Fetched for the host: `systemd-boot-efi` and `systemd-ukify` `257.13-1~deb13u1`; `grub-pc-bin` and `grub-common` `2.12-9+deb13u2`; `libc6` `2.41-12+deb13u4`.
- Guest kernel: `6.12.111+deb13-cloud-amd64`. Recovery build: `linux-image-6.12.107+deb13-amd64` `6.12.107-1`, `busybox-static` `1:1.37.0-6+b9`.
- Stock loader packages on the cloud image before P0: `grub-cloud-amd64` `0.1.1`, `grub-efi-amd64-signed` `1+2.12+9+deb13u2`, `shim-signed` `1.51~1+deb13u1+16.1-2~deb13u1`, with `grub-efi-amd64-bin`, `grub-pc-bin`, `grub-common` and `grub2-common` `2.12-9+deb13u2`. Neither `grub-pc` nor `grub-efi-amd64` was installed.
- Installed by the operations: `linux-image-amd64` `6.12.111-1` (K1), `dracut` `106-6` (U2), `systemd-boot` `257.13-1~deb13u1` with `shim-signed` (X1), `grub-efi-amd64` `2.12-9+deb13u2` (X2), `shim-signed` (X3), `grub-cloud-amd64` `0.1.1` (X5), `grub-pc` `2.12-9+deb13u2` (X4, X6).

## Results

GitHub Actions, KVM, 2026-10-15, commit `e9f7096`. Copied from each run's `results.tsv`; the full change list of every operation is in its `<op>-changes.txt`.

### UEFI (`run.sh uefi`)

| ID | Status | Exit | Vintner files changed | NVRAM vs C0 | Boot 2 |
|---|---|---|---|---|---|
| P0 | PASS | 0 | none | not compared (no control run yet) | vintner os-1 |
| X0 | OBSERVED | 0 | modified `/EFI/BOOT/BOOTX64.EFI` | not compared (no control run yet) | other: `BOOT_IMAGE=/boot/vmlinuz-6.12.111+deb13-cloud-amd64 root=PARTUUID=… ro console=tty0 …`, bless not active |
| C0 | PASS | 0 | none | none | vintner os-1 |
| N1 | PASS | 0 | modified `/loader/loader.conf`, added `/vintner/bios/os-1/lifecycle-negative-test` | none | vintner os-1 |
| K1 | PASS | 0 | none | none | vintner os-1 |
| K2 | PASS | 0 | none | none | vintner os-1 |
| K3 | PASS | 0 | none | none | vintner os-1 |
| K4 | PASS | 0 | none | none | vintner os-1 |
| U1 | PASS | 0 | none | none | vintner os-1 |
| U2 | PASS | 0 | none | none | vintner os-1 |
| U3 | PASS | 0 | none | none | vintner os-1 |
| X1 | OBSERVED | 0 | modified `/EFI/BOOT/BOOTX64.EFI` | `+Boot0008` "Linux Boot Manager" `\EFI\systemd\systemd-bootx64.efi`; `+Boot0009` "Debian" `EFI\debian\shimx64.efi` (loading `\EFI\systemd\systemd-bootx64.efi`); `BootOrder` now `0009, 0008, 0000…` | vintner os-1 |
| X2 | OBSERVED | 0 | modified `/EFI/BOOT/BOOTX64.EFI` | `+Boot0008` "debian" `\EFI\debian\grubx64.efi`; `BootOrder` now `0008, 0000…` | other: `BOOT_IMAGE=/boot/vmlinuz-6.12.111+deb13-cloud-amd64 … ro quiet`, bless not active |
| X3 | OBSERVED | 0 | none | none | vintner os-1 |
| X4 | OBSERVED | 100 | none | none | vintner os-1 |
| X5 | OBSERVED | 0 | modified `/EFI/BOOT/BOOTX64.EFI`, modified BIOS Boot Partition | none | other: `BOOT_IMAGE=/boot/vmlinuz-6.12.111+deb13-cloud-amd64 …`, bless not active |
| X6 | OBSERVED | 0 | modified BIOS Boot Partition | none | vintner os-1 |

### BIOS (`run.sh bios`)

| ID | Status | Exit | Vintner files changed | Boot 2 |
|---|---|---|---|---|
| P0 | PASS | 0 | none | vintner os-1 |
| X0 | OBSERVED | 0 | none | vintner os-1 |
| C0 | PASS | 0 | none | vintner os-1 |
| N1 | PASS | 0 | modified `/loader/loader.conf`, added `/vintner/bios/os-1/lifecycle-negative-test` | vintner os-1 |
| K1 | PASS | 0 | none | vintner os-1 |
| K2 | PASS | 0 | none | vintner os-1 |
| K3 | PASS | 0 | none | vintner os-1 |
| K4 | PASS | 0 | none | vintner os-1 |
| U1 | PASS | 0 | none | vintner os-1 |
| U2 | PASS | 0 | none | vintner os-1 |
| U3 | PASS | 0 | none | vintner os-1 |
| X1 | OBSERVED | 100 | modified `/EFI/BOOT/BOOTX64.EFI` | vintner os-1 |
| X2 | OBSERVED | 0 | modified `/EFI/BOOT/BOOTX64.EFI` | vintner os-1 |
| X3 | OBSERVED | 0 | none | vintner os-1 |
| X4 | OBSERVED | 100 | none | vintner os-1 |
| X5 | OBSERVED | 0 | modified `/EFI/BOOT/BOOTX64.EFI`, modified BIOS Boot Partition | other: stock GRUB printed ``Booting `Debian GNU/Linux'`` and nothing more on the serial console |
| X6 | OBSERVED | 0 | modified BIOS Boot Partition | other: nothing on the serial console |

NVRAM does not apply to the BIOS run.

## Findings

1. **Removing the stock loaders hands a UEFI machine back to Debian's GRUB, unless `\EFI\debian` is removed first (X0).** Purging `grub-cloud-amd64`, `grub-efi-amd64-signed` and `shim-signed` replaced `\EFI\BOOT\BOOTX64.EFI`; the next boot came up through Debian's GRUB, and `vintner-bless.service` was not active. The cause is `shim-signed`'s `postrm`: on `remove` or `purge`, on an EFI-booted system, when `/boot/efi/EFI/debian` exists and `grub-install` is present, it runs `grub-install --target=x86_64-efi`, adding `--force-extra-removable` when `grub2/force_efi_extra_removable` is true, as on the cloud image. On BIOS the `postrm` exits early, and X0 changed nothing. With `\EFI\debian` removed first, P0 changed no vintner file on either firmware.
2. **`grub-efi-amd64-signed` is `Protected: yes`.** `apt-get purge` refuses to remove it, and so removes nothing, without `--allow-remove-essential` (exit 100).
3. **The ordinary operations change nothing vintner owns, on either firmware.** Installing a second kernel flavour and removing it again, rebuilding the initramfs, reinstalling the running kernel, installing `systemd-ukify`, replacing `initramfs-tools` with `dracut`, and reinstalling `systemd` and `udev` left the loader files, the MBR, the BIOS Boot Partition, boot storage and NVRAM unchanged, and the next boot came up through vintner os-1.
4. **Every conflicting loader package that installs successfully replaces a vintner loader file.**
   - `systemd-boot` (X1) replaces `\EFI\BOOT\BOOTX64.EFI`, installs shim, and puts two entries ahead of the firmware's in `BootOrder`. The next UEFI boot still reached vintner os-1, because Debian's systemd-boot read vintner's `loader.conf` and boot storage. On BIOS its `postinst` fails ("EFI variables are not supported on this system", exit 100) after replacing `\EFI\BOOT\BOOTX64.EFI`.
   - `grub-efi-amd64` (X2) replaces `\EFI\BOOT\BOOTX64.EFI`, adds a "debian" entry first in `BootOrder`, and the next UEFI boot is Debian's GRUB.
   - `grub-cloud-amd64` (X5) runs `grub-install` for both BIOS and UEFI: it replaces `\EFI\BOOT\BOOTX64.EFI` and `core.img` in the BIOS Boot Partition, and the next boot on either firmware is Debian's GRUB.
   - `grub-pc` (X6, with `grub-pc/install_devices` answered) replaces `core.img`; the next BIOS boot is Debian's GRUB. The MBR boot code stayed byte-identical: `grub-install` writes the same `boot.img`, so only the BIOS Boot Partition hash shows the change.
   - `shim-signed` alone (X3) changed nothing: its `postinst` reported that the system does not support Secure Boot.
   - `grub-pc` without the answer (X4) stops in its `postinst` ("You must correct your GRUB install devices before proceeding", exit 100) and writes nothing.

   This agrees with the `Conflicts` in architecture §5 for `systemd-boot`, `grub-efi-amd64`, `grub-pc`, `shim-signed` and `grub-cloud-amd64`. The reason given there for `shim-signed` ("its post-install script runs `grub-install` on EFI-booted systems") is incomplete: it was the removal script that ran `grub-install` here (finding 1).
5. **The cloud image ships `grub-cloud-amd64`, `grub-efi-amd64-signed` and `shim-signed`**, not `grub-pc` or `grub-efi-amd64`.
