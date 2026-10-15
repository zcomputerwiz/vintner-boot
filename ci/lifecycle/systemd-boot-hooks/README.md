# systemd-boot Hooks Lifecycle Experiment

## How to run
Run `./run.sh` inside the `ci/lifecycle/systemd-boot-hooks` directory. It requires QEMU, cloud-image-utils, mtools, fdisk, and standard build tools.
The script accepts the configuration argument, e.g., `./run.sh c2`.

## Setup
Images are downloaded from Debian cloud images repository.
Tested on Debian 13 genericcloud amd64. Packages used: systemd-boot, initramfs-tools, linux-image-amd64.

## Conclusion
See "Finding" in [`results.md`](results.md). Even with `layout=other` and `systemd-boot-update.service` masked (C2, C3), installing the `systemd-boot` package writes to the ESP and NVRAM from its postinst. `vintner-platform` therefore lists `systemd-boot` as a `Conflicts` (architecture §5), and steps 2-7, which only exercise that package's hooks, were not re-run.
