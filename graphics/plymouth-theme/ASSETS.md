# Vintner BGRT Theme Assets

This document catalogs every image asset included in the `vintner-bgrt` Plymouth theme, detailing file dimensions, upstream package/version origins, licensing, and plugin loading mechanisms (WO-0008, WO-0008a).

## Plugin Loading Analysis (Plymouth 24.004.60)

The `two-step` module (`plugins/splash/two-step/plugin.c` in Plymouth 24.004.60, pinned in `third_party/pins.tsv` at commit `d6e6366d` / Debian `24.004.60-5`) loads theme assets as follows:

1. **Progress Animation:**
   The `two-step` plugin loads `animation-%04d.png` (or `animation-*.png`, 36 frames) sequentially using `ply_image_new` when animating boot and shutdown transitions. The `throbber-*.png` files (legacy pulsing throbber) are only referenced by the `throbgress` plugin or obsolete themes; `two-step` does not load or reference them. Therefore, `throbber-*.png` (30 files) was removed, while `animation-0001.png` through `animation-0036.png` are loaded and retained.

2. **Dialog and Keyboard Indicators:**
   Passphrase and decryption entry prompts load `entry.png`, `lock.png`, `bullet.png`, and `capslock.png`. The keyboard layout indicator (`ply_keyboard_layout_indicator_new`) loads `keyboard.png` and `keymap-render.png` (the layout glyph strip used to render keys).

3. **Fallback and Watermark:**
   When ACPI BGRT (`/sys/firmware/acpi/bgrt/image`) is unavailable (such as under SeaBIOS or virtual guests without BGRT), `bgrt-fallback.png` is loaded and rendered at the golden ratio (vertical 38.2%). `watermark.png` is loaded and rendered at the screen bottom (vertical 96%).

## Asset Catalog

| File / Pattern | Dimensions | Count | Origin | License |
|---|---|---|---|---|
| `bgrt-fallback.png` | 256x256 | 1 | Vintner Boot project original artwork (clean fallback logo with transparent background) | GPL-2.0-or-later |
| `watermark.png` | 160x24 | 1 | Vintner Boot project original artwork (monochrome text branding) | GPL-2.0-or-later |
| `animation-0001.png` ... `animation-0036.png` | 32x32 | 36 | Debian `plymouth-themes` 24.004.60-5 (`themes/spinner/animation-*.png`) | GPL-2.0-or-later |
| `entry.png` | 305x34 | 1 | Debian `plymouth-themes` 24.004.60-5 (`themes/spinner/entry.png`) | GPL-2.0-or-later |
| `lock.png` | 35x34 | 1 | Debian `plymouth-themes` 24.004.60-5 (`themes/spinner/lock.png`) | GPL-2.0-or-later |
| `bullet.png` | 10x10 | 1 | Debian `plymouth-themes` 24.004.60-5 (`themes/spinner/bullet.png`) | GPL-2.0-or-later |
| `capslock.png` | 24x28 | 1 | Debian `plymouth-themes` 24.004.60-5 (`themes/spinner/capslock.png`) | GPL-2.0-or-later |
| `keyboard.png` | 36x36 | 1 | Debian `plymouth-themes` 24.004.60-5 (`themes/spinner/keyboard.png`) | GPL-2.0-or-later |
| `keymap-render.png` | 25881x50 | 1 | Debian `plymouth-themes` 24.004.60-5 (`themes/spinner/keymap-render.png`) | GPL-2.0-or-later |

Total retained PNG files: 44. All assets are licensed under GPL-2.0-or-later, fully compatible with the project license and `third_party/pins.tsv`.
