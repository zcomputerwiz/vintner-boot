# WO-0008a: Finish the boot logo and Plymouth theme, and test it by booting

- **For:** the agent that delivered WO-0008 (`wo-0008`, commit `3406af9`). It needs a Linux environment with `sudo`, KVM, loop devices with partition scanning (`losetup -P`), Go 1.24 and outbound network access.
- **Issued:** 2026-10-29
- **Status:** ready.
- **Branch:** keep working on `wo-0008`. First merge `origin/main` into it with a merge commit, not a rebase; `main` carries this work order. Then open one pull request against `main`.

## Why

WO-0008 delivered a good start: the theme definition, a fallback logo, the spinner frames and the dialog assets. A review found that the rest of its own work order is missing, and that its test cannot show that any of it works. What is missing:

1. Normal images are not published with `quiet splash vt.global_cursor_default=0`.
2. `/etc/plymouth/plymouthd.conf` is shown only in README text. Nothing installs it.
3. Nothing puts the theme and the early graphics drivers into the initramfs.
4. `firmware/bios/grub.cfg` sets `gfxpayload=keep` but loads no video module, so nothing hands a framebuffer to Linux.
5. `ci/graphics/test-plymouth-theme.sh` mostly checks that a string occurs in a file, or that a file is a PNG. It never checks dimensions, though WO-0008 requires it. Nothing boots a guest.
6. `DialogClearsFirmwareBackground=true` in the theme contradicts the README, which says passphrase prompts keep the splash.

This work order closes each gap and replaces the test with one that boots a guest on both firmwares and looks at the result.

## Read first

- `docs/work-orders/WO-0008-boot-logo-plymouth.md` on `wo-0008`, and `graphics/plymouth-theme/`.
- `docs/architecture.md` §4 "Graphics", §10 "Graphics", and §13 WP-009.
- `src/internal/publish/publish.go`, steps 5 to 8 and the step that builds the UKI command line. **The publisher does not take the command line from `/proc/cmdline`.** It copies the command line of the newest `GOOD` image (`baseCmdline`), so a new generation inherits the previous image's options, `quiet splash` included. That is why the publisher stays unchanged (§1).
- `firmware/bios/INTERFACE.md` and `firmware/bios/grub.cfg`. WO-0001 to WO-0007 depend on the serial output and the `vintner: trying <dir>` lines, so keep them.
- `ci/publish/run.sh`, `ci/publish-interrupt/run.sh` and `ci/publish-interrupt/inspector.py`: how to prepare a disk, run a guest operation, read serial output and results, and write `results.tsv` and the final gate. Reuse that approach and the helpers in `ci/lib/`.

## Rules

The rules of WO-0003 to WO-0007 still apply:
- **Disks:** print `lsblk` before every `losetup`, and only touch disks you created.
- **Output:** commit no generated output, image or log; everything goes under `ci/out/`.
- **Evidence:** copy it from output, never hardcode it. Missing evidence is a FAIL.
- **Comparisons:** compare whole values. A command-line option is a whole token.
- **Every check must be able to fail.** The README says what output would make each check FAIL.
- **Expected values:** do not adjust them to fit. Report what the system does.
- **Executable bit:** set it in git.
- **Versions:** take them from the run's `versions.txt`.
- **Pull request body:** pass it as a file.
- **Go:** standard library only, `CGO_ENABLED=0`, SPDX header on new files; `gofmt`, `go vet` and `go test ./...` pass in `src/`.
- **Scope:** you may change `graphics/`, `ci/graphics/`, `debian/`, `integration/initramfs-hooks/`, `firmware/bios/grub.cfg`, `.github/workflows/`, and the narrow addition to `ci/lib/vintner-base.sh` in §3. You may add tests to `src/internal/publish/`. Change no other code under `src/`. Do not change `schema/`, `recovery/` or `docs/architecture.md`. If the work needs a change elsewhere, stop and report why.

## Task

**Deliverables:**
- the fixes in §1 to §4;
- `ci/graphics/run.sh <uefi|bios>` with `inspector.py` if you need one, and `ci/graphics/README.md` with the method, versions and both results tables;
- two CI rows: `graphics-uefi` and `graphics-bios`;
- the corrected static test, run by the `graphics-theme` lint job.

### 1. Command line

**Do not change the publisher.** A new generation inherits the command line of the newest `GOOD` image, so the splash options belong in the command line of the first image, which the installer creates. The installer is not built yet, so this work order does two things:

- Add a Go test to `src/internal/publish/` that publishes a new generation from a `GOOD` image whose command line contains `quiet splash vt.global_cursor_default=0 console=ttyS0,115200`, and requires the new image's command line to contain each of those four options as a whole token, in the same order. The test must fail if the publisher dropped or reordered one.
- Add an optional variable `VINTNER_EXTRA_CMDLINE` to `ci/lib/vintner-base.sh`. When set, its value is appended to the base disk's os-1 command line (the one that contains `vintner.bootstorage=`). When empty or unset the output is byte for byte what it was. Prove that: build the base disk's `base.env` and os-1 command line with the variable unset before and after your change, and compare them. The recovery images' command lines stay as they are, because recovery must stay verbose.

State in `ci/graphics/README.md` that the installer will own the default.

### 2. Plymouth configuration and the initramfs

- Install `/etc/plymouth/plymouthd.conf` (`Theme=vintner-bgrt`, `ShowDelay=0`, `DeviceTimeout=8`) from the package: add it to `debian/vintner-core.install`, with its source under `integration/`.
- Add an initramfs-tools hook under `integration/initramfs-hooks/`, installed to `/etc/initramfs-tools/hooks/`. It runs while the initramfs is built, so the image that the existing `zz-vintner` post-update hook then publishes already contains the theme. Read `integration/initramfs-hooks/README.md` and `integration/initramfs-hooks/zz-vintner` for how the existing hook is installed and why it is a post-update hook. The hook must:
  - include the theme directory, `/usr/share/plymouth/themes/vintner-bgrt/`, in full;
  - include the early graphics drivers `simpledrm`, `bochs` (`bochs-drm` on older kernels) and `virtio-gpu`, using `manual_add_modules`, and tolerate a kernel that does not have one of them as a module;
  - leave the initramfs unchanged on a system without `plymouth` installed, and exit 0.
- The hook is copied into the guest by the test (§3); it is not a package payload until the installer exists.

### 3. BIOS framebuffer

- In `firmware/bios/grub.cfg`, make `gfxpayload=keep` take effect: load the video modules the BIOS build needs (`insmod all_video`, or the smallest set that works) and set a graphics mode (`gfxmode`) before the `linux` command. Do not touch the serial settings, the `vintner: trying <dir>` lines, or the counting logic.
- The test must show, before and after the change, which framebuffer the guest has: print `/sys/class/graphics/fb0/name` and its `virtual_size` (or `none`) from the guest. If a linear framebuffer needs `terminal_output gfxterm` as well, do the minimum, and keep `serial` in `terminal_output` and `terminal_input`.
- `ci/bios-trial` and `ci/recovery-fallback` must still pass unchanged in this pull request's CI.

### 4. Assets

- Remove every PNG that the plymouth `two-step` plugin does not load from this theme. Find out which files it loads (run `plymouthd` with `--debug --debug-file=…` in the guest, or read the plugin's source for Plymouth 24.004.60) and say how you found out. At least one of the `animation-*` and `throbber-*` sets is probably unused; do not remove a file the plugin loads, and `keymap-render.png` is loaded by the keyboard-layout indicator, so keep it.
- Add `graphics/plymouth-theme/ASSETS.md`: for every file or file set, its origin (copied from which package and version, or generated by which committed script) and its licence. Assets you cannot show to be GPL-2.0-or-later compatible are replaced or removed.
- Fix the contradiction in point 6 of the Why section. Keep `DialogClearsFirmwareBackground=true`, because a passphrase prompt should be readable on a plain background, and correct the README to say that the passphrase prompt clears the firmware logo and the splash returns afterwards.

## Verification

### Static checks (`ci/graphics/test-plymouth-theme.sh`, run by the `graphics-theme` lint job)

Replace the grep checks. Python 3 and a real INI parser are available. Every check that cannot fail is removed or made able to fail.

| ID | Check | Fails when |
|---|---|---|
| S1 | The theme file parses; `[Plymouth Theme]` has `ModuleName=two-step`; `[two-step]` has the `ImageDir` the package installs to; `[boot-up]` has `UseFirmwareBackground=true` | any key missing or different; a section missing |
| S2 | Every image file the theme or the plugin loads exists, is a valid PNG (check the signature and parse the header with Python, not the `file` command) and has the dimensions listed in the table in `ci/graphics/README.md`, compared as whole `WxH` values | a file missing or invalid, or any dimension different from the table |
| S3 | No PNG in `graphics/plymouth-theme/` is unlisted in the table | an extra file |
| S4 | `grub-script-check firmware/bios/grub.cfg` passes, and the file loads a video module and sets `gfxmode` before the `linux` command | `grub-script-check` missing (install it; do not fall back to a text search), a syntax error, or either line missing |
| S5 | `debian/vintner-core.install` installs the theme files and `plymouthd.conf` to the paths above, and every source path it names exists | a missing source file or path |
| S6 | The initramfs hook is executable in git, has the right shebang, and `sh -n` passes | any of these |

### Boot tests (`ci/graphics/run.sh <uefi|bios>`)

Prepare a disk as `ci/publish/run.sh` does, with `VINTNER_EXTRA_CMDLINE='quiet splash vt.global_cursor_default=0'`. Install in the guest: `plymouth`, the theme (copied from the repository, as the package would install it), `plymouthd.conf`, and the new initramfs hook. Run `update-initramfs -u -k "$(uname -r)"` so that the publisher builds the image with them. Do not run any other guest operation as part of this.

Boot the result under the firmware given to `run.sh`, with the serial console to a file and QEMU's display on (`-vga std`, `-display none`, a QMP socket). The guest runs a test unit, `vintner-test-splash.service`, that is ordered `Before=plymouth-quit.service plymouth-quit-wait.service` and sleeps for 30 seconds, so that Plymouth is on screen long enough to look at. The unit prints the guest's evidence to `/dev/ttyS0` between marker lines (`VINTNER-GFX-BEGIN`, `VINTNER-GFX-END`), then lets the boot end and the guest power off.

| ID | Expected | Fails when |
|---|---|---|
| G1 | The kernel's `Command line:` line, split into tokens, has the whole tokens `quiet`, `splash`, `vt.global_cursor_default=0` and `console=ttyS0,115200`, in that order | a token missing, or in another order |
| G2 | Serial output still works with `quiet`: the report between the markers and the guest's `VINTNER-REPORT` block both arrive, and a kernel message of level warning or above that the guest prints on purpose (`echo '<4>vintner-test-warning' > /dev/kmsg`) appears on the serial console as a whole line | any of them missing |
| G3 | `lsinitramfs` of the booted image lists `usr/share/plymouth/themes/vintner-bgrt/vintner-bgrt.plymouth` and every image the plugin loads, and the three drivers where the kernel has them | any file missing |
| G4 | `plymouth-set-default-theme` prints `vintner-bgrt`, and `/etc/plymouth/plymouthd.conf` in the guest equals the repository's file byte for byte | a different name or different bytes |
| G5 | While the test unit runs, `plymouthd` is running, and its log (`--debug`) names `vintner-bgrt` as the theme it loaded and records no image it failed to load | not running, another theme, or any load failure |
| G6 | A QMP `screendump` taken while the test unit runs (retry for up to 20 seconds, once a second) is not blank: at least 0.1 % of its pixels differ from the background colour `0x000000` | every dump blank; or no dump taken, which is a FAIL |
| G7 | In the same dump, the fallback logo is where it should be: take `bgrt-fallback.png` (256x256), centred horizontally and with its centre at 38.2 % of the screen height, and compare that rectangle with the logo's alpha-blended pixels over black. The mean absolute difference per channel is at most the value you measure in a control run, plus a margin you state | a larger difference, a missing dump, or a missing control |
| G8 | BIOS only: the framebuffer information in the evidence shows a linear framebuffer (not `none`), and the same check on the build before the `grub.cfg` change shows what changed | `none` after the change |
| G9 | UEFI only, OBSERVED: whether `/sys/firmware/acpi/bgrt/` exists in the guest, and the same pixel comparison as G7 for the BGRT image if it does. OVMF can create a BGRT when QEMU is given a boot splash (`-boot splash=<file.bmp>`); try it as a second run, and report what you get | none; it is evidence, not a gate |

G7's control run: boot once with a theme whose `bgrt-fallback.png` is replaced by a 256x256 solid red image, and show that G7 fails on it. Keep the control run in the CI job and require that it fails.

`results.tsv` has one row per check and firmware: ID, firmware, status, evidence. The script exits 0 when every PASS/FAIL row is present and passes, including the control. Build the final gate as in `ci/publish-interrupt/run.sh`: one row for each expected (check, firmware) pair, no duplicates, no missing rows.

### CI

Add `graphics-uefi` and `graphics-bios` to `.github/workflows/qemu-tests.yml`, with the same setup as the other rows, and keep the lint job `graphics-theme`. All of them, and every existing row, must pass in the pull request's CI. Each new row should finish within 40 minutes.

## Reporting back

- **The pull request:** both results tables with evidence copied from the CI run; the before and after output of the framebuffer check (G8); how you found which images the plugin loads; and `ASSETS.md`.
- **Findings:** anything that differs from the expected tables, with evidence. Report in particular:
  - what `quiet` does to the serial console on this kernel (which messages are lost);
  - whether QEMU's `screendump` captured the Plymouth screen on both firmwares, and how long Plymouth was up;
  - what G9 shows, and whether OVMF created a BGRT;
  - anything in the theme that the pixel comparison shows to be off.

  Do not adjust expected values or margins to make a check pass; say what happened.
- **A step that cannot be done as written:** say exactly why. Do not work around it outside the scope above.

## Notes

- The architecture's graphics test list (§10) also names real GPUs, encrypted-root prompts, `PixelBltOnly` and EDID-native modes. They need hardware or later work and are not part of this work order.
- One statement in my review of WO-0008 was wrong: `keymap-render.png` (25881x50) is not a stray file. Plymouth's keyboard-layout indicator loads it, so it stays.
