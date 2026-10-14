# vintner-boot security threat model

- **Status:** Draft 0.1, for review. Covers v1 (phases 0 to 5 in architecture §9).
- **Date:** 2026-10-14
- **Related:** [architecture](architecture.md) §4, §5, §8 and §12; [ADR-0003](adr/0003-uefi-entry-naming-and-boot-counting.md); WP-008 (§13)

This document lists what vintner-boot protects, from whom, and where v1 deliberately stops. Each threat names the requirement it serves (R01 to R14, architecture §3), what mitigates it, what risk remains, and how it is tested. Claims about systemd behaviour refer to the v257 sources that Debian 13 ships. Items marked **verify** have not been confirmed yet.

## 1. Scope and assumptions

- x86-64 machines running Debian 13, with UEFI (Secure Boot on or off) or legacy BIOS firmware.
- APT repository signatures are trusted. A compromised Debian archive is out of scope.
- The running Debian system is trusted while it runs. v1 does not authenticate the root filesystem (architecture §8, "Root integrity"). Root on the running system can do anything that root can do, so v1 does not try to keep root from changing the boot configuration. The aim is narrower: root's changes should be detectable, should survive a reboot only when made through the publisher, and must never leave the machine without a bootable path.
- Firmware is trusted. Firmware implants, SMM attacks and DMA attacks are out of scope.
- No network access exists before the root filesystem is mounted (architecture §2, non-goals).

## 2. Assets

| ID | Asset | Where it lives |
|---|---|---|
| A1 | Signing private keys: the project release key (offline) and, in tiers A and B, the key that signs UKIs built on the machine | Release key: offline. Local key: Debian root, never on the ESP or boot storage (R11) |
| A2 | Loader payload: systemd-boot (and shim, if used) in `\EFI\vintner\` on UEFI; GRUB boot sector and core image on BIOS | ESP; BIOS Boot Partition |
| A3 | Ordinary images (UKIs or BIOS slots) and the manifest that lists them | Boot storage (XBOOTLDR) |
| A4 | Recovery images A and B | Boot storage |
| A5 | Boot state: boot counters in file names, `grubenv`, the EFI variables `LoaderEntryDefault`, `LoaderEntryOneShot` and `LoaderConfigTimeout`, and the NVRAM `Boot####` entries | Boot storage; NVRAM |
| A6 | The kernel command line and root identity (PARTUUID) | Inside each UKI; BIOS slot metadata |
| A7 | The existence of at least one bootable path (R08) | Everything above |
| A8 | Boot paths of other operating systems, for example `\EFI\Microsoft\` and Windows' `Boot####` entries | ESP; NVRAM |

## 3. Adversaries and failure sources

| ID | Source | Capability |
|---|---|---|
| S1 | Root on the running Debian system: malware, or an operator mistake | Writes any file on any mounted partition and any non-authenticated EFI variable. Reads local signing keys |
| S2 | A Debian package maintainer script or hook, buggy or unaware of vintner | Runs as root during `apt` operations. Typically runs `grub-install`, `update-grub`, `bootctl` or `efibootmgr` |
| S3 | Another operating system sharing the ESP: Windows servicing, or another Linux installation | Writes the ESP and NVRAM while Debian is not running |
| S4 | A physical attacker with brief access ("evil maid") | Uses the console and the firmware menus. Can remove the disk and modify it offline |
| S5 | Power loss, resets and hangs (not adversarial) | Interrupts any write at any point |
| S6 | Revocation from outside: DBX or SBAT updates applied by firmware, Windows or fwupd | Makes a signed binary unbootable |

## 4. Trust chain by tier

| Backend | What firmware verifies | What is not authenticated |
|---|---|---|
| UEFI, Secure Boot tier A or B (architecture §8) | Loader, UKI (kernel, initrd, command line, `.osrel`), recovery UKI, signed addons | `loader.conf`, file names (boot counters), EFI variables, credentials and system extensions loaded by systemd-stub (T6), root filesystem |
| UEFI, tier C (Debian-signed only, Type #1 entries) | shim, systemd-boot and the Debian kernel | Initramfs, command line, entry files, everything in the row above |
| UEFI without Secure Boot | Nothing | Everything |
| BIOS | Nothing | Everything. BIOS is reliability-only (architecture §8) |

## 5. Threats

### T1: modified or substituted image on boot storage

- **Source:** S4 offline, or S1.
- **Serves:** R07.
- **Mitigation:** In tiers A and B, firmware or shim rejects any UKI not signed by an enrolled key, and systemd-boot exits to the firmware when the image fails to load (ADR-0003, finding 5). Under option B of ADR-0003, the counter bump that happens before loading means a repeatedly rejected image becomes bad and is skipped.
- **Residual risk:** Tier C does not authenticate the initramfs or command line. Neither does UEFI without Secure Boot, or BIOS. Release notes must say so; the tier C wording in architecture §8 already does.
- **Test:** §10 Security rows ("signed/untrusted UKI", "tampered fallback").

### T2: downgrade to an older, validly signed image

- **Source:** S1 or S4 copies back an older UKI that was signed by the same key and has a known vulnerability.
- **Serves:** R07 (indirectly).
- **Mitigation in v1:**
  - The manifest lists the allowed images, and the health service reports any `vintner-*` entry that the manifest does not list (ADR-0003).
  - Loader-level revocation uses SBAT through shim and systemd-boot.
- **Residual risk:** v1 has no per-image anti-rollback. An old signed UKI placed on boot storage boots once every newer entry is bad. Making them bad only takes renaming them to `+0`, because boot counters live in unsigned file names. With a shared fleet key (T3), an image from any machine in the fleet qualifies.
- **Decision requested (D3):** accept no anti-rollback in v1 and document it (recommended), or add a minimum generation recorded outside the attacker's reach. There is no such place in v1 without a TPM.

### T3: theft of the local signing key

- **Source:** S1.
- **Serves:** R11.
- **Facts:**
  - In tiers A and B, UKIs are built and signed on the machine, so the publisher must be able to read the key. Root on the running system can therefore read it too, which architecture §8 already notes.
  - Firmware `db` X.509 entries act as trust anchors, so any signature that chains to an enrolled certificate is accepted.
  - With a site-wide key, or a site CA in `db`, one compromised machine yields a signing key that every machine in the fleet accepts.
- **Mitigation:**
  - The key is readable only by root (mode 0600). It is never on the ESP, boot storage or recovery media (R11).
  - Enrolled keys are per machine: one MOK or `db` entry per machine, generated at installation. That limits the damage from one compromised root to that machine.
- **Residual risk:** On the compromised machine itself the attacker can sign persistent boot code. This is inherent to local signing. The fix is offline or hardware-backed signing, which is a later choice (architecture §8).
- **Decision requested (D2):** tier A uses per-machine keys only (recommended), or a shared site key with the fleet-wide blast radius documented.

### T4: command line or initramfs changed through unsigned configuration

- **Source:** S1 or S4.
- **Serves:** R07; architecture §8 "Metadata threat model".
- **Facts (systemd v257):**
  - systemd-stub ignores a command line passed by the boot loader when Secure Boot is on and the UKI has a `.cmdline` section.
  - `editor no` in `loader.conf` disables the menu's command line editor.
  - Type #1 entries (tier C) carry their command line in an unsigned file.
- **Mitigation:** Every vintner UKI embeds `.cmdline`. `editor no` is fixed in `firmware/uefi/loader.conf`.
- **Residual risk:** In tier C, without Secure Boot, and on BIOS, the command line is not authenticated.

### T5: selection changed through EFI variables or foreign entries

- **Source:** S1, S3 or S4 (the `d` key in the menu).
- **Serves:** R09.
- **Facts:** `LoaderEntryOneShot` and `LoaderEntryDefault` override `loader.conf`, and `LoaderConfigTimeout` overrides its timeout (ADR-0003, findings 4 and 8). Any entry in `/EFI/Linux/` or `/loader/entries/` on either partition is bootable from the menu.
- **Mitigation:**
  - `default vintner-*` ignores foreign entries.
  - The health service reports a set `LoaderEntryDefault`, a set `LoaderConfigTimeout`, foreign entries, and `vintner-*` files the manifest does not list. It deletes nothing (architecture §4.1).
  - Under Secure Boot, a foreign entry still has to be signed by an enrolled key.
- **Residual risk:** Root can always redirect the next boot. Detection, not prevention, is the v1 goal.

### T6: side-loaded files picked up by systemd-stub

- **Source:** S1 or S4.
- **Facts (systemd v257, `src/boot/stub.c`):** the stub loads:
  - **Addons:** `*.addon.efi` from `\loader\addons\` and from the UKI's `.extra.d` directory. They are verified through shim or firmware, so under Secure Boot they must be signed, but they can append to the command line and add initrds and devicetrees.
  - **Credentials:** `*.cred` from `\loader\credentials\` and from `.extra.d`.
  - **System and configuration extensions:** `*.raw` and `*.confext.raw` from `.extra.d`.
  Credentials and extensions are passed into the initrd without a firmware signature check.
- **Mitigation:**
  - vintner ships no addons, credentials or extensions.
  - The health service reports any file in `\loader\addons\`, `\loader\credentials\` or any `*.extra.d` directory on the ESP or boot storage.
- **Verify:** how systemd in a Debian 13 initrd handles unencrypted credentials and unsigned extension images from these paths under Secure Boot. Also verify whether a Debian-signed or Microsoft-signed addon exists that could change a vintner command line. If unencrypted credentials are imported and trusted, the publisher needs a build or configuration option to disable them.

### T7: package scripts reinstall or reconfigure a boot loader

- **Source:** S2.
- **Serves:** R02, R03.
- **Mitigation:**
  - `Conflicts` with `grub-pc`, `grub-efi-amd64` and `shim-signed`.
  - kernel-install `layout=other`, and `systemd-boot-update.service` masked (architecture §5).
  - Platform payloads live in project-owned paths.
  - The lifecycle test (§10) installs, upgrades and removes each relevant package and checks that the loader, recovery and NVRAM hashes are unchanged. The list of packages comes from the survey of Debian 13 packages that write bootloaders (in progress under `ci/lifecycle/`).
- **Residual risk:** A package added to Debian after the survey. The survey script is rerun in CI against the Debian testing lane.

### T8: another operating system changes the ESP or NVRAM

- **Source:** S3.
- **Serves:** R01, R13; architecture §12 risk 7.
- **Mitigation:**
  - vintner never writes `\EFI\Microsoft\` or Windows' boot entries.
  - It writes `\EFI\BOOT\BOOTX64.EFI` only when no other operating system uses the ESP (R13).
  - The health service reports when `BootOrder` no longer starts with the vintner entry, or when that entry is missing. It recreates the entry only through an explicit command.
- **Residual risk:** Firmware that resets `BootOrder` boots the other operating system until the operator acts.

### T9: physical access to the boot menu and the recovery console

- **Source:** S4.
- **Facts:** `timeout 0` still opens the menu on a key press (architecture §4.1). The recovery image offers a shell (WP-006).
- **Mitigation:**
  - Without disk encryption, a physical attacker can already boot their own media and read the disk. An unauthenticated recovery shell gives them nothing new.
  - With LUKS, the recovery image unlocks the root only with an explicit passphrase (architecture §8, "Encryption").
- **Residual risk:** The reasoning above holds only while nothing unlocks the disk automatically. TPM auto-unlock is a v1 non-goal. Adding it later makes the recovery shell a bypass and forces this decision to be revisited.
- **Decision requested (D1):** the recovery console needs no authentication in v1, documented with the condition above (recommended), or it requires a password. A password would have to come from somewhere the recovery image can trust without the root filesystem, and v1 has no such place.

### T10: interrupted writes leave no bootable path

- **Source:** S5.
- **Serves:** R08, R14.
- **Mitigation:**
  - Images are published under new names and committed by a rename on the same filesystem, after flushing data and directory metadata (architecture §6).
  - The previous GOOD image and recovery are never modified during publication.
  - Recovery B never carries a counter (ADR-0003).
- **Residual risk:** FAT renames are not transactional, and the `grubenv` block can tear across two sectors (risk 3). The power-cut tests (§10 failure injection) and the `grubenv` tear experiments (in progress under `ci/grubenv/`) decide whether the BIOS configuration must treat an unreadable `grubenv` as "boot recovery" or as "boot the newest GOOD slot".

### T11: revocation makes the loader unbootable

- **Source:** S6.
- **Serves:** R12 and R13; architecture §8 "Microsoft CA transition".
- **Mitigation:**
  - A signed platform update replaces the loader payload before a revocation reaches the machine.
  - CI rows cover the 2011 and 2023 Microsoft CA combinations and SBAT/DBX levels (§10).
  - External rescue media is documented (risk 5).
- **Residual risk:** The recovery UKI is loaded by the same revoked loader, so on-disk recovery does not help when the loader itself is revoked. Only external media helps.

### T12: wrong root selected after a disk clone or swap

- **Source:** S5 (operational), or S4.
- **Serves:** R09; architecture §5.
- **Mitigation:** The command line names the root by PARTUUID, which a clone duplicates.
- **Residual risk:** Two disks with the same PARTUUID make the selection ambiguous.
- **Verify, during WP-002:** an initramfs check that compares the mounted root with an identity recorded in the manifest (for example the machine ID), and refuses to continue on a mismatch.

### T13: hostile filesystem content parsed during recovery or publication

- **Source:** S4 (a crafted disk), or S1.
- **Serves:** architecture §8 "Metadata threat model".
- **Mitigation:**
  - The publisher never takes paths from disk labels or file contents without validating them.
  - The recovery image mounts the root read-only, and runs `fsck` only with operator consent.
- **Residual risk:** Mounting a crafted filesystem exercises kernel filesystem parsers. This is accepted, since physical access is already assumed in that case.

## 6. Out of scope for v1

TPM measurements, sealing and auto-unlock; dm-verity; root filesystem authentication; network boot or network repair; firmware attacks (SMM, implants, DMA); an evil maid on BIOS machines; and a compromised Debian archive.

## 7. Decisions requested

| ID | Question | Recommendation |
|---|---|---|
| D1 | Recovery console authentication (T9) | None in v1, documented, valid only while no auto-unlock exists |
| D2 | Tier A key model (T3) | Per-machine keys; no shared site signing key on machines |
| D3 | Image anti-rollback (T2) | None in v1 beyond SBAT; documented |

## 8. Follow-up

- Resolve the T6 and T12 **verify** items before the publisher (WP-005) is written.
- Add the health-service reports named in T2, T5, T6 and T8 to the WP-002 state and reporting definitions.
- Update T10 when the `grubenv` experiments report back.
