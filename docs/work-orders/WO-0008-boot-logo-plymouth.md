# WO-0008: Boot logo and Plymouth BGRT theme

- **For:** an agent with a Linux environment (Debian 13 or Ubuntu 24.04), `plymouth`, Go 1.24, and QEMU.
- **Issued:** 2026-10-27
- **Status:** in progress. Work on branch `wo-0008` and open one pull request against `main`.
- **Base branch:** `main`.

## Why

Phase 6 of the architecture (§9) and WP-009 (§13) define the graphics and boot visibility requirements:
- **P1:** Consistent OEM-logo splash on UEFI using BGRT plus Plymouth; predictable fallback on BIOS/headless machines.
- **R10:** Boot visibility: on success, no boot menu and no avoidable text; on failure, errors available on console/serial and via recovery logs.
- §4 ("Graphics"): `systemd-stub`/firmware splash may render early image; Plymouth handles continuing kernel/initramfs/userland animation and unlock prompts. Try OEM BGRT logo preservation. On BIOS, a linear framebuffer (`insmod vbe`, `gfxpayload=keep`) provides framebuffer handoff until KMS loads.

Previously, `graphics/plymouth-theme/` contained only a placeholder README. UKIs and BIOS slot images booted with verbose kernel console logging and no splash theme configured.

This work order delivers the Plymouth BGRT theme, fallback artwork for non-BGRT and BIOS environments, packaging integration, kernel command-line splash defaults, and BIOS framebuffer configuration.

## Read first

- `docs/architecture.md`: §4 ("Graphics"), §9 (phase 6), §10 ("Graphics" matrix axis), and §13 (WP-009).
- `third_party/pins.tsv`: pinned `plymouth` version (24.004.60).
- `debian/control`: package descriptions and relations.
- `firmware/uefi/loader.conf`: `console-mode keep`, `timeout 0`.
- `firmware/bios/grub.cfg`: static GRUB configuration for BIOS machines.
- `src/internal/publish/publish.go`: UKI and BIOS slot generation.

## Rules

- **Output:** commit no generated logs, disk images, or temporary build artifacts; everything goes under `ci/out/`.
- **Evidence:** copy test evidence from actual test runs, never hardcode it.
- **Debian packages:** take packages from `deb.debian.org` trixie as pinned in `third_party/pins.tsv`.
- **Licensing:** theme configuration and assets must be GPL-2.0-or-later compatible, consistent with the repository LICENSE and `third_party/pins.tsv`.
- **Go code:** standard library only, `CGO_ENABLED=0`, SPDX header on new files; `gofmt`, `go vet`, and `go test ./...` must pass in `src/`.
- **Serial logging preserved:** `quiet splash` must not silence serial console output when `console=ttyS0,115200` is present.

## Tasks and Deliverables

### 1. Plymouth Theme (`graphics/plymouth-theme/`)

Implement the `vintner-bgrt` Plymouth theme:
- `vintner-bgrt.plymouth`: Theme definition using `ModuleName=two-step` with `UseFirmwareBackground=true` and `bgrt-fallback.png` fallback image.
- `bgrt-fallback.png`: Clean fallback logo displayed at the golden ratio (vertical 38.2%) when ACPI BGRT is not present (BIOS, virtual machines without BGRT).
- Animation frames (`animation-0001.png` ... `animation-0036.png` or `throbber-*.png`): Discreet circular spinner animation centered beneath the logo at `VerticalAlignment=0.7`.
- Dialog assets: Passphrase prompt box (`entry.png`), lock icon (`lock.png`), bullet (`bullet.png`), and keyboard/capslock indicators for disk encryption unlock prompts.
- `graphics/plymouth-theme/README.md`: Updated documentation describing theme configuration, asset dimensions, BGRT behavior, and manual testing.

### 2. Packaging and System Integration

- Update `debian/vintner-core.install` (or `debian/rules` / `debian/control`) so that `vintner-bgrt` theme files are packaged into `/usr/share/plymouth/themes/vintner-bgrt/`.
- Add Plymouth configuration `/etc/plymouth/plymouthd.conf` setting `Theme=vintner-bgrt` and `ShowDelay=0`.
- Provide an initramfs hook or ensure `update-initramfs -u` includes the theme and early DRM drivers (`simpledrm`, `bochs-drm`, `virtio-gpu`, etc.) in the initramfs.

### 3. Kernel Command Line and Loader Configuration

- Ensure standard normal images published by `vintner publish` include `quiet splash vt.global_cursor_default=0` in their command line, while retaining any configured serial console (e.g. `console=ttyS0,115200`).
- Update `firmware/bios/grub.cfg` to include VBE video support and `gfxpayload=keep` so that BIOS boots hand off an initialized framebuffer to Linux rather than plain 80x25 VGA text mode.

### 4. Verification and CI Test

- Provide a test script `ci/graphics/test-plymouth-theme.sh` that validates:
  1. Theme syntax: `vintner-bgrt.plymouth` parses cleanly with Plymouth keyfile parser.
  2. Asset completeness: all referenced image assets exist, are valid PNG files, and match expected dimensions.
  3. BGRT and fallback mechanics: verifies `two-step` configuration handles both `/sys/firmware/acpi/bgrt/image` presence and `bgrt-fallback.png` fallback.
  4. BIOS GRUB syntax: verifies `grub-script-check` passes on updated `firmware/bios/grub.cfg`.
