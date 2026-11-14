# Secure Boot design (WP-008, phase 4)

Status: design for review. It refines architecture §8 and `docs/security-threat-model.md`, and decides what the first implementation slice builds. Tier A is implemented (WO-0009c): `publish.Publish` signs with the machine key; `vintner install --secure-boot A` (WO-0010 5.1) creates the key, signs the loader and the recovery images, and refuses to take booting over until the certificate is in `db` (`docs/accepted-platforms.md`). Tier B is implemented as §8 describes (WO-0010 5.2): `vintner install --secure-boot B`, `vintner secureboot request`, and the publisher signs as in tier A; `ci/mok-chain` proves its chain on hand-made disks and `ci/secure-boot-b` the installed system. Tier C is not: the publisher refuses it (`src/internal/publish/publish.go`). Out-of-tree modules under Secure Boot are §9 (WO-0010 5.3).

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

## 8. Tier B: shim and a Machine Owner Key (WO-0010 5.2)

Status: implemented (WO-0010 5.2). [`ci/mok-chain`](../ci/mok-chain/README.md) proves the chain on Debian's binaries; [`ci/secure-boot-b`](../ci/secure-boot-b/README.md) installs with `vintner install --secure-boot B`, writes the request from the installed system, confirms it in MokManager and boots with Secure Boot enforced. Real firmware is WO-0010 6.1. Tier A needs the firmware in setup mode, which an ordinary PC is not in. Tier B works on a PC as it is sold, with Microsoft's keys in `db`, at the cost of one confirmation at the console on the first boot.

### 8.1 Chain of trust

```text
firmware db: Microsoft UEFI CA (2011 or 2023)  ──trusts──►  Debian's shim (shimx64.efi.signed)
shim's MOK list: machine certificate           ──trusts──►  systemd-boot (signed with the machine key)
                                               ──trusts──►  every UKI and both recovery UKIs (the same key)
```

- **shim.** Debian's `shimx64.efi.signed`, unchanged, from the pinned `shim-signed` package at payload build time (architecture §5: the package itself is never installed). It carries a signature by each Microsoft CA, so a `db` with only one of them starts it (`ci/mok-chain` C1, C2).
- **Loader.** The same unsigned `systemd-bootx64.efi` as in tier A, signed with the machine key by the same code. shim verifies it against the MOK list (M0, M1, M4). Debian's own signed systemd-boot would also start a machine-signed UKI (M5); the machine-signed loader is used so that tiers A and B share one signing path and one payload.
- **UKIs and recovery.** Signed exactly as in tier A. systemd-boot's `LoadImage()` goes through shim's verification, so a UKI that the MOK list does not vouch for is refused (M2, M3, M6). This is the answer to architecture §12 risk 1 for Debian 13's binaries.
- **What tier B adds to the trusted base.** Microsoft's CA, Debian's shim and its built-in Debian CA: anything that Debian signs for shim also starts on the machine. Tier A trusts only the machine key.

### 8.2 Files on the ESP

| Path | Content |
|---|---|
| `EFI/vintner/shimx64.efi` | shim; the `Boot####` entry points here |
| `EFI/vintner/grubx64.efi` | systemd-boot, signed with the machine key. The name is shim's built-in name for the program it starts; no GRUB is involved |
| `EFI/vintner/mmx64.efi` | MokManager (`shim-helpers-amd64-signed`) |
| `EFI/BOOT/BOOTX64.EFI`, `grubx64.efi`, `mmx64.efi` | The same three, for firmware that ignores `Boot####` |
| `EFI/vintner/machine-cert.cer` | The machine certificate (DER), for MokManager's Enroll key from disk (§8.4). Public; the private key stays in `/etc/vintner/secureboot` (R11) |

shim's `fbx64.efi` is not installed: it creates boot entries from `BOOTX64.CSV` files, and vintner owns its entry itself. `vintner-platform` gains `uefi/shimx64.efi` and `uefi/mmx64.efi` in its payload set, and keeps its `Conflicts: shim-signed`.

### 8.3 Enrollment

The certificate reaches the MOK list only through MokManager, at the console, before the operating system starts. Nothing in a running system can add to the list, which is the point of it.

1. **The installer writes the request.** `vintner install --secure-boot B` writes three variables through efivarfs: `MokNew` (the certificate as a signature list), `MokAuth` (SHA-256 over `MokNew` and a password in UTF-16, MokManager's plain form) and `MokTimeout` = -1. This is what `mokutil --import` and `mokutil --timeout -1` write; vintner writes them itself so that it needs no further tool and the request can be unit-tested against a fake efivarfs.
2. **The password.** One-time, used once at the console: 1 to 256 printable ASCII characters, because the firmware console can type nothing else. The product passes it (`--mok-password-file`; it is not printed then) or lets `vintner install` make one (10 characters from `a-z` and `2-9`, without the easily confused `l`, `o`, `0` and `1`) and print it with the three steps; either way the product must show it to the person before the restart.
3. **First boot.** shim finds the request and starts MokManager, which waits with no time limit because of `MokTimeout`. The person chooses Enroll MOK, confirms, types the password and restarts (E1). From then on the machine boots like tier A.
4. **Already enrolled.** If `MokListRT` already holds the certificate (a reinstall that kept the key), no request is written.

`MokTimeout` -1 is required, not a refinement: with the default, a person who misses a 10-second prompt loses the request, and the machine then refuses its own loader (E2, E3).

### 8.4 When the first boot goes wrong

Between the takeover and the confirmation the machine can start nothing but MokManager. That is the price of the tier, and the design keeps one way back that needs no second system:

| Event | Result |
|---|---|
| The person types a wrong password three times (E5) | Nothing is enrolled, and nothing boots |
| The request is gone (E2 shows it for a missed prompt; Continue boot in MokManager's menu is expected to end the same way and is not a row yet) | shim refuses the loader, shows the refusal and offers MokManager again |
| From there | Enroll key from disk, then `EFI/vintner/machine-cert.cer`: no password, physical presence is the proof. The next boot works (E4) |
| The ESP copy of the certificate was removed | External recovery media, or Secure Boot off in firmware setup, as for a tier A machine whose `db` lost the certificate |

A product that cannot accept this window uses the two-step order where the stock Debian shim is still the boot path: `vintner secureboot request` (the request alone; it makes the key in `/etc/vintner/secureboot` when there is none, `--password-file` as above), restart, confirm, then `vintner install --secure-boot B`, which keeps that key, finds the certificate in `MokListRT` and has nothing left to ask. The certificate's subject then names a random ID, not the installation's; nothing reads it.

### 8.5 Installer and publisher

- **`vintner install --secure-boot B`.** As tier A (key, signed loader, signed recovery UKIs, tier and fingerprint in the manifest), plus: shim, MokManager and the certificate file on the ESP (§8.2), the boot entry at shim, and the request (§8.3). Where tier A refuses to take booting over until the certificate is in `db`, tier B takes over once the request is written or the certificate is in `MokListRT` (read from efivarfs, `MokListRT1` and on when shim split it, or `/sys/firmware/efi/mok-variables`). Without efivarfs (a chroot on another machine) it needs `--secure-boot-enrolled`, as tier A does. Unlike tier A it keeps a complete key set whose key matches its certificate (a reinstall, or the two-step order of §8.4), and refuses part of one. A `fbx64.efi` in `EFI/BOOT` is removed when the removable path is taken: shim started from there would run it instead of vintner.
- **`vintner boot-entry`.** With `--esp` the entry starts shim when shim is on the ESP; without it, `--secure-boot B` says so. `--check` and the health report look for the entry the manifest's tier needs.
- **`vintner publish`.** The tier gate accepts `B`; signing and the verification before publication are tier A's.
- **Health.** `secure_boot=off` and the certificate mismatch as in tier A. New: the certificate is not in `MokListRT` (the request was never confirmed, or the MOK list was reset in firmware setup).
- **Reconcile and status.** Reconcile looks only at boot storage, so the ESP's `grubx64.efi` and `mmx64.efi` need nothing there. The QEMU rows' disk inspector treats them as vintner's own files in tier B.

### 8.6 Revocation and updates

- shim applies `dbx`, its own `MokListX` and SBAT to the machine-signed loader as to any other program (R1, R2). A raised SBAT level can therefore refuse a loader that the MOK list vouches for, and a shim update is a platform update in the sense of architecture §12 risk 8: the new shim and a loader that satisfies its SBAT level go out together, both verified before either replaces the old one.
- The Microsoft 2011 CA expired on June 27, 2026. Firmware does not check expiry, and Debian's shim carries the 2023 signature as well, so both kinds of `db` start it today (C1, C2). A firmware whose `db` has neither, or whose `dbx` lists this shim, refuses it before anything of vintner's runs (C3); the installer cannot repair that and must say so.

### 8.7 Out-of-tree modules (5.3)

Section 9.

### 8.8 Order of work

1. This section and `ci/mok-chain` (the chain, on Debian's binaries).
2. Payload: shim and MokManager in `vintner-platform`.
3. `vintner install --secure-boot B`, `vintner secureboot request`, the publisher's gate, health and reconcile, with unit tests.
4. A QEMU row that installs with tier B, confirms the request in MokManager and boots with Secure Boot enforced: `ci/secure-boot-b` T1 to T3. Tier A's S1 to S9 are not repeated: the images are signed and checked by the same code, and `ci/mok-chain` M2, M3, M6, R1 and R2 show that shim refuses what the MOK list does not vouch for.
5. Tier C.

Steps 1 to 4 are done.

### 8.9 Decisions

Both were decided by the owner on 2026-11-14, as proposed.

1. **Take over with the request pending (decided), or only after the certificate is enrolled.** The first is one restart and one console step, with the window of §8.4. The second never leaves a machine that cannot boot, and costs a second restart through the stock loader.
2. **vintner writes the request itself (decided), or calls `mokutil`.** `mokutil` would be a new dependency of `vintner-core` and uses a salted password hash by default; the plain form above is what MokManager accepted in E1.

## 9. Out-of-tree kernel modules (WO-0010 5.3)

Status: implemented. What the kernel trusts was measured on Debian 13's `6.12.111+deb13-cloud-amd64` in QEMU with Secure Boot enforced ([`ci/secure-boot-b`](../ci/secure-boot-b/README.md) T4 to T6); the rest is unit-tested.

### 9.1 What the kernel trusts

With Secure Boot on the kernel is locked down (`lockdown=integrity`) and `module.sig_enforce` reads `Y` although `CONFIG_MODULE_SIG_FORCE` is not set. An unsigned module fails with "Loading of unsigned module is rejected", a module signed with a key the kernel does not hold with "Loading of module with unavailable key is rejected" (T4 controls).

| | Tier B | Tier A |
|---|---|---|
| How the machine certificate reaches the kernel | shim passes `MokListRT`; the kernel puts it in `.machine` (`CONFIG_INTEGRITY_MACHINE_KEYRING=y`), linked into `.secondary_trusted_keys` | the kernel loads `db` into `.platform` (`CONFIG_INTEGRITY_PLATFORM_KEYRING=y`); `.machine` is empty |
| Module check | finds the key in the secondary keyring | no key there, so it falls back to `.platform` and finds it |
| A module signed with the machine key | loads (T4, T5) | loads (T6) |

**The work order assumed that tier A cannot load DKMS modules ("no shim, so no MOK list"). That is false on this kernel**: the platform keyring, which holds `db`, is accepted for module signatures. So vintner install and vintner status do not say that modules cannot load in tier A; they say that signed modules load in both tiers.

- `CONFIG_INTEGRITY_CA_MACHINE_KEYRING` is not set, so the machine certificate (RSA 2048, CA:FALSE, `digitalSignature`, code-signing EKU) is accepted as it is. If a later Debian kernel sets it, only CA certificates would reach `.machine` in tier B, and this has to be tested again.
- Measured on one kernel flavour (cloud), on OVMF. The `.platform` fallback is kernel behaviour, not a documented interface; `ci/secure-boot-b` T6 is what notices a change.
- Modules signed by anyone else, and unsigned ones, never load with Secure Boot on. A vendor that signs its own modules needs its key enrolled as a MOK (tier B) by the product.

### 9.2 DKMS signs with the machine key

`vintner install --secure-boot A|B` writes [`/etc/dkms/framework.conf.d/vintner-secureboot.conf`](../src/internal/secureboot/mok.go) (`DKMSDropInContent`):

```text
mok_signing_key=/etc/vintner/secureboot/key.pem
mok_certificate=/etc/vintner/secureboot/cert.der
```

- The values are single-quoted, because DKMS sources the file as shell. DKMS reads the key where it lies and copies nothing, so the key stays off the ESP and boot storage (R11). The installer's test checks that no copy of it exists there.
- DKMS 3.2.2 (Debian 13) reads `framework.conf.d/*.conf`, takes the certificate as PEM or DER, signs with the kernel's `scripts/sign-file` (SHA-256) and signs before it compresses the module (`.ko.xz`). Modules land in `/lib/modules/<abi>/updates/dkms`.
- **DKMS makes a key pair of its own when either file is missing.** The drop-in is therefore written only after the key exists, and `vintner status` reports `out_of_tree: blocked` when the files are gone, the key does not belong to the certificate, or the certificate is not the manifest's (and `unknown` for a user who cannot read the key: it is root's).
- Key scope: a DKMS build runs a vendor's build scripts as root, with the signing key readable, and the module key is the boot key. A vendor script could sign anything the machine trusts. Threat model T3 already has root reading the key; a separate module key is a later option, not done here.
- Tier none has no machine key: with Secure Boot on, `vintner status` says modules are blocked.

### 9.3 A rebuild publishes a generation

A published image holds the kernel and initramfs, but the modules come from `/lib/modules/<abi>` on the root, so a DKMS build, rebuild or removal changes what an image runs with. Each image records the tree it was published with (`module_tree`: the kernel's hash, `modules.dep`'s and `dkms_sha256`, see [`schema/manifest.md`](../schema/manifest.md)).

- **`vintner publish` compares the tree.** Step 6 used to call a publish "unchanged" when the kernel, command line and initramfs matched, so a module change published nothing and only flagged the image `stale`. It now also compares `modules.dep` and the DKMS files:
  - the newest image of the kernel is `GOOD` (or `TRYING` after it was good once): **a new generation is published**, a counted trial of the new tree. The earlier image is then `stale`. The output says `module tree changed (dkms, modules.dep) since generation N was published: publishing a new generation` (a real DKMS install changes `modules.dep` too);
  - the newest image is a `TRYING` one that never booted good: nothing falls back to it, so its record takes the new tree instead of a trial of a trial for each DKMS call of one package transaction;
  - an image with no recorded tree (generation 1 of an installation) differs only when DKMS modules exist, so that installing makes no needless successor;
  - a second publish with nothing changed is `unchanged` again.
- **Who calls publish.**
  - A new kernel: Debian's `/etc/kernel/postinst.d` runs `dkms`, then `initramfs-tools`, then vintner's `zz-vintner`, so the modules are built first. Nothing new is needed.
  - `dkms install`, `remove`, `unbuild` and `uninstall`, which is what a `*-dkms` package's scripts run: vintner-core ships [`/etc/dkms/framework.conf.d/vintner-publish.conf`](../integration/dkms/vintner-publish.conf), whose `post_transaction` runs `/usr/lib/vintner/dkms-post-transaction <kernelver>`. It is silent during a kernel package's `prerm` and `postrm` (a new image for a kernel on its way out is noise), and a failing publish is reported without failing DKMS. **It publishes only for the newest installed kernel**: a new generation sorts newest and so boots first, DKMS visits every kernel that has headers, and an admin can run `dkms install -k <old>`, so a module change must not make an older kernel the default. The health report names the image left out of date, and `vintner publish --kernel <old>` publishes it when that is wanted.
  - Installing linux-headers: DKMS builds with `dkms autoinstall`, and **dkms 3.2.2 does not run `post_transaction` after `autoinstall`** (its `autoinstall` declares `installed_modules` local, and the caller tests the global one). vintner-core therefore ships `/etc/kernel/header_postinst.d/zz-vintner`, which `linux-run-hooks` runs after DKMS's own hook in that directory and which runs the same script.
  - Not covered: a module that DKMS builds on its own at boot. The health report is what shows it.
  - A small window: an image that booted but is not blessed yet still counts as unproven, so a DKMS build in that window changes its record in place, and the bless that follows marks it `GOOD` although no boot began with the new tree. Bless runs when `boot-complete.target` is reached, seconds into the boot.
- **The health report names it.** `vintner bless` and `vintner status` compare the newest image of each kernel with the tree on disk and report `generation N is out of date: the module tree of <abi> changed since it was published (dkms); vintner publish --kernel <abi> ...`, before any publish has recomputed the manifest's `stale` flag.
- **Garbage collection.** When no `GOOD` image is fresh, the newest `GOOD` image still names the kernel whose fallback matters, so a `GOOD` image of another kernel is kept (`state.Keep`, [`schema/state-machine.md`](../schema/state-machine.md)).
- **What a new generation does not give.** It gives a counted trial boot of the new tree, and a record of the tree the image runs with. The earlier image of the same kernel loads the same `/lib/modules/<abi>`, so it is no fallback from a module that breaks the boot: the real fallbacks are a different kernel and recovery (architecture §6.1).

### 9.4 What `vintner status` says

`modules.out_of_tree` is `allowed` when Secure Boot is on, the tier is A or B and DKMS is configured to sign with this installation's machine key; `requires_signature` is then `true` and `reason` says so. It is `blocked` when the drop-in is missing or wrong, and in tier none. With Secure Boot off it follows the kernel's `sig_enforce` as before. The check is of vintner's side; the kernel's side is the measurement in 9.1.

### 9.5 Not tested

- A real kernel install with a pending DKMS module (the order in `/etc/kernel/postinst.d` was read, not run), and a real vendor driver (`nvidia-kernel-dkms` has steps of its own).
- Other kernel flavours, a Debian kernel newer than 6.12.111, real firmware (WO-0010 6.1), and the generic `amd64` image without `pahole` (DKMS then needs the `dwarves` package).
- The boot-time DKMS service, and a changed `dkms` that fixes `autoinstall`: `post_transaction` would then run twice, which is harmless (the second publish is `unchanged`).