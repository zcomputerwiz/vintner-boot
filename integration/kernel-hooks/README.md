# Kernel Hooks

Hooks installed into Debian's `/etc/kernel/postinst.d/zz-vintner` that trigger the publisher (`vintner publish --kernel "$1"`, architecture §6). Thin POSIX `sh` shims, per the ADR-0002 recommendation (§16).

## Why No `postrm.d` Hook

There is intentionally no hook installed in `/etc/kernel/postrm.d`.

Published images are copies on boot storage, so removing a kernel package does not remove its image, and there is nothing for a removal hook to do. The next `vintner publish` flags an image whose module tree is gone as `stale` (step 12), and garbage collection (step 13) then keeps a `GOOD` image of a different ABI as the fallback (`schema/state-machine.md`). The `APT::NeverAutoRemove` list (step 14) keeps `apt autoremove` from removing the kernels of retained images in the first place.

## DKMS (WO-0010 5.3)

A DKMS build changes the module tree of a kernel that already has an image, and `vintner publish` treats that as a change: it publishes a new generation that records the tree ([`docs/secure-boot.md`](../../docs/secure-boot.md) §9.3). Three things make publish run:

- **A new kernel.** `/etc/kernel/postinst.d/dkms` runs before `initramfs-tools` and `zz-vintner` (`run-parts` order), so the modules exist when `zz-vintner` publishes.
- **`dkms install`, `remove`, `unbuild`, `uninstall`.** `integration/dkms/vintner-publish.conf` is a DKMS `framework.conf.d` drop-in with `post_transaction`, which runs `integration/dkms/dkms-post-transaction <kernelver>`. It does nothing while a kernel package's `prerm` or `postrm` runs, publishes only for the newest installed kernel (a new generation boots first, and a module change must not make an older kernel the default), and never fails DKMS: a failed publish is reported on stderr, and the health report shows the image as out of date.
- **A linux-headers package.** DKMS builds with `dkms autoinstall` there, and dkms 3.2.2 does not run `post_transaction` after `autoinstall`. `integration/header-hooks/zz-vintner` is installed as `/etc/kernel/header_postinst.d/zz-vintner`, which `linux-run-hooks` runs after DKMS's own hook in that directory. It runs the same script, so it never fails the headers package and also publishes only for the newest installed kernel.
