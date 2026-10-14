# OVMF Secure Boot Variable Stores

This directory builds the OVMF UEFI Secure Boot variable stores needed by the `vintner-boot` CI test matrix (specifically the Secure Boot rows of the "Firmware axis" defined in `docs/architecture.md` §10).

## Context

As explained in `docs/architecture.md` §8 ("Boot platform safety vs freezes" and "Microsoft CA transition"), the Microsoft Corporation UEFI CA 2011 expired on June 27, 2026. Therefore, testing how the bootloader behaves with old, new, and mixed variable stores is necessary. The generated stores allow the CI environment to boot virtual machines representing these different tiers of Secure Boot trust.

## Required Packages

To build the variable stores, ensure the following packages are installed on Ubuntu 24.04 (or Debian 13):
- `ovmf` (provides the base variable template `OVMF_VARS_4M.fd`)
- `python3-virt-firmware` (provides `virt-fw-vars` and `virt-fw-dump`)
- `openssl` (for generating test keys)

## How to Run

From the root of the repository, execute the build script:

```bash
./ci/ovmf-vars/make-vars.sh
```

The script verifies the downloaded certificates, creates new test keys, builds the `.fd` stores into `ci/out/ovmf-vars/`, and verifies their contents.

## Store Contents and Architecture Mappings

The stores cover the CI firmware axis matrix defined in `docs/architecture.md` §10:

- **`vars-ms2011.fd`**:
  - `KEK`: Microsoft Corporation KEK CA 2011
  - `db`: Microsoft Windows Production PCA 2011 + Microsoft Corporation UEFI CA 2011
  - Test PK and Secure Boot enabled.
  - **Purpose:** Represents an older machine shipped prior to 2023 without updated CA certificates.

- **`vars-ms2023.fd`**:
  - `KEK`: Microsoft Corporation KEK 2K CA 2023
  - `db`: Windows UEFI CA 2023 + Microsoft UEFI CA 2023
  - Test PK and Secure Boot enabled.
  - **Purpose:** Represents a new machine shipping exclusively with the modern CAs.

- **`vars-ms-both.fd`**:
  - `KEK`: Both 2011 and 2023 KEKs.
  - `db`: All 2011 and 2023 db CAs.
  - Test PK and Secure Boot enabled.
  - **Purpose:** Represents a machine that has received firmware updates bridging the CA transition.

- **`vars-project-test.fd`**:
  - `KEK`: Both 2011 and 2023 KEKs.
  - `db`: All Microsoft CAs + the project-specific test db key.
  - Test PK and Secure Boot enabled.
  - **Purpose:** Represents a Tier A pre-enrolled machine where the project key is directly trusted in `db`.

- **`vars-setup-mode.fd`**:
  - Empty (no keys, no PK), Secure Boot disabled (setup mode).
  - **Purpose:** Used for enrollment tests.

## Key Locations

The generated stores are placed in `ci/out/ovmf-vars/`.

During execution, `make-vars.sh` generates a test Platform Key (PK) and a test project key. The private keys are placed here (and are gitignored/ephemeral to the CI run):
- `ci/out/ovmf-vars/keys/test-pk.key`
- `ci/out/ovmf-vars/keys/project-test.key`

Their corresponding public certificates are placed next to the variable stores so the CI can use them to sign test payloads:
- `ci/out/ovmf-vars/test-pk.crt`
- `ci/out/ovmf-vars/project-test.crt`

## Microsoft Certificates

The official Microsoft PEM certificates in `ci/ovmf-vars/certs/` are extracted from the [microsoft/secureboot_objects](https://github.com/microsoft/secureboot_objects) repository, specifically pinned to commit `daa32713651f2ffe97b18df5aaf93ffe238aa464`.
