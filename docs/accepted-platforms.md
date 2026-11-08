# Accepted platforms

What `vintner install` accepts and what CI proves (WO-0010 3.1; architecture §11 lists this file). Three words are used on purpose:

- **Accepted**: `vintner install` deploys onto it and the project supports it.
- **Tested**: a row in `.github/workflows/qemu-tests.yml` (or the lint workflow) exercises it, named in the table.
- **Not accepted**: `vintner install` refuses it, and says why.

A line that is accepted but not tested says so. Nothing here is accepted on the strength of the design alone unless the table says "designed".

## Platform

| Item | Accepted | Tested by |
|---|---|---|
| CPU architecture | x86-64 (`amd64`) only | every QEMU row |
| Distribution | Debian 13 (trixie). Debian testing runs as a non-blocking lane (architecture §10) | `ci/lib/fetch-image.sh` pins the Debian 13 genericcloud image |
| Kernel | Debian's `linux-image-amd64` and the cloud flavour, with initramfs-tools | `lifecycle-*`, `publish-*`, `graphics-*` |
| Init system | systemd | all |
| Disks | One disk holds the ESP (UEFI), the BIOS Boot Partition (BIOS), boot storage and the root. Two-disk and cross-disk layouts are **not accepted** (architecture §5 mentions external USB recovery as a failure contingency, not as a layout) | single-disk images in every row |
| Partition table | GPT | all |
| MBR/DOS partition table | **Not accepted** | none |

## Firmware

| Firmware | Accepted | Tested by |
|---|---|---|
| UEFI, Secure Boot off | yes | `*-uefi` rows |
| UEFI, Secure Boot on, tier A (per-machine key in `db`) | yes | `secure-boot-uefi`, `ci/secure-boot` |
| UEFI, Secure Boot on, tier B (MOK) or tier C (Debian shim only) | **not accepted yet**: Phase 5 | none |
| BIOS (CSM, SeaBIOS) on GPT with a BIOS Boot Partition | yes | `*-bios` rows, `bios-trial` |
| BIOS and UEFI on one disk (lab disks, and any disk that boots on both) | yes, one boot storage serves both | the base disk of every row is dual-firmware |
| UEFI 32-bit, ARM, Apple, coreboot, Xen | **not accepted** | none |

## Root filesystem

| Root | Accepted | Tested by |
|---|---|---|
| ext4 on a plain partition | yes | all rows |
| ext4 on md RAID, LVM or LUKS | recovery assembles and opens it read-only. As the installed root it is **designed, not tested**: the publisher only records `root=`, and the initramfs has to unlock it; the installer does not create such a layout | `recovery-smoke` (recovery side only) |
| Btrfs, XFS, ZFS | **not accepted** | none |

**Boot storage is always a plain, unencrypted FAT32 partition on one disk**, whatever the root is. `grubenv` and the boot counters live there, never on the root, so a LUKS, LVM or md root changes nothing about them. The loader (firmware, systemd-boot, GRUB's `core.img`) cannot unlock anything.

## Installations and coexistence

| Case | Status |
|---|---|
| New disk partitioned by the product's installer (D-A) | accepted, tested (the layout of the CI base disk) |
| Existing ESP reused, never resized, never reformatted | accepted; the CI base disk reuses the Debian cloud image's 124 MiB ESP. A Windows ESP of 100 to 300 MiB with other systems' entries on it is **designed, not tested** |
| Another operating system on the same ESP | designed (R13: `\EFI\BOOT\BOOTX64.EFI` is written only when no other operating system uses the ESP), not tested |
| Debian's stock loaders (`grub-pc`, `grub-efi-amd64`, `shim-signed`, `systemd-boot`, ...) | replaced by `vintner install` (3.2 step 4), kept out by `Conflicts` (`docs/packaging.md`); tested by `lifecycle-*` |
| In-place conversion of an existing Debian system | the installer deploys onto a prepared layout; it does not repartition (D-A). Whether the layout was prepared by hand is not checked, only that it matches `examples/disk-layouts/` |

## Refusals

`vintner install` refuses, and names the rule it broke, when:

- the disk has an MBR partition table, or any of the rules above says "not accepted";
- boot storage is missing, not FAT32, encrypted, on another disk than the root, or smaller than the minimum in `examples/disk-layouts/README.md`;
- the ESP is smaller than the minimum there, or would have to be resized;
- UEFI is chosen and no ESP exists, or BIOS is chosen and no BIOS Boot Partition (or, on MBR, embed gap) exists;
- the running system is not Debian 13, or a boot loader package that conflicts with `vintner-platform` cannot be removed.

These checks belong to 3.2; this file is the list they implement.

## `vintner install --check`

`vintner install --check --disk DEV --firmware uefi|bios|both` reads the disk and the payloads and changes nothing. It prints what it found and refuses with one line per broken rule:

| Rule | Refuses when |
|---|---|
| L0 | the firmware is not `uefi`, `bios` or `both` |
| L1 | the disk has no GPT, or an MBR partition sits beside it, or a CRC does not match |
| L2 | there is not exactly one boot storage partition (type `BC13C2FF-...`), unless `--boot-storage-partuuid` names it |
| L3 | boot storage is not FAT32 (so also: encrypted, LVM, md or blank) |
| L4 | boot storage is under 1 GiB |
| L5 | UEFI or both, and there is no single ESP (or `--esp-partuuid`), or it is not FAT or under 2 MiB |
| L6 | BIOS or both, and there is no BIOS Boot Partition of at least 1 MiB, or the disk has 4096-byte sectors (the GRUB embedding is written in 512-byte sectors) |
| L7 | the root is not on this disk, is the boot storage, is not ext4 on a plain partition, or is ambiguous (name it with `--root-partuuid`) |

It also verifies the payload directory (`/usr/lib/vintner/payloads`, or `--payload-dir`) against its `MANIFEST`: a file that differs, is missing or is not listed, or a file a deployment needs that is absent, refuses. It reads the partition table, the FAT boot sectors and the ext4 superblock itself, so it needs no mounts. Deploying (3.2) comes next; without `--check` the command says so and exits 2.
