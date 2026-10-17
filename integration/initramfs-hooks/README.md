# Initramfs Hooks

Hooks installed into `/etc/initramfs/post-update.d/zz-vintner` so that initramfs-only regeneration publishes a new image (`vintner publish --kernel "$1"`, architecture §6), plus any initramfs-tools hooks the normal boot path needs (§4).

## Why No `postrm.d` Hook

There is intentionally no postrm hook for kernel/initramfs cleanup. The publisher's image-budget garbage collector (publish step 14) and APT retention file (`/etc/apt/apt.conf.d/50vintner-retained-kernels`) already handle ABI cleanup automatically: when an old kernel package is removed by APT, its boot image is preserved until either the budget forces it out or a subsequent publish run cleans it up. A postrm hook would only risk deleting the currently-running kernel's boot files if called during an interrupted upgrade.
