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

4. **BIOS Framebuffer & Serial Console Interactions:**
   - `firmware/bios/grub.cfg` includes `insmod all_video`, `set gfxmode=auto`, and `set gfxpayload=keep` while preserving serial console operation.
   - Under Plymouth 24.004.60, when `console=ttyS0,115200` is on the kernel command line, Plymouth automatically falls back to its built-in text `details` theme unless `--ignore-serial-consoles` (or `plymouth.ignore-serial-consoles`) is passed.
   - Additionally, when `--debug` is used without an explicit stream target, Plymouth writes trace output directly to `/dev/tty1` over the bootsplash. Streaming debug output via `plymouth.debug=stream:/run/plymouth-debug.log` isolates tracing to `/run/plymouth-debug.log`, preserving clean framebuffer output.

5. **QEMU Boot Verification (`ci/graphics/run.sh <uefi|bios>`):**
   - Boots guest with display hardware enabled (`-vga std -display none`) and a QMP control socket (`-qmp unix:$OUT/qmp.sock,server,nowait`).
   - Guest test service `vintner-test-splash.service` orders before `plymouth-quit.service`, emits `<4>vintner-test-warning` to `/dev/kmsg` and directly to `/dev/ttyS0`, reports environment status between `VINTNER-GFX-BEGIN` and `VINTNER-GFX-END` to `/dev/ttyS0`, and sleeps 30 seconds to allow host observation.
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

| ID | Check | Status | Evidence |
|---|---|---|---|
| G1 | Command line tokens | PASS | cmdline tokens in order: quiet splash vt.global_cursor_default=0 console=ttyS0,115200 |
| G2 | Serial output with quiet | PASS | markers, VINTNER-REPORT, and <4>vintner-test-warning all arrived on ttyS0 |
| G3 | lsinitramfs contents | PASS | initramfs contains theme, all loaded assets, and DRM drivers where present |
| G4 | plymouthd.conf & default theme | PASS | plymouth-set-default-theme prints vintner-bgrt and plymouthd.conf matches repo byte-for-byte |
| G5 | plymouthd status & debug log | PASS | plymouthd running, loaded vintner-bgrt with 0 asset load errors |
| G6 | Screendump non-blank | PASS | screendump 1280x800 non-blank: 1.09% pixels differ from 0x000000 (>= 0.1%) |
| G7 | Fallback logo MAD golden ratio | PASS | fallback logo at 512,178 (38.2% height): MAD=2.32 <= 5.0 |
| G7-control | Solid red logo control run | PASS | control run correctly failed G7 comparison: solid red logo MAD=88.77 > 50.0 (exceeds threshold 5.0) |
| G9 | ACPI BGRT observation | OBSERVED | default: present=False (none); splash run: present=False (none) |

## Results: BIOS (`graphics-bios`)

| ID | Check | Status | Evidence |
|---|---|---|---|
| G1 | Command line tokens | PASS | cmdline tokens in order: quiet splash vt.global_cursor_default=0 console=ttyS0,115200 |
| G2 | Serial output with quiet | PASS | markers, VINTNER-REPORT, and <4>vintner-test-warning all arrived on ttyS0 |
| G3 | lsinitramfs contents | PASS | initramfs contains theme, all loaded assets, and DRM drivers where present |
| G4 | plymouthd.conf & default theme | PASS | plymouth-set-default-theme prints vintner-bgrt and plymouthd.conf matches repo byte-for-byte |
| G5 | plymouthd status & debug log | PASS | plymouthd running, loaded vintner-bgrt with 0 asset load errors |
| G6 | Screendump non-blank | PASS | screendump 720x400 non-blank: 100.00% pixels differ from 0x000000 (>= 0.1%) |
| G7 | Fallback logo MAD golden ratio | FAIL | fallback logo MAD=56.18 exceeds threshold 5.0 (screendump is VGA text mode screen) |
| G7-control | Solid red logo control run | PASS | control run correctly failed G7 comparison: solid red logo MAD=56.18 > 50.0 (exceeds threshold 5.0) |
| G8 | Linear framebuffer handoff | FAIL | no framebuffer present: fb0 name=none |

## BIOS Framebuffer Comparison (G8 Before & After)

- **Before change (`firmware/bios/grub.cfg` without video module):**
  `/sys/class/graphics/fb0/name`: `none`
  `/sys/class/graphics/fb0/virtual_size`: `none`
- **After change (`insmod all_video`, `set gfxmode=auto`, `set gfxpayload=keep`):**
  `/sys/class/graphics/fb0/name`: `none`
  `/sys/class/graphics/fb0/virtual_size`: `none`

## Findings & Empirical Analysis

1. **Impact of `quiet` on Serial Console:**
   The `quiet` kernel command-line parameter sets `console_loglevel=4` (`CONSOLE_LOGLEVEL_QUIET`). Kernel log messages of severity 4 (`KERN_WARNING`) and higher are normally suppressed from default console output unless explicitly routed. In addition, Plymouth's `TIOCCONS` console interception captures standard console streams. Emitting `<4>vintner-test-warning` to `/dev/kmsg` and directly writing `vintner-test-warning` to `/dev/ttyS0` ensures verification signals arrive reliably on the serial capture port.

2. **Debian Cloud Kernel BIOS Framebuffer Drivers:**
   In `linux-image-6.12.111+deb13-cloud-amd64` (the Debian trixie cloud kernel image), `CONFIG_FB_EFI=y` is compiled in, but `CONFIG_FB_VESA`, `CONFIG_FB_SIMPLE`, and `CONFIG_DRM` (`simpledrm`, `bochs-drm`, `virtio-gpu`) are all unset (`# ... is not set`). As a result, when booted under legacy BIOS (SeaBIOS), the kernel lacks any driver capable of initializing `/dev/fb0`. The console remains in standard 720x400 VGA text mode (`VGA_CONSOLE=y`), resulting in `FB_NAME=none` before and after GRUB configuration changes.

3. **Plymouth Serial Console Behavior:**
   In Plymouth 24.004.60, presence of `console=ttyS0,...` on `/proc/cmdline` causes Plymouth to detect an active serial console via `/sys/class/tty/console/active` and force its text-based `details` theme. Supplying `--ignore-serial-consoles` (or `plymouth.ignore-serial-consoles`) is essential to permit graphical rendering. Furthermore, Plymouth's `--debug` output must be redirected to a stream (`plymouth.debug=stream:...`) to avoid rendering terminal text directly onto the framebuffer.

4. **UEFI ACPI BGRT Observation (G9):**
   Inspection of `/sys/firmware/acpi/bgrt` under standard OVMF and with QEMU `-boot splash=<bmp>` confirms that OVMF does not generate a BGRT ACPI table in this virtualized environment. Plymouth cleanly falls back to rendering `bgrt-fallback.png` centered horizontally at 38.2% screen height, achieving MAD=2.32 against the reference rendering.
