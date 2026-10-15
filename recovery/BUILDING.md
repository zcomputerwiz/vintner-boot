# Building and testing the recovery image

The phase-1 recovery image (architecture §4 "Recovery", §9 phase 1, WP-006) gives you:

- a shell on the console and on `ttyS0`;
- the Debian root mounted read-only at `/mnt/root`;
- logs written to `/run/vintner/logs/<time>/`, and to `/vintner/recovery-logs/<time>/` on the boot storage partition.

It is built only from Debian trixie packages, never from the installed system (R06).

## Build

```sh
bash recovery/build.sh
```

**Needs:** `curl xz-utils dpkg cpio gzip kmod python3 python3-pefile`. No root.

**What it does:**

1. Unpacks the packages in `packages.list` from deb.debian.org.
2. Copies the dependency closure of `initramfs/modules.list`, decompressed so busybox `modprobe` can load it.
3. Writes `recovery/out/` (ignored by git):
   - `vmlinuz`
   - `initrd.img`
   - `vintner-recovery-a.efi` and `vintner-recovery-b.efi`, with `.osrel` per ADR-0003.

## Kernel command line

| Parameter | Effect |
|---|---|
| `vintner.root=UUID=<fs uuid>` or `vintner.root=/dev/...` | Root to mount read-only. The UUID is the manifest's `root.uuid`. ext4 is mounted with `noload`, so no journal is replayed. |
| `vintner.recovery_test=1` | Print `VINTNER-RECOVERY-TEST root=<absent\|mounted\|failed> ro=<yes\|no> logs=<path>` on `ttyS0` and power off. |

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
- once with a boot storage partition attached.

It checks that the root stays byte-identical.

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

## Sizes

Measured with `linux-image-6.12.107+deb13-amd64` 6.12.107-1 and `busybox-static` 1:1.37.0-6+b9:

| Artifact | Size |
|---|---|
| `vmlinuz` | 11.6 MiB |
| `initrd.img` | 6.0 MiB (49 modules) |
| each recovery UKI | 17.7 MiB |

Architecture §5 budgets 300 MiB per recovery image. The full WP-006 toolset (cryptsetup, LVM, filesystem repair tools, rollback) will add to this.

## Not yet included

- LUKS, LVM and md roots. These report `root=failed`.
- The rollback and diagnostics commands.
- Secure Boot signing of the UKIs.
