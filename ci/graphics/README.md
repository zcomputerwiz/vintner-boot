# Graphics & Plymouth Theme Verification (WO-0008, WO-0008a)

This directory implements the test harness, asset validation, and full QEMU guest boot verification for the Vintner Boot OEM BGRT theme and graphics infrastructure on both UEFI and BIOS platforms (architecture §4 "Graphics", §10, and WP-009).

## Method & Architecture

1. **Static Theme & Asset Verification (`ci/graphics/test-plymouth-theme.sh`):**
   Run under the `graphics-theme` workflow job in `.github/workflows/lint.yml`.
   - **S1:** Parses `vintner-bgrt.plymouth` using Python's `configparser`, validating `ModuleName=two-step`, `ImageDir=/usr/share/plymouth/themes/vintner-bgrt`, and `UseFirmwareBackground=true`.
   - **S2:** Unpacks PNG binary IHDR chunk headers via Python `struct.unpack`, confirming the PNG signature and exact whole `WxH` pixel dimensions for all 44 loaded theme assets against the specification table.
   - **S3:** Asserts that zero unlisted PNG files exist in `graphics/plymouth-theme/`.
   - **S4:** Executes `grub-script-check` on `firmware/bios/grub.cfg`, validating syntax and asserting video driver loading (`insmod all_video`) and graphics mode initialization (`set gfxmode=auto`) prior to `linux`.
   - **S5:** Validates `debian/vintner-core.install` mappings for theme assets and `plymouthd.conf`, verifying that all referenced source paths exist.
   - **S6:** Validates executable bit mode `100755` in git, `#!/bin/sh` shebang, and `sh -n` shell syntax for `integration/initramfs-hooks/plymouth-vintner`.

2. **Kernel Splash Defaults & Installer Ownership:**
   Normal generation 1 images boot with `quiet splash vt.global_cursor_default=0 console=ttyS0,115200`. In accordance with architecture §4 and WP-009, **the installer will own the default command line** for generation 1. The publisher preserves this command line across subsequent published generations via image inheritance (`baseCmdline`). A Go unit test in `src/internal/publish/publish_test.go` verifies that token content and ordering are preserved across generations.

3. **Initramfs Integration:**
   - Package rule installs `/etc/plymouth/plymouthd.conf` (`Theme=vintner-bgrt`, `ShowDelay=0`, `DeviceTimeout=8`).
   - Hook `integration/initramfs-hooks/plymouth-vintner` copies `/usr/share/plymouth/themes/vintner-bgrt/` into early userspace and adds DRM drivers (`simpledrm`, `bochs`/`bochs-drm`, `virtio-gpu`) using `manual_add_modules`.

4. **BIOS Framebuffer Handoff:**
   `firmware/bios/grub.cfg` loads `all_video`, sets `gfxmode=auto` and `gfxpayload=keep`, and appends `gfxterm` to `terminal_output`. This transitions SeaBIOS / legacy BIOS from 80x25 text mode to a linear VBE framebuffer (`fb0`), enabling Plymouth to render boot graphics before DRM KMS drivers load.

5. **QEMU Boot Verification (`ci/graphics/run.sh <uefi|bios>`):**
   - Boots guest with display hardware enabled (`-vga std -display none`) and a QMP control socket (`-qmp unix:$OUT/qmp.sock,server,nowait`).
   - Guest test service `vintner-test-splash.service` orders before `plymouth-quit.service`, emits `<4>vintner-test-warning` to `/dev/kmsg`, reports environment status between `VINTNER-GFX-BEGIN` and `VINTNER-GFX-END` to `/dev/ttyS0`, and sleeps 30 seconds to allow host observation.
   - Host inspector polls QMP `screendump` once per second for up to 20 seconds, saving non-blank screendumps (`screendump-active.ppm`).
   - Evaluates G6 (non-blank pixels >= 0.1%) and G7 (golden ratio 38.2% height fallback logo placement compared with alpha-blended `bgrt-fallback.png` over black; mean absolute difference per channel <= 5.0).
   - Runs control run with `bgrt-fallback.png` replaced with a 256x256 solid red image; asserts that G7 fails on the control run.

## Theme Asset Dimensions Table

| Asset | Dimensions | Count | Role |
|---|---|---|---|
| `bgrt-fallback.png` | 256x256 | 1 | Fallback boot logo when ACPI BGRT is unavailable |
| `watermark.png` | 160x24 | 1 | Bottom center branding watermark |
| `animation-0001.png` ... `animation-0036.png` | 32x32 | 36 | Spinner progress animation sequence |
| `entry.png` | 305x34 | 1 | Decryption passphrase input box |
| `lock.png` | 35x34 | 1 | Encryption lock symbol |
| `bullet.png` | 10x10 | 1 | Password mask bullet character |
| `capslock.png` | 24x28 | 1 | Caps Lock warning indicator |
| `keyboard.png` | 36x36 | 1 | Keyboard layout indicator icon |
| `keymap-render.png` | 25881x50 | 1 | Keyboard layout character glyph atlas |

Total active PNG assets: 44. See [ASSETS.md](../../graphics/plymouth-theme/ASSETS.md) for licensing and upstream package provenance.

## Results: UEFI (`graphics-uefi`)

*(Evidence will be populated directly from PR #52 CI run)*

| ID | Check | Status | Evidence |
|---|---|---|---|
| G1 | Command line tokens | PENDING | PENDING |
| G2 | Serial output with quiet | PENDING | PENDING |
| G3 | lsinitramfs contents | PENDING | PENDING |
| G4 | plymouthd.conf & default theme | PENDING | PENDING |
| G5 | plymouthd status & debug log | PENDING | PENDING |
| G6 | Screendump non-blank | PENDING | PENDING |
| G7 | Fallback logo MAD golden ratio | PENDING | PENDING |
| G7-control | Solid red logo control run | PENDING | PENDING |
| G9 | ACPI BGRT observation | PENDING | PENDING |

## Results: BIOS (`graphics-bios`)

*(Evidence will be populated directly from PR #52 CI run)*

| ID | Check | Status | Evidence |
|---|---|---|---|
| G1 | Command line tokens | PENDING | PENDING |
| G2 | Serial output with quiet | PENDING | PENDING |
| G3 | lsinitramfs contents | PENDING | PENDING |
| G4 | plymouthd.conf & default theme | PENDING | PENDING |
| G5 | plymouthd status & debug log | PENDING | PENDING |
| G6 | Screendump non-blank | PENDING | PENDING |
| G7 | Fallback logo MAD golden ratio | PENDING | PENDING |
| G7-control | Solid red logo control run | PENDING | PENDING |
| G8 | Linear framebuffer handoff | PENDING | PENDING |

## BIOS Framebuffer Comparison (G8 Before & After)

- **Before change (`firmware/bios/grub.cfg` without video module):**
  `/sys/class/graphics/fb0/name`: `none`
  `/sys/class/graphics/fb0/virtual_size`: `none`
- **After change (`insmod all_video`, `set gfxmode=auto`, `terminal_output --append gfxterm`):**
  `/sys/class/graphics/fb0/name`: *to be updated from CI evidence*
  `/sys/class/graphics/fb0/virtual_size`: *to be updated from CI evidence*
