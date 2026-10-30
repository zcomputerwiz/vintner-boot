# Secure Boot design (WP-008, phase 4)

Status: design for review. It refines architecture §8 and `docs/security-threat-model.md`, and decides what the first implementation slice builds. Nothing here is implemented yet: `publish.Publish` refuses every `secure_boot_tier` except `none` (`src/internal/publish/publish.go`).

## 1. Scope of the first slice

**Tier A with a per-machine key in firmware `db`, no shim.** The reasons:

- It is the only tier that can be tested completely in QEMU: `virt-fw-vars` writes the variable store, so no console interaction is needed (tier B needs a person in MokManager).
- It exercises every piece that tiers B and C reuse: signing in the publisher, signature verification before publication, signed loader and recovery, fallback on a rejected image.
- It matches decision D2 (per-machine keys, no shared site key).

Out of scope for this slice, in this order of later work: tier B (shim and MOK), tier C (Type #1 entries under Debian's signed shim), the Microsoft CA 2011/2023 and SBAT/DBX rows (they need shim), dm-verity, TPM measurements and anti-rollback (D3).

## 2. Chain of trust

```text
firmware db: machine certificate  ──trusts──►  systemd-boot (project-signed)
                                  ──trusts──►  every UKI (project-signed)
                                  ──trusts──►  recovery UKIs (project-signed)
```

- There is no shim in tier A. The firmware verifies the loader and, through `LoadImage()`, every UKI that systemd-boot starts, against `db`. Both use the same machine key, so one enrollment covers the whole chain.
- **Loader.** `systemd-boot-efi` carries the unsigned binary (`systemd-bootx64.efi`). The installer signs it with the machine key (`sbsign`) and installs the signed copy. Debian's `systemd-boot-efi-amd64-signed` is signed by a Debian key that is not in `db`, so tier A does not use it. A platform update (loader payload) is signed with the same procedure before it replaces the old one, as §12 risk 8 requires.
- **UKI.** `ukify build` signs the UKI itself (`--secureboot-private-key`, `--secureboot-certificate`, `--signtool=sbsign`). The signature covers the stub, kernel, initrd, command line and `.osrel` sections, so T4 (changed command line or initramfs) is closed in tier A. Signed addons and credentials stay out of scope (T6 forbids side-loaded files in v1).
- **Recovery.** The installer signs the recovery UKIs with the machine key, at installation and at every recovery update (architecture §8, "Recovery freshness").
- **Module signatures.** Unchanged: the per-build ephemeral kernel key stays inside each image (§6.1).

## 3. Keys

| Item | Decision |
|---|---|
| Key type | RSA 2048, SHA-256, X.509 v3, subject `CN=vintner machine <installation_id>`, code-signing extended key usage, 10-year validity (the `ukify genkey` default is 3650 days). RSA 2048 because the Debian wiki reports that shim failed on a 4096-bit key in MokList (2022); 2048 is also the size every firmware accepts. `db` ignores expiry (firmware has no trusted clock), so expiry is informational. |
| Location | `/etc/vintner/secureboot/key.pem` (mode 0600, root) and `cert.pem` (0644). Never on the ESP, boot storage or recovery media (R11). |
| Creation | `vintner secureboot init` at installation. It refuses to overwrite an existing key. It can call `ukify genkey` (it refuses existing output files, which gives the same guarantee) or `openssl`; either gives the key, a PEM certificate for `sbsign`/`sbverify`, and a DER copy for `mokutil`. |
| Enrollment | Not done by the publisher. See §3.1. |
| Backup | Not stored by the project. Losing the key means re-enrolling a new certificate (documented rehearsal in WP-008). |

### 3.1 Enrollment: what existing implementations do

Checked against the Debian trixie manual pages and the Debian wiki (2026-10-30).

**Tier B, MOK (`mokutil` 0.7.2-1).**
- `mokutil --import <cert.der>` stages an enrolling request for shim. The file must be DER. It asks for a one-time password (`--password`, or `--hash-file`, `--root-pw` and `--generate-hash` for scripted use); `--timeout` sets the MokManager prompt timeout; `--list-new` shows the pending request; `--test-key` and `--list-enrolled` check enrollment; `--sb-state` shows the Secure Boot state.
- The request is confirmed only on the console at the next boot, in MokManager, with that password (Debian wiki: changes to the MOK "may only be confirmed directly from the console at boot time"). This is the one unavoidable manual step of tier B.
- Debian's DKMS does exactly this for module signing: it creates `/var/lib/dkms/mok.key` and `mok.pub` on first build (or with `dkms generate_mok`; `mok_signing_key` and `mok_certificate` in `/etc/dkms/framework.conf` select other files), and the person enrolls `mok.pub` manually with `mokutil --import`. Debian does **not** ship Ubuntu's `update-secureboot-policy` (the `shim-signed` package in trixie contains only `shimx64.efi.signed`). Ubuntu keeps its key under `/var/lib/shim-signed/mok/`.
- The wiki documents signing a kernel with `sbsign` and a MOK key, but says nothing about UKIs or about whether a MOK-trusted key is accepted for images that systemd-boot loads. That stays risk 1 in architecture §12 and the tier B test matrix.

**Tier A, firmware `db` (systemd-boot `loader.conf`, `secure-boot-enroll`, since systemd 253).**
- Key sets live under `/loader/keys/<NAME>/` on the ESP or boot storage, each with three authenticated UEFI variable files: `PK.auth`, `KEK.auth` and `db.auth`. They must be `.auth` files, not bare certificates (the manual page builds them with `sbsiglist` and `sbvarsign`; `efitools` and `sbctl` do the same job).
- It acts only when the firmware is in setup mode. Values: `off`; `manual` (the loader adds boot entries for each key set and a person selects one to enroll); `if-safe` (as `manual`, and enrolls the set named `auto` automatically only when it considers that safe, which today means inside a virtual machine); `force` (always enrolls `auto`, with a warning and timeout when not known to be safe). The manual page warns that the feature "might soft-brick your device if used improperly" and states no default.
- Consequences for us: in QEMU the harness enrolls with `virt-fw-vars` instead, so none of this is needed there. On real hardware `if-safe` does nothing, so the installer uses `manual` (a person picks the key set at the next boot) or `force` (unattended, with the warning screen). The first slice documents `manual` and records `force` as an option.
- The key set must contain a PK, so enrolling it replaces the platform key. Whether the vendor's `KEK` and `db` entries (Microsoft's keys, which Windows and many option ROMs rely on) must be carried over is **not verified** here: the `sbctl` README does not say. Treat it as a hardware risk to settle in the pilot before any real-hardware claim: never enroll a set that drops the existing `db` entries on a machine that dual-boots Windows or needs vendor option ROMs.

**Signing tools.**
- `ukify build` signs with `--secureboot-private-key` and `--secureboot-certificate` and a `--signtool` of `sbsign` (the default), `pesign` or `systemd-sbsign`. `ukify genkey` creates a Secure Boot key and certificate (`--secureboot-certificate-validity` days, default 3650) and refuses to overwrite its outputs.
- `sbsign` needs the private key and certificate in PEM. `sbverify --cert` checks a signature. These two come from `sbsigntool`.

## 4. Publisher changes

1. **Tier gate.** `Publish` accepts tier `A`. Tier `B` and `C` stay refused until their slices exist.
2. **Signing.** `RealUKIBuilder.Build` takes the key and certificate paths and passes the `ukify` signing options. `UKIRequest` gains `SecureBootKey` and `SecureBootCert`, both empty for tier `none`.
3. **Verification before publication.** Step 10 gets one more check on the staged UKI: `sbverify --cert <cert.pem> <uki>` must succeed. This is the check that the state machine already names ("verified (hash, size, signature where the tier requires one)"). A failure is a failed publish: staging is removed by the next reconcile, nothing reaches `/EFI/Linux/`.
4. **Manifest.** The record of each image gains `uefi.signed` (boolean, true in tier A) and the manifest a `signing` object with `cert_sha256`. The health service compares `cert_sha256` with the certificate on disk and reports a mismatch.
5. **Fallback behaviour is unchanged.** A UKI that the firmware refuses to load makes systemd-boot return to the firmware menu or continue with the next entry (ADR-0003, finding 5). Option B of ADR-0003 already bumps the counter before loading, so a repeatedly refused image becomes bad and is skipped. The tests in §6 verify that this holds with a real Secure Boot refusal, not only with a missing file.

## 5. Failure behaviour the design accepts

| Event | Result |
|---|---|
| Newest UKI unsigned, or signed by another key, or modified | Firmware refuses it; systemd-boot ends on the next entry; the refused image's counter reaches 0 and the health service records `FAILED`. |
| Every ordinary UKI refused | Recovery boots (§4.1 policy). |
| Recovery UKI modified | It is refused like any other entry. If an ordinary image still boots, the machine boots; the health service reports the recovery image as invalid (new report). |
| Loader modified or unsigned | The firmware refuses to start it and falls to its next boot option. No on-disk recovery helps (T11 residual risk); only external media does. |
| Certificate removed from `db`, or `db` replaced | Nothing signed by it boots. Same as above; documented in the rehearsal. |
| Certificate listed in `dbx` | Same as a refused image. Tested as the revocation row. |
| Secure Boot off in firmware | The signatures are not checked. The health service reports `secure_boot=off` when the manifest says tier A (new report, read from `SecureBoot` and `SetupMode`). |

## 6. Test plan (feeds WO-0009b)

Run under OVMF with Secure Boot enforced (`OVMF_VARS_4M.fd` converted with `virt-fw-vars --set-pk` and `--add-db`, then `-global driver=cfi.pflash01,property=secure,value=on`). Each case is a QEMU run with serial capture; a check passes only on evidence from the serial log and the host's reads of boot storage.

| ID | Setup | Expected |
|---|---|---|
| S0 | Signed loader and UKI; `db` holds the machine cert | Boots os-1; the kernel log shows Secure Boot enabled (`secureboot: Secure boot enabled`); bless marks os-1 `GOOD`. Control for every other row. |
| S1 | Publish generation 2, then replace its UKI with an unsigned one | Boot 1 does not run os-2; os-1 boots; os-2 ends `FAILED`. |
| S2 | Generation 2 signed by a second, unenrolled key | Same as S1. |
| S3 | One byte flipped in generation 2's initrd section of a validly signed UKI | Same as S1. |
| S4 | Modified recovery UKI, ordinary image intact | os-1 boots; the recovery image is reported invalid. |
| S5 | Modified recovery UKI and every ordinary UKI refused | Machine does not boot an image. The expected result is recorded (no recovery available); it documents the limit of on-disk recovery. |
| S6 | The machine cert's hash added to `dbx` | Nothing boots from disk. Recorded, as T11. |
| S7 | Loader replaced by the unsigned Debian binary | Firmware refuses it; recorded, as T11. |
| S8 | Publisher with a wrong key (the cert on disk does not match the key) | The publish fails at step 10; nothing is published; the previous image still boots. |
| S9 | Secure Boot disabled in firmware, tier A in the manifest | Boots; the health service reports `secure_boot=off`. |

S0 to S4 and S8 are blocking for phase 4. S5, S6, S7 and S9 record behaviour and are reported, not asserted against a bootable outcome.

## 7. Decisions

1. **Loader signing at update time (decided).** The machine key signs loader updates too, in the publisher's platform-update path. This has the same trust as UKI signing: root can read the key (T3), and the residual risk stays as T3 records it. An offline or hardware-backed step is a later, higher-assurance option.
2. **`db` enrollment on real hardware (open).** §3.1 records what systemd-boot offers. The first slice proves the chain in QEMU only. Real hardware needs the vendor-key question in §3.1 answered, and setup mode. That belongs to the installer slice (WP-007) and the hardware pilot list.
3. **Signing tool (decided): `sbsigntool`.** `sbsign` and `sbverify` are in Debian main, `ukify` supports them directly (`--signtool=sbsign`), and they are widely used for exactly this. `vintner-core` gains a dependency on `sbsigntool`. `pesign` is not used.
