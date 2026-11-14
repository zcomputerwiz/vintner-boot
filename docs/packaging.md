# Debian packaging

What `debian/` builds and why (WO-0010 2.1 to 2.4). The packages build with `dpkg-buildpackage -us -uc -b` on Debian 13 (Go 1.24, standard library only). The Go build needs no network, but the payload stage runs in every build of this source package (`vintner-core` included) and downloads the recovery packages unless `VINTNER_RECOVERY_DIR` is set (see below).

## vintner-core (amd64)

| Path | What |
|---|---|
| `/usr/sbin/vintner` | the static binary (`CGO_ENABLED=0`, ADR-0002) |
| `/etc/kernel/postinst.d/zz-vintner` | kernel hook: `vintner publish --kernel` |
| `/etc/initramfs/post-update.d/zz-vintner` | initramfs hook: publish after an initramfs update |
| `/usr/share/initramfs-tools/hooks/plymouth-vintner` | puts the Plymouth theme into the initramfs (a package ships hooks here, not in `/etc/initramfs-tools/hooks/`) |
| `/usr/lib/systemd/system/vintner-bless.service` | blesses the boot after `boot-complete.target`; enabled for `basic.target`, not started by the postinst |
| `/etc/systemd/system/systemd-bless-boot.service`, `.../systemd-boot-update.service` | symlinks to `/dev/null` (masks) |
| `/etc/kernel/install.conf` | `layout=other` |
| `/usr/share/plymouth/themes/vintner-bgrt/` | the theme |

`Depends: sbsigntool, systemd-boot-efi, systemd-ukify`. `systemd-boot-efi` is there for the stub: `vintner publish` builds the images with `/usr/lib/systemd/boot/efi/linuxx64.efi.stub`, which `systemd-ukify` does not pull in (found by the lifecycle rows in WO-0010 2.5: a kernel install on a guest with `vintner-core` and no stub failed in the hook, and with it the dpkg configure of the kernel package). `systemd-boot-efi` carries no maintainer script, unlike `systemd-boot`, which `vintner-platform` conflicts with.

### How the two systemd-boot services stay inactive
`systemd-bless-boot.service` would remove the boot counter that vintner re-arms (ADR-0003); `systemd-boot-update.service` runs `bootctl update`. Both units exist only in Debian's `systemd-boot` package (checked against the trixie Contents index). The package ships `/etc/systemd/system/<unit>` as a symlink to `/dev/null`, which wins over the unit in `/usr/lib/systemd/system` if `systemd-boot` is installed later. `deb-systemd-helper unmask` in that package's postinst leaves them alone, because it removes only masks that it created itself (it needs its own state file). Removing `vintner-core` removes the symlinks and the units are stock again. `ci/package/install-test.sh` checks the masks, and that remove and purge take them away.

### /etc/kernel/install.conf
No Debian package ships this file (trixie Contents index), so `vintner-core` owns it. It is a conffile: an edit by the administrator survives upgrades and is kept on `remove`; `purge` deletes it.

## /etc/plymouth/plymouthd.conf is not shipped (D-E)
Debian's `plymouth` ships this file, and as a conffile (`dpkg-deb -e` of `plymouth_24.004.60-5`: it is in `conffiles`; its content is all comments). `vintner-core` used to ship its own, which made the two packages fight over one path. It no longer does. `vintner install --plymouth-theme` (WO-0010 3.2) will set `Theme`, `ShowDelay=0` and `DeviceTimeout=8` as an administrator edit.

What an upgrade of `plymouth` does to such an edit:
- reinstalling the same version keeps the edit and asks nothing (tested in `ci/package/install-test.sh`);
- a new `plymouth` version whose shipped file is unchanged also asks nothing, because dpkg only asks when the maintainer's file changed and the local file differs;
- if a future `plymouth` changes its shipped file, dpkg asks interactively; `unattended-upgrades` passes `--force-confold` by default, so it keeps the edit. I did not test this last case.

## vintner-platform (amd64)
`Conflicts: systemd-boot, grub-pc, grub-efi-amd64, shim-signed, grub-cloud-amd64, grub-efi-ia32, grub-coreboot, grub-xen, grub-ieee1275, refind` (architecture §5). apt resolves a conflict by removing the installed package, so installing one of them removes `vintner-platform`; `dpkg -i` refuses.

`Recommends: grub-common`: `vintner install` builds GRUB's `core.img` on a BIOS machine with `grub-mkimage` from that package, and refuses when it is missing. `grub-common` has no `update-grub` and no kernel hook (those are in `grub2-common`, architecture §5). A UEFI-only product does not need it and can install with `--no-install-recommends`.

It is `amd64`, not `all` as architecture §5 first had it: it carries x86-64 EFI binaries and i386-pc GRUB objects, which an `all` package must not.

### Payloads (WO-0010 2.4)
Everything is under `/usr/lib/vintner/payloads/`. The package has **no maintainer scripts** and no other path than that directory, `/usr/share/doc` and `/usr/share/lintian`, so installing, upgrading or removing it cannot write to the ESP, boot storage or NVRAM (`ci/package/install-test.sh` checks both). Deploying a payload is the installer's act (WO-0010 3.2).

| Path | Source | Used for |
|---|---|---|
| `uefi/systemd-bootx64.efi` | `systemd-boot-efi` (Build-Depends) | the loader, `\EFI\vintner\systemd-bootx64.efi` |
| `uefi/linuxx64.efi.stub` | `systemd-boot-efi` | the stub `ukify` puts before a kernel, for the recovery UKIs |
| `uefi/loader.conf` | `firmware/uefi/loader.conf` | `\loader\loader.conf` |
| `bios/i386-pc/` | `grub-pc-bin` (Build-Depends) | what `grub-mkimage -d` reads: `boot.img`, `kernel.img`, `diskboot.img`, `lzma_decompress.img`, `moddep.lst` and every module. None of GRUB's own programs is shipped, and the modules are not stripped (GRUB reads their symbol tables; `debian/vintner-platform.lintian-overrides`) |
| `bios/grub.cfg`, `bios/core-modules.txt` | `firmware/bios/` | the static configuration and the modules `core.img` is built with |
| `recovery/vmlinuz`, `recovery/initrd.img`, `recovery/versions.txt`, `recovery/version` | `recovery/build.sh --no-uki` | the recovery kernel and initramfs, the package versions they came from, and the recovery version string that `vintner install` records in the manifest |
| `VERSIONS` | | package versions of the two Debian sources and of the recovery packages |
| `MANIFEST` | | `sha256sum` of every other file; what "deploy and verify" checks against |

Three things are deliberately not shipped:
- **`core.img`.** Its early configuration names the boot storage by FAT UUID, which exists only on the target, so the installer builds it from `bios/i386-pc/` and `core-modules.txt` (`grub-mkimage` is in `grub-common`).
- **The recovery UKIs.** Tier A signs them with the owner's key, so the installer assembles them with `ukify` from `recovery/vmlinuz`, `recovery/initrd.img` and `uefi/linuxx64.efi.stub` (the command line is `console=tty0 console=ttyS0,115200 vintner.recovery=<a|b>`, as in `recovery/build.sh`, and `vintner install` adds `vintner.bootstorage=PARTUUID=<boot storage>`). A prebuilt unsigned UKI would be thrown away in tier A and cost 23 MiB each.
- **shim.** It belongs to WO-0010 Phase 5 (Secure Boot completion), which takes it from the pinned `shim-signed` `.deb`.

### Where the payload files come from, and what that costs
The build needs `systemd-boot-efi` and `grub-pc-bin` installed, and takes the files from them. The recovery kernel and initramfs come from `recovery/build.sh`, which **downloads** the Debian trixie packages it unpacks from `deb.debian.org`. That is a network access during the build, which Debian's own build daemons do not allow; it is acceptable here because this is not an archive package, and the alternative, Build-Depends on a kernel, busybox, cryptsetup, lvm2 and mdadm, would install the same trixie packages from the same archive. Set `VINTNER_RECOVERY_DIR` to a directory that already holds `vmlinuz`, `initrd.img`, `versions.txt` and `version` to build offline. The recovery initramfs holds the same static `vintner` as `vintner-core`, built from the same tree.

Licences: the payloads are unmodified Debian files and keep their upstream licences (`debian/copyright`); `VERSIONS` names the source packages for the corresponding source.

## Checks
- `debian-package` (lint workflow): build in a Debian 13 container, `lintian --fail-on error`. Overrides are in `debian/vintner-core.lintian-overrides` with their reasons.
- `build-debs` (`qemu-tests.yml`): the same build, for the QEMU rows to install from; see the last section.
- `package-install` (lint workflow, `ci/package/install-test.sh`): debootstrap a trixie chroot, install `vintner-core` before and after `plymouth`, check the files and masks, remove, purge, the `Conflicts`, that `vintner-platform` is inert and its `MANIFEST` verifies, and that its `Recommends: grub-common` brings `grub-mkimage` and no `update-grub`.

## The QEMU rows install from the packages (WO-0010 2.5)
`ci/lib/vintner-base.sh`, which builds the guest every row starts from, no longer copies the binary, the unit, the masks and `install.conf` into the guest by hand. It installs `vintner-core` with `apt-get install /root/vintner-core.deb` in a chroot of the mounted guest root, so the binary, both hooks, the Plymouth hook, the enabled unit, the masks and `/etc/kernel/install.conf` come from the package, and `systemd-ukify` and `sbsigntool` from the Debian archive through its `Depends`. The binary the host side of the rows runs (`$OUT/vintner`) is extracted from the same `.deb`. `ci/graphics` no longer copies the theme or the Plymouth hook either.

The two kernel and initramfs hooks come from the package too, but the base guest leaves them **off** (not executable), as they were before the rows used the package: the rows install kernels and other packages before the step they test, and an armed hook would publish a new generation at each of those (the first run of the lifecycle rows on the packages showed it: a kernel install on a guest with `vintner-core` published during the `apt-get` and changed what the rows count). `ci/publish`, `ci/publish-interrupt`, `ci/candidate-fallback` and `ci/graphics` arm the packaged hooks with `chmod` at the point where they used to copy them in. The lifecycle rows therefore still run with the hooks off; running them with the hooks armed means saying what an ordinary kernel install on a vintner system may change (new generations, nothing of the loader or recovery), and is open.

Where the packages come from:
- **CI:** the `build-debs` job of `qemu-tests.yml` builds them in a `debian:trixie` container once and hands them to every row as the artifact `vintner-debs`, through `VINTNER_DEBS`.
- **Locally:** `ci/local/run-all.sh` runs `ci/package/build-debs.sh` once before the qemu rows and sets `VINTNER_DEBS`. A row run by hand builds them itself into `ci/out/vintner-debs` when they are missing or were built from another tree (`SOURCE_TREE`); that needs a Debian 13 host with the build dependencies, otherwise it stops and says so.

Still by hand: `ci/integration/uefi-bless` and `ci/integration/bios-bless` (they do not use `vintner-base.sh`), and `/etc/plymouth/plymouthd.conf` in `ci/graphics` until `vintner install --plymouth-theme` exists (WO-0010 3.2).
