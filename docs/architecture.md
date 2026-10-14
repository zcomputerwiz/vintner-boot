# vintner-boot — Debian Boot Platform Implementation Plan

**Status:** Architecture baseline, revision 1.1 (amended after review, October 14, 2026; see §17 for the change log)  
**Target:** Debian 13 (Trixie) onward, x86-64 UEFI and legacy BIOS  
**Primary objective:** Windows-like installation/boot/recovery reliability without coupling the machine's boot platform to routine Debian package upgrades.  
**Naming:** the project is `vintner-boot`; Debian packages, boot entries, ESP directories and NVRAM labels use the `vintner` prefix.

## 1. Executive decision (ADR-001)

Create a **new project repository (`vintner-boot`) and Debian integration packages**. Do not begin with a deep ReactOS FreeLoader fork or a novel bootloader. Use **systemd-boot for UEFI**, **minimal, statically configured GRUB 2 for BIOS** in the first release, and abstract platform boot policy from both. Evaluate **Limine** as a replacement for BIOS GRUB (and eventually a shared firmware loader) only after boot-attempt semantics, signed-image handling, and crash consistency meet the same acceptance tests. Reuse ReactOS FreeLoader as a reference for BGRT/framebuffer features, not as the first production loader. Normal startup uses **one Debian kernel + its initramfs**, not a LinuxBoot → kexec → Debian two-kernel path. A separate independently maintained recovery Linux image is selected only when necessary.

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
| R01 | Supported Debian installation | Unattended installation on fresh UEFI/GPT and BIOS/GPT VMs; coexisting EFI entries not destroyed; an existing ESP is reused without resizing. With Secure Boot enforced, unattended installation is required only on Secure Boot tier A (§8) |
| R02 | Bootstrap autonomy | Loader and recovery have project-owned paths and are not managed by normal Debian kernel package hooks |
| R03 | Normal update interoperability | `apt upgrade`, `apt full-upgrade`, `dpkg-reconfigure` kernel, `update-initramfs -u` stage valid images without changing protected loader/recovery hashes |
| R04 | Bootable version retention | At least one known-good kernel+initramfs+matching module tree retained, with no in-place overwrite of the active artifact; the manifest records each image's module-tree identity, and a retained image whose module tree changed or disappeared is reported and not selected silently (§6.1) |
| R05 | Failure recovery | Exhausted trial count returns to known-good; a missing or broken root can boot recovery |
| R06 | Recovery independence | Independent kernel/initramfs/driver set, not generated from the currently installed Debian kernel |
| R07 | Trust verification | On UEFI Secure Boot test systems, unsigned/altered loader, UKI, and recovery image rejected; keys and chain documented |
| R08 | Crash consistency | Power-loss injection cannot leave both active and fallback images invalid or erase the recovery route |
| R09 | Deterministic selection | System selects configured root/image by stable identity and does not indiscriminately boot an arbitrary detected OS; foreign entries in the shared BLS directories and a persisted menu-default override are detected and reported (§4.1) |
| R10 | Boot visibility | On success, no boot menu and no avoidable text; on failure, errors available on console/serial and via recovery logs |
| R11 | Key ownership | Signing private keys are not embedded in loader, initramfs, writable ESP or boot storage; signing/update threat model recorded |
| R12 | Supportable build | Pinned upstream references, reproducible CI artifacts, source license inventory, signed release process |
| R13 | Alternate loader recovery | Backup EFI boot route/removable media guidance (UEFI) and BIOS boot-sector repair procedure; `\EFI\BOOT\BOOTX64.EFI` is written only when no other operating system uses the ESP |
| R14 | Bounded retry semantics | Explicit trial/good/bad/recovery states on **both** firmware backends; power cycles and hung systems included; BIOS trial state lives only on storage GRUB can write (§4) |

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

**UEFI:** signed UKIs built with `ukify`, staged under versioned names in `/EFI/Linux/` on the **boot storage partition**, an XBOOTLDR partition (§5). `systemd-boot` reads Type #2 entries from both the ESP and XBOOTLDR, so the ESP only needs to hold the loader and can be an existing small (Windows-created) ESP. `systemd-boot` implements UAPI Boot Loader Specification Type #2 entry discovery and boot-counting/automatic fallback. Maintain an independently bootable recovery UKI. The project owns installation path (`\EFI\vintner\`) and update policy, even if the binary originally came from a Debian package.

**BIOS:** conventional `bzImage` and initramfs in versioned slots, with GRUB 2 in a stable location, a minimal non-generative config, and a tested `grubenv`-driven trial/fallback policy (RAUC supplies working reference patterns). BIOS/GPT requires a BIOS Boot Partition, or BIOS/MBR a suitable boot-embedding strategy. The BIOS disk layout should not rely on UEFI-only UKIs. Keep recovery kernel and initramfs in a separate boot-storage location accessible without Debian root. **The slots, recovery images and `grubenv` live on the plain, unencrypted boot storage partition, never on the Debian root:** GRUB writes its environment block only on a plain disk (no LVM or RAID), on a non-checksumming filesystem (no Btrfs or ZFS) and without encryption, so a `grubenv` on a LUKS, LVM or Btrfs root would silently disable BIOS boot counting.

**Health and blessing:** health checks are systemd units ordered `Before=boot-complete.target` and pulled in by it (`RequiredBy=`/`WantedBy=` per check). On UEFI, upstream `systemd-bless-boot.service` marks the entry good after `boot-complete.target` is reached. On BIOS, a `vintner` twin service with the same ordering clears the trial state in `grubenv`. Both backends therefore share one definition of "successful boot".

**Normal early userspace:** reuse Debian kernel and ordinary initramfs plumbing. Only add a small root-selector and health/identity metadata reader if necessary. No separate Linux kernel or `kexec` in the common path.

**Recovery:** a separately built, project-signed UKI on UEFI; separate Linux kernel/initramfs artifacts on BIOS. Can mount Debian read-only, inspect damaged filesystems, inspect published boot images, revert selection, and perform offline repairs with explicit consent. Recovery boot does not depend on the target OS's `/usr`, package database, modules, or fstab.

**Graphics:** `systemd-stub`/firmware splash may render early image; Plymouth handles continuing kernel/initramfs/userland animation and unlock prompts. Try OEM BGRT logo preservation. Boot-manager spinner is optional: once firmware hands over to a Linux kernel the boot manager cannot keep animating on its own. A short discontinuity is an acceptable v1 tradeoff if the framebuffer remains stable.

### 4.1 systemd-boot selection policy

systemd-boot has no project-private entry directory: it always scans the shared `/loader/entries/` and `/EFI/Linux/` directories on the ESP and XBOOTLDR. Any UKI or Type #1 entry written there by another tool or operating system becomes bootable and may sort above ours. Selection is therefore pinned by configuration and verified at runtime:

- `loader.conf`: `default vintner-*`, `timeout 0` (never `menu-disabled`, which also blocks the key-press menu), `editor no`, `auto-entries no`.
- Upstream orders entries that have reached zero tries ("bad") after all non-bad entries. Entry IDs and the UKI `.osrel` fields (ID, version, sort key) are chosen so that every non-bad ordinary entry sorts above recovery A, and recovery A above recovery B, while all of them match `default`. When every ordinary entry is bad, recovery is the first match and boots without user input. Upstream does not document the all-bad case, so this behaviour is a CI acceptance test (§10), not an assumption. The exact field values are fixed in WP-002.
- A choice made with the menu's default key is persisted in the `LoaderEntryDefault` EFI variable and overrides `loader.conf`. The health service reports a set `LoaderEntryDefault` and any foreign entry in the shared directories; it does not delete foreign files.
- One-shot recovery (`set_recovery_next_boot`) uses `LoaderEntryOneShot`.

## 5. Installer and disk ownership

Suggested single-disk x86-64 GPT layout for new installations:

| Partition | Role | Initial recommendation |
|---|---|---|
| BIOS Boot | Small unformatted GRUB embed area | ~2 MiB; used only on BIOS/GPT |
| ESP | UEFI loader only (`\EFI\vintner\`) | New installs: 512 MiB FAT32. Existing ESP: reused as-is, never resized |
| Boot storage (XBOOTLDR) | Normal UKIs and recovery UKIs (UEFI); kernel+initramfs slots, recovery images and `grubenv` (BIOS) | **Required.** GPT type `bc13c2ff-59e6-4262-a352-b275fd6f7172` (MBR type `0xEA`); FAT32, plain, unencrypted; size from the image budget below (default 2 GiB) |
| Debian root | Standard Debian filesystem | ext4 in v1; optional Btrfs later |
| Data | Optional separate persistent state | `/home` or `/var` policy decided at install |

**Image budget.** Boot storage must hold, without garbage collection: N ordinary images (default 3: candidate, current good, previous good), two recovery images (A and B, §8), one image of staging headroom, and a 20% margin. With placeholder sizes of 150 MiB per ordinary UKI or slot and 300 MiB per recovery image this is about 1.5 GiB, hence the 2 GiB default. WP-002 replaces the placeholders with measured sizes.

For mixed-firmware lab testing, one GPT can contain a BIOS Boot Partition, an ESP and the boot storage partition; both firmware backends then share the same boot storage. For production BIOS-only installations, the ESP is not required. In-place conversions must never repartition or resize an existing Windows ESP without explicit approval. Preserve EFI boot entries. Supply external USB recovery as a separate-disk failure contingency.

Partition labels are for humans; boot configuration should use stable UUID/PARTUUIDs or explicitly resolved device identities. A disk replacement or clone must not accidentally select the wrong root.

Installer choice: **Debian Installer preseed with late commands and custom installer package**, then investigate an installer component/u-deb for native integration. Before disabling the stock GRUB installer, confirm the project loader and recovery are successfully installed; on errors, retain an ordinary Debian boot path and report installation incomplete. Document preseed variables but don't assume generic `grub-installer/skip` is sufficient across all d-i versions without an end-to-end test. Because `vintner-platform` conflicts with the stock loader packages (below), the order is fixed: deploy and verify the project loader and recovery from the installer, then remove the stock loader packages, then install the `vintner` packages. If verification fails, the `vintner` packages are not installed and the stock GRUB path remains.

Debian package boundaries (v1):

- `vintner-core`: common boot metadata schema, manifest validation, state model, CLI and API; the publisher (kernel/initramfs hooks, image build/verify/sign, atomic staging and garbage collection); health checks and the BIOS blessing service.
- `vintner-platform`: **inert** platform payloads (systemd-boot and shim binaries, GRUB core image and modules, recovery images) installed under `/usr/lib/vintner/payloads/`. Installing or upgrading this package never touches the ESP, boot storage or NVRAM; only an explicit, signed `vintner platform-update` deploys a payload. This is the ownership mechanism: APT delivers, the operator deploys.
- `vintner-installer`: guided/automated installation integration, partition setup, firmware probe, boot entry registration; later also a udeb.
- The recovery image is a separately versioned **build target** in this repository, shipped inside `vintner-platform`, not a runtime package of its own.

Splitting `vintner-core` further (separate publisher, health or per-firmware packages) is deferred until a concrete installation needs one part without the others.

Package manager interactions: avoid competing GRUB EFI/systemd-boot/shim maintainer-script installation paths that target our ESP entries. Merely running `apt-mark hold` or mounting ESP read-only is not a complete ownership mechanism. Use project-owned directories and boot NVRAM identifiers, lifecycle tests, and a dedicated publication workflow. APT may update the userspace publisher itself, but it must not replace platform binaries except by an explicit signed platform update. The Debian 13 package relations are:

| Debian package | Relation | Reason |
|---|---|---|
| `systemd-boot-efi`, `systemd-boot-efi-amd64-signed` | Payload source at build time (`vintner-platform`) | Loader and `systemd-stub` binaries only, no maintainer-script installation; a Debian-signed build exists in Trixie (257.13-1~deb13u1) |
| `systemd-ukify` | `Depends` (`vintner-core`) | UKI generation |
| `grub-pc-bin` | Payload source at build time (`vintner-platform`) | GRUB BIOS modules and tools without the auto-install scripts of `grub-pc` |
| `systemd-boot` | `Depends` (`vintner-core`), with `systemd-boot-update.service` masked | Ships `systemd-bless-boot`, which the UEFI backend uses; it also ships `systemd-boot-update.service` (updates systemd-boot copies on the ESP from `/usr`) and the hooks `/etc/kernel/postinst.d/zz-systemd-boot`, `/etc/kernel/postrm.d/zz-systemd-boot` and `/etc/initramfs/post-update.d/systemd-boot`. The lifecycle test must show those hooks write nothing to the ESP or boot storage while the loader lives in `\EFI\vintner\`; if it cannot, switch to `Conflicts` and ship a `vintner` bless service |
| `grub-pc`, `grub-efi-amd64` | `Conflicts` (`vintner-platform`) | Maintainer scripts run `grub-install` and the kernel hooks run `update-grub` |
| `shim-signed` | `Conflicts` (`vintner-platform`); shim binary taken from the pinned `.deb` at payload build time | Its post-install script runs `grub-install` on EFI-booted systems |
| kernel-install | `vintner-core` sets `layout=other` | Keeps kernel-install's built-in loader-entry and UKI plugins from writing to `$BOOT`; verified by the lifecycle test |

Every other package whose maintainer scripts call `grub-install`, `update-grub`, `bootctl install`/`update` or `efibootmgr` is found by the lifecycle test (§10), which installs, upgrades and removes each such package and asserts the loader, recovery and NVRAM state are unchanged.

## 6. Boot image publishing protocol

States (semantic, shared across firmware): `GOOD`, `STAGED`, `TRYING`, `FAILED`, `RECOVERY`. A new candidate is never visible as selectable until its complete contents have been written, verified, signed and synchronized.

1. Debian creates a new `vmlinuz-<ABI>` and matching initramfs; publisher is triggered by relevant kernel **and** initramfs hooks. Queue or coalesce updates so repeated/overlapping invocations do not race.
2. Resolve and record the exact kernel ABI and module tree, including the module-tree identity defined in §6.1. Validate the root identity and boot arguments.
3. Create an immutable, versioned UKI (UEFI) or complete kernel+initramfs slot (BIOS). Write to a staging location, check signature or hashes, size, dependency metadata, and on-disk readback as appropriate.
4. Flush files and directory metadata in a documented order. Publish by a same-filesystem atomic rename or a state-record commit; validate behavior under FAT filesystem limitations and abrupt power loss.
5. Set candidate as trial with a finite number of attempts (initial default: 3, configurable). Existing good entry and independent recovery are unaffected.
6. Boot manager consumes one attempt on each trial boot. When `boot-complete.target` is reached (all health checks passed, §4), the backend's blessing service marks it `GOOD`.
7. Exhausted attempts select previous `GOOD`; repeated failure of all ordinary entries selects recovery (or a visible recovery choice if hardware cannot reliably reboot unattended). On UEFI this follows from the sort order in §4.1; on BIOS from the static GRUB config reading `grubenv`.
8. Garbage-collect old images only after at least one independently boot-tested good image remains; never garbage-collect recovery in the OS image publisher. The kernel packages of every retained image are protected from `apt autoremove` (written to an `APT::NeverAutoRemove` list) and released when the image is collected.

Kernel rollback here is **not full Debian root rollback**. A broken `/usr`, critical package migration, or incompatible `/lib/modules` state could remain broken after selecting an old kernel. Release notes and test results must make this explicit. For true whole-OS rollback, expand to A/B root slots or an atomic snapshot/update system, always pairing root image and kernel ABI.

Use `kernel-install` where its conventions fit, and Debian's `/etc/kernel/postinst.d`, `/etc/kernel/postrm.d`, `/etc/initramfs/post-update.d` hooks. Hook ordering, kernel removals, `dpkg` interruptions, initramfs-only upgrades, and DKMS updates must be in CI. Avoid allowing an old Debian kernel package post-removal to remove the sole last-good artifact.

### 6.1 Module-tree coherence

A UKI or BIOS slot contains the kernel and initramfs, but after `switch_root` the kernel loads further modules from `/lib/modules/<ABI>` on the mutable root. In Debian 13 the ABI name is the upstream version plus `+deb13` (for example `6.12.43+deb13`), so most kernel uploads get their own module directory. The tree of a retained image can still change in place:

- DKMS writes rebuilt modules into `/lib/modules/<ABI>/updates/dkms`.
- A new Debian revision of the same upstream version can reuse the ABI name.
- `/lib/firmware` is updated in place by firmware packages.
- `apt autoremove` or a manual purge removes the module tree of a kernel the publisher still treats as `GOOD`.

Under Secure Boot this is worse than a version mismatch: since 2023 Debian signs modules with an ephemeral per-build key embedded in that build's kernel, so an older kernel cannot load modules from a different build at all.

v1 rule: the manifest records a module-tree identity for each image (the kernel build ID plus a hash of `modules.dep` and the DKMS module list). The publisher pins retained kernel packages (§6 step 8). At boot and on every publish, an image whose recorded identity no longer matches the installed tree is reported as stale and no longer counts as the known-good fallback required by R04; garbage collection must then keep a `GOOD` image of a different ABI. Fallback across a changed tree is therefore only guaranteed between different ABIs. A per-image module snapshot (for example an EROFS image of the tree mounted by an initramfs hook) is the v2 option if same-ABI fallback proves necessary.

## 7. Project-by-project adoption decisions

| Project | Confirmed relevant feature | Decision / exact reuse |
|---|---|---|
| **systemd-boot** | UEFI loader, BLS Type #2 UKIs, boot counting, EFI boot-loader interface, XBOOTLDR support | **Adopt UEFI v1**; binary taken from `systemd-boot-efi`/`systemd-boot-efi-amd64-signed` and kept project-controlled in `\EFI\vintner\`; use boot-count/entry semantics with the selection policy in §4.1 |
| **systemd kernel-install / ukify / systemd-stub** | Extensible installation plugins, UKI generation, EFI stub, signing pipeline | **Adopt**, with Debian-specific publisher and explicit artifact verification |
| **Debian initramfs-tools** | Standard Debian kernel and root setup plus update hooks | **Adopt first**, avoid new early-userspace stack prematurely |
| **dracut** | Host-only dependency-focused image generation, modular early init | **Evaluate opt-in** after compatibility tests; do not make a fragile host-only image the sole recovery path |
| **GRUB 2** | Mature BIOS x86 loading, configurable environment blocks | **Adopt BIOS v1** in minimal static form, built from `grub-pc-bin` (not `grub-pc`); `grubenv` on the boot storage partition only; derive trial/fallback from RAUC GRUB examples |
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
| **shim / Debian Secure Boot** | Signed bootstrap, embedded distro trust, MOK | **Adopt as trust reference/integration option**; shim binary taken from the pinned `shim-signed` `.deb` at payload build time, the package itself is not installed (§5); fully verify the signed next-stage and `LoadImage()` semantics; MOK is not the same as firmware `db` |
| **Plymouth** | Kernel/userspace animated splash and unlock UI | **Adopt**, favor supported BGRT-style theme over custom bootloader animation |
| **dm-verity** | Kernel block-integrity verification for read-only root filesystem | **Optional v2** with signed root hash and read-only system images; not directly compatible with unrestricted writable root |

**Licensing:** this repository is licensed under the GPL-2.0 (`LICENSE`); whether that is GPL-2.0-only or GPL-2.0-or-later is an open decision (§16). Limine is BSD-2-Clause, ReactOS/FreeLoader is GPL-2.0 family, GRUB is GPL-3.0-or-later, systemd is mostly LGPL-2.1-or-later, and other dependencies carry distinct licenses. Platform payloads built from upstream sources (for example a GRUB core image) are shipped with that upstream's license and corresponding-source obligations. GPL-3.0 code cannot be copied into GPL-2.0-only files. Reference algorithms/designs freely where permitted; if code is copied, comply with copyright and license obligations and record it in the `third_party/` inventory.

## 8. Security architecture and explicit constraints

- **UEFI chain:** firmware trust database → signed loader (possibly through Debian shim) → trusted UKI/recovery image → optional authenticated root. Test image-loading trust **end to end**, not only firmware acceptance of first executable.
- **MOK constraint:** a key trusted by shim/MOK is not necessarily accepted by an arbitrary UEFI firmware `LoadImage()` call. Use a supported, tested chain or provision custom certificate in firmware `db` for controlled pilot hardware. Turnkey Secure Boot on arbitrary factory-key-only machines requires a separately validated signing/distribution strategy. Do not silently disable Secure Boot.
- **Secure Boot tiers (R01 vs R07):** UKIs built on the machine must be signed by a key enrolled in MOK or the firmware `db`. MOK enrollment requires a person to confirm it at the console in MokManager on the next boot, so a fully unattended installation with Secure Boot is impossible on machines that only carry factory keys. The project therefore supports three explicit tiers, and each installation records which tier it is in:
  - **Tier A — pre-enrolled key:** the project or site key is already in `db` (or MOK) before installation, as in lab and pilot fleets. Full chain, unattended installation.
  - **Tier B — interactive MOK enrollment:** installation is unattended up to one console confirmation in MokManager on first boot. Full chain once enrolled.
  - **Tier C — distribution-signed only:** Debian-signed shim, systemd-boot and kernel with Type #1 entries and no local signing key. No enrollment, but the initramfs and command line are not authenticated; this tier is documented as reduced assurance and never described as an equivalent chain. The publisher emits Type #1 entries instead of UKIs in this tier, and whether the chain verifies end to end under Debian's shim is part of risk 1 (§12).
- **Microsoft CA transition:** the Microsoft Corporation UEFI CA 2011, which signs shim, expired on June 27, 2026. Since October 2025 new shims are signed with both the 2011 and 2023 CAs; Debian 13 carries `shim-signed` 1.51~1+deb13u1 (shim 16.1). Expiry does not invalidate already-signed binaries, but machines with only one of the CAs in `db`, and SBAT/DBX revocations, are test-matrix rows (§10), not assumptions.
- **Kernel module signatures:** Debian signs kernel modules with an ephemeral per-build key embedded in that build's kernel. Under Secure Boot (lockdown) a kernel cannot load modules from another build, which is why §6.1 treats module-tree identity as part of each image.
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
| **1 — independent boot platform** | Project-owned boot paths; hidden UEFI/BIOS menus; minimal recovery image (shell, read-only root mount, logs; the full WP-006 toolset follows later); scripted installation in QEMU (Debian Installer integration is phase 5) | Fresh installation and deliberately corrupted Debian root can still select recovery; ordinary package ops, including the §5 package-relation lifecycle test, leave loader+recovery hashes unchanged |
| **2 — versioned image publication** | UKI generator + BIOS kernel/initrd slots, exclusive lock, image verifier, postinst/initramfs adapters | Kernel upgrade and initramfs-only regeneration create a bootable candidate, preserving known-good |
| **3 — trial, blessing, fallback** | Cross-backend slot API; UEFI BLS counter; GRUB env trial logic; health gate | Inject failed kernel, incomplete boot, forced power cycle and hung boot; correct fallback after bounded attempts |
| **4 — Secure Boot/robustness** | Controlled key enrollment, signed chain, recovery signing, revocation plan; power-cut tests | Tampered images rejected; 100% of scripted publication interruption points preserve bootable fallback/recovery in tested configurations |
| **5 — installer and compatibility** | Debian Installer preseed integration, upgrade audits, BIOS/GPT+UEFI/GPT + existing ESP test cases, documentation | Installer fully configures system, upgrade matrix passes without modifying platform infrastructure |
| **6 — graphics and performance** | Plymouth BGRT theme, early framebuffer behavior, rescue accessibility, boot timings | No avoidable menu/console flash on reference UEFI GPUs; cold-boot timing and failure UX documented |
| **7 — transactional OS (optional)** | RAUC or sysupdate evaluation, dual root slots/snapshots, dm-verity optional | Deliberately broken entire Debian userspace rolls back to previous root+kernel pair |

**Gate priority:** 1 → 2 → 3 before splash polishing. If BIOS/UEFI parity becomes a blocker, ship UEFI as a supported release first and classify BIOS as experimental until phase 3 passes, rather than weakening rollback guarantees.

## 10. Test plan and CI matrix

Matrix axes:

- Firmware: OVMF UEFI without Secure Boot, OVMF UEFI with Secure Boot, SeaBIOS legacy BIOS. Secure Boot variable stores: `db` with only the Microsoft UEFI CA 2011, only the 2023 CA, both, and a project test key; SBAT and DBX revocation levels current and one step ahead. Test keys are enrolled into OVMF variable stores with `virt-fw-vars` (`python3-virt-firmware`).
- Distribution: Debian 13 is the blocking lane; Debian testing (currently forky) runs as a non-blocking lane to catch upcoming changes early.
- Storage: GPT ESP + BIOS Boot Partition + boot storage, MBR BIOS (if supported), existing small Windows ESP (100–300 MiB) plus a new boot storage partition, single disk and two disks.
- Root: ext4/plain first; LUKS, LVM, Btrfs and Secure Boot combinations behind distinct tested features. BIOS trial state must keep working on a LUKS, LVM or Btrfs root because `grubenv` never lives there.
- Kernel lifecycle: new ABI install, same ABI initramfs rebuild, same-ABI Debian revision, old kernel removal, `apt autoremove` with retained images, DKMS rebuild (module-tree identity changes), APT interrupted mid-transaction, disk full.
- Package lifecycle (§5): install, upgrade and remove each conflicting or adjacent package (`systemd-boot`, `grub-pc`, `grub-efi-amd64`, `shim-signed`, `systemd-ukify`, `dracut`) and assert loader, recovery and NVRAM hashes are unchanged and nothing is written to the ESP or boot storage outside the publisher.
- Selection policy (§4.1): all ordinary entries bad → recovery boots without input; foreign UKI or Type #1 entry placed in `/EFI/Linux/` or `/loader/entries/` → not selected and reported; stale `LoaderEntryDefault` → reported; key-press menu still reachable with `timeout 0`.
- Failure injection: candidate missing, hash mismatch, broken command line, malformed image, missing modules, root absent, filesystem damaged, before/after metadata commits, reboot/power cut during publication, repeated crash, permanent hang with/without watchdog.
- Recovery: menu invocation, automatic fallback, mount root read-only, stage prior known-good, recovery independently boots if `/usr` unavailable.
- Security: signed/untrusted UKI, revoked certificate simulation, MOK-vs-db trust behavior, each Secure Boot tier (§8), tampered fallback, unsigned config changes, image downgrade/rollback policy, malicious disk labels/paths.
- Graphics: Intel/AMD/NVIDIA-relevant modes, firmware BGRT present/absent, GOP simple framebuffer to KMS transition, encrypted root prompt, serial/headless fallback.

CI should record serial logs, QEMU exit reason, UEFI variables, BLS entry changes, GRUB environment state, root selection, image hashes, and mounted root identity. Add deterministic filesystem image fault-injection harnesses. CI runs QEMU with KVM where the runner exposes `/dev/kvm` and falls back to TCG with longer timeouts otherwise. No hardware experiments until virtual no-loss upgrade/recovery tests pass.

## 11. Repository outline

```text
vintner-boot/
├── debian/                     # Debian source packaging only: control, rules, changelog, *.install
├── docs/
│   ├── architecture.md
│   ├── security-threat-model.md
│   ├── accepted-platforms.md
│   └── adr/
├── third_party/                # pinned refs/manifest and license inventory, not unchecked copies
├── schema/                     # manifest, configuration and state-machine definitions shared by host and recovery
├── src/                        # core tool: CLI, publisher, health, backend interface (layout set by ADR-0002)
├── firmware/
│   ├── uefi/                   # loader.conf, entry naming and sort policy (§4.1)
│   ├── bios/                   # static grub.cfg and grubenv interface
│   └── experimental-limine/
├── integration/                # files installed onto Debian systems
│   ├── kernel-hooks/
│   ├── initramfs-hooks/
│   ├── systemd-units/
│   └── installer/
├── recovery/                   # recovery image build target
│   ├── kernel-config/
│   ├── initramfs/
│   ├── tools/
│   └── tests/
├── graphics/
│   └── plymouth-theme/
├── ci/
│   ├── qemu-ovmf/
│   ├── qemu-seabios/
│   └── fault-injection/
└── examples/
    ├── disk-layouts/
    └── configs/
```

A top-level `debian/` directory is reserved by Debian tooling (`dpkg-buildpackage`, `gbp`) for the source package's `control`, `rules` and related files, so it holds packaging only; files installed onto target systems live under `integration/`. The former `policy/`, `image-builder/` and `update/` trees are merged into `schema/` (language-neutral definitions) and `src/` (one code base for the CLI, publisher, platform updater and health checks), because they share the state model and are built together.

Backend interface (semantics, not final API signatures): `list_images`, `publish_candidate`, `set_trial(image, tries)`, `get_booted_image`, `mark_good`, `mark_bad`, `set_recovery_next_boot`, `get_platform_health`. Represent logical `GOOD`/`TRYING` states at this layer; let systemd-boot and GRUB implement their own persistent formats. Recovery is separately protected from normal publisher access.

## 12. Key risks / decisions still to verify

1. **UEFI Secure Boot with Debian shim and custom images:** actual downstream trust policy must be tested under the shipping Debian 13 shim/systemd-boot binary combination (`systemd-boot-efi-amd64-signed` loaded by `shim-signed` 1.51~1+deb13u1), for each tier in §8. MOK does not automatically guarantee generic EFI image acceptance. Pin a lab test matrix, including the 2011/2023 Microsoft CA combinations, before any release claim.
2. **Limine updater conflict:** config hash embedded into loader could force loader resigning for each changing kernel path/hash. A static fixed-slot config and separate signed manifest may work, but is a design change requiring analysis. No Limine trial-counter protocol equivalent to systemd-boot's is assumed.
3. **GRUB BIOS state robustness:** the write location is now fixed (boot storage partition, §4). Retry atomicity (a 1 KiB block can tear across two 512-byte sectors) and fallback must still be verified with abrupt resets; RAUC example supplies baseline logic only.
4. **Kernel/module coherence:** prior kernel rollback with mutable root may fail after `apt` removes modules or DKMS, a same-ABI revision or firmware updates change the tree in place; §6.1 limits v1 to detection, package pinning and different-ABI fallback. Per-image module snapshots or full root snapshots/slots are the upgrade path.
5. **Recovery longevity:** independent recovery on same disk does not survive complete disk failure; offer external rescue media and documented firmware-entry reconstruction.
6. **Unattended reboot on hang:** boot counter cannot advance if a hung machine never restarts; watch the availability of watchdogs and document fallback limitations.
7. **Windows dual boot:** some firmware resets boot order or Windows servicing adjusts ESP entries; never overwrite `\EFI\Microsoft\Boot` or automatic Windows boot entries. Firmware/NVRAM resilience tests required.
8. **Boot platform safety vs freezes:** immutable baseline avoids accidental changes but may accumulate vulnerabilities and revocations. Signed, rollback-safe, intentionally invoked platform updates are required.
9. **Shared BLS namespace:** systemd-boot scans `/loader/entries/` and `/EFI/Linux/` on the ESP and XBOOTLDR for every installed OS and tool, and only one `loader.conf` exists per ESP. Another systemd-boot-based OS on the same ESP would contend for `loader.conf`; v1 detects and reports this rather than supporting it.
10. **Undocumented all-bad behaviour:** upstream documents that bad entries sort after non-bad ones, but not what happens when every entry matching `default` is bad. The recovery-on-exhaustion design in §4.1 holds only once its CI test passes on the pinned systemd-boot version, and must be re-run on every loader payload update.

## 13. Immediate engineering work packages

- **WP-001:** build reproducible Debian 13 images and QEMU/OVMF+SeaBIOS test harness, including OVMF variable stores for the Secure Boot rows in §10; capture baseline GRUB and systemd-boot results.
- **WP-002:** define signed image manifest schema (including module-tree identity, §6.1), explicit partition layout constraints and measured image budget (§5), the `loader.conf` and entry naming/sort policy (§4.1), and backend-independent state machine. Produce architecture decision records, including ADR-0002 (implementation language, §16).
- **WP-003:** prototype UEFI UKI publication with systemd-boot counting and recovery UKI; demonstrate failure fallback, including the all-bad → recovery case, without modifying installed Debian root.
- **WP-004:** prototype BIOS minimal GRUB config and grubenv trial/fallback on the boot storage partition; verify known-good kernel modules retained.
- **WP-005:** implement Debian postinst/postrm/initramfs hooks, lock, coalescing and publish transaction; write the package-relation lifecycle tests from §5.
- **WP-006:** build independent recovery Linux image with root diagnostics, rollback command, and logging; verify unavailable `/usr` case. A minimal version (shell, read-only root mount, logs) is delivered first for phase 1.
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
- Debian Installer Trixie preseed: https://www.debian.org/releases/trixie/amd64/apbs04.en.html (pinned to `trixie`; the `stable` alias moves when Debian 14 is released)
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
- systemd-boot `loader.conf` (Debian 13): https://manpages.debian.org/trixie/systemd-boot/loader.conf.5.en.html
- Discoverable Partitions Specification (XBOOTLDR type): https://uapi-group.org/specifications/specs/discoverable_partitions_specification/
- Debian 13 `systemd-boot` file list: https://packages.debian.org/trixie/amd64/systemd-boot/filelist
- Debian `systemd-boot-efi-amd64-signed`: https://packages.debian.org/search?keywords=systemd-boot-efi-amd64-signed&searchon=names&suite=all&section=all
- Debian `shim-signed` changelog (Trixie): https://tracker.debian.org/media/packages/s/shim-signed/changelog-1.511deb13u1
- GRUB environment block: https://www.gnu.org/software/grub/manual/grub/html_node/Environment-block.html
- Debian ephemeral module signing key announcement: https://lists.reproducible-builds.org/pipermail/rb-general/2023-September/003079.html
- Microsoft 2011 Secure Boot CA expiry: https://www.redhat.com/en/blog/expiration-secure-boot-signing-certificates-2026

## 15. Final go/no-go decision

The project's first release should be judged on unattended installation, autonomous recovery, image publication, and resistance to routine Debian updates—not whether it uses a single bootloader for both firmware families or perfectly imitates the Windows dots. **Go** if phases 0–5 satisfy the acceptance gates. **No-go** if kernel updates can invalidate recovery, if Secure Boot trust is unverified, if boot counting is only simulated in userspace, or if an interrupted update can erase the last good boot path.

## 16. Open decisions

| ID | Decision | Needed by | Recommendation |
|---|---|---|---|
| ADR-0002 | Implementation language for `src/` (CLI, publisher, health, platform updater, recovery tooling) | Before any code under `src/` | One statically linked binary shared by the installed system and the recovery image (Go or Rust), with thin POSIX `sh` shims for the Debian hook directories |
| — | GPL-2.0-only or GPL-2.0-or-later for project code | Before the first source file with a license header | Decide together with ADR-0002 |

## 17. Change log

**Revision 1.1 (October 14, 2026)** — amendments from the plan review:

- Named the project `vintner-boot` and the package/entry prefix `vintner`.
- Required: made Debian package relations explicit (§5), including masking `systemd-boot-update.service`, conflicting with `grub-pc`, `grub-efi-amd64` and `shim-signed`, and setting kernel-install `layout=other`.
- Required: added the systemd-boot selection policy (§4.1) and the all-bad → recovery acceptance test.
- Required: replaced the optional boot/recovery storage with a mandatory XBOOTLDR boot storage partition and an image budget, so an existing small ESP is never resized (§5).
- Required: fixed the BIOS `grubenv` location to plain, unencrypted boot storage (§4).
- Required: added Secure Boot tiers, the Microsoft 2011 CA expiry and Debian's ephemeral module signing key (§8).
- Required: added module-tree coherence rules (§6.1).
- Suggested: reserved top-level `debian/` for packaging and merged `policy/`, `image-builder/` and `update/` into `schema/` and `src/` (§11); reduced v1 to three packages with an inert payload package (§5); moved a minimal recovery image into phase 1 and Debian Installer integration to phase 5 (§9); restricted writes to `\EFI\BOOT\BOOTX64.EFI` (R13); tied health checks to `boot-complete.target` (§4); pinned the preseed reference to `trixie` and added a non-blocking Debian testing lane (§10, §14); added OVMF Secure Boot variable stores via `virt-fw-vars` and KVM/TCG handling (§10); corrected the licensing note (§7).
