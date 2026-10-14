# ADR-0002: Implementation language and license

- **Status:** Accepted
- **Date:** 2026-10-14
- **Details:** [`docs/architecture.md`](../architecture.md) §11 and §16

## Context

`src/` holds one code base for the CLI, the publisher, the health checks, the bless service, the platform updater and the recovery tooling (architecture §11). The same binary has to run on the installed Debian system and inside the recovery image. The recovery image must not depend on the target system's `/usr` or libraries (R06). The project ships as Debian packages built from a Debian source package (§5), so build dependencies must be available in Debian or carried in the repository.

The work is mostly file handling, so the tool needs:

- a JSON manifest and state records;
- reading the `.osrel` section of UKIs, which are PE/COFF files;
- hashing, and inspecting X.509 certificates;
- EFI variables through efivarfs;
- renames and `fsync` on FAT;
- file locking.

Signing itself is done by existing tools (`ukify`, `sbsign`).

Architecture §16 recommended one statically linked binary in Go or Rust, with thin POSIX `sh` shims for the Debian hook directories, and asked for the license choice to be decided with the language.

## Decision

- **Go**, using only the standard library.
  - Third-party modules need their own ADR.
  - Build with `CGO_ENABLED=0`, producing one static binary, `vintner`.
  - The Go module lives in `src/` (`go.mod` there), with the command in `src/cmd/vintner/` and packages under `src/internal/`.
  - The minimum Go version is the one Debian 13 ships (Go 1.24).
- **Debian hooks** under `integration/` are POSIX `sh` shims that call `vintner`. They contain no logic beyond argument passing.
- **License: GPL-2.0-or-later** for project code. Every source file carries `SPDX-License-Identifier: GPL-2.0-or-later`. `debian/copyright` says `GPL-2+`.

## Reasons

- The Go standard library already covers the needs above: `encoding/json`, `debug/pe`, `crypto/*` and `crypto/x509`, `os` and `syscall`. With no third-party modules, the Debian source package needs only `golang-go`, and there is no dependency tree to package or vendor. Rust would need crates for JSON and PE parsing (for example `serde` and `goblin`). Those crates would have to be packaged in Debian or vendored.
- A static Go binary runs unchanged in the recovery image. Its size (a few MiB) is small against the recovery budget in §5.
- GPL-2.0-or-later can be combined with GPL-2.0-only code (such as the Linux kernel interfaces it might borrow from) and with GPL-3.0-or-later code (such as GRUB). GPL-2.0-only would rule out the latter.

## Consequences

- Garbage collection and a larger binary than Rust or C would give. Neither matters for a short-lived command-line tool.
- Code that copies from a GPL-3.0-or-later project makes the combined file GPL-3.0-or-later. That needs a note in `third_party/` (architecture §7).
- The recovery image includes the same `vintner` binary as the installed system, so recovery and host always agree on the manifest and state formats in `schema/`.
