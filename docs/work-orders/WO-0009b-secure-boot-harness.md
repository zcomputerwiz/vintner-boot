# WO-0009b: Secure Boot test harness, tier A

- **For:** the agent that delivers the Secure Boot test harness (`ci/secure-boot/`). It works from `docs/secure-boot.md` (design; read it first, §2, §4 and §6 in particular).
- **Issued:** 2026-10-30
- **Status:** open.
- **Branch:** `wo-0009b`, from `origin/main` (the design is merged). Open a PR against `main`.
- **Independent of** WO-0009c (product: signing in the publisher), which is developed in parallel. This work order builds the signing and verification steps inside the harness (see §3) and keeps them in one place so that they can be replaced by the product commands later.

## Why

Phase 4's exit gate says tampered images are rejected. Nothing in the repository checks that under a real Secure Boot refusal. This harness proves, in QEMU, that a project-signed chain boots under enforced Secure Boot and that every tampered or foreign image is refused with a correct fallback.

## Rules

The rules of WO-0006, WO-0007 and WO-0007a apply in full:
- print `lsblk` before every `losetup` and touch only disks you created;
- commit no images, logs or keys; outputs under `ci/out/`;
- compare whole values and whole lines; every check must be able to fail; missing evidence is a FAIL;
- do not adjust expected values or margins;
- evidence comes from the PR's own CI run;
- Go standard library only, `CGO_ENABLED=0`, SPDX headers; the PR body as a file.

**Keys.** Generate every key and certificate during the run, under `ci/out/secure-boot/`, with `openssl`. Never commit one. Never print a private key; print certificate subjects and SHA-256 fingerprints only.

**Scope:** `ci/secure-boot/`, `ci/lib/` only for additions that other harnesses do not use, and `.github/workflows/qemu-tests.yml` for the new rows (`secure-boot-uefi`). Nothing under `src/`. If a fix needs anything else, stop and report.

## 1. Variable stores

Build the OVMF variable store with `virt-fw-vars` (package `python3-virt-firmware`) from `/usr/share/OVMF/OVMF_VARS_4M.fd`:

- `--set-pk` and `--add-kek` with a test platform key (self-signed, generated per run);
- `--add-db` with the machine certificate;
- `--set-json` or `--secure-boot` as needed so that the store reports Secure Boot enforced.

Run QEMU with `-global driver=cfi.pflash01,property=secure,value=on` as `ci/qemu-ovmf/boot-baseline.sh` does with `--secure-boot on`. Print `virt-fw-vars --input <store> --output-json /dev/stdout` (or the equivalent listing) into the log: it is the evidence of which keys are enrolled. For S6, add the certificate's hash to `dbx` and print the listing again.

## 2. Disks

Reuse `ci/lib/vintner-base.sh` and `ci/lib/vintner-qemu.sh` the way `ci/publish-interrupt/run.sh` does: one base disk per run with `firmware: uefi` boot storage and os-1 booted once to `GOOD`. Pre-build and sign the loader, the stub-built UKIs and the recovery UKIs on the host or inside the guest as §3 says.

## 3. Signing

Until WO-0009c lands, implement signing in one shell function `sb_sign <in> <out>` and verification in `sb_verify <file> <cert>`, both using `sbsign` and `sbverify` (package `sbsigntool`):

- loader: `sbsign --key <key> --cert <cert> --output <out> /usr/lib/systemd/boot/efi/systemd-bootx64.efi`;
- UKI: `ukify build ... --secureboot-private-key=<key> --secureboot-certificate=<cert> --signtool=sbsign`, or `sbsign` on the output;
- recovery UKIs: the same, with the same key.

Install the signed loader to `/EFI/systemd/systemd-bootx64.efi` and `/EFI/BOOT/BOOTX64.EFI`. Print `sbverify --cert` output for every signed file used by a case, and the SHA-256 of every file that reaches boot storage.

## 4. Cases

Each case is one disk copy, its own copy of the variable store, one QEMU run per boot, identified as in `ci/candidate-fallback` (`identify_booted`). Kill any boot after 120 seconds and record it as killed.

| ID | Setup | Expected |
|---|---|---|
| S0 | Signed loader, UKI and recovery; `db` has the machine cert | os-1 boots; the serial log has the whole line `secureboot: Secure boot enabled` (kernel message, console form `[<uptime>] secureboot: Secure boot enabled`); bless marks os-1 `GOOD`. Control: S0 must PASS before any other row can PASS. |
| S1 | Publish generation 2 (signed), then replace its file in `/EFI/Linux/` with an unsigned UKI of the same name | Boot 1 boots os-1, never os-2; os-2 ends `FAILED` or `TRYING` with counter 0 after the retries the counter allows; record the retry count seen. |
| S2 | Generation 2 signed by a second, unenrolled key | Same as S1. |
| S3 | A validly signed generation 2 UKI with one byte flipped inside the initrd section | Same as S1. The flipped offset and the file hashes before and after go into the evidence. |
| S4 | A signed recovery UKI with one byte flipped, ordinary images intact | os-1 boots; the recovery image is not booted; record that the recovery entry is refused when selected (a second boot that selects it through the loader menu key or `LoaderEntryOneShot`, firmware permitting, otherwise state why not tested). |
| S5 | Recovery UKI modified and every ordinary UKI refused | Records the outcome: no on-disk image boots. PASS means the harness observed and recorded the exact serial ending (no image ran, no recovery ran). |
| S6 | The machine certificate's hash added to `dbx` | Nothing boots from disk. Record as S5. |
| S7 | The loader replaced by the unsigned Debian `systemd-bootx64.efi` | Firmware refuses it; record the serial ending. |
| S8 | A publish with a certificate that does not match the key | Publish (or `sb_verify` in this harness) fails, nothing reaches `/EFI/Linux/`, the previous image still boots. |
| S9 | Secure Boot off in the variable store (no PK), tier A manifest | The image boots; the serial log shows no `Secure boot enabled`. Record only: the health report arrives with WO-0009c. |

For every refused-image case (S1 to S3) the evidence must show that the refusal is a Secure Boot refusal and not a missing file: print `sbverify` output of the file, show that the file exists with the expected name and size, and show the loader's error text if systemd-boot prints one on the serial console.

## 5. Checks

| ID | Cases | Expected |
|---|---|---|
| B1 | all | The variable store listing shows exactly the keys the case intends (`db` entries by subject and fingerprint, `dbx` for S6, no PK for S9). |
| B2 | all | The signed files used are verified on the host with `sbverify` before the case runs, and the unsigned or foreign ones fail `sbverify --cert <machine cert>` (the negative control). |
| B3 | all | Boot identity as the table says, never recovery A or B except where the table says otherwise. |
| B4 | S0 | `secureboot: Secure boot enabled` as a whole line, and bless output showing os-1 `GOOD`. |
| B5 | S1 to S3 | Boot 1 is os-1, and the manifest after the boot shows the refused generation not `GOOD`. |
| B6 | S4 | Recovery image never ran while it was invalid. |
| B7 | S5 to S7, S9 | Recorded (OBSERVED), with the exact serial text that ends the run. |
| B8 | S8 | Nothing published; os-1 boots. |

Final gate: every (check, case) pair that applies has exactly one row, PASS (or OBSERVED for B7 and S9). A duplicate or missing row fails the run. S0 failing fails the whole run.

## Validation before you push

- `bash -n` and `shellcheck -x` on every script; `python3 -m py_compile` with no `__pycache__` committed.
- A synthetic test of the evaluator, not committed: a correct run gives every row PASS (OBSERVED where stated); each of these turns its row FAIL: S0 without the Secure Boot line, S1 where os-2 boots, S3 where the flipped byte is outside the signed region (the test must prove the signature really covers it), B2 where an unsigned file passes `sbverify`, a variable store without the machine cert in `db`, a boot killed after 120 seconds, a duplicated row.

## Reporting back

- Results tables in `ci/secure-boot/README.md` and the PR, from the PR's own CI run; a versions section (`ovmf`, `qemu-system-x86`, `systemd-boot-efi`, `systemd-ukify`, `sbsigntool`, `python3-virt-firmware`, kernel).
- Findings with evidence: any case that differs from §4, and what S5 to S7 and S9 observed.
- Anything that cannot be done as written, with the exact reason. Do not adjust expected values.
