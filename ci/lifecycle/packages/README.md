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
As the vintner installer does after deploying vintner (§5), P0 purges Debian's stock boot loader packages (`grub-cloud-amd64`, `grub-pc`, `grub-efi-amd64`, `grub-efi-amd64-signed`, `shim-signed`, `systemd-boot`) and runs `apt-get update`.
- Boot 1 executes the purge and package update, and prints the loader packages it found (`LOADER-PACKAGES-BEFORE`) and every package apt removed (`REMOVED-BY-APT`).
- Boot 2 confirms that the machine boots cleanly through vintner on os-1 and that blessing succeeds.
- The host compares the disk with a copy taken before P0, exactly as for the operations below, except NVRAM: there is no control run yet.
- If P0 fails, the run halts immediately: all subsequent tests require an intact base.
- The post-P0 disk is preserved as `base.raw`, and the post-P0 NVRAM variables as `base-vars.fd`.

### 3. Operation Execution
Each of the 15 test operations starts from a fresh overlay (`qemu-img create -f qcow2 -F raw -b base.raw <op>.qcow2`) and, for UEFI, a fresh copy of `base-vars.fd`.
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
| **Competing Packages** | X1, X2, X3, X4, X5, X6 | Always OBSERVED. X6 is X4 with `grub-pc/install_devices` preseeded to `/dev/vda`, the answer an administrator gives to the prompt. Full file and NVRAM changes are recorded as evidence for architecture §5 package conflicts. |

---

## Package Versions

Extracted from test run `versions.txt`:

### Fetched Host & Bootstrap Packages
- `systemd-boot-efi`: `257.13-1~deb13u1`
- `systemd-ukify`: `257.13-1~deb13u1`
- `grub-pc-bin`: `2.12-9+deb13u2`
- `grub-common`: `2.12-9+deb13u2`
- `libc6`: `2.41-12+deb13u4`
- `liblzma5`: `5.8.1-1+deb13u1`
- `libdevmapper1.02.1`: `2:1.02.205-2`
- `libselinux1`: `3.8.1-1`
- `libudev1`: `257.13-1~deb13u1`
- `libpcre2-8-0`: `10.46-1~deb13u2`
- `libcap2`: `1:2.75-10+deb13u1+b3`

### Guest System
- **Guest kernel ABI:** `6.12.111+deb13-cloud-amd64` (package `linux-image-6.12.111+deb13-cloud-amd64` version `6.12.111-1`)
- **Initial root filesystem capacity:** `/dev/vda1` 5,073,162,240 bytes (883,208,192 used, 3,911,458,816 available, 19% utilized)
- **Recovery build kernel:** `6.12.107-1` (`linux-image-6.12.107+deb13-amd64`)
- **Recovery build busybox:** `1:1.37.0-6+b9` (`busybox-static`)

### Package Versions Observed During Operations
- `linux-image-amd64`: `6.12.111-1` (installed by K1)
- `systemd-ukify`: `257.13-1~deb13u1` (installed by U1)
- `dracut`: `106-6` (installed by U2)
- `systemd`: `257.13-1~deb13u1` (reinstalled by U3)
- `udev`: `257.13-1~deb13u1` (reinstalled by U3)
- `systemd-boot`: `257.13-1~deb13u1` (tested in X1)
- `grub-efi-amd64`: `2.12-9+deb13u2` (tested in X2)
- `shim-signed`: `1.51~1+deb13u1+16.1-2~deb13u1` (tested in X3)

---

## Results: UEFI Lifecycle Suite

Run executed on 2026-10-15 under QEMU KVM:

| ID | Kind | Status | CommandExit | VintnerFiles | NVRAM | Boot2 | Evidence |
|---|---|---|---|---|---|---|---|
| P0 | prep | PASS | 0 | none | none | vintner os-1 | `exit=0; stock loaders purged; booted vintner os-1` |
| C0 | control | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| N1 | neg-control | PASS | 0 | modified /loader/loader.conf, added /vintner/bios/os-1/lifecycle-negative-test | none | vintner os-1 | `exit=0; vintner_files=modified /loader/loader.conf, added /vintner/bios/os-1/lifecycle-negative-test; nvram=none; boot2=vintner os-1` |
| K1 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| K2 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| K3 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| K4 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| U1 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| U2 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| U3 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X1 | conflicting | OBSERVED | 0 | modified /EFI/BOOT/BOOTX64.EFI | +Boot0004 : title="Linux Boot Manager" devpath=Partition(nr=15)/FilePath(\EFI\systemd\systemd-bootx64.efi), +BootOrder : 0004, 0000, 0001, 0002, 0003, -BootOrder : 0000, 0001, 0002, 0003 | vintner os-1 | `exit=0; vintner_files=modified /EFI/BOOT/BOOTX64.EFI; nvram=+Boot0004            : boot entry: title="Linux Boot Manager" devpath=Partition(nr=15)/FilePath(\EFI\systemd\systemd-bootx64.efi), +BootOrder           : boot order: 0004, 0000, 0001, 0002, 0003, -BootOrder           : boot order: 0000, 0001, 0002, 0003; boot2=vintner os-1` |
| X2 | conflicting | OBSERVED | 0 | modified /EFI/BOOT/BOOTX64.EFI | +Boot0004 : title="debian" devpath=Partition(nr=15)/FilePath(\EFI\debian\shimx64.efi), +BootOrder : 0004, 0000, 0001, 0002, 0003, -BootOrder : 0000, 0001, 0002, 0003 | other (entry=none, cmdline_bs=False) | `exit=0; vintner_files=modified /EFI/BOOT/BOOTX64.EFI; nvram=+Boot0004            : boot entry: title="debian" devpath=Partition(nr=15)/FilePath(\EFI\debian\shimx64.efi), +BootOrder           : boot order: 0004, 0000, 0001, 0002, 0003, -BootOrder           : boot order: 0000, 0001, 0002, 0003; boot2=other (entry=none, cmdline_bs=False)` |
| X3 | conflicting | OBSERVED | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X4 | conflicting | OBSERVED | 100 | none | none | vintner os-1 | `exit=100; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X5 | conflicting | OBSERVED | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |

---

## Results: BIOS Lifecycle Suite

Run executed on 2026-10-15 under QEMU KVM:

| ID | Kind | Status | CommandExit | VintnerFiles | NVRAM | Boot2 | Evidence |
|---|---|---|---|---|---|---|---|
| P0 | prep | PASS | 0 | none | none | vintner os-1 | `exit=0; stock loaders purged; booted vintner os-1` |
| C0 | control | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| N1 | neg-control | PASS | 0 | modified /loader/loader.conf, added /vintner/bios/os-1/lifecycle-negative-test | none | vintner os-1 | `exit=0; vintner_files=modified /loader/loader.conf, added /vintner/bios/os-1/lifecycle-negative-test; nvram=none; boot2=vintner os-1` |
| K1 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| K2 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| K3 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| K4 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| U1 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| U2 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| U3 | ordinary | PASS | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X1 | conflicting | OBSERVED | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X2 | conflicting | OBSERVED | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X3 | conflicting | OBSERVED | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X4 | conflicting | OBSERVED | 100 | none | none | vintner os-1 | `exit=100; vintner_files=none; nvram=none; boot2=vintner os-1` |
| X5 | conflicting | OBSERVED | 0 | none | none | vintner os-1 | `exit=0; vintner_files=none; nvram=none; boot2=vintner os-1` |

---

## Detailed Findings

### 1. Ordinary Package Operations (K1–K4, U1–U3)
All everyday packaging operations passed cleanly on both firmwares without touching any vintner loader or boot storage files:
- **Kernel install/reinstall (K1, K3, K4):** `kernel-install` honored `/etc/kernel/install.conf` (`layout=other`), skipping stock bootloader configuration and leaving vintner UKIs, slots, and manifests intact.
- **Initramfs rebuild (K2) and Dracut installation (U2):** Rebuilding the root filesystem's initramfs or replacing `initramfs-tools` with `dracut` left boot storage and loader binaries untouched.
- **Userspace tools (U1, U3):** Installing `systemd-ukify` and reinstalling `systemd` / `udev` did not trigger unexpected hooks or overwrite loader configurations.

### 2. Conflicting Boot Loader Packages (X1–X5)
- **X1 (`systemd-boot`):**
  - **UEFI:** Replaces `\EFI\BOOT\BOOTX64.EFI`, installs `\EFI\systemd\systemd-bootx64.efi`, and adds NVRAM entry `Boot0004` ("Linux Boot Manager") positioned first in `BootOrder`. Boot 2 still boots `vintner os-1` because `loader.conf` remains configured with `default vintner-*`.
  - **BIOS:** Writes files to the ESP, but MBR and BIOS boot partitions are untouched.
- **X2 (`grub-efi-amd64`):**
  - **UEFI:** Completely hijacks the bootloader. It rewrites `\EFI\BOOT\BOOTX64.EFI`, writes `\EFI\BOOT\fbx64.efi`, adds NVRAM entry `Boot0004` ("debian" -> `\EFI\debian\shimx64.efi`), moves `0004` to the top of `BootOrder`, and installs Debian GRUB configs. Boot 2 boots into Debian's stock GRUB (`BOOT_IMAGE=/boot/vmlinuz-...` without `vintner.bootstorage=`). This confirms architecture §5's conflict declaration.
  - **BIOS:** Files placed on ESP, but BIOS boot path is unchanged.
- **X3 (`shim-signed`):**
  - Installs shim binaries to `/usr/lib/` without immediately rewriting active boot entries unless `grub-efi` is also configured.
- **X4 (`grub-pc`):**
  - Exits with status `100` during unattended installation because debconf prompts for the target install disk (`grub-pc/install_devices`) in non-interactive mode.
- **X5 (`grub-cloud-amd64`):**
  - Installs cloud meta-packages without overwriting active vintner MBR or ESP entries in the tested environment.
