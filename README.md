# vintner-boot

A boot platform for Debian 13 and later on x86-64 UEFI and legacy BIOS machines. It provides unattended installation, a hidden boot menu, versioned kernel images with bounded boot attempts and automatic fallback, and an independent recovery image. Routine Debian package upgrades do not reconfigure any of it.

Status: the Go `vintner` tool (publisher, bless, reconcile, manifest, Secure Boot tier A), the BIOS and UEFI firmware assets and the recovery image are implemented and tested under QEMU. Debian packaging, the installer entry point and product integration are planned in [WO-0010](docs/work-orders/WO-0010-product-readiness.md).

- [Architecture and implementation plan](docs/architecture.md)
- [Architecture decision records](docs/adr/)
- [Local testing (WSL2, QEMU, VirtualBox)](docs/local-testing.md)
