# Kernel Hooks

Hooks installed into Debian's `/etc/kernel/postinst.d/zz-vintner` that trigger the publisher (`vintner publish --kernel "$1"`, architecture §6). Thin POSIX `sh` shims, per the ADR-0002 recommendation (§16).

## Why No `postrm.d` Hook

There is intentionally no hook installed in `/etc/kernel/postrm.d`.

A kernel post-removal hook would need to identify which ABI was just removed, but by the time `postrm` executes, the kernel and initramfs files under `/boot` for that ABI are already gone. The publisher's image-budget garbage collector (publish step 14) and APT retention file (`/etc/apt/apt.conf.d/50vintner-retained-kernels`) already handle ABI cleanup automatically: when an old kernel package is removed by APT, its boot image is preserved until either the budget forces it out or a subsequent publish run cleans it up. A postrm hook would only risk deleting the currently-running kernel's boot files if called during an interrupted upgrade.
