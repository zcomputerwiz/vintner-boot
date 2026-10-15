# systemd-boot Hooks Lifecycle Experiment

## How to run
Run `./run.sh` inside the `ci/lifecycle/systemd-boot-hooks` directory. It requires QEMU, cloud-image-utils, mtools, fdisk, and standard build tools.
The script accepts the configuration argument, e.g., `./run.sh c2`.

## Setup
Images are downloaded from Debian cloud images repository.
Tested on Debian 13 genericcloud amd64. Packages used: systemd-boot, initramfs-tools, linux-image-amd64.

## Conclusion
In C2 and C3 (with layout=other and systemd-boot-update.service masked), standard systemd-boot and kernel hooks do not attempt to write boot entries or kernels to the ESP or XBOOTLDR, bypassing conflicts. C1 (stock) modifies the ESP heavily.
