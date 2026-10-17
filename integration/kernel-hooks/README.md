# Kernel Hooks

Hooks installed into Debian's `/etc/kernel/postinst.d/zz-vintner` that trigger the publisher (`vintner publish --kernel "$1"`, architecture §6). Thin POSIX `sh` shims, per the ADR-0002 recommendation (§16).

## Why No `postrm.d` Hook

There is intentionally no hook installed in `/etc/kernel/postrm.d`.

Published images are copies on boot storage, so removing a kernel package does not remove its image, and there is nothing for a removal hook to do. The next `vintner publish` flags an image whose module tree is gone as `stale` (step 12), and garbage collection (step 13) then keeps a `GOOD` image of a different ABI as the fallback (`schema/state-machine.md`). The `APT::NeverAutoRemove` list (step 14) keeps `apt autoremove` from removing the kernels of retained images in the first place.
