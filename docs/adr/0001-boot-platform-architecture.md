# ADR-0001: Boot platform architecture

- **Status:** Accepted
- **Date:** 2026-10-14
- **Details:** [`docs/architecture.md`](../architecture.md), revision 1.1

## Context

Debian's stock boot path lets routine package upgrades reinstall or reconfigure the bootloader, and keeps no verified known-good kernel to fall back to. vintner-boot needs installation, boot and recovery behaviour that survives routine Debian upgrades, on both UEFI and legacy BIOS x86-64 machines, starting with Debian 13.

## Decision

- **UEFI:** systemd-boot, installed by the project in `\EFI\vintner\`, booting signed UKIs from an XBOOTLDR boot storage partition with BLS boot counting. Entry selection is pinned by `loader.conf` and entry sort order (architecture §4.1).
- **BIOS:** minimal, statically configured GRUB 2 with `grubenv` trial/fallback state on the same plain, unencrypted boot storage partition.
- **Normal boot:** one Debian kernel and its initramfs-tools initramfs. No second kernel and no `kexec`.
- **Recovery:** an independently built recovery kernel and initramfs (a UKI on UEFI), updated only by an explicit, signed platform update.
- **Ownership:** platform payloads arrive through APT as inert files and are deployed only by `vintner platform-update`. The packages conflict with Debian packages whose maintainer scripts reinstall a bootloader (architecture §5).
- **Deferred:** Limine (evaluated after parity tests), ReactOS FreeLoader (reference only), whole-root A/B rollback (v2).

## Consequences

- Kernel rollback is not whole-OS rollback. Module-tree changes are detected rather than prevented in v1 (architecture §6.1).
- Unattended installation with Secure Boot works only where a key is pre-enrolled (tier A). Other machines need one MokManager confirmation (tier B) or accept reduced assurance (tier C) (architecture §8).
- Recovery-on-exhaustion depends on systemd-boot sort behaviour that upstream does not document. A CI test must prove it on every loader payload update.
