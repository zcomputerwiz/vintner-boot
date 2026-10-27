# Vintner BGRT Plymouth Theme

The Plymouth theme that keeps the firmware's BGRT logo through kernel and userspace start-up (architecture §4 "Graphics", WP-009, and WO-0008).

## Overview

Modern UEFI platforms draw an OEM boot logo on screen during early firmware POST and register the image and its coordinates in the ACPI Boot Graphics Resource Table (BGRT).

This theme implements the Plymouth `two-step` module configured for seamless BGRT preservation:
1. **UEFI with BGRT:** When `/sys/firmware/acpi/bgrt/image` is available, Plymouth retains the OEM firmware logo directly from the framebuffer without flickering or redrawing, and positions the spinner animation at `VerticalAlignment=0.7` beneath the logo.
2. **BIOS / Non-BGRT Fallback:** On SeaBIOS, legacy BIOS, or virtual machines where BGRT is absent, Plymouth loads `bgrt-fallback.png` and centers it at the golden ratio (vertical 38.2%), matching the standard UEFI/Microsoft boot logo geometry.
3. **Password & Decryption Prompts:** Disk encryption (LUKS) passphrase prompts use `entry.png`, `lock.png`, `bullet.png`, and `capslock.png` without clearing the boot splash unless dialogs explicitly require it (`DialogClearsFirmwareBackground=true`).

## Files and Directory Layout

Target installation path on Debian systems: `/usr/share/plymouth/themes/vintner-bgrt/`

| File | Description |
|---|---|
| `vintner-bgrt.plymouth` | Theme definition file (two-step plugin configuration) |
| `bgrt-fallback.png` | Fallback boot logo (256x256 RGBA) when ACPI BGRT is unavailable |
| `watermark.png` | Subtle branding watermark displayed at bottom center |
| `animation-*.png` | Spinner animation sequence (frames 0001 to 0036) |
| `throbber-*.png` | Alternative pulsing indicator frames (frames 0001 to 0030) |
| `entry.png` | Password input box background |
| `lock.png` | Encryption lock symbol for password prompts |
| `bullet.png` | Password character mask bullet |
| `capslock.png` | Caps Lock active warning indicator |
| `keyboard.png` | Keyboard indicator symbol |
| `keymap-render.png` | Keymap preview / reference layout |

## Configuration

To activate the theme on an installed Debian system:

```sh
# Set default theme
plymouth-set-default-theme vintner-bgrt

# Ensure daemon configuration specifies zero show delay
cat << 'EOF' > /etc/plymouth/plymouthd.conf
[Daemon]
Theme=vintner-bgrt
ShowDelay=0
DeviceTimeout=8
EOF

# Update initramfs so theme and DRM drivers are in early userspace
update-initramfs -u
```

## Kernel Command-Line Parameters

For flicker-free splash operation, normal UKI or BIOS boot entries should include:

```text
quiet splash vt.global_cursor_default=0
```

Serial console output (e.g. `console=ttyS0,115200`) remains functional for automated logging and failure diagnosis while suppressing console noise on the graphical display (`console=tty0`).
