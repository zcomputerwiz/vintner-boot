# systemd-boot selection-policy test

`run.sh` proves on Debian 13's own systemd-boot binary how vintner UKI entries are ordered, counted and selected. It is the acceptance test that [architecture §4.1 and §10](../../docs/architecture.md) and [ADR-0003](../../docs/adr/0003-uefi-entry-naming-and-boot-counting.md) require. Rerun it on every loader payload update (architecture §12, risk 10).

## What it does

1. **Fetches packages.** It downloads trixie `systemd-boot-efi`, `systemd-ukify`, `busybox-static` and the kernel that `linux-image-amd64` points to from deb.debian.org, and unpacks them without apt or root.
2. **Builds test UKIs.** Each UKI uses the trixie `ukify` and stub. Its initramfs prints `VINTNER-TEST-BOOTED id=<id>` and then powers off, or reboots to simulate a failed boot. The `.osrel` fields follow ADR-0003.
3. **Builds a disk per scenario.** A fresh GPT disk has an ESP holding `\EFI\BOOT\BOOTX64.EFI`, `\EFI\vintner\systemd-bootx64.efi` and `firmware/uefi/loader.conf`. An XBOOTLDR partition holds the UKIs in `/EFI/Linux/`. Each scenario also gets a fresh OVMF 4M variable store, with LoaderInfo variables preset via `virt-fw-vars` where the scenario needs them.
4. **Boots and records.** Each boot is one QEMU process with `-no-reboot`, using KVM when `/dev/kvm` is writable and TCG otherwise. After each boot the script records which entry booted and the file names in `/EFI/Linux`.

Requirements: `curl xz-utils dpkg cpio gzip fdisk mtools openssl sbsigntool python3 python3-pefile python3-virt-firmware qemu-system-x86 ovmf`. Run with `bash ci/selection-policy/run.sh`; output goes to `ci/out/selection-policy/`. It exits non-zero if any non-observation scenario fails.

## Results (2026-10-15, TCG)

Debian packages: `systemd-boot-efi 257.13-1~deb13u1`, `systemd-ukify 257.13-1~deb13u1`, `busybox-static 1:1.37.0-6+b9`, `linux-image-6.12.107+deb13-amd64 6.12.107-1`.

| Scenario | Entries at start | Booted, in order | `/EFI/Linux` afterwards | Result |
|---|---|---|---|---|
| S1 order | os-2+3, os-1, recovery-a+3, recovery-b | os-2 | os-2+2-1 | PASS |
| S2 failing candidate | os-2+1-2 (reboots), os-1 | os-2, os-1 | os-2+0-3 | PASS |
| S3 all ordinary bad | os-2+0-3, os-1+0-3, recovery-a+3, recovery-b | recovery A | recovery-a+2-1 | PASS |
| S4 recovery A bad too | as S3, recovery-a+0-3 | recovery B | unchanged | PASS |
| S5 foreign UKI on ESP and XBOOTLDR, foreign Type #1 entry | S1 plus `debian-6.12.efi` and `aaa.conf` | os-2 | os-2+2-1 | PASS |
| S6 counted image keeps failing | os-1+3 (reboots), recovery-a+3, recovery-b | os-1, os-1, os-1, recovery A | os-1+0-3, recovery-a+2-1 | PASS |
| S7 `LoaderEntryDefault=debian-6.12.efi` | S1 plus foreign UKI | foreign | unchanged | PASS |
| S8 `LoaderEntryOneShot=vintner-recovery-*` | S1 | recovery A, then os-2 | recovery-a+2-1, os-2+2-1 | PASS |
| S9 one-shot, recovery A bad | os-2+3, os-1, recovery-a+0-3, recovery-b | recovery B | unchanged | PASS |
| S10 UKI truncated to 4 KiB | os-2+3 (truncated), os-1 | os-1 | os-2+3 unchanged | OBSERVED |
| S11 Secure Boot, all signed | os-2+3, os-1 | os-2 | os-2+2-1 | PASS |
| S12 Secure Boot, os-2 unsigned | os-2+3 (unsigned), os-1 | nothing, then os-1 | os-2+0-3 | PASS |
| S13 key pressed during boot (`timeout 0`) | S1 | menu, then os-2 on Enter | os-2+2-1 | OBSERVED |

**Observations:**

- **S3, S4 and S6:** confirm the all-bad case that upstream does not document. When every ordinary entry is bad, `default vintner-*` selects recovery A without input, and recovery B when recovery A is bad too. Under the re-arm model, an image that was good before and then fails `tries` times in a row falls to recovery.
- **S7:** a set `LoaderEntryDefault` overrides `loader.conf`, which is why the health service reports it.
- **S8 and S9:** `LoaderEntryOneShot` is matched as a glob and consumed after one boot.
- **S10:** a UKI whose headers cannot be read is not listed at all. Its counter is not touched, and the next entry boots. This case is benign. It does not exercise ADR-0003 finding 5 (an entry that is listed but fails `LoadImage`); S12 does.
- **S11 and S12** use `OVMF_CODE_4M.secboot.fd` with a throwaway key made per run as PK, KEK and db. systemd-boot and the UKIs that should load are signed with `sbsign`.
- **S12 confirms finding 5, with a twist.** On the first power-on, systemd-boot counts os-2 (`+2-1`), `LoadImage` fails with `Access denied`, and systemd-boot returns to the firmware. OVMF then runs its platform-recovery fallback, `\EFI\BOOT\BOOTX64.EFI`, which is systemd-boot again. That run takes a second try (`+1-2`) and fails the same way. The firmware then stops at `BdsDxe: No bootable option or device was found. Press any key to enter the Boot Manager Menu.` and waits. The second power-on takes the last try (`+0-3`); the fallback run then sorts os-2 last and boots os-1. A rejected image therefore holds the machine at the firmware prompt until it is reset, once per power-on until its tries are used up. How many tries a power-on takes depends on how many boot options lead to systemd-boot.
- **S13:** the space key, held through the first 15 seconds by QMP key events, brings up the menu despite `timeout 0`. The menu lists `os-2`, `os-1`, `ra`, `rb` and `Reboot Into Firmware Interface`, the last because `auto-firmware` is on by default. Enter boots the default entry and counts it.
