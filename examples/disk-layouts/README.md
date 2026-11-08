# Disk layouts

The layouts `vintner install` accepts (WO-0010 3.1, architecture §5 and §10; `docs/accepted-platforms.md` lists what is accepted and tested). **The product's installer partitions** (decision D-A): `vintner install` checks the layout against this file and deploys onto it. It never partitions, formats, resizes or moves a partition.

Identity is by **PARTUUID** (and the filesystem UUID of the root), never by device name, partition number or label. Labels are for humans: the CI base disk labels boot storage `BOOTSTORAGE`, and nothing reads it. A disk replacement or clone must not select the wrong root.

## Partition types

| Role | GPT type GUID | Filesystem | Notes |
|---|---|---|---|
| ESP | `C12A7328-F81F-11D2-BA4B-00A0C93EC93B` | FAT32 (FAT12/16 on an existing ESP is reused as it is) | UEFI only. Holds `\EFI\vintner\systemd-bootx64.efi`, `\loader\loader.conf` and, when no other operating system uses the ESP, `\EFI\BOOT\BOOTX64.EFI` |
| BIOS Boot Partition | `21686148-6449-6E6F-744E-656564454649` | none (raw) | BIOS on GPT only. GRUB's `core.img` is embedded here |
| Boot storage (XBOOTLDR) | `BC13C2FF-59E6-4262-A352-B275FD6F7172` | FAT32, plain, unencrypted | **Required.** Never in fstab, never mounted outside the vintner services. Found through `vintner.bootstorage=PARTUUID=` |
| Root | `4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709` (x86-64 root) or the type of the filesystem's kind | ext4 | Plain partition. md, LVM and LUKS: see `docs/accepted-platforms.md` |

The boot storage partition carries the GPT attribute bit 63 (`no-auto`, "do not mount automatically"), so that `systemd-gpt-auto-generator` never mounts it. `ci/integration/uefi-bless` (A5) checks that no generated mount unit names it.

## Sizes

| Partition | Minimum | Default | Why |
|---|---|---|---|
| BIOS Boot Partition | 1 MiB | 2 MiB | `core.img` built from `firmware/bios/core-modules.txt` is about 185 KiB; the Debian cloud images use 3 MiB. The partition must also leave the sectors GRUB's `boot.img` points to in place |
| ESP, new | 100 MiB | 512 MiB | `systemd-bootx64.efi` is about 122 KiB and is written twice (`\EFI\vintner\` and `\EFI\BOOT\`), plus `loader.conf`. 512 MiB leaves room for other tools' files and a signed copy |
| ESP, existing | needs about 1 MiB free | **reused as it is, never resized** | A Windows ESP is 100 to 300 MiB. Nothing of vintner's grows beyond the loader files |
| Boot storage | 1 GiB | 2 GiB | The image budget of architecture §5: N ordinary images (default 3), two recovery images, one image of staging headroom and a 20% margin. With the placeholder 150 MiB per ordinary image and the measured 23 MiB per recovery image (`recovery/BUILDING.md`) that is about 0.8 GiB, so 1 GiB is the least that holds the budget; 2 GiB leaves room for larger kernels and initramfs. The placeholders are not yet measured (WP-002) |
| Root | what the installed system needs | the installer's choice | not constrained here |

The CI base disk is a deliberately small instance of these rules: a 3 GiB root, a 3 MiB BIOS Boot Partition, a 124 MiB ESP and a 512 MiB boot storage.

## Layouts

### 1. New GPT disk, UEFI only (production)

```
#  type                   size        role
1  ESP                    512 MiB     \EFI\vintner, \loader
2  XBOOTLDR boot storage  2 GiB       UKIs, recovery UKIs, manifests
3  Linux root (x86-64)    rest        ext4
```

### 2. New GPT disk, BIOS only (production)

```
#  type                   size        role
1  BIOS Boot Partition    2 MiB       GRUB core.img
2  XBOOTLDR boot storage  2 GiB       grub/, slots, recovery slots, grubenv, manifests
3  Linux root (x86-64)    rest        ext4
```

The ESP is not required on a BIOS-only machine.

### 3. New GPT disk that boots on both firmwares (lab disks, VMs, removable images)

```
#  type                   size        role
1  BIOS Boot Partition    2 MiB       GRUB core.img (BIOS)
2  ESP                    512 MiB     systemd-boot (UEFI)
3  XBOOTLDR boot storage  2 GiB       one boot storage for both backends
4  Linux root (x86-64)    rest        ext4
```

Both firmware backends share boot storage, and the manifest's `firmware` is `both`. This is the layout of the CI base disk (with the partition order of the Debian cloud image).

### 4. Existing ESP, new boot storage

```
#  type                   size        role
n  ESP (existing)         unchanged   reused: nothing is resized, reformatted or removed
m  XBOOTLDR boot storage  2 GiB       created by the product's installer in free space
r  Linux root             ...         ext4
```

Another operating system's loader entries and files on the ESP stay. `\EFI\BOOT\BOOTX64.EFI` is written only when no other operating system uses the ESP (R13), and a `Boot####` entry for `\EFI\vintner\systemd-bootx64.efi` is created (WO-0010 3.3). This layout is designed but not yet tested against a real Windows ESP (`docs/accepted-platforms.md`).

## What the root being md, LVM or LUKS means for boot storage

Nothing changes for boot storage: it is always one plain, unencrypted FAT32 partition on one disk, outside the RAID, the volume group and the encrypted container. The loader reads it before any of those exist, and `grubenv` and the boot counters live there. A root on a RAID spanning two disks still has boot storage on one of them; if that disk fails, recovery from a USB stick is the contingency (architecture §5).

## Not accepted

MBR partition tables; boot storage on LUKS, LVM, md or Btrfs; boot storage on another disk than the root; a second boot storage; an ESP that vintner would have to resize.
