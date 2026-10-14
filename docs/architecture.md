# Debian Boot Platform — Implementation Plan

**Status:** Architecture proposal, based on upstream documentation/source as reviewed October 14, 2026  
**Target:** Debian 13 (Trixie) onward, x86-64 UEFI and legacy BIOS  
**Primary objective:** Windows-like installation/boot/recovery reliability without coupling the machine's boot platform to routine Debian package upgrades.

## 1. Executive decision (ADR-001)

Create a **new project repository and Debian integration packages**. Do not begin with a deep ReactOS FreeLoader fork or a novel bootloader. Use **systemd-boot for UEFI**, **minimal, statically configured GRUB 2 for BIOS** in the first release, and abstract platform boot policy from both. Evaluate **Limine** as a replacement for BIOS GRUB (and eventually a shared firmware loader) only after boot-attempt semantics, signed-image handling, and crash consistency meet the same acceptance tests. Reuse ReactOS FreeLoader as a reference for BGRT/framebuffer features, not as the first production loader. Normal startup uses **one Debian kernel + its initramfs**, not a LinuxBoot → kexec → Debian two-kernel path. A separate independently maintained recovery Linux image is selected only when necessary.

This recommendation prioritizes reliable unattended rollback over theoretical common-loader elegance. The architecture must not depend on the upstream loaders remaining unmodified across Debian upgrades, and must not require writing custom firmware-level storage or EFI parsing code for the first product release.

## 2. Mission, scope, and definition of success

- Install automatically, detecting BIOS vs UEFI and preserving other operating systems where applicable.
- Boot the chosen Debian system without a visible menu in healthy normal use; expose a recovery menu when needed.
- Normal Debian package/kernel updates never replace, remove, or silently reconfigure the platform bootloader or the recovery environment.
- New kernels and matching initramfs images are published as **new, immutable/versioned artifacts**. A known-good image is retained until a candidate is verified and blessed.
- A boot that never reaches configured health criteria must not be marked successful. A finite number of attempts leads to a prior working image, then recovery; platform reset/watchdog behavior is explicitly tested.
- Independent recovery boots even with the Debian root filesystem missing, unmountable, or corrupted, provided boot storage and hardware still function.
- UEFI Secure Boot can be enforced through a demonstrable chain of trust; do not claim BIOS is equally secure without hardware-rooted verified boot.
- All update and recovery transitions are reproducible in QEMU and instrumented for power-cut testing.

**Non-goals for v1:** a new Linux kernel, general multiboot discovery, network package updates from early userspace, custom GUI bootloader, TPM auto-unlock, complete atomic `apt` OS rollback, custom filesystem readers, arbitrary root-file-system scan, replacing systemd, or reproducing Microsoft proprietary components.

## 3. Requirements and prioritization

### P0 — absolute requirements (release blockers)

| ID | Requirement | Concrete acceptance |
|---|---|---|
| R01 | Supported Debian installation | Unattended installation on fresh UEFI/GPT and BIOS/GPT VMs; coexisting EFI entries not destroyed |
| R02 | Bootstrap autonomy | Loader and recovery have project-owned paths and are not managed by normal Debian kernel package hooks |
| R03 | Normal update interoperability | `apt upgrade`, `apt full-upgrade`, `dpkg-reconfigure` kernel, `update-initramfs -u` stage valid images without changing protected loader/recovery hashes |
| R04 | Bootable version retention | At least one known-good kernel+initramfs+matching module tree retained, with no in-place overwrite of the active artifact |
| R05 | Failure recovery | Exhausted trial count returns to known-good; a missing or broken root can boot recovery |
| R06 | Recovery independence | Independent kernel/initramfs/driver set, not generated from the currently installed Debian kernel |
| R07 | Trust verification | On UEFI Secure Boot test systems, unsigned/altered loader, UKI, and recovery image rejected; keys and chain documented |
| R08 | Crash consistency | Power-loss injection cannot leave both active and fallback images invalid or erase the recovery route |
| R09 | Deterministic selection | System selects configured root/image by stable identity and does not indiscriminately boot an arbitrary detected OS |
| R10 | Boot visibility | On success, no boot menu and no avoidable text; on failure, errors available on console/serial and via recovery logs |
| R11 | Key ownership | Signing private keys are not embedded in loader, initramfs, or writable ESP; signing/update threat model recorded |
| R12 | Supportable build | Pinned upstream references, reproducible CI artifacts, source license inventory, signed release process |
| R13 | Alternate loader recovery | Backup EFI boot route/removable media guidance (UEFI) and BIOS boot-sector repair procedure |
| R14 | Bounded retry semantics | Explicit trial/good/bad/recovery states on **both** firmware backends; power cycles and hung systems included |

### P1 — desirable for initial stable release

- A single high-level configuration schema and common status/reporting commands across BIOS/UEFI.
- Consistent OEM-logo splash on UEFI using BGRT plus Plymouth; predictable fallback on BIOS/headless machines.
- Opt-in host-specific initramfs optimization, with general-purpose rescue image preserved.
- Automatic diagnosis of broken kernel, root UUID mismatch, failed filesystem checks, and exhausted boot attempts.
- Manual rollback and one-shot recovery invocation from running Debian.
- Accessibility of boot menu, serial console support, key-press menu override.

### P2 — later expansion

- Whole-system A/B root slots or transactional Btrfs snapshots, including coordinated kernel/root rollback.
- dm-verity authenticated root images and TPM2 measurements/sealing with key rotation/recovery.
- Network repair, remote diagnostics, firmware update workflow, optional WinRE-like graphical recovery.
- Common Limine backend or dedicated frontend if it demonstrably reduces code and remains secure.
- Automated TPM/watchdog-assisted hang recovery and anti-rollback policies.

## 4. Boot architecture

```
UEFI firmware ---------------------- BIOS firmware
       |                                  |
 project-controlled systemd-boot     project-controlled GRUB 2
       |                                  |
 BLS Type #2 UKI entries            fixed/controlled BIOS Linux entries
       |                                  |
       +----------- OS choice ------------+
                     |
     +---------------+----------------+
     |                                |
 Normal Debian kernel + initramfs     Recovery kernel + independent initramfs
     |                                |
 root verification / mount           local diagnostics / repair / restore
     |
 switch_root to Debian root
     |
 systemd boot-complete checks -> bless selected image
```

**UEFI:** signed UKIs built with `ukify`, staged under versioned names on a sufficiently sized FAT32 EFI System Partition. `systemd-boot` implements UAPI Boot Loader Specification Type #2 entry discovery and boot-counting/automatic fallback. Maintain an independently bootable recovery UKI. The project owns installation path and update policy, even if the binary originally came from a Debian package.

**BIOS:** conventional `bzImage` and initramfs in versioned slots, with GRUB 2 in a stable location, a minimal non-generative config, and a tested `grubenv`-driven trial/fallback policy (RAUC supplies working reference patterns). BIOS/GPT requires a BIOS Boot Partition, or BIOS/MBR a suitable boot-embedding strategy. The BIOS disk layout should not rely on UEFI-only UKIs. Keep recovery kernel and initramfs in a separate boot-storage location accessible without Debian root.

**Normal early userspace:** reuse Debian kernel and ordinary initramfs plumbing. Only add a small root-selector and health/identity metadata reader if necessary. No separate Linux kernel or `kexec` in the common path.

**Recovery:** a separately built, project-signed UKI on UEFI; separate Linux kernel/initramfs artifacts on BIOS. Can mount Debian read-only, inspect damaged filesystems, inspect published boot images, revert selection, and perform offline repairs with explicit consent. Recovery boot does not depend on the target OS's `/usr`, package database, modules, or fstab.

**Graphics:** `systemd-stub`/firmware splash may render early image; Plymouth handles continuing kernel/initramfs/userland animation and unlock prompts. Try OEM BGRT logo preservation. Boot-manager spinner is optional: once firmware hands over to a Linux kernel the boot manager cannot keep animating on its own. A short discontinuity is an acceptable v1 tradeoff if the framebuffer remains stable.

## 5. Installer and disk ownership

Suggested single-disk x86-64 GPT layout for new installations:

| Partition | Role | Initial recommendation |
|---|---|---|
| BIOS Boot | Small unformatted GRUB embed area | ~2 MiB; used only on BIOS/GPT |
| ESP | UEFI loader, normal UKIs, recovery UKI | ~2 GiB or provision based on image-budget calculation; FAT32 |
| Independent boot/recovery storage | Optional, especially for BIOS or large recovery payloads | Supported simple filesystem; keep distinct from Debian root |
| Debian root | Standard Debian filesystem | ext4 in v1; optional Btrfs later |
| Data | Optional separate persistent state | `/home` or `/var` policy decided at install |

For mixed-firmware lab testing, one GPT can contain both a BIOS Boot Partition and an ESP. For production BIOS-only installations, the ESP is not required for firmware itself, but can serve as a common staging location if carefully tested. In-place conversions must never repartition or resize an existing Windows ESP without explicit approval. Preserve EFI boot entries. Supply external USB recovery as a separate-disk failure contingency.

Partition labels are for humans; boot configuration should use stable UUID/PARTUUIDs or explicitly resolved device identities. A disk replacement or clone must not accidentally select the wrong root.

Installer choice: **Debian Installer preseed with late commands and custom installer package**, then investigate an installer component/u-deb for native integration. Before disabling the stock GRUB installer, confirm the project loader and recovery are successfully installed; on errors, retain an ordinary Debian boot path and report installation incomplete. Document preseed variables but don't assume generic `grub-installer/skip` is sufficient across all d-i versions without an end-to-end test.

Debian package boundaries:

- `dbp-installer`: guided/automated installation integration, partition setup, firmware probe, boot entry registration.
- `dbp-core`: common boot metadata schema, manifest validation, state model, CLI and API.
- `dbp-publisher`: Debian kernel/initramfs hooks, image build/verify/sign, atomic staging and garbage collection.
- `dbp-health`: systemd boot assessment/blessing and health checks.
- `dbp-recovery-build`: separately versioned and patched recovery Linux image build.
- `dbp-platform-update`: **explicit** signed updates of loader and recovery, independent of APT's routine OS update hooks.
- `dbp-uefi` and `dbp-bios`: adapters/artifacts for the firmware-specific bootstrap methods.

Package manager interactions: avoid competing GRUB EFI/systemd-boot/shim maintainer-script installation paths that target our ESP entries. Merely running `apt-mark hold` or mounting ESP read-only is not a complete ownership mechanism. Use project-owned directories and boot NVRAM identifiers, lifecycle tests, and a dedicated publication workflow. APT may update the userspace publisher itself, but it must not replace platform binaries except by an explicit signed platform update.

## 6. Boot image publishing protocol

States (semantic, shared across firmware): `GOOD`, `STAGED`, `TRYING`, `FAILED`, `RECOVERY`. A new candidate is never visible as selectable until its complete contents have been written, verified, signed and synchronized.

1. Debian creates a new `vmlinuz-<ABI>` and matching initramfs; publisher is triggered by relevant kernel **and** initramfs hooks. Queue or coalesce updates so repeated/overlapping invocations do not race.
2. Resolve and record the exact kernel ABI and module tree. Validate the root identity and boot arguments.
3. Create an immutable, versioned UKI (UEFI) or complete kernel+initramfs slot (BIOS). Write to a staging location, check signature or hashes, size, dependency metadata, and on-disk readback as appropriate.
4. Flush files and directory metadata in a documented order. Publish by a same-filesystem atomic rename or a state-record commit; validate behavior under FAT filesystem limitations and abrupt power loss.
5. Set candidate as trial with a finite number of attempts (initial default: 3, configurable). Existing good entry and independent recovery are unaffected.
6. Boot manager consumes one attempt on each trial boot. On successful completion of configured systemd target/health tests, the userspace health service marks it `GOOD`.
7. Exhausted attempts select previous `GOOD`; repeated failure of all ordinary entries selects recovery (or a visible recovery choice if hardware cannot reliably reboot unattended).
8. Garbage-collect old images only after at least one independently boot-tested good image remains; never garbage-collect recovery in the OS image publisher.

Kernel rollback here is **not full Debian root rollback**. A broken `/usr`, critical package migration, or incompatible `/lib/modules` state could remain broken after selecting an old kernel. Release notes and test results must make this explicit. For true whole-OS rollback, expand to A/B root slots or an atomic snapshot/update system, always pairing root image and kernel ABI.

Use `kernel-install` where its conventions fit, and Debian's `/etc/kernel/postinst.d`, `/etc/kernel/postrm.d`, `/etc/initramfs/post-update.d` hooks. Hook ordering, kernel removals, `dpkg` interruptions, initramfs-only upgrades, and DKMS updates must be in CI. Avoid allowing an old Debian kernel package post-removal to remove the sole last-good artifact.

## 7. Project-by-project adoption decisions

| Project | Confirmed relevant feature | Decision / exact reuse |
|---|---|---|
| **systemd-boot** | UEFI loader, BLS Type #2 UKIs, boot counting, EFI boot-loader interface | **Adopt UEFI v1**; keep its binary project-controlled on ESP; use boot-count/entry semantics |
| **systemd kernel-install / ukify / systemd-stub** | Extensible installation plugins, UKI generation, EFI stub, signing pipeline | **Adopt**, with Debian-specific publisher and explicit artifact verification |
| **Debian initramfs-tools** | Standard Debian kernel and root setup plus update hooks | **Adopt first**, avoid new early-userspace stack prematurely |
| **dracut** | Host-only dependency-focused image generation, modular early init | **Evaluate opt-in** after compatibility tests; do not make a fragile host-only image the sole recovery path |
| **GRUB 2** | Mature BIOS x86 loading, configurable environment blocks | **Adopt BIOS v1** in minimal static form; derive trial/fallback from RAUC GRUB examples |
| **Limine** | BIOS+UEFI Linux and EFI protocols; BGRT option; measured and config-hash boot enforcement | **Prototype candidate** for replacing BIOS GRUB; upstream config hash tied to loader conflicts with changing signed config and must be addressed |
| **ReactOS FreeLoader** | BIOS Linux loader, UEFI GOP/BGRT graphics, menu UI | **Study, don't fork for v1**; UEFI Linux dispatcher is absent; NT-specific code and ReactOS build coupling add cost |
| **ReactOS GOP spinner PR #8831** | Reference for kernel-side GOP animation and handoff | **Design reference only**; closed unmerged, requires ReactOS kernel facilities |
| **SYSLINUX/EXTLINUX** | Compact BIOS Linux loader/config | **Reference or fallback only**; fewer advantages than GRUB on broad boot/media/recovery support |
| **LinuxBoot** | Boot Linux as firmware/early loader | **Architectural reference**; BIOS/UEFI PC firmware replacement is unnecessary and highly hardware-dependent |
| **Heads** | Independent verification/recovery Linux, TPM and signed boot manifests | **Security/recovery reference**; not a drop-in generic PC loader |
| **Petitboot** | Kernel-backed discovery service/UI and kexec selection | **Recovery discovery reference**; reject two-kernel ordinary boot |
| **u-root** | One-binary Go initramfs and boot utilities | **Alternative recovery prototype** if smaller/static recovery tooling is preferable; audit runtime size and kexec assumptions |
| **U-Boot** | Verified-image manifest design and bootcount/fallback logic | **Policy/reference only** on ordinary x86 PCs; do not port whole U-Boot firmware stack |
| **RAUC** | Formal slot state/health API, EFI/GRUB/custom backends | **Adopt concepts and likely v2 implementation** if moving to full root A/B; v1 can implement a smaller backend |
| **systemd-sysupdate** | Coordinated versioned partition/file updates | **Evaluate v2** for image-managed OS; it is not a generic drop-in replacement for APT on a mutable Debian root |
| **BLS / Automatic Boot Assessment** | Type #1/#2 entries and tries-left/done filename conventions; successful boot workflow | **Adopt as UEFI source of truth**, define equivalent BIOS state operations; do not claim all bootloaders support it automatically |
| **shim / Debian Secure Boot** | Signed bootstrap, embedded distro trust, MOK | **Adopt as trust reference/integration option**; fully verify the signed next-stage and `LoadImage()` semantics; MOK is not the same as firmware `db` |
| **Plymouth** | Kernel/userspace animated splash and unlock UI | **Adopt**, favor supported BGRT-style theme over custom bootloader animation |
| **dm-verity** | Kernel block-integrity verification for read-only root filesystem | **Optional v2** with signed root hash and read-only system images; not directly compatible with unrestricted writable root |

**Licensing:** Limine is BSD-2-Clause, ReactOS/FreeLoader is GPL-2.0 family, and other dependencies carry distinct licenses. Reference algorithms/designs freely where permitted; if code is copied, comply with copyright and license obligations; do not blindly amalgamate GPL sources into a proprietary loader.

## 8. Security architecture and explicit constraints

- **UEFI chain:** firmware trust database → signed loader (possibly through Debian shim) → trusted UKI/recovery image → optional authenticated root. Test image-loading trust **end to end**, not only firmware acceptance of first executable.
- **MOK constraint:** a key trusted by shim/MOK is not necessarily accepted by an arbitrary UEFI firmware `LoadImage()` call. Use a supported, tested chain or provision custom certificate in firmware `db` for controlled pilot hardware. Turnkey Secure Boot on arbitrary factory-key-only machines requires a separately validated signing/distribution strategy. Do not silently disable Secure Boot.
- **Signing key storage:** keep keys off ESP. Unattended signing from mutable Debian root is operationally convenient but trusts that root; hardware-backed or offline signing is a later higher-assurance choice.
- **Recovery freshness:** protected does not mean permanently unpatched. Recovery receives *separate* signed updates with its own A/B backup and an independent rescue path.
- **BIOS trust limit:** ordinary BIOS offers no firmware Secure Boot root of trust; file hashes stored on writable media do not establish tamper resistance. Treat BIOS as reliability-first unless equipped with a separate hardware trust arrangement.
- **Metadata threat model:** unsigned configuration must not alter a verified root/hash target or bypass enforcement. Untrusted disk labels, paths, and operator-selected recovery features need explicit handling.
- **FAT/ESP:** read-only normal mount limits accidental writes, not root malware; evaluate power loss and metadata integrity rather than calling FAT renames fully transactional.
- **Encryption:** first release may support LUKS root via established initramfs utilities. Recovery unlocking requires explicit user action unless a defensible TPM policy is designed; don't put unsealed secrets in ESP.
- **Root integrity:** UEFI Secure Boot of a UKI does not authenticate a mutable Debian root filesystem; use dm-verity later for full chain and document the difference.

## 9. Phases and exit gates

| Phase | Deliverables | Exit gate |
|---|---|---|
| **0 — upstream baselines** | Pinned Debian 13, systemd-boot, GRUB, Limine, FreeLoader references; license/SBOM notes; QEMU/OVMF/SeaBIOS harness | Reproducible ordinary Debian boot under both firmwares; upstream behavior recorded |
| **1 — independent boot platform** | Project-owned boot paths; hidden UEFI/BIOS menus; working signed/unsigned as appropriate recovery image; installer integration | Fresh installation and deliberately corrupted Debian root can still select recovery; ordinary package ops leave loader+recovery hashes unchanged |
| **2 — versioned image publication** | UKI generator + BIOS kernel/initrd slots, exclusive lock, image verifier, postinst/initramfs adapters | Kernel upgrade and initramfs-only regeneration create a bootable candidate, preserving known-good |
| **3 — trial, blessing, fallback** | Cross-backend slot API; UEFI BLS counter; GRUB env trial logic; health gate | Inject failed kernel, incomplete boot, forced power cycle and hung boot; correct fallback after bounded attempts |
| **4 — Secure Boot/robustness** | Controlled key enrollment, signed chain, recovery signing, revocation plan; power-cut tests | Tampered images rejected; 100% of scripted publication interruption points preserve bootable fallback/recovery in tested configurations |
| **5 — installer and compatibility** | Preseed integration, upgrade audits, BIOS/GPT+UEFI/GPT + existing ESP test cases, documentation | Installer fully configures system, upgrade matrix passes without modifying platform infrastructure |
| **6 — graphics and performance** | Plymouth BGRT theme, early framebuffer behavior, rescue accessibility, boot timings | No avoidable menu/console flash on reference UEFI GPUs; cold-boot timing and failure UX documented |
| **7 — transactional OS (optional)** | RAUC or sysupdate evaluation, dual root slots/snapshots, dm-verity optional | Deliberately broken entire Debian userspace rolls back to previous root+kernel pair |

**Gate priority:** 1 → 2 → 3 before splash polishing. If BIOS/UEFI parity becomes a blocker, ship UEFI as a supported release first and classify BIOS as experimental until phase 3 passes, rather than weakening rollback guarantees.

## 10. Test plan and CI matrix

Matrix axes:

- Firmware: OVMF UEFI without Secure Boot, OVMF UEFI with Secure Boot, SeaBIOS legacy BIOS.
- Storage: GPT ESP + BIOS Boot Partition, MBR BIOS (if supported), existing mixed Windows ESP, single disk and two disks.
- Root: ext4/plain first; LUKS, LVM, Btrfs and Secure Boot combinations behind distinct tested features.
- Kernel lifecycle: new ABI install, same ABI initramfs rebuild, old kernel removal, DKMS rebuild, APT interrupted mid-transaction, disk full.
- Failure injection: candidate missing, hash mismatch, broken command line, malformed image, missing modules, root absent, filesystem damaged, before/after metadata commits, reboot/power cut during publication, repeated crash, permanent hang with/without watchdog.
- Recovery: menu invocation, automatic fallback, mount root read-only, stage prior known-good, recovery independently boots if `/usr` unavailable.
- Security: signed/untrusted UKI, revoked certificate simulation, MOK-vs-db trust behavior, tampered fallback, unsigned config changes, image downgrade/rollback policy, malicious disk labels/paths.
- Graphics: Intel/AMD/NVIDIA-relevant modes, firmware BGRT present/absent, GOP simple framebuffer to KMS transition, encrypted root prompt, serial/headless fallback.

CI should record serial logs, QEMU exit reason, UEFI variables, BLS entry changes, GRUB environment state, root selection, image hashes, and mounted root identity. Add deterministic filesystem image fault-injection harnesses. No hardware experiments until virtual no-loss upgrade/recovery tests pass.

## 11. Repository outline

```text
debian-boot-platform/
├── docs/
│   ├── architecture.md
│   ├── security-threat-model.md
│   ├── accepted-platforms.md
│   └── adr/
├── third_party/               # pinned refs/manifest, not unchecked copies
├── firmware/
│   ├── uefi/                   # systemd-boot deployment and BLS
│   ├── bios/                   # minimal GRUB cfg and grubenv interface
│   └── experimental-limine/
├── policy/
│   ├── schema/
│   ├── state-machine/
│   └── backend-interface/
├── image-builder/
│   ├── uki/
│   ├── bios-payload/
│   └── verification/
├── debian/
│   ├── packages/
│   ├── kernel-hooks/
│   ├── initramfs-hooks/
│   ├── systemd-units/
│   └── installer/
├── recovery/
│   ├── kernel-config/
│   ├── initramfs/
│   ├── tools/
│   └── tests/
├── graphics/
│   └── plymouth-theme/
├── update/
│   ├── publisher/
│   ├── platform-updater/
│   └── transaction-tests/
├── ci/
│   ├── qemu-ovmf/
│   ├── qemu-seabios/
│   └── fault-injection/
└── examples/
    ├── disk-layouts/
    └── configs/
```

Backend interface (semantics, not final API signatures): `list_images`, `publish_candidate`, `set_trial(image, tries)`, `get_booted_image`, `mark_good`, `mark_bad`, `set_recovery_next_boot`, `get_platform_health`. Represent logical `GOOD`/`TRYING` states at this layer; let systemd-boot and GRUB implement their own persistent formats. Recovery is separately protected from normal publisher access.

## 12. Key risks / decisions still to verify

1. **UEFI Secure Boot with Debian shim and custom images:** actual downstream trust policy must be tested under the shipping Debian 13 shim/systemd-boot binary combination. MOK does not automatically guarantee generic EFI image acceptance. Pin a lab test matrix before any release claim.
2. **Limine updater conflict:** config hash embedded into loader could force loader resigning for each changing kernel path/hash. A static fixed-slot config and separate signed manifest may work, but is a design change requiring analysis. No Limine trial-counter protocol equivalent to systemd-boot's is assumed.
3. **GRUB BIOS state robustness:** `grubenv` write location, retry atomicity, filesystem constraints, and fallback must be verified with abrupt resets; RAUC example supplies baseline logic only.
4. **Kernel/module coherence:** prior kernel rollback with mutable root may fail after `apt` removes modules. Retain matching module ABI or graduate to full root snapshots/slots.
5. **Recovery longevity:** independent recovery on same disk does not survive complete disk failure; offer external rescue media and documented firmware-entry reconstruction.
6. **Unattended reboot on hang:** boot counter cannot advance if a hung machine never restarts; watch the availability of watchdogs and document fallback limitations.
7. **Windows dual boot:** some firmware resets boot order or Windows servicing adjusts ESP entries; never overwrite `\EFI\Microsoft\Boot` or automatic Windows boot entries. Firmware/NVRAM resilience tests required.
8. **Boot platform safety vs freezes:** immutable baseline avoids accidental changes but may accumulate vulnerabilities and revocations. Signed, rollback-safe, intentionally invoked platform updates are required.

## 13. Immediate engineering work packages

- **WP-001:** build reproducible Debian 13 images and QEMU/OVMF+SeaBIOS test harness; capture baseline GRUB and systemd-boot results.
- **WP-002:** define signed image manifest schema, explicit partition layout constraints, and backend-independent state machine. Produce architecture decision records.
- **WP-003:** prototype UEFI UKI publication with systemd-boot counting and recovery UKI; demonstrate failure fallback without modifying installed Debian root.
- **WP-004:** prototype BIOS minimal GRUB config and grubenv trial/fallback; verify known-good kernel modules retained.
- **WP-005:** implement Debian postinst/postrm/initramfs hooks, lock, coalescing and publish transaction; write package ownership tests.
- **WP-006:** build independent recovery Linux image with root diagnostics, rollback command, and logging; verify unavailable `/usr` case.
- **WP-007:** installer integration in Debian Installer and upgrade lifecycle audit; verify no cross-package overwrites on ESP.
- **WP-008:** Secure Boot test matrix, threat model, key provisioning procedure, and recovery revocation rehearsal.
- **WP-009:** BGRT/Plymouth polish, boot metrics, accessible recovery UX.
- **WP-010:** isolated Limine experiment against baseline: BIOS/UEFI boot, secure configuration update lifecycle, fallback, and recovery. Promote only if parity is shown.

## 14. Source index (upstream references)

- ReactOS FreeLoader code: https://github.com/reactos/reactos/tree/master/boot/freeldr/freeldr ; BGRT: https://github.com/reactos/reactos/blob/master/boot/freeldr/freeldr/arch/uefi/uefivid.c ; experimental spinner https://github.com/reactos/reactos/pull/8831
- Limine v12 usage/security: https://github.com/limine-bootloader/limine/blob/v12.x/USAGE.md ; configuration: https://github.com/limine-bootloader/limine/blob/v12.x/CONFIG.md
- systemd-boot: https://manpages.debian.org/trixie/systemd-boot/systemd-boot.7.en.html
- Automatic boot assessment: https://github.com/systemd/systemd/blob/main/docs/AUTOMATIC_BOOT_ASSESSMENT.md
- Boot Loader Specification: https://uapi-group.org/specifications/specs/boot_loader_specification/
- kernel-install Debian 13: https://manpages.debian.org/trixie/systemd/kernel-install.8.en.html
- Debian UKI: https://wiki.debian.org/UKI
- Debian Secure Boot: https://wiki.debian.org/SecureBoot
- Debian kernel hooks: https://kernel-team.pages.debian.net/kernel-handbook/ch-update-hooks.html
- Debian Installer Trixie preseed: https://www.debian.org/releases/stable/amd64/apbs04.en.html
- Debian Plymouth: https://wiki.debian.org/plymouth
- dracut hostonly: https://github.com/dracut-ng/dracut/blob/main/man/dracut.8.adoc
- Petitboot: https://open-power.github.io/petitboot/overview.html
- LinuxBoot overview: https://book.linuxboot.org/
- Heads architecture: https://github.com/linuxboot/heads/blob/master/doc/architecture.md
- u-root: https://github.com/u-root/u-root
- U-Boot verified boot: https://docs.u-boot.org/en/latest/usage/fit/verified-boot.html
- U-Boot bootcount: https://docs.u-boot.org/en/v2026.04/api/bootcount.html
- RAUC integration: https://github.com/rauc/rauc/blob/master/docs/integration.rst
- RAUC GRUB example: https://github.com/rauc/rauc/blob/master/docs/examples.rst
- systemd-sysupdate: https://github.com/systemd/systemd/blob/main/man/systemd-sysupdate.xml
- dm-verity: https://docs.kernel.org/admin-guide/device-mapper/verity.html

## 15. Final go/no-go decision

The project's first release should be judged on unattended installation, autonomous recovery, image publication, and resistance to routine Debian updates—not whether it uses a single bootloader for both firmware families or perfectly imitates the Windows dots. **Go** if phases 0–5 satisfy the acceptance gates. **No-go** if kernel updates can invalidate recovery, if Secure Boot trust is unverified, if boot counting is only simulated in userspace, or if an interrupted update can erase the last good boot path.
