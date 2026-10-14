# ADR-0003: UEFI entry naming, sort keys and boot counting

- **Status:** Accepted (option B, re-arm)
- **Date:** 2026-10-14
- **Details:** [`docs/architecture.md`](../architecture.md) §4.1 and §6; WP-002 (§13)

## Context

Architecture §4.1 pins entry selection with `loader.conf` and the systemd-boot sort order, and leaves the exact entry IDs and `.osrel` field values to WP-002. It also calls the case where every ordinary entry is bad undocumented (risk 10, §12).

Debian 13 ships systemd 257. The findings below come from reading the systemd v257 sources: `src/boot/boot.c`, `src/fundamental/bootspec-fundamental.c` and `src/bless-boot/bless-boot.c`. Function names are given so each finding can be rechecked against a later version.

## Findings (systemd v257)

1. **An entry ID is the whole file name, lowercased, including the boot counter and the `.efi` or `.conf` suffix** (`boot_entry_add_type1`, `boot_entry_add_type2`). The ID of a counted entry therefore changes on every boot. `default` in `loader.conf`, `LoaderEntryDefault` and `LoaderEntryOneShot` are all matched as glob patterns (`config_find_entry`, `efi_fnmatch`). A policy that must survive renames uses patterns, not exact IDs.
2. **Sort order** (`boot_entry_compare`), applied to all entries from the ESP and XBOOTLDR together:
   1. entries with zero tries left go last;
   2. entries with a sort key go before entries without one;
   3. sort key, ascending byte order;
   4. machine ID;
   5. version, descending (`strverscmp_improved`, so the newest is first);
   6. ID, descending;
   7. more tries left first, then fewer tries done first.
3. **A UKI's sort key is `IMAGE_ID` from its `.osrel` section, or `ID` if `IMAGE_ID` is unset. Its version is `IMAGE_VERSION`, then `VERSION`, then `VERSION_ID`, then `BUILD_ID`.** The display name is `PRETTY_NAME` first (`bootspec_pick_name_version_sort_key`).
4. **Default selection takes the first match in sorted order.** It tries `LoaderEntryOneShot`, then `LoaderEntryDefault`, then `default` from `loader.conf`, and then the first entry that is not an automatic entry (`config_select_default_entry`). A bad entry is still selected when it is the first or only match.
5. **The boot counter is decremented before the image is started** (`boot_entry_bump_counters` runs before `image_start`). An image that fails to load, for example because Secure Boot rejects it, still uses up a try. If starting the image fails, systemd-boot returns the error to the firmware and does not try the next entry. The firmware then continues with the next `Boot####` option, which may be another operating system.
6. **`systemd-bless-boot good` removes the counter from the file name** (`make_good`). An entry that has been marked good is never counted again, so it can never become bad.
7. **`auto-entries no` hides the automatic menu entries** for Windows Boot Manager, macOS, the EFI shell and `\EFI\BOOT\BOOTX64.EFI`. `auto-firmware` is a separate setting.
8. **The `LoaderConfigTimeout` EFI variable overrides the `loader.conf` timeout.** It is set from the menu (`+` and `-` keys) or by `bootctl set-timeout`. The `d` key in the menu writes `LoaderEntryDefault`.

From findings 2 and 4, the all-bad case is defined by the code. Bad entries sort last, and `default vintner-*` takes the first match, so the first non-bad `vintner-*` entry boots. This still has to be proven by the CI test in §10, because it is implementation behaviour and not a documented interface.

## Decision: names and `.osrel` fields

All vintner UKIs live in `/EFI/Linux/` on the boot storage (XBOOTLDR) partition.

| Image | File name | `IMAGE_ID` (sort key) | `IMAGE_VERSION` | `PRETTY_NAME` |
|---|---|---|---|---|
| Ordinary | `vintner-os-<gen>+<left>-<done>.efi` | `vintner-os` | `<gen>` | Debian `PRETTY_NAME` plus `, <ABI>` |
| Recovery A | `vintner-recovery-a+<left>-<done>.efi` | `vintner-recovery-a` | recovery build version | `vintner recovery A (<version>)` |
| Recovery B | `vintner-recovery-b.efi` (no counter) | `vintner-recovery-b` | recovery build version | `vintner recovery B (<version>)` |

- **`<gen>` is a publish generation:** a decimal integer, strictly increasing per installation and never reused. The kernel ABI is not used in names or versions for two reasons. Debian ABIs contain `+` (for example `6.12.43+deb13-amd64`), and systemd-boot reads the last `+` in a file name as the start of the boot counter. ABI order is also not publish order: a reinstalled older kernel, or an initramfs rebuild for the same ABI, must still sort as the newest image. The ABI and the module-tree identity (§6.1) are recorded in the manifest and shown in `PRETTY_NAME`.
- **All other `.osrel` fields** are copied from the installed system's `/etc/os-release`.
- **Resulting order:** all non-bad ordinary images come first, newest generation first, because `vintner-os` sorts below `vintner-recovery-a` in byte order and versions sort descending. Recovery A comes next, then recovery B, then every bad entry. CI asserts this invariant on every loader payload update. It does not rely only on the string comparison above.
- **Recovery B never carries a counter,** so it never becomes bad and is always the last automatic choice. Recovery A is counted (see option B below), so a recovery A that fails to load or start is skipped after the configured number of tries.
- **One-shot recovery** writes `LoaderEntryOneShot=vintner-recovery-*`. That pattern selects recovery A while it is not bad, and recovery B otherwise.
- **Foreign entries** keep their own sort keys and do not match `vintner-*` unless their file names start with `vintner-`. The health service reports any `vintner-*` file that the manifest does not list, any foreign entry, a set `LoaderEntryDefault` and a set `LoaderConfigTimeout`. It deletes nothing.
- **`loader.conf`** is in [`firmware/uefi/loader.conf`](../../firmware/uefi/loader.conf). Because of finding 7, Windows does not appear in the systemd-boot menu; on dual-boot machines it is reached from the firmware boot menu. Finding 5 also has to be covered by CI: when a vintner image fails to load, the firmware may boot the next `Boot####` option instead of rebooting.

## Decision: how ordinary entries are counted

**Option B (re-arm) is accepted.** Option A is recorded as the rejected alternative.

Finding 6 conflicts with §6 step 7 ("repeated failure of all ordinary entries selects recovery") and with R05 ("a missing or broken root can boot recovery").

**Option A: upstream semantics.** Only candidate images carry a counter, and `systemd-bless-boot` removes it once the boot is good. A GOOD image is then never counted again. If it breaks later, it keeps being selected. Typical causes are a changed module tree (§6.1), a missing root, or a damaged file (finding 5). Recovery is reached only when the running system marks the image bad, or when the operator uses the menu. R05 and §6 step 7 hold only for candidates.

**Option B: re-arm (accepted).** Every ordinary image and recovery A keeps a counter for life:

- After `boot-complete.target`, a vintner bless service renames the booted entry back to `+<N>-0`, where N is the configured number of tries. It finds the entry through the `LoaderBootCountPath` EFI variable that systemd-boot sets for counted entries. It also records GOOD in the manifest. The GOOD and TRYING states live in the manifest, not in the file name.
- Upstream `systemd-bless-boot.service` is masked.
- The recovery image re-arms recovery A once its own early checks pass.
- Any ordinary image that fails N consecutive boots becomes bad and sorts below recovery. When every ordinary image has failed, recovery A boots without user input, as R05 and §6 step 7 require.
- The BIOS backend uses the same rule with `grubenv`: GRUB decrements before booting, and the bless service re-arms after `boot-complete.target`. Both firmware backends then share one definition of a successful boot (§4).

The costs of option B:

- **One more FAT rename on boot storage per successful boot.** systemd-boot already makes one rename per counted boot.
- **A good image can be demoted by transient failures.** For example, N power cuts during boot demote a GOOD image. The health service reports a demoted image, and the operator or publisher re-arms it.
- **The `systemd-boot` dependency changes.** `vintner-core` would no longer need `systemd-bless-boot` from the `systemd-boot` package, so the `Depends` row in §5 becomes a candidate for `Conflicts`. The lifecycle test decides this, as §5 already requires.

## Consequences

- The names and fields above are fixed for WP-003 and for the publisher (WP-005). Changing them later needs a migration, because counters and IDs are part of the file names.
- The selection-policy CI test (§10) checks findings 1 to 8 and the resulting order on the pinned systemd-boot payload, and runs again on every loader payload update.
- Architecture §4, §5 and §6 are updated to option B (revision 1.2).
