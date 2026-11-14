# The tier B chain: shim, MOK, systemd-boot and a UKI

WO-0010 5.2. Architecture §12 risk 1 asked whether a key that shim trusts as a Machine Owner Key (MOK) is accepted for a locally signed systemd-boot and for the UKIs that systemd-boot starts. This rig answers it on Debian 13's own binaries, and proves the rest of what the tier B design in [`docs/secure-boot.md`](../../docs/secure-boot.md) §8 relies on: how the certificate is enrolled, what the Microsoft CAs in `db` do, and that SBAT and `dbx` revoke.

No `vintner` code runs here. The rig builds a disk by hand: Debian's shim as the firmware's boot file, systemd-boot beside it as `grubx64.efi` (shim's built-in name for the program it starts), MokManager as `mmx64.efi`, and one UKI. A row installing with `vintner install --secure-boot B` follows with the implementation.

## Run it

```sh
ci/mok-chain/run.sh
```

Needs `curl`, `xz`, `dpkg-deb`, `cpio`, `gzip`, `sfdisk`, `mtools`, `openssl`, `sbsigntool`, `python3` with `pefile`, `python3-virt-firmware`, `qemu-system-x86_64` and the OVMF 4M firmware with its Secure Boot build. No root. It takes about five minutes with KVM. Results are in `ci/out/mok-chain/summary.tsv`, and each power-on's serial log is beside it.

## How it works

- **Packages.** It downloads trixie's `shim-signed`, `shim-helpers-amd64-signed` (MokManager), `systemd-boot-efi`, `systemd-boot-efi-amd64-signed`, `systemd-ukify`, `busybox-static` and the kernel, and unpacks them without installing.
- **Keys.** A machine key and a foreign key, made per run, and a test platform key that is `PK` and `KEK`. The two Microsoft CA certificates are taken from shim's own two signatures.
- **Variable stores.** `virt-fw-vars` builds each store from OVMF's empty template: `PK`, `KEK`, the chosen `db`, Secure Boot on. The shim variables are written the same way: `MokList` (the certificate is enrolled), `MokNew` and `MokAuth` (a pending request), `MokTimeout`, `SbatLevel` and `dbx`.
- **Keys typed in MokManager.** `drive.py` runs QEMU with the serial console on a pipe. Each step waits for a text on the screen and then types.
- **Result of a power-on.** Three words: the image that booted (`os-1`, from a marker that the test initramfs prints) or `NONE`; `enabled` if the kernel logged `secureboot: Secure boot enabled`; and `violation` if shim printed `Security Violation`.

## Rows

Every row has Secure Boot enforced. "MOK" means the machine certificate is in `MokList`.

| Row | Setup | Result |
| --- | --- | --- |
| M0 | Machine-signed loader and UKI, no MOK | shim refuses the loader |
| M1 | The same, with the MOK | Boots, Secure Boot enabled |
| M2 | MOK, UKI signed with a foreign key | The loader starts, the UKI is refused |
| M3 | MOK, unsigned UKI | The UKI is refused |
| M4 | MOK, unsigned loader | shim refuses the loader |
| M5 | Debian's signed systemd-boot, machine-signed UKI, MOK | Boots, Secure Boot enabled |
| M6 | The same, no MOK | The UKI is refused |
| C1 | `db` holds only the Microsoft 2011 CA | Boots |
| C2 | `db` holds only the Microsoft 2023 CA | Boots |
| C3 | `db` holds neither | The firmware refuses shim itself |
| R1 | MOK, and `SbatLevel` requires a newer systemd-boot | shim refuses the loader |
| R2 | MOK, and the machine certificate is in `dbx` | shim refuses the loader |
| E1 | A pending request; a person chooses Enroll MOK and types the password | The next power-on boots |
| E2 | A pending request; nobody presses a key | After 10 seconds shim deletes the request, then refuses the loader |
| E3 | A pending request with `MokTimeout` -1; nobody presses a key | MokManager waits at its menu; the request is still there |
| E4 | No request; after the refusal a person chooses Enroll key from disk and the certificate file on the ESP | The next power-on boots |
| E5 | A pending request; a wrong password three times | Nothing is enrolled, nothing boots |

## Findings

1. **The chain holds (risk 1).** shim verifies the loader against the MOK list, and systemd-boot's `LoadImage()` of a UKI goes through shim's verification too. A UKI that the MOK does not vouch for is refused (M2, M3), whichever loader started it (M6).
2. **Debian's signed systemd-boot would also work (M5).** The design signs the loader with the machine key anyway, so that tiers A and B share one signing path and the loader does not depend on a second package.
3. **Both Microsoft CAs start Debian's shim.** `shim-signed` 1.51~1+deb13u1 carries a 2011 and a 2023 signature, so a machine whose `db` has only one of them boots (C1, C2).
4. **An unanswered request is lost (E2).** MokManager's prompt lasts 10 seconds. When it ends, shim deletes `MokNew` and `MokAuth`, and the machine-signed loader is then refused. The installer must set `MokTimeout` to -1 (E3).
5. **There is a way back without a request (E4).** After a refusal shim offers MokManager, and Enroll key from disk takes the certificate from a file on the ESP with no password. The design keeps the certificate there for this.
6. **Revocation reaches the machine-signed loader (R1, R2).** A raised `SbatLevel` refuses a systemd-boot whose `.sbat` generation is too low, although the MOK vouches for its signature, so a shim or SBAT update can require a newer loader payload first.

## Limits

- QEMU and OVMF only. Real firmware, and a `db` as vendors ship it, are WO-0010 6.1.
- The request is written into the variable store from outside, in MokManager's plain form of `MokAuth` (SHA-256 over `MokNew` and the password in UTF-16). That is what the installer will write through efivarfs; a running system writing it is proved with the implementation.
- R1 raises `SbatLevel` by hand to a value no real update has; it shows the mechanism, not a real revocation.

## Versions

Last run on 2026-11-13: `shim-signed` 1.51~1+deb13u1+16.1-2~deb13u1, `shim-helpers-amd64-signed` 1+16.1+2~deb13u1, `systemd-boot-efi` and `systemd-boot-efi-amd64-signed` 257.13-1~deb13u1, `linux-image-6.12.107+deb13-amd64` 6.12.107-1; on the host OVMF 2025.02-8+deb13u1, QEMU 10.0.13, `python3-virt-firmware` 24.11-2, `sbsigntool` 0.9.4-3.2+deb13u1.
