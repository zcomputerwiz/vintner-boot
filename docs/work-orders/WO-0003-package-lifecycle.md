# WO-0003: Package lifecycle test on an installed vintner system

- **For:** an agent with a local Linux environment that has root via `sudo`, KVM, loop devices with partition scanning (`losetup -P`) and outbound network access for the guest (QEMU user networking).
- **Issued:** 2026-10-15
- **Base branch:** `main`. Work on branch `wo-0003` and open one pull request against `main`.

## Why

Phase 1 ends when "ordinary package operations, including the §5 package-relation lifecycle test, leave loader and recovery hashes unchanged" (architecture §9). Nothing tests this yet:

- `ci/lifecycle/find-boot-writers.sh` lists, statically, the Debian packages whose scripts can write a loader, boot entries or NVRAM.
- `ci/lifecycle/systemd-boot-hooks/` ran only the first two of its eight steps, because its reboots timed out under TCG.

This work order runs real `apt-get` operations inside a Debian 13 system that boots through vintner on both firmwares. After each operation it records, from the disk and the firmware variables, exactly what changed. The result tells us two things:

- which ordinary operations are safe today;
- what each conflicting package really does, which is the evidence for the `Conflicts` list in §5.

## Read first

- `docs/architecture.md`:
  - §5 "Installer and disk ownership", in particular the installation order and the package-relation table;
  - §10 "Package lifecycle";
  - §9 phase 1.
- `ci/lifecycle/README.md` and `ci/lifecycle/systemd-boot-hooks/results.md`: what is already known.
- `ci/integration/uefi-bless/run.sh` and `ci/integration/bios-bless/run.sh`, with their READMEs. They build a vintner disk for one firmware each. This work order needs one disk that boots on both. Reuse their steps; the review fixes in both are the standard to meet.
- `recovery/BUILDING.md`: `recovery/build.sh` produces `vmlinuz` and `initrd.img` for the BIOS slots, and `vintner-recovery-a.efi` and `vintner-recovery-b.efi` for UEFI.
- `schema/manifest.md`: `firmware` `both`, with both a `uefi` and a `bios` record per image and per recovery slot.

## Rules

These are the WO-0002 rules plus two lessons from its review.

- **Disks:** never write to a disk you did not create for this work order. Print `lsblk` before every `losetup` and check that the target is your loop device.
- **Generated output:** commit none of it (images, debs, logs, keys). Put it all under `ci/out/`.
- **Scope:** do not change `src/`, `firmware/`, `schema/`, `recovery/`, `integration/`, `docs/architecture.md`, the ADRs, or the existing scripts under `ci/`. If a check finds a bug there, report it in the pull request with the evidence. Do not work around it.
- **Evidence:** copy it from output, from the guest's serial log or from the disk read back on the host. Never hardcode an evidence string or a conclusion. A check whose evidence is missing is a FAIL.
- **Git:** scripts must be executable in git (`git update-index --chmod=+x`; check with `git ls-files -s`).
- **Versions:** take every version in the README and the pull request from the run's own `versions.txt`.
- **Pull request body:** write it to a file and pass that file.
- **New: compare exactly.** Compare whole values: a hash with a hash, a file list with a file list, `NAME=value` with `NAME=value`. Never use a substring or `grep -q` match for a value. In WO-0002, `VINTNER_T_1=x` matched `xx`.
- **New: every check must be able to fail.** For each check, the README says what output would make it FAIL. Operation N1 below proves this on a real run.

## Task

**Deliverables:**
- `ci/lifecycle/packages/run.sh`. It takes one argument, `uefi` or `bios`, and runs every operation under that firmware.
- `ci/lifecycle/packages/README.md`, with the method, the versions and the results tables from one run of each firmware.
- Two CI rows.

### 1. Base disk

Start from the pinned Debian 13 `genericcloud` image (`ci/lib/fetch-image.sh`), converted to raw.

**Grow the root.** The kernel operations need room. Before appending boot storage, grow the root partition and its filesystem by 2 GiB, and record the free space on the root after the first boot.

**Build one disk that boots on both firmwares,** as the uefi-bless and bios-bless disks do, sharing one boot storage partition:

- **ESP:** trixie `systemd-bootx64.efi` as `\EFI\BOOT\BOOTX64.EFI` and `\EFI\vintner\systemd-bootx64.efi`, and `firmware/uefi/loader.conf` as `\loader\loader.conf`.
- **GRUB:** `boot.img` in the MBR and `core.img` in the BIOS Boot Partition, with the early config finding boot storage by its FAT UUID.
- **Boot storage:**
  - `/EFI/Linux/vintner-os-1+3-0.efi`, `vintner-recovery-a+3-0.efi` and `vintner-recovery-b.efi`;
  - `/grub/grub.cfg` and `/grub/grubenv` (`VINTNER_ORDER=1`, `VINTNER_T_1=xxx`, `VINTNER_T_RA=xxx`);
  - `/vintner/bios/os-1/`, `recovery-a/` and `recovery-b/`;
  - `manifest-a` and `manifest-b`, with `firmware` `both` and both records for the image and each recovery slot. Generate them with a throwaway program that calls `manifest.Save`, removed on any exit, as in bios-bless.
- **Command line of os-1, in both the UKI and `slot.cfg`:** `root=UUID=<root fs uuid> ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=<boot storage partuuid>`.
- **Root filesystem:**
  - `/usr/sbin/vintner` (a static build of `src/`);
  - `vintner-bless.service`, enabled;
  - `systemd-bless-boot.service` and `systemd-boot-update.service`, masked;
  - `/etc/kernel/install.conf` with `layout=other`. This is what `vintner-core` will ship (§5).
- **Report service:** a guest unit ordered `After=multi-user.target vintner-bless.service cloud-final.service`. It is the only thing that powers the guest off. Before it does, it prints to `ttyS0`:
  - `/proc/cmdline`;
  - `LoaderEntrySelected`, when the guest was booted through UEFI;
  - `systemctl is-active vintner-bless.service`;
  - `dpkg-query -W` for the packages named in §3.

  Mark each line so the host can find it.

### 2. Preparation (operation P0)

The installer removes the stock loader packages after it has deployed vintner (§5). P0 does the same, and records its effect.

- **Boot 1:** record `dpkg -l` for `grub*`, `shim*` and `systemd-boot*`. Then purge the packages that install a loader, whichever of these are installed: `grub-cloud-amd64`, `grub-pc`, `grub-efi-amd64`, `grub-efi-amd64-signed`, `shim-signed`, `systemd-boot`.

  ```
  DEBIAN_FRONTEND=noninteractive apt-get -y purge <those packages>
  ```

  Do not name the `-bin` and `-common` packages yourself. Record everything apt removed.
- **Boot 2:** the "after" boot (§3).

P0 runs under the firmware given to `run.sh`. Its resulting disk is the base for every other operation in that run. If P0 changes a vintner file, or the after boot does not come up through vintner, stop the run and report it as the first finding: every other operation would be measured on a broken base.

### 3. Operations

**Each operation:**
- starts from the base: a fresh qcow2 overlay (`qemu-img create -f qcow2 -F raw -b base.raw <op>.qcow2`), plus, for UEFI, a fresh copy of the base OVMF variable store;
- runs in boot 1, through a NoCloud seed of its own with a new `instance-id` and the commands in `runcmd`, with `DEBIAN_FRONTEND=noninteractive` and `apt-get -y`;
- records the command's exit status and the last 20 lines of its output on `ttyS0`;
- is followed by boot 2, with nothing changed, to see what the machine boots next.

Give the guest network access with QEMU user networking (`-nic user,model=virtio-net-pci`) and let cloud-init configure it. The bless tests disabled networking; this one needs apt. Allow at least 10 minutes for boot 1 under KVM, because it downloads packages.

| ID | Kind | Boot 1 runs |
|---|---|---|
| C0 | control | nothing |
| N1 | negative control | mounts boot storage, creates `/vintner/bios/os-1/lifecycle-negative-test`, unmounts; appends an empty line to `/boot/efi/loader/loader.conf` |
| K1 | ordinary | `apt-get install linux-image-amd64` (a second kernel flavour, new ABI) |
| K2 | ordinary | `update-initramfs -u -k all` |
| K3 | ordinary | K1, then `apt-get purge` of the `linux-image-<abi>` package K1 installed |
| K4 | ordinary | `apt-get install --reinstall linux-image-$(uname -r)` (stands in for a same-ABI upgrade) |
| U1 | ordinary | `apt-get install systemd-ukify` |
| U2 | ordinary | `apt-get install dracut` (replaces initramfs-tools and rebuilds the initramfs) |
| U3 | ordinary | `apt-get install --reinstall systemd udev` |
| X1 | conflicting | `apt-get install systemd-boot` |
| X2 | conflicting | `apt-get install grub-efi-amd64` |
| X3 | conflicting | `apt-get install shim-signed` |
| X4 | conflicting | `apt-get install grub-pc` |
| X5 | conflicting | `apt-get install grub-cloud-amd64` |

### 4. What to measure

After every boot, read the disk back on the host: `qemu-img convert` the overlay to a temporary raw file, then use mtools and `dd` at the partition offsets. Delete the raw file afterwards. Compare against the base.

**Vintner loader files:**
- UEFI: `\EFI\BOOT\BOOTX64.EFI`, `\EFI\vintner\systemd-bootx64.efi` and `\loader\loader.conf` (sha256);
- BIOS: MBR bytes 0 to 439, and the whole BIOS Boot Partition (sha256).

**Boot storage:**
- the sorted list of files with the sha256 of each;
- leave out what a normal boot changes: `/grub/grubenv`, `/vintner/state/` and `/vintner/recovery-logs/`;
- compare UKIs by content, with the `+N-M` counter removed from their names, because systemd-boot and bless rename them on every boot.

**Rest of the ESP:** every other file, created, changed or removed (sha256). This is OBSERVED, not a pass or fail criterion. For example, `\EFI\debian\`.

**NVRAM, UEFI only:** every `Boot####` variable and `BootOrder`, read from the variable store with `python3-virt-firmware`. OVMF adds its own boot entries, so compare against the C0 run, not against the base.

**Which loader booted boot 2:**
- UEFI: `LoaderEntrySelected`;
- BIOS: `VINTNER_BOOTED` in grubenv on the host, and the `vintner: trying` lines in the serial log;
- both: whether `/proc/cmdline` contains `vintner.bootstorage=PARTUUID=<boot storage partuuid>`. A stock GRUB entry does not.

Write the full list of changes for every operation to `ci/out/lifecycle/packages/<firmware>/<op>-changes.txt`.

### 5. Results

`results.tsv`, one row per operation, with:
- the evidence: the exit status of the command;
- the changed vintner files, or "none";
- the changed NVRAM entries, or "none";
- what booted boot 2.

**Status:**

| Kind | PASS when |
|---|---|
| C0, ordinary (K, U) | No vintner loader or boot storage file changed, no NVRAM entry differs from C0, and boot 2 came up through vintner on os-1. Anything else is FAIL. |
| N1 | The run reports both changes it made: the new file on boot storage and the changed `loader.conf`. If it misses either, it is FAIL, because then the checks cannot see a change. |
| P0 | As for ordinary operations. It stops the run on FAIL. |
| X | Always OBSERVED, with the full list of changes and what booted next. |

If a command in boot 1 exits non-zero, the row is FAIL for C0, K and U operations, and OBSERVED for X operations, with the error lines as evidence.

**`versions.txt`:**
- the trixie packages the script fetched;
- the guest kernel;
- the recovery build;
- for every operation, the guest's `dpkg-query -W` output after boot 1, as printed by the report service.

### 6. CI

Add two rows to `.github/workflows/qemu-tests.yml`: `lifecycle-uefi` (`ci/lifecycle/packages/run.sh uefi`) and `lifecycle-bios` (`ci/lifecycle/packages/run.sh bios`). Each needs:
- Go;
- `cloud-image-utils`, `qemu-utils` and the cached image;
- `recovery/build.sh` run first;
- logs and `*.tsv`/`*.txt` uploaded from `ci/out/lifecycle/packages/`.

Both rows must pass in the pull request's CI. "Pass" means the script exits 0 when every C0, N1, P0, K and U row is PASS. X rows never fail the run. Each row should finish within 60 minutes.

## Reporting back

- **The pull request** contains both results tables, with evidence copied from the CI run, and the `versions.txt` excerpt for the packages in §3.
- **Findings.** Report each X operation's effect as a finding, compared with the reason the §5 table gives for its `Conflicts`. Say where they agree and where they differ.
- **Ordinary failures.** An ordinary operation that changed a vintner file or NVRAM is the most important result of this work order. Report it first, with the change list and the maintainer script or hook responsible, if you can find it in the package.
- **Checks you cannot run as written:** say exactly why. Do not substitute another check.
