# Debian packaging

What `debian/` builds and why (WO-0010 2.1 to 2.3). The packages build with `dpkg-buildpackage -us -uc -b` on Debian 13 (Go 1.24, no network, standard library only).

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

`Depends: sbsigntool, systemd-ukify`.

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

## vintner-platform (all)
`Conflicts: systemd-boot, grub-pc, grub-efi-amd64, shim-signed, grub-cloud-amd64, grub-efi-ia32, grub-coreboot, grub-xen, grub-ieee1275, refind` (architecture §5). apt resolves a conflict by removing the installed package, so installing one of them removes `vintner-platform`; `dpkg -i` refuses. The payloads this package is meant to carry (WO-0010 2.4) are not shipped yet.

## Checks
- `debian-package` (lint workflow): build in a Debian 13 container, `lintian --fail-on error`. Overrides are in `debian/vintner-core.lintian-overrides` with their reasons.
- `package-install` (lint workflow, `ci/package/install-test.sh`): debootstrap a trixie chroot, install `vintner-core` before and after `plymouth`, check the files and masks, remove, purge, and the `Conflicts`.
