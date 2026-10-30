# Secure Boot integration test harness, tier A (WO-0009b)

Tests that a project-signed boot chain boots under enforced UEFI Secure Boot in QEMU, that signed ordinary and recovery images are validated against the firmware machine key in `db`, that tampered, unsigned, foreign-signed, or revoked images are refused with correct fallback, and that unsigned or mismatched publications are aborted before reaching boot storage.

```bash
ci/secure-boot/run.sh uefi
```

CI runs it as `secure-boot-uefi` in `.github/workflows/qemu-tests.yml`.

## Method

1. **Base disk and artifacts:**
   - The base disk is prepared by `ci/lib/vintner-base.sh` with dual-firmware partitioning (`firmware: uefi` boot storage and ESP).
   - In Step 3, the test platform key (`pk.key` / `pk.crt`), machine key (`machine.key` / `machine.crt`), and an unenrolled foreign key (`foreign.key` / `foreign.crt`) are generated per run with RSA 2048, SHA-256, and 10-year validity. Keys are generated exclusively under `ci/out/secure-boot/uefi/keys/` and never committed or logged.
   - The systemd-boot loader (`/usr/lib/systemd/boot/efi/systemd-bootx64.efi`), recovery UKIs (`vintner-recovery-a.efi`, `vintner-recovery-b.efi`), and os-1 UKI (`vintner-os-1+3-0.efi`) are signed with `sbsign` using the machine key.
   - The signed loader is installed to `/EFI/systemd/systemd-bootx64.efi`, `/EFI/BOOT/BOOTX64.EFI`, and `/EFI/vintner/systemd-bootx64.efi`.
   - The installation manifest is updated with `firmware: "uefi"`, `secure_boot_tier: "A"`, and the signed image hashes and sizes.
   - Candidate generation 2 is built via `systemd-ukify` with identical command line and ABI, signed with the machine key, and staged on `prepared-gen2.raw` with 1 try allowed (`vintner-os-2+1-0.efi`).
2. **Variable store setup:**
   - The OVMF variable store is built from `/usr/share/OVMF/OVMF_VARS_4M.fd` using `virt-fw-vars`.
   - The test platform key is enrolled as PK and KEK (`--set-pk`, `--add-kek`).
   - The machine certificate is enrolled in `db` (`--add-db`).
   - Secure Boot is enforced (`--sb`).
   - For S6 (revocation), both the machine certificate (`add_cert`) and its DER SHA-256 hash (`add_hash`) are enrolled in `dbx`.
   - For S9, the unmodified variable store template without PK is used (`SecureBootEnable=False`).
3. **QEMU execution:**
   - QEMU is invoked with `-machine q35,accel=kvm,smm=on -global driver=cfi.pflash01,property=secure,value=on`, code FD `/usr/share/OVMF/OVMF_CODE_4M.secboot.fd`, and the case-specific variable store copy.
   - Boots timeout and are killed after 120 seconds.
4. **Inspections and Verification:**
   - Before and after each boot, `inspector.py read-disk` inspects the manifest via `vintner manifest show`, enumerates `/EFI/Linux/` and ESP files, and computes SHA-256 hashes of all on-disk EFI binaries.
   - `inspector.py inspect-varstore` parses the variable store via `virt.firmware` and validates enrolled keys, certificates, fingerprints, and Secure Boot enablement status.

## Cases

| ID | Setup | Expected |
|---|---|---|
| S0 | Signed loader, UKI, and recovery; `db` holds machine cert | os-1 boots; serial log confirms `secureboot: Secure boot enabled`; bless marks os-1 `GOOD`. Control for all subsequent cases. |
| S1 | Candidate generation 2 replaced by unsigned UKI | Boot 1 boots os-1, never os-2; os-2 ends with counter 0 (`+0-1`) after 1 try; recorded in manifest as `TRYING`. |
| S2 | Candidate generation 2 signed with foreign, unenrolled key | Same as S1. |
| S3 | Candidate generation 2 validly signed with 1 byte flipped in `.initrd` | Same as S1; flipped offset and hashes recorded in evidence. |
| S4 | Recovery UKI with 1 byte flipped in `.initrd`; ordinary image intact | Boot 1 boots os-1; Boot 2 selects recovery entry and is refused by firmware (`Access denied`). |
| S5 | Recovery UKI and ordinary UKIs all modified | No on-disk image boots; firmware halts at boot manager menu; killed after 120s timeout (OBSERVED). |
| S6 | Machine certificate and hash added to `dbx` | Firmware refuses signed loader (`Access denied`); nothing boots; killed after 120s timeout (OBSERVED). |
| S7 | Loader replaced by unsigned Debian binary | Firmware refuses unsigned loader (`Access denied`); nothing boots; killed after 120s timeout (OBSERVED). |
| S8 | Publish attempted with mismatched certificate | Publish verification (`sb_verify`) fails before files reach boot storage; only os-1 exists on disk; os-1 boots. |
| S9 | Secure Boot disabled in firmware (no PK), tier A manifest | os-1 boots cleanly; serial log shows `secureboot: Secure boot enabled` line is absent (OBSERVED). |

## Checks

| ID | Cases | Expected |
|---|---|---|
| B1 | S0–S9 | Variable store listing shows exactly the intended keys (`db` entries by subject and fingerprint, `dbx` for S6, no PK for S9). |
| B2 | S0–S9 | Signed files verified with `sbverify` on host before boot; unsigned or foreign files fail verification against machine certificate. |
| B3 | S0–S9 | Boot identity matches expected image (`os-1` for S0–S4, S8, S9; `none` for S5–S7). |
| B4 | S0 | Whole line `secureboot: Secure boot enabled` on console and bless marks os-1 `GOOD`. |
| B5 | S1–S3 | Boot 1 is os-1, and post-boot manifest shows refused generation 2 not `GOOD` (counter 0). |
| B6 | S4 | Tampered recovery image never runs while invalid. |
| B7 | S5–S7, S9 | Recorded (OBSERVED) with exact serial ending text. |
| B8 | S8 | Nothing published; os-1 boots. |

## Findings

1. **Refusal fallback and boot counting (S1–S3):**
   When systemd-boot encounters an EFI binary that firmware refuses under Secure Boot (`gBS->StartImage` returns `EFI_ACCESS_DENIED`), systemd-boot returns the error code to firmware rather than continuing to the next entry in the same boot (confirming ADR-0003 Finding 5). OVMF's boot manager attempts a fallback boot to `\EFI\BOOT\BOOTX64.EFI`.
   When candidate generation 2 is configured with 1 try allowed (`vintner-os-2+1-0.efi`), systemd-boot decrements the counter to `vintner-os-2+0-1.efi` during the first attempt. On the fallback invocation within the same power-on, systemd-boot sees that generation 2 has counter 0 (bad), sorts it last according to selection policy, and immediately boots `vintner-os-1+3-0.efi`. The entire fallback and bless boot completes in ~28 seconds.
2. **Revocation in `dbx` (S6):**
   In EDK2 / OVMF, `dbx` signature lists containing `EFI_CERT_SHA256_GUID` match PE/COFF Authenticode image digests rather than X.509 certificate hashes. To revoke a certificate and all binaries signed by it, the certificate must be enrolled in `dbx` as an `EFI_CERT_X509_GUID` signature list (`vl.add_cert('dbx', guid, cert_path)`). Enrolling the certificate causes OVMF to refuse `BOOTX64.EFI` (`BdsDxe: failed to load Boot0001: Access Denied`), successfully blocking execution.
3. **Serial observations for unbootable states (S5–S7):**
   When all images or the loader are invalid, firmware halts at `BdsDxe: No bootable option or device was found. Press any key to enter the Boot Manager Menu.` and the harness records the 120-second timeout kill.
4. **Secure Boot disabled (S9):**
   When the variable store has no PK enrolled and `SecureBootEnable` is OFF, the system boots `os-1` cleanly, and the kernel reports neither lockdown nor `secureboot: Secure boot enabled`.

## Versions

- **Firmware:** `ovmf` `2025.02-8+deb13u1`
- **Emulator:** `qemu-system-x86` `1:10.0.13+ds-0+deb13u1`
- **Loader:** `systemd-boot-efi:amd64` `257.13-1~deb13u1`
- **UKI builder:** `systemd-ukify` `257.13-1~deb13u1`
- **Signing tools:** `sbsigntool` `0.9.4-3.2+deb13u1`
- **Variable store tool:** `python3-virt-firmware` `24.11-2`
- **Guest kernel:** `6.12.111+deb13-cloud-amd64` (`linux-image-6.12.111+deb13-cloud-amd64` `6.12.111-1`)
- **Recovery kernel:** `6.12.107+deb13-amd64` (`linux-image-6.12.107+deb13-amd64` `6.12.107-1`)

## Results

Verbatim `results.tsv` from Secure Boot integration test run on commit `wo-0009b`. All 40 check rows present and valid (PASS or OBSERVED); final gate PASSED.

| ID | Case | Status | Evidence |
|---|---|---|---|
| B1 | S0 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S1 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S2 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S3 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S4 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S5 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S6 | PASS | PK enrolled, db machine cert enrolled (73650a7e...), dbx has machine hash (73650a7e...) |
| B1 | S7 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S8 | PASS | PK enrolled, db=[73650a7e], machine cert not in dbx, SecureBootEnabled=True |
| B1 | S9 | PASS | no PK enrolled, SecureBootEnabled=False |
| B2 | S0 | PASS | valid signed files verified=True, negative controls rejected=True; details: loader=True, os-1=True, rec-a=True, rec-b=True, neg_unsigned_loader=True |
| B2 | S1 | PASS | valid signed files verified=True, negative controls rejected=True; details: unsigned os-2 UKI rejected by sbverify: No signature table present |
| B2 | S2 | PASS | valid signed files verified=True, negative controls rejected=True; details: foreign-signed os-2 UKI rejected by machine cert: Signature verification failed |
| B2 | S3 | PASS | valid signed files verified=True, negative controls rejected=True; details: flipped byte in .initrd (offset 11814500) failed signature check |
| B2 | S4 | PASS | valid signed files verified=True, negative controls rejected=True; details: flipped byte in recovery-a (offset 12236388) failed signature check |
| B2 | S5 | PASS | valid signed files verified=True, negative controls rejected=True; details: every on-disk UKI is invalid |
| B2 | S6 | PASS | valid signed files verified=True, negative controls rejected=True; details: files validly signed on host, machine cert listed in dbx |
| B2 | S7 | PASS | valid signed files verified=True, negative controls rejected=True; details: loader is unsigned Debian binary |
| B2 | S8 | PASS | valid signed files verified=True, negative controls rejected=True; details: publish aborted before copying to /EFI/Linux/ |
| B2 | S9 | PASS | valid signed files verified=True, negative controls rejected=True; details: files validly signed, Secure Boot disabled in firmware |
| B3 | S0 | PASS | boot 1 identity=os-1 (expected os-1) |
| B3 | S1 | PASS | boot 1 identity=os-1 (expected os-1) |
| B3 | S2 | PASS | boot 1 identity=os-1 (expected os-1) |
| B3 | S3 | PASS | boot 1 identity=os-1 (expected os-1) |
| B3 | S4 | PASS | boot 1 identity=os-1 (expected os-1) |
| B3 | S5 | PASS | boot 1 identity=none (expected none, no image ran) |
| B3 | S6 | PASS | boot 1 identity=none (expected none, no image ran) |
| B3 | S7 | PASS | boot 1 identity=none (expected none, no image ran) |
| B3 | S8 | PASS | boot 1 identity=os-1 (expected os-1) |
| B3 | S9 | PASS | boot 1 identity=os-1 (expected os-1) |
| B4 | S0 | PASS | sb_line_found=True, bless_active=True, manifest_good=True |
| B5 | S1 | PASS | boot 1=os-1, gen 2 state=FAILED, refused UKI files=['vintner-os-2+0-1.efi'], sbverify refusal: No signature table present |
| B5 | S2 | PASS | boot 1=os-1, gen 2 state=FAILED, refused UKI files=['vintner-os-2+0-1.efi'], sbverify refusal: Signature verification failed |
| B5 | S3 | PASS | boot 1=os-1, gen 2 state=FAILED, refused UKI files=['vintner-os-2+0-1.efi'], flipped offset=11814500 (inside .initrd=True), sha_before=1670312fd25b..., sha_after=c4e6a8f5a637..., sbverify refusal: flipped byte at 11814500 rejected by sbverify: Hash doesn't match image  got:       827819488c221990169975e0220686ac1c3252b5b301ad09618c125f40f276c2  expecting: df2e53076b19906ff0d1622f24ef01559709bb23d582e22743f9072119af2e41 Image fails hash check |
| B6 | S4 | PASS | boot 1=os-1 (recovery not run), boot 2 (recovery entry selected)=none (recovery refused, not run) |
| B7 | S5 | OBSERVED | how_ended: killed after 120s timeout in 120.06s; serial ending: BdsDxe: failed to start Boot0001 "UEFI Misc Device" from PciRoot(0x0)/Pci(0x2,0x0): Access Denied \| ../src/boot/boot.c:2576@image_start: Error loading \EFI\Linux\vintner-os-1+1-2.efi: Access denied \| BdsDxe: No bootable option or device was found. \| BdsDxe: Press any key to enter the Boot Manager Menu. |
| B7 | S6 | OBSERVED | how_ended: killed after 120s timeout in 120.12s; serial ending: BdsDxe: loading Boot0001 "UEFI Misc Device" from PciRoot(0x0)/Pci(0x2,0x0) \| BdsDxe: failed to load Boot0001 "UEFI Misc Device" from PciRoot(0x0)/Pci(0x2,0x0): Access Denied \| BdsDxe: No bootable option or device was found. \| BdsDxe: Press any key to enter the Boot Manager Menu. |
| B7 | S7 | OBSERVED | how_ended: killed after 120s timeout in 120.11s; serial ending: BdsDxe: loading Boot0001 "UEFI Misc Device" from PciRoot(0x0)/Pci(0x2,0x0) \| BdsDxe: failed to load Boot0001 "UEFI Misc Device" from PciRoot(0x0)/Pci(0x2,0x0): Access Denied \| BdsDxe: No bootable option or device was found. \| BdsDxe: Press any key to enter the Boot Manager Menu. |
| B7 | S9 | OBSERVED | how_ended: poweroff in 24.88s; secureboot_enabled_line_present=False; serial ending: [   19.188003] systemd-shutdown[1]: Syncing filesystems and block devices. \| [   19.209982] systemd-shutdown[1]: Powering off. \| [   19.225968] ACPI: PM: Preparing to enter system sleep state S5 \| [   19.245364] reboot: Power down |
| B8 | S8 | PASS | mismatched publish rejected=True, boot storage images=1 (only os-1), boot 1=os-1 |
