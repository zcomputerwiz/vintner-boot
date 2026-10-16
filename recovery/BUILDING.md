# Building and testing the recovery image

The phase-1 recovery image (architecture §4 "Recovery", §9 phase 1, WP-006) gives you:

- a shell on the console and on `ttyS0`;
- the Debian root mounted read-only at `/mnt/root`, on a plain partition, md RAID, LVM or LUKS;
- `cryptsetup`, `lvm`, `mdadm`, `e2fsck` and `mke2fs` from Debian trixie;
- logs written to `/run/vintner/logs/<time>/`, and to `/vintner/recovery-logs/<time>/` on the boot storage partition.

It is built only from Debian trixie packages, never from the installed system (R06).

## Build

```sh
bash recovery/build.sh
```

**Needs:** `curl xz-utils dpkg cpio kmod binutils python3 python3-pefile`. No root.

**What it does:**

1. Unpacks the packages in `packages.list` from deb.debian.org.
2. Copies the dependency closure of `initramfs/modules.list`, decompressed so busybox `modprobe` can load it.
3. Copies the storage tools with their shared-library closure (read with `objdump`) and trixie's own `ld.so`. The build fails if a library's package is missing from `packages.list`.
4. Writes `recovery/out/` (ignored by git), with the initramfs compressed with xz:
   - `vmlinuz`
   - `initrd.img`
   - `vintner-recovery-a.efi` and `vintner-recovery-b.efi`, with `.osrel` per ADR-0003.

## Kernel command line

| Parameter | Effect |
|---|---|
| `vintner.root=UUID=<fs uuid>` or `vintner.root=/dev/...` | Root to mount read-only. The UUID is the manifest's `root.uuid`. ext4 is mounted with `noload`, so no journal is replayed. Without this parameter, recovery takes `root.uuid` from the valid manifest copy with the highest `seq` on boot storage ([`schema/manifest.md`](../schema/manifest.md); valid here means line 2 is the SHA-256 of line 1). The UEFI recovery UKIs rely on this: they are shared by every installation, so their command line names no root. |
| `vintner.recovery_test=1` | Print `VINTNER-RECOVERY-TEST root=<absent\|mounted\|failed> ro=<yes\|no> logs=<path>` on `ttyS0` and power off. |

**Storage layers:**
- md arrays are assembled with `--readonly`.
- LVM volumes are activated with every volume group in `read_only_volume_list`.
- LUKS devices are opened with `--readonly`.

The root and every layer under it therefore stay byte-identical. A LUKS passphrase is asked for only when the root is not found without it, once per LUKS device, on both the screen and `ttyS0`; the first answer counts. An empty answer skips the device.

**Shells:** there is one shell on the screen (`tty1`) and one on `ttyS0`, each respawned. The busybox shell runs its own applets before `PATH`, and busybox has a `mke2fs` of its own; use `/usr/sbin/mke2fs` for the e2fsprogs one.

Boot storage is found as the vfat partition holding `/vintner`, because busybox has no `blkid` to read GPT type GUIDs.

## Test

```sh
bash recovery/tests/qemu-smoke.sh
```

**Needs:** additionally `fdisk mtools e2fsprogs qemu-system-x86 ovmf`.

**What it runs:**

- the image as a UKI under OVMF;
- the image as kernel + initrd under SeaBIOS;
- each with no root disk and with an ext4 root;
- once with a boot storage partition attached;
- LVM on LUKS, and md RAID1, under SeaBIOS.

The host has no device-mapper, so the recovery shell on `ttyS0` builds the LVM-on-LUKS and RAID1 disks itself, driven over a FIFO serial port. The LUKS test then answers the passphrase prompt over the same port. Every root and layer must stay byte-identical.

### Results (2026-10-15, TCG)

| Case | Result |
|---|---|
| uefi-no-root | PASS `root=absent` |
| uefi-root | PASS `root=mounted ro=yes` |
| bios-no-root | PASS `root=absent` |
| bios-root | PASS `root=mounted ro=yes` |
| bios-logs | PASS logs on `/dev/vdb1:/vintner/recovery-logs/...` |
| logs-on-disk | PASS |
| root-unchanged | PASS (sha256 of the ext4 image identical before and after) |
| provision | PASS (LVM on LUKS2 and RAID1 built by the recovery shell) |
| lvm-on-luks | PASS `root=mounted ro=yes` after the passphrase prompt |
| md-raid1 | PASS `root=mounted ro=yes` |
| layers-unchanged | PASS (sha256 of the three disk images identical before and after) |

## Sizes

Measured with `linux-image-6.12.107+deb13-amd64` 6.12.107-1 and `busybox-static` 1:1.37.0-6+b9:

| Artifact | Size |
|---|---|
| `vmlinuz` | 11.6 MiB |
| `initrd.img` | 10.0 MiB, xz (67 modules, the storage tools and their libraries) |
| each recovery UKI | 21.7 MiB |

Architecture §5 budgets 300 MiB per recovery image. `libcrypto` (6.3 MiB) is the largest addition. The initramfs is xz-compressed; with gzip it would be 14.2 MiB.

## Not yet included

- Btrfs, XFS and FAT repair tools; only ext4 has `e2fsck`.
- The rollback and diagnostics commands.
- Secure Boot signing of the UKIs.
