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

It also verifies the payload directory (`/usr/lib/vintner/payloads`, or `--payload-dir`) against its `MANIFEST`: a file that differs, is missing or is not listed, or a file a deployment needs that is absent, refuses. It reads the partition table, the FAT boot sectors and the ext4 superblock itself, so it needs no mounts. Without `--check` it installs.

## `vintner install`

`vintner install --disk DEV --firmware uefi|bios|both --target DIR --esp DIR --boot-storage DIR [--kernel ABI] [--cmdline-file FILE]` turns a prepared target into a vintner system. It runs from inside a chroot of the target (`--target /`, the default), where `/proc/cmdline` belongs to the installer's kernel. It runs `--check` first and refuses on any finding. Then, from the verified payloads of `vintner-platform`, in the order of architecture §5:

1. **Stage**, which changes nothing about how the machine boots:
   - boot storage: the directories; the recovery images (UEFI: two UKIs built by `ukify` from `recovery/vmlinuz`, `recovery/initrd.img` and the stub, named `vintner-recovery-a+3-0.efi` and `vintner-recovery-b.efi`; BIOS: `vmlinuz`, `initrd.img` and `slot.cfg` per slot); for BIOS `grub/grub.cfg` and a `grub/grubenv` that gives recovery A its tries;
   - the manifest (`seq` 1, no images, both recovery records with their hashes, `next_generation` 1) with the target's `/etc/machine-id`, a new installation ID and the root, boot storage and ESP PARTUUIDs;
   - the ESP: `\EFI\vintner\systemd-bootx64.efi` and `\loader\loader.conf` (it refuses a `loader.conf` that is not vintner's: there is one per ESP).
2. **Publish generation 1** with `vintner publish`'s code, on an explicit command line: `root=UUID=<root filesystem> ro <the product's options from /etc/kernel/cmdline> vintner.bootstorage=PARTUUID=<boot storage>` (decision D-B; the options file may not name `root=`, `ro`/`rw` or anything under `vintner.`: [`cmdline.md`](cmdline.md)). The kernel is `--kernel`, or the newest `vmlinuz-<abi>` with an `initrd.img-<abi>` in `<target>/boot`. The manifest must then hold exactly generation 1, or nothing is taken over.
3. **Take booting over**, only now: on UEFI first the NVRAM boot entry (`Boot####`, description `Vintner Boot`, a hard-drive media node of the ESP's PARTUUID, number, start and size, then the file `\EFI\vintner\systemd-bootx64.efi`), put first in `BootOrder` through `src/internal/efivar` with other systems' entries kept after it and never changed (an entry that already has that description and path is reused, so running again adds none; it is read back). It is written even when another system uses the ESP, because it names only vintner's own file; it is skipped, with a message, when the installer did not start through UEFI (no efivarfs; `TookOver` then depends on the removable path alone). Then `\EFI\BOOT\BOOTX64.EFI` is written unless another operating system uses the ESP (a directory under `\EFI` other than `BOOT`, `vintner` and Debian's `debian`) or it holds another loader (it is replaced when absent, already vintner's, or when Debian's stock loader is installed); for BIOS `core.img` (built by `grub-mkimage` from the payload's `i386-pc` directory, with an early configuration that finds boot storage by its FAT serial) goes to the BIOS Boot Partition and GRUB's `boot.img` to the boot sector, keeping the partition table, each write flushed to the device. Then everything is read back and compared (ESP files with the payload, boot storage with the hashes written, the manifest through the loader the other commands use, the BIOS boot code from the disk after the device cache is dropped).
4. **Remove the stock loader**: `\EFI\debian` on the ESP first, then the installed packages among `grub-cloud-amd64 grub-pc grub-efi-amd64 grub-efi-amd64-signed grub-efi-ia32 grub-coreboot grub-xen grub-ieee1275 shim-signed systemd-boot refind` with `apt-get --allow-remove-essential purge` (`ci/lifecycle/packages` findings 1 and 2). **Only when booting now starts vintner on every firmware asked for.** When the removable path was left alone and no NVRAM entry could be written, the stock loaders stay and the command says so. A `dpkg-query` that cannot run is an error, not "nothing installed".
5. Verify again.

**Initramfs freshness (WO-0010 3.5).** A product that installs by copying a live system carries the live system's initramfs, which lacks what the installed hooks add. Before publishing generation 1, `vintner install` compares the modification time of `<target>/boot/initrd.img-<abi>` of the kernel it will publish with the newest file under `<target>/etc/initramfs-tools`, `usr/share/initramfs-tools` and `usr/lib/initramfs-tools`. An older initramfs **is refused** (step `initramfs`, nothing published, booting unchanged) with the command to run: `update-initramfs -u -k <abi>` in the target. With `--update-initramfs` the installer runs that command itself (in the target through `chroot` when `--target` is not `/`) and checks again; an initramfs still older than its inputs fails. Installed Debian package files keep their build-time modification times, so a copied or freshly debootstrapped target passes; touching a hook after the initramfs was built is what trips it. Only the published kernel is checked (generation 1 publishes one); later generations are built by the kernel packages' own `update-initramfs`.

Everything that can fail without writing is checked first: `grub-mkimage` is installed (package `grub-common`; the command refuses and says so), `/etc/machine-id` exists, boot storage and the ESP accept a new file, the payload's `boot.img` is one sector, and the core image fits. A manifest on boot storage that publishes no image is what a deployment that stopped half-way leaves: it is replaced. One with an image, or one that cannot be read, is an installed system and is refused.

**Failure.** A failure before step 3 leaves booting as it was; the report lists what was changed (files on boot storage and the ESP). The command prints "created UEFI boot entry ...", "wrote EFI/BOOT/BOOTX64.EFI ..." or "writing core.img ..." before the first write that changes booting. A failure after publishing generation 1 is resumable: `vintner install ... --resume` (same arguments) takes booting over, removes the stock loaders when that is safe, and verifies; it refuses a boot storage that does not hold exactly generation 1 of this layout.

**Options for installing from another machine (WO-0010 3.6).** `--efivars DIR` names the efivarfs the NVRAM entry is written to; `--efivars none` writes no UEFI variable, for an installer whose firmware is not the target's (a CI runner, a build host). `--recovery-cmdline-a` and `--recovery-cmdline-b` append kernel options to recovery A and B (UKI and BIOS `slot.cfg`), for example a serial console.

**Secure Boot tier A (WO-0010 5.1).** `vintner install --secure-boot A` (UEFI or both; BIOS has no Secure Boot) needs `sbsign` and `sbverify` (package `sbsigntool`) and refuses before writing anything when they are missing, when a key already exists in the key directory, or for any other tier. It then:
- creates the machine key and certificate in `<target>/etc/vintner/secureboot` (`--secure-boot-dir` changes it; `key.pem` 0600, `cert.pem`, `cert.der`), as `vintner secureboot init` does, and records the tier and the certificate's sha256 in the manifest;
- signs the recovery UKIs through `ukify` and the loader with `sbsign`, and checks each signature with `sbverify` before it is written, so `\EFI\vintner\systemd-bootx64.efi` and `\EFI\BOOT\BOOTX64.EFI` hold the signed loader; publishing generation 1 signs it the same way;
- **refuses to take booting over until the certificate is in the firmware's `db`** (step `secure boot`; generation 1 is published, the stock boot path is untouched, the error is resumable). It reads `db` through efivarfs; where there is none (installing from outside the machine) it asks for `--secure-boot-enrolled`. Without this a signed-but-unenrolled loader would be refused by the firmware and, once the stock loaders were removed, nothing would start. After enrolling the certificate (`cert.der`; see `docs/secure-boot.md` §3.1), `vintner install ... --resume` finishes, and checks the loader on the ESP against the machine certificate first.
The installer prints that out-of-tree kernel modules cannot load with Secure Boot on in tier A (no shim, no MOK list; WO-0010 5.3). The installer does not enroll the certificate: that needs the firmware in setup mode, and whether the vendor's `KEK`/`db` must be kept is still open for real hardware (5.1). The signing and the enrollment check are unit-tested with fake `sbsign`/`sbverify` and a fake efivarfs; no QEMU row installs with `--secure-boot A` yet (the `secure-boot-uefi` row still signs by script; moving it to `vintner install` is WO-0010 3.6).

**Not done by this version:** Secure Boot tiers B and C. The NVRAM entry's
 byte layout is checked by unit tests against the UEFI load option and device path formats; no QEMU row has the firmware boot through it yet (the `install` row installs from a BIOS-booted host, where there are no UEFI variables), so that is **designed and unit-tested, not boot-tested**.

**`BootOrder` health report and `vintner boot-entry` (WO-0010 3.4).** `vintner bless` (the health service, on UEFI, for a manifest whose firmware is `uefi` or `both`) reports, next to its other findings and without failing the boot, when no `Boot####` entry named `Vintner Boot` starts `EFIintnersystemd-bootx64.efi` ("NVRAM boot entry missing") or when `BootOrder` does not start with it ("BootOrder does not start with the vintner entry Boot0001 (BootOrder: 0000,0001)"). A system without efivarfs reports nothing. The report never repairs anything: the explicit command is `vintner boot-entry --disk DISK [--esp-partuuid UUID] [--esp MOUNTED_ESP]`, which creates or reuses the entry and puts it first (other entries keep their order after it); with `--esp` it refuses when vintner's loader is not on the ESP; `--check` only prints the report and exits 1 when something is wrong. This is threat model T8.

`ci/install` (a row of `qemu-tests.yml`) runs this on a debootstrapped Debian 13 disk, with nothing copied in by hand: it installs the vintner packages, runs `vintner install` from a chroot, and boots the disk under SeaBIOS and OVMF. Each must boot generation 1 and bless it GOOD. A grub-pc stock loader and a stock `\EFI\debian` directory are installed first, so the removal is exercised too.
