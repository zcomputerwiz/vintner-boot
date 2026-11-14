# Secure Boot tier B, installed

WO-0010 5.2. [`ci/mok-chain`](../mok-chain/README.md) proves the tier B chain (Debian's shim, a Machine Owner Key, a machine-signed systemd-boot and UKI) on disks it builds by hand. This row proves what vintner itself writes: the guest is installed by `vintner install --secure-boot B`, writes its own request for MokManager from the running system, a person confirms it at the console, and the machine then boots with Secure Boot enforced and reports nothing. T4 to T6 then build a DKMS module in the installed system and show that Debian 13's kernel loads it with Secure Boot on (WO-0010 5.3, [`docs/secure-boot.md`](../../docs/secure-boot.md) §9). The design is [`docs/secure-boot.md`](../../docs/secure-boot.md) §8.

## Run it

```sh
recovery/build.sh && ci/secure-boot-b/run.sh
```

Needs what the other base-image rows need (root through `sudo`, KVM, loop devices, `mtools`, `qemu-system-x86`, `qemu-utils`, `cloud-image-utils`, `sfdisk`, `python3`, `python3-virt-firmware`, `sbsigntool`, `openssl`, `golang-go`) and the OVMF 4M firmware with its Secure Boot build. Results are in `ci/out/secure-boot-b/results-b.tsv`, the serial log of each power-on beside it.

## How it works

- **The base.** `ci/lib/vintner-base.sh` installs the guest with `vintner install --secure-boot B --secure-boot-enrolled` in a chroot. A chroot has no UEFI variables, so no request can be written there; `--secure-boot-enrolled` lets the install finish, and T1 writes the request from the installed system instead. Base row P0 boots the result once through shim, with Secure Boot off.
- **db.** The two Microsoft CAs, taken from the signatures of the shim the installer put on the ESP, as on a PC as sold, with a test platform key as `PK` and `KEK`.
- **The person at the console.** [`ci/mok-chain/drive.py`](../mok-chain/drive.py) runs QEMU with the serial console on a pipe and types MokManager's menu items when they appear.
- **What the guest reports.** Each boot's cloud-init script prints the health service's output (`vintner-bless.service`), the `SecureBoot` variable, the kernel's Secure Boot line and the MOK list that shim passed; the base's report service adds the entry systemd-boot started.

## Rows

| Row | What happens | Passes when |
| --- | --- | --- |
| T1 | Secure Boot off. The installed system boots through shim, then runs `vintner secureboot request --password-file` | systemd-boot started `vintner-os-1`; the health service reported `secure_boot=off` and that the certificate is not in shim's MOK list; the command exited 0; the variable store holds `MokNew` with the machine certificate, `MokAuth` for the password, and `MokTimeout` -1 |
| T2 | Secure Boot on (both Microsoft CAs in `db`). shim starts MokManager; Enroll MOK, Continue, Yes, the password, Reboot | every step was seen; `MokList` holds the certificate and `MokNew` is gone |
| T3 | Secure Boot on, the next power-on | the firmware and the kernel say Secure Boot is enabled; systemd-boot started `vintner-os-1`; the health service is active and reports neither `secure_boot=off` nor anything about tier B; `MokListRT` holds the certificate |
| T4 | Secure Boot on, MOK enrolled. The guest installs `dkms`, `build-essential` and the headers, builds a test module with DKMS (the installer's drop-in signs it with the machine key), then two controls: a module DKMS signs with a foreign key, and an unsigned one | DKMS exits 0 and `modinfo` names the machine certificate as signer; `modprobe` loads the module; both controls are refused (`unavailable key`, `unsigned module is rejected`) |
| T4b | The same boot, read from `vintner status` | DKMS's `post_transaction` ran `vintner publish` (`module tree changed (dkms, modules.dep)` in `/var/lib/dkms/post_transaction.log`), the newest image is not stale and differs from the one before the build, and the health report has no out-of-date line |
| T5 | Next power-on, Secure Boot on | systemd-boot started the generation the rebuild published; `modprobe` loads the module; the health service is active and `vintner status` says `out_of_tree: allowed`, `requires_signature: true` |
| T6 | `db` holds only the machine certificate, no shim, no MokList, systemd-boot started directly (tier A's firmware setup), on a copy of the disk | `/proc/keys` shows `.machine` empty and `.platform` with one key (the certificate from `db`); the same module loads |

Tier A's S1 to S9 are not repeated: tier B signs and checks every image with the same code, and `ci/mok-chain` M2, M3, M6, R1 and R2 show that shim refuses what the MOK list does not vouch for.

## Last run

2026-11-14, on a local Debian 13 host: T1 to T6 (and T4b) PASS, with dkms 3.2.2-1~deb13u1 and `linux-headers-6.12.111+deb13-cloud-amd64` from the Debian archive in the guest, and with the `shim-signed` 1.51~1+deb13u1+16.1-2~deb13u1 and `shim-helpers-amd64-signed` 1+16.1+2~deb13u1 that `vintner-platform` ships, OVMF 2025.02-8+deb13u1 and QEMU 10.0.13.

## Limits

- QEMU and OVMF only; real firmware is WO-0010 6.1.
- The request is written by `vintner secureboot request`. `vintner install` writes it through the same function (`secureboot.WriteMOKRequest`), but here it runs in a chroot with no UEFI variables; its own request is unit-tested against a fake efivarfs (`src/internal/install/tierb_test.go`).
