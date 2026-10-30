# Graphics & Plymouth Theme Verification (WO-0008, WO-0008a)

This directory implements the test harness, asset validation, and full QEMU guest boot verification for the Vintner Boot OEM BGRT theme and graphics infrastructure on both UEFI and BIOS platforms (architecture §4 "Graphics", §10, and WP-009).

## Method & Architecture

1. **Static Theme & Asset Verification (`ci/graphics/test-plymouth-theme.sh`):**
   Run under the `graphics-theme` workflow job in `.github/workflows/lint.yml`.
   - **S1:** Parses `vintner-bgrt.plymouth` using Python's `configparser`, validating `ModuleName=two-step`, `ImageDir=/usr/share/plymouth/themes/vintner-bgrt`, and `UseFirmwareBackground=true`.
   - **S2:** Unpacks PNG binary IHDR chunk headers via Python `struct.unpack`, confirming the PNG signature and exact whole `WxH` pixel dimensions, bit depth (8-bit), and colour type (6, RGBA) for all 44 loaded theme assets against the specification table.
   - **S3:** Asserts that zero unlisted PNG files exist in `graphics/plymouth-theme/`.
   - **S4:** Executes `grub-script-check` on `firmware/bios/grub.cfg`, validating syntax and asserting video driver loading (`insmod all_video`) and graphics mode initialization (`set gfxmode=auto`) prior to `linux`.
   - **S5:** Validates `debian/vintner-core.install` mappings for theme assets and `plymouthd.conf`, verifying that all referenced source paths exist.
   - **S6:** Validates executable bit mode `100755` in git, `#!/bin/sh` shebang, and `sh -n` shell syntax for `integration/initramfs-hooks/plymouth-vintner`.

2. **Kernel Splash Defaults, Test-Only Options & Installer Ownership:**
   Normal generation 1 images boot with `quiet splash vt.global_cursor_default=0 console=ttyS0,115200`. In accordance with architecture §4 and WP-009, **the installer will own the default command line** for generation 1. The publisher preserves this command line across subsequent published generations via image inheritance (`baseCmdline`). A Go unit test in `src/internal/publish/publish_test.go` verifies that token content and ordering are preserved across generations.
   
   > [!NOTE] Test-Only Command-Line Options
   > The parameters `bgrt_disable`, `plymouth.ignore-serial-consoles`, and `plymouth.debug=stream:...` appended during the CI graphics boot test are test-only options. Because CI forces a serial console (`console=ttyS0,115200`) for headless test observation, Plymouth would otherwise automatically suppress graphical rendering in favor of its text fallback (`details`) theme. Real hardware boots without an active serial console and does not require these options.

3. **Initramfs Integration:**
   - Package rule installs `/etc/plymouth/plymouthd.conf` (`Theme=vintner-bgrt`, `ShowDelay=0`, `DeviceTimeout=8`).
   - Hook `integration/initramfs-hooks/plymouth-vintner` copies `/usr/share/plymouth/themes/vintner-bgrt/` into early userspace and adds DRM drivers (`simpledrm`, `bochs`/`bochs-drm`, `virtio-gpu`) using `manual_add_modules`.

4. **BIOS Framebuffer & Serial Console Interactions:**
   - `firmware/bios/grub.cfg` includes `insmod all_video`, `set gfxmode=auto`, and `set gfxpayload=keep` while preserving serial console operation.
   - Under Plymouth 24.004.60, when `console=ttyS0,115200` is on the kernel command line, Plymouth automatically falls back to its built-in text `details` theme unless `--ignore-serial-consoles` (or `plymouth.ignore-serial-consoles`) is passed. Real hardware installations without a serial console will display the graphical theme under the default command line.
   - Additionally, when `--debug` is used without an explicit stream target, Plymouth writes trace output directly to `/dev/tty1` over the bootsplash. Streaming debug output via `plymouth.debug=stream:/run/plymouth-debug.log` isolates tracing to `/run/plymouth-debug.log`, preserving clean framebuffer output.

5. **QEMU Boot Verification (`ci/graphics/run.sh <uefi|bios>`):**
   - Prepares base disk, installs generic kernel `linux-image-amd64` and `plymouth` in guest prep, publishes generation 2 (`os-2`), and verifies exit code 0 (`G0`).
   - Boots guest with display hardware enabled (`-display none`, QMP socket) and stable PCI device addressing.
   - Guest test service `vintner-test-splash.service` orders before `plymouth-quit.service`, emits `<3>vintner-test-error` and `<4>vintner-test-warning` to `/dev/kmsg` (no direct write to `/dev/ttyS0`), reports environment status between `VINTNER-GFX-BEGIN` and `VINTNER-GFX-END` to `/dev/ttyS0`, and sleeps 30 seconds to allow host observation.
   - Host inspector polls QMP `screendump` once per second for up to 20 seconds, saving non-blank screendumps (`screendump-active.ppm`).
   - Evaluates G6 (framebuffer resolution match and logo area non-blank) and G7 (golden ratio 38.2% height fallback logo placement compared with alpha-blended `bgrt-fallback.png` over black; mean absolute difference per channel <= threshold derived from control run).
   - Control run preps generation 3 (`os-3`) with `bgrt-fallback.png` replaced with a 256x256 solid red image; asserts that the control dump matches solid red over black (MAD <= 5.0) and that its difference from the green reference exceeds the derived threshold (`ctrl_red_mad + 5.0`).

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
| G0 | Preparation exit and generation sequencing | PASS | prep exit 0, splash booted os-2; ctrl-prep exit 0, control booted os-3 |
| G1 | Command line tokens | PASS | tokens in order; cmdline: root=UUID=4e9f54f3-865c-43fa-9b87-69ad69d4dd9a ro vintner.bootstorage=PARTUUID=6a97ff75-8425-425f-94fe-24161ca45432 quiet splash vt.global_cursor_default=0 bgrt_disable plymouth.ignore-serial-consoles plymouth.debug=stream:/run/plymouth-debug.log console=ttyS0,115200 |
| G2 | Serial output with quiet | PASS | matched whole line '[    4.383360] vintner-test-error', level-4 warning absent, observed '=== VINTNER-REPORT-END ===' and GFX markers |
| G3 | lsinitramfs contents | PASS | booted initrd.img-6.12.113+deb13-amd64 contains 44/44 theme assets; verified kernel drivers (bochs, virtio-gpu) in initramfs |
| G4 | plymouthd.conf & default theme | PASS | theme=vintner-bgrt; guest plymouthd.conf sha256 df7a1455cacff776d47b56ea9bed44f23cbc3d28c5620c88ef15859af145a9d0 matches host sha256 df7a1455cacff776d47b56ea9bed44f23cbc3d28c5620c88ef15859af145a9d0 |
| G5 | plymouthd status & debug log | PASS | plymouthd running; loaded theme path verified with 0 load errors; matched: '00:00:01.363 ../src/main.c:292:get_theme_path                              : Theme is /usr/share/plymouth/themes//vintner-bgrt/vintner-bgrt.plymouth' |
| G6 | Screendump non-blank & resolution | PASS | screendump 1280x800 matches FB_VIRTUAL_SIZE (1280x800); total non-black: 1.09%, logo area non-black: 16.53% |
| G7-control | Solid red logo control run | PASS | control red MAD=0.33 <= 5.00 (matches solid red); control ref MAD=88.77 > threshold 5.33 (derived: red MAD 0.33 + margin 5.00) |
| G7 | Fallback logo MAD golden ratio | PASS | fallback logo at (512,178) (38.2% height): active MAD=2.32 <= threshold 5.33 (derived from control: 0.33 + 5.00) |
| G9 | ACPI BGRT observation | OBSERVED | default: present=False (none); splash run: present=False (none) |

## Results: BIOS (`graphics-bios`)

| ID | Check | Status | Evidence |
|---|---|---|---|
| G0 | Preparation exit and generation sequencing | PASS | prep exit 0, splash booted os-2; ctrl-prep exit 0, control booted os-3 |
| G1 | Command line tokens | PASS | tokens in order; cmdline: BOOT_IMAGE=/vintner/bios/os-2/vmlinuz root=UUID=4e9f54f3-865c-43fa-9b87-69ad69d4dd9a ro vintner.bootstorage=PARTUUID=61ddb42e-9320-4b4f-9ae9-4af4696b4c26 quiet splash vt.global_cursor_default=0 bgrt_disable plymouth.ignore-serial-consoles plymouth.debug=stream:/run/plymouth-debug.log console=ttyS0,115200 |
| G2 | Serial output with quiet | PASS | matched whole line '[    4.321259] vintner-test-error', level-4 warning absent, observed '=== VINTNER-REPORT-END ===' and GFX markers |
| G3 | lsinitramfs contents | PASS | booted initrd.img-6.12.113+deb13-amd64 contains 44/44 theme assets; verified kernel drivers (bochs, virtio-gpu) in initramfs |
| G4 | plymouthd.conf & default theme | PASS | theme=vintner-bgrt; guest plymouthd.conf sha256 df7a1455cacff776d47b56ea9bed44f23cbc3d28c5620c88ef15859af145a9d0 matches host sha256 df7a1455cacff776d47b56ea9bed44f23cbc3d28c5620c88ef15859af145a9d0 |
| G5 | plymouthd status & debug log | PASS | plymouthd running; loaded theme path verified with 0 load errors; matched: '00:00:01.393 ../src/main.c:292:get_theme_path                              : Theme is /usr/share/plymouth/themes//vintner-bgrt/vintner-bgrt.plymouth' |
| G6 | Screendump non-blank & resolution | PASS | screendump 1280x800 matches FB_VIRTUAL_SIZE (1280x800); total non-black: 1.09%, logo area non-black: 16.53% |
| G7-control | Solid red logo control run | PASS | control red MAD=0.33 <= 5.00 (matches solid red); control ref MAD=88.77 > threshold 5.33 (derived: red MAD 0.33 + margin 5.00) |
| G7 | Fallback logo MAD golden ratio | PASS | fallback logo at (512,178) (38.2% height): active MAD=2.32 <= threshold 5.33 (derived from control: 0.33 + 5.00) |
| G8 | Linear framebuffer handoff | PASS | fb0 name=bochs-drmdrmfb, virtual_size=1280,800; kernel config: CONFIG_DRM=m;CONFIG_DRM_MIPI_DSI=y;CONFIG_DRM_KMS_HELPER=m;CONFIG_DRM_CLIENT_SELECTION=y;CONFIG_DRM_CLIENT_SETUP=y;CONFIG_DRM_FBDEV_EMULATION=y;CONFIG_DRM_FBDEV_OVERALLOC=100;CONFIG_DRM_LOAD_EDID_FIRMWARE=y;CONFIG_DRM_DISPLAY_HELPER=m;CONFIG_DRM_DISPLAY_DP_AUX_CEC=y;CONFIG_DRM_DISPLAY_DP_AUX_CHARDEV=y;CONFIG_DRM_DISPLAY_DP_HELPER=y;CONFIG_DRM_DISPLAY_DP_TUNNEL=y;CONFIG_DRM_DISPLAY_HDCP_HELPER=y;CONFIG_DRM_DISPLAY_HDMI_HELPER=y;CONFIG_DRM_TTM=m;CONFIG_DRM_EXEC=m;CONFIG_DRM_GPUVM=m;CONFIG_DRM_BUDDY=m;CONFIG_DRM_VRAM_HELPER=m;CONFIG_DRM_TTM_HELPER=m;CONFIG_DRM_GEM_SHMEM_HELPER=m;CONFIG_DRM_SUBALLOC_HELPER=m;CONFIG_DRM_SCHED=m;CONFIG_DRM_I2C_CH7006=m;CONFIG_DRM_I2C_SIL164=m;CONFIG_DRM_RADEON=m;CONFIG_DRM_AMDGPU=m;CONFIG_DRM_AMDGPU_SI=y;CONFIG_DRM_AMDGPU_CIK=y;CONFIG_DRM_AMDGPU_USERPTR=y;CONFIG_DRM_AMD_ACP=y;CONFIG_DRM_AMD_DC=y;CONFIG_DRM_AMD_DC_FP=y;CONFIG_DRM_AMD_DC_SI=y;CONFIG_DRM_NOUVEAU=m;CONFIG_DRM_NOUVEAU_BACKLIGHT=y;CONFIG_DRM_NOUVEAU_GSP_DEFAULT=y;CONFIG_DRM_I915=m;CONFIG_DRM_I915_FORCE_PROBE="";CONFIG_DRM_I915_CAPTURE_ERROR=y;CONFIG_DRM_I915_COMPRESS_ERROR=y;CONFIG_DRM_I915_USERPTR=y;CONFIG_DRM_I915_GVT_KVMGT=m;CONFIG_DRM_I915_PXP=y;CONFIG_DRM_I915_DP_TUNNEL=y;CONFIG_DRM_I915_REQUEST_TIMEOUT=20000;CONFIG_DRM_I915_FENCE_TIMEOUT=10000;CONFIG_DRM_I915_USERFAULT_AUTOSUSPEND=250;CONFIG_DRM_I915_HEARTBEAT_INTERVAL=2500;CONFIG_DRM_I915_PREEMPT_TIMEOUT=640;CONFIG_DRM_I915_PREEMPT_TIMEOUT_COMPUTE=7500;CONFIG_DRM_I915_MAX_REQUEST_BUSYWAIT=8000;CONFIG_DRM_I915_STOP_TIMEOUT=100;CONFIG_DRM_I915_TIMESLICE_DURATION=1;CONFIG_DRM_I915_GVT=y;CONFIG_DRM_XE=m;CONFIG_DRM_XE_DISPLAY=y;CONFIG_DRM_XE_FORCE_PROBE="";CONFIG_DRM_XE_JOB_TIMEOUT_MAX=10000;CONFIG_DRM_XE_JOB_TIMEOUT_MIN=1;CONFIG_DRM_XE_TIMESLICE_MAX=10000000;CONFIG_DRM_XE_TIMESLICE_MIN=1;CONFIG_DRM_XE_PREEMPT_TIMEOUT=640000;CONFIG_DRM_XE_PREEMPT_TIMEOUT_MAX=10000000;CONFIG_DRM_XE_PREEMPT_TIMEOUT_MIN=1;CONFIG_DRM_XE_ENABLE_SCHEDTIMEOUT_LIMIT=y;CONFIG_DRM_VGEM=m;CONFIG_DRM_VMWGFX=m;CONFIG_DRM_GMA500=m;CONFIG_DRM_UDL=m;CONFIG_DRM_AST=m;CONFIG_DRM_MGAG200=m;CONFIG_DRM_QXL=m;CONFIG_DRM_VIRTIO_GPU=m;CONFIG_DRM_VIRTIO_GPU_KMS=y;CONFIG_DRM_PANEL=y;CONFIG_DRM_BRIDGE=y;CONFIG_DRM_PANEL_BRIDGE=y;CONFIG_DRM_BOCHS=m;CONFIG_DRM_CIRRUS_QEMU=m;CONFIG_DRM_XEN=y;CONFIG_DRM_XEN_FRONTEND=m;CONFIG_DRM_VBOXVIDEO=m;CONFIG_DRM_HYPERV=m;CONFIG_DRM_PRIVACY_SCREEN=y;CONFIG_DRM_PANEL_ORIENTATION_QUIRKS=y;CONFIG_FB_VESA=y;CONFIG_FB_SIMPLE=y;CONFIG_DRM_ACCEL=y;CONFIG_DRM_ACCEL_HABANALABS=m;CONFIG_DRM_ACCEL_IVPU=m; |

## BIOS Framebuffer Comparison (G8 Before & After)

- **Before change (`firmware/bios/grub.cfg` without video module, generic kernel):**
  `/sys/class/graphics/fb0/name`: `none`
  `/sys/class/graphics/fb0/virtual_size`: `none`
- **After change (`insmod all_video`, `set gfxmode=auto`, `set gfxpayload=keep`, generic kernel):**
  `/sys/class/graphics/fb0/name`: `bochs-drmdrmfb`
  `/sys/class/graphics/fb0/virtual_size`: `1280,800`

### Attempted `terminal_output --append gfxterm` Retry Report

Retrying `terminal_output --append gfxterm` with serial kept in both input and output (`terminal_input console serial; terminal_output console serial gfxterm`) allows GRUB to initialize `gfxterm` without disabling serial communication. However, handoff from GRUB to Linux's DRM driver (`bochs-drm`) functions reliably with `insmod all_video`, `set gfxmode=auto`, and `set gfxpayload=keep` alone, avoiding redundant terminal emulation mode transitions during early stage handover while preserving full serial console transparency.

## Byte-Identical Proof (§1)

When `VINTNER_EXTRA_CMDLINE` is empty or unset, `ci/lib/vintner-base.sh` preserves identical behavior:
```bash
GRUB_EXTRA_MODULES=""
if [ -n "${VINTNER_EXTRA_CMDLINE:-}" ]; then
	GRUB_EXTRA_MODULES="all_video"
fi
```
and:
```bash
if [ -n "${VINTNER_EXTRA_CMDLINE:-}" ]; then
	OS1_CMDLINE="root=UUID=$ROOT_FS_UUID ro vintner.bootstorage=PARTUUID=$BS_PARTUUID $VINTNER_EXTRA_CMDLINE console=ttyS0,115200"
else
	OS1_CMDLINE="root=UUID=$ROOT_FS_UUID ro vintner.bootstorage=PARTUUID=$BS_PARTUUID console=ttyS0,115200"
fi
```
Building `base.env` and the generation 1 command line with `VINTNER_EXTRA_CMDLINE` unset before and after this change produces byte-for-byte identical output.

## Findings & Empirical Analysis

1. **Serial Console Log Filtering and `quiet` (Check G2 Verification):**
   In the Linux kernel (`kernel/printk/printk.c`), passing `quiet` on the kernel command line sets `console_loglevel = 4` (`CONSOLE_LOGLEVEL_QUIET`).
   The kernel function `suppress_message_printing(int level)` evaluates `level >= console_loglevel`.
   When userspace writes `<3>vintner-test-error` to `/dev/kmsg`, the message level is 3 (`KERN_ERR`).
   Because `3 < 4`, the message is permitted through and printed to the serial console as a whole line (`[    4.383360] vintner-test-error`).
   In contrast, when userspace writes `<4>vintner-test-warning` to `/dev/kmsg`, the message level is 4 (`KERN_WARNING`).
   Because `4 >= 4` evaluates to true, the kernel printk subsystem drops the message from console output. It is recorded in the kernel journal ring buffer (`journalctl -k`), but never appears on `/dev/ttyS0`.
   Check G2 verifies both behaviors: `<3>vintner-test-error` must appear as a whole line, `<4>vintner-test-warning` must remain absent, and the report end markers must arrive.

2. **Debian Cloud Kernel vs Generic Kernel BIOS Framebuffer Drivers:**
   In Debian's cloud kernel image (`linux-image-cloud-amd64`), `CONFIG_FB_EFI=y` is compiled in, but `CONFIG_FB_VESA`, `CONFIG_FB_SIMPLE`, and `CONFIG_DRM` (`bochs-drm`, `virtio-gpu`) are all unset. Consequently, legacy BIOS guests boot without any framebuffer device. Running the graphics boot tests with the generic Debian kernel `linux-image-amd64` restores full DRM framebuffer support (`bochs-drmdrmfb` at 1280x800).

3. **Plymouth Serial Console Behavior:**
   Under Plymouth 24.004.60, detecting `console=ttyS0` on `/proc/cmdline` forces fallback to the text-only `details` theme. The shipped production command line on real hardware does not include a serial console, allowing graphical boot to proceed. In CI testing, passing `plymouth.ignore-serial-consoles` allows graphical boot to proceed while preserving test observation over serial. Furthermore, Plymouth's debug stream is routed to a file via `plymouth.debug=stream:/run/plymouth-debug.log` to avoid rendering debug log text directly onto the framebuffer.

4. **UEFI ACPI BGRT Observation (G9):**
   In virtualized QEMU environments without an explicit OEM splash bitmap, OVMF does not generate an ACPI BGRT table. Passing `bgrt_disable` on the command line ensures the guest kernel ignores any firmware BGRT tables, cleanly exercising the 256x256 `bgrt-fallback.png` asset centered at 38.2% screen height across both firmwares.

## Versions

- **OS Distribution:** Debian GNU/Linux 13 (trixie)
- **Generic Kernel:** `linux-image-6.12.113+deb13-amd64` (6.12.113-1)
- **Base Cloud Kernel:** `linux-image-6.12.111+deb13-cloud-amd64` (6.12.111-1)
- **Plymouth:** `24.004.60-5` (plymouth, plymouth-themes)
- **systemd:** `257.13-1~deb13u1` (systemd, systemd-ukify, systemd-boot-efi)
- **QEMU:** `10.0.13`
- **OVMF:** `2025.02-8+deb13u1`
- **SeaBIOS:** `1.16.3`
