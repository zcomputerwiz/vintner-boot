# WO-0009c: Secure Boot signing in the publisher, tier A

- **For:** the agent that delivers the Go side of Secure Boot (`src/`). Read `docs/secure-boot.md` first: §2 (chain of trust), §3 (keys), §4 (publisher changes), §5 (accepted failures).
- **Issued:** 2026-10-30
- **Status:** open.
- **Branch:** `wo-0009c`, from `origin/main`. Open a PR against `main`. WO-0009b (the QEMU harness) is developed in parallel and does not depend on this work order; it uses its own `sbsign`/`sbverify` shell functions until this lands.

## Goal

Make the publisher produce signed UKIs for tier A and refuse to publish one that does not verify, and give the installer a command that creates the machine key. After this work order, `publish.Publish` accepts `secure_boot_tier` `A` as well as `none`.

## Rules

- Go standard library only, `CGO_ENABLED=0`, SPDX header on every file, the style of the surrounding code (small packages, interfaces for external tools so tests use fakes, as `UKIBuilder` and `DiskSpaceChecker` already do).
- Print no private key material anywhere (logs, errors, test output). Print certificate subject and SHA-256 fingerprint only.
- Every check must be able to fail: each new behaviour gets a test that fails without the change.
- Tier `none` behaviour must not change: every existing test passes unmodified, and the generated UKI for tier `none` is built with exactly the same `ukify` arguments as today.
- Scope: `src/`, `schema/`, `debian/control`, and the `go` job of `.github/workflows/lint.yml` (to install the tools the real-tool test needs, see §6). If a fix needs anything else, stop and report.
- PR body as a file; commits and the PR end with the usual attribution lines.

## 1. Package `src/internal/secureboot`

- `GenerateKey(dir, installationID string) (certSHA256 string, err error)`:
  - RSA 2048 key, X.509 v3 certificate signed with SHA-256, subject `CN=vintner machine <installation_id>`, key usage digital signature, extended key usage code signing (`1.3.6.1.5.5.7.3.3`), validity 3650 days from now, a random 128-bit serial, no CA flag.
  - Writes `<dir>/key.pem` (mode 0600, PKCS#8 `PRIVATE KEY`), `<dir>/cert.pem` (mode 0644) and `<dir>/cert.der` (mode 0644, the DER encoding of the same certificate, for `mokutil --import`). Creates `<dir>` with mode 0700 if missing.
  - Refuses to overwrite: if any of the three files exists, it returns an error and writes nothing. Write each file by creating it with `O_EXCL`.
  - Returns the SHA-256 of the DER certificate as 64 lowercase hex characters.
- `CertSHA256(certPEMPath string) (string, error)`: the same fingerprint for an existing `cert.pem`.
- `KeyMatchesCert(keyPEMPath, certPEMPath string) error`: parses both and fails unless the private key's public half equals the certificate's public key. It must also fail on a missing or unparsable file, and on a key that is not RSA.
- `Verifier` interface `Verify(ctx context.Context, file, certPEMPath string) error` and `SbverifyVerifier`, which runs `sbverify --cert <cert> <file>` (look the binary up with `exec.LookPath`) and returns an error containing the tool's stderr text when the exit status is not zero. A missing `sbverify` is an error, never a pass.

## 2. Command `vintner secureboot init`

In `src/cmd/vintner/`, a new `secureboot.go` and a `case "secureboot"` in `main.go` (usage text updated).

- `vintner secureboot init [--dir /etc/vintner/secureboot] [--installation-id <uuid>]`.
- Without `--installation-id`, it reads it from the manifest the way the other commands find it (see `manifest.go`, `bootstorage.go`). If neither works, it exits non-zero with a clear message.
- On success it prints, on stdout, whole lines: `certificate: <dir>/cert.pem`, `der: <dir>/cert.der`, `sha256: <64 hex>`, `subject: CN=vintner machine <id>`. It never prints the key.
- It exits non-zero and changes nothing if any of the three files already exists.
- It does not touch the manifest and does not enroll anything (enrollment is the installer's job).

## 3. Manifest and schema

- `manifest.Manifest` gains `Signing *Signing` (`json:"signing,omitempty"`), where `Signing` has `CertSHA256 string` (`json:"cert_sha256"`).
- `manifest.UEFIFile` gains `Signed bool` (`json:"signed,omitempty"`).
- `Validate`:
  - tier `A` requires `signing` with a 64-hex lowercase `cert_sha256` and firmware `uefi` or `both` (tier A with firmware `bios` is invalid);
  - tier `none` must not have `signing`;
  - in tier `A` every image's `uefi.signed` must be `true`; in tier `none` it must not be true.
- `schema/manifest.schema.json`, `schema/manifest.md` and a new tier A example under `schema/examples/` (`manifest-uefi-tier-a.json`) are updated and consistent; the existing example validation test covers the new file. `additionalProperties: false` stays.

## 4. Publisher

In `src/internal/publish/publish.go`:

- **Tier gate.** The check that today refuses anything except `none` accepts `none` and `A`. Tiers `B` and `C` keep failing with `unsupported secure boot tier "B" (only "none" and "A" supported)`; update that message and its test.
- **Options.** `Options` gains `SecureBootDir` (default `/etc/vintner/secureboot`) and `Verifier secureboot.Verifier` (default `SbverifyVerifier{}`).
- **Before building (tier A only).** Fail the publish, with nothing written to boot storage, unless all of these hold:
  - `<dir>/cert.pem` and `<dir>/key.pem` exist and `KeyMatchesCert` passes;
  - `CertSHA256(cert.pem)` equals `manifest.signing.cert_sha256` (a mismatch error names both fingerprints).
- **UKI build.** `UKIRequest` gains `SecureBootKey` and `SecureBootCert` (empty for tier `none`). `RealUKIBuilder.Build` adds `--secureboot-private-key=<key>`, `--secureboot-certificate=<cert>` and `--signtool=sbsign` when they are set, and nothing otherwise.
- **Verification before publication.** In step 10, after the readback checks, a tier A publish calls `Verifier.Verify(ctx, stagedUKI, cert.pem)`. On error it follows the same path as the other step 10 failures (`cleanupStagingAndRefuse`): staging is removed, nothing is renamed into `/EFI/Linux/`, no record is left behind. The error text says the signature check failed and includes the verifier's message.
- **Record.** The new image record sets `uefi.signed = true` in tier A.
- **Reconcile.** `state.Reconcile` is unchanged: a signed UKI is still identified by its SHA-256, which is the hash of the signed file.
- Leave test points where they are; no new test point is needed.

## 5. Tests

Unit tests with fakes (they run everywhere, including the `go` job):

- `secureboot` package: generated certificate properties (RSA 2048, `SHA256WithRSA`, code-signing EKU, no CA flag, subject, validity about 3650 days, DER equals the PEM block); file modes 0600 and 0644; refusal to overwrite writes nothing and leaves existing files intact; `KeyMatchesCert` passes for a pair, fails for a mismatched pair, a missing file and an ECDSA key; the fingerprint equals `sha256(DER)`.
- `vintner secureboot init`: prints the four lines, never the key, refuses on existing files, reads the installation id from the manifest and from the flag.
- Manifest: the new example validates; tier A without `signing`, tier A with firmware `bios`, tier `none` with `signing`, and a tier A image with `signed: false` each fail `Validate`.
- Publisher with a fake `UKIBuilder` and a fake `Verifier`:
  - tier A publish passes the key and certificate paths to the builder, calls the verifier on the staged file, and records `signed: true`;
  - a verifier that returns an error leaves `/EFI/Linux/` without the new file, staging empty and no record of the generation (assert all three);
  - a certificate whose fingerprint differs from the manifest, a key that does not match the certificate, and a missing key each fail before the builder is called;
  - tier `none` passes empty signing fields and never calls the verifier;
  - tiers `B` and `C` are refused.

Real-tool test (skips with `t.Skip` when `sbsign`, `sbverify` or `ukify` is missing; runs in CI, see §6): `TestRealSigningRoundTrip` in the `publish` package. It generates a key with `GenerateKey`, builds a UKI with `RealUKIBuilder` (a small file for the kernel and initrd, the stub from `/usr/lib/systemd/boot/efi/linuxx64.efi.stub`), then:

- `SbverifyVerifier` accepts it with the generated certificate;
- it rejects the same file against a certificate from a second `GenerateKey` run;
- it rejects the file after one byte inside the initrd section is flipped (find the section offset with `debug/pe`; the test must prove that the flipped byte is inside the signed content: assert the flipped file differs from the original at that offset);
- it rejects an unsigned UKI built without the signing options.

## 6. Packaging and CI

- `debian/control`: `vintner-core` has no `Depends` field today (only `Recommends`). Add `Depends: sbsigntool` to `vintner-core`, and nothing else; do not change the relationships of the other packages. The package-lifecycle boot tests (`lifecycle-uefi`, `lifecycle-bios`) and the `debian-package` job must still pass, and say so in the PR.
- `.github/workflows/lint.yml`, `go` job: install `sbsigntool`, `systemd-ukify` and `systemd-boot-efi` before `go test`, so that `TestRealSigningRoundTrip` runs and is not skipped. State in the PR how you confirmed that it ran (the `-v` output line `--- PASS: TestRealSigningRoundTrip`, not `SKIP`).

## Validation before you push

- `gofmt -l .` clean, `go vet ./...`, `go test ./...`, `go test -tags vintner_testpoints ./internal/testpoint/` in `src/`.
- `CGO_ENABLED=0 go build ./...`.
- Run `TestRealSigningRoundTrip -v` locally if the tools are available, otherwise say it ran only in CI and quote the CI line.
- Show that a new test fails without its change for these four: key/cert mismatch refusal, verifier failure leaving nothing published, tier gate, schema validation of tier A.

## Out of scope (do not do)

- Signing the loader and the recovery images: the installer and recovery build do that (follow-up work orders).
- Enrolling the certificate anywhere (`db`, MOK).
- The health reports `secure_boot=off`, certificate mismatch and invalid recovery image (follow-up).
- Tiers B and C.

## Reporting back

- The PR body: what changed, the four "fails without the change" proofs, the CI line for the real-tool test, and the full `vintner secureboot init` output of a run (without the key).
- Anything that cannot be done as written, with the exact reason. Do not change `docs/secure-boot.md`; report a conflict with it instead.
