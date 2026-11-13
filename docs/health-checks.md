# Health checks for products

WO-0010 item 4.6. A product (Wine Explorer, any desktop built on vintner-boot) decides when a boot counts as good by adding its own systemd units to `boot-complete.target`. vintner does the rest: when the target is reached, `vintner bless` records the image as `GOOD` and re-arms its boot counter ([architecture §4](architecture.md), [ADR-0003](adr/0003-uefi-entry-naming-and-boot-counting.md)). If a check fails, the boot is not blessed, the try stays spent, and after the last try the next image or recovery boots. This page says how to write a check.

## The contract

A check is a systemd unit that is:

- `Before=boot-complete.target`, so it finishes first, and
- pulled in by the target with `RequiredBy=boot-complete.target` in its `[Install]` section, enabled in the image (`systemctl enable`, a preset, or the symlink in `boot-complete.target.requires/`).

```ini
[Unit]
Description=Desktop session check
Before=boot-complete.target

[Service]
Type=oneshot
ExecStart=/usr/lib/myproduct/check-session
TimeoutStartSec=60

[Install]
RequiredBy=boot-complete.target
```

`ci/recovery-fallback` F4 uses exactly this shape with `ExecStart=/bin/false`: the boot reaches multi-user, the check fails, the image is not blessed, and recovery A boots on the fourth start ([README](../ci/recovery-fallback/README.md), finding 1).

What happens around it:

| Event | Result |
| --- | --- |
| Every check succeeds | `boot-complete.target` is reached, `vintner-bless.service` runs `vintner bless`, the image is `GOOD` and its counter is re-armed |
| A check fails or times out | systemd reports `Dependency failed for boot-complete.target`; `vintner-bless.service` requires the target and does not run; the try stays spent |
| The machine hangs or panics before the target | the same: a hang costs a try like a clean failure ([README](../ci/recovery-fallback/README.md), finding 2) |
| The image has no check at all | nothing holds the target back, and the boot is blessed as soon as systemd reaches it; a product that wants a real test must add one |

vintner adds no check of its own to this target. The `health` and `healthy` fields of `vintner status --json` are something else: the findings of vintner's reconciliation (a foreign boot entry, a `LoaderEntryDefault`, `BootOrder`), not the product's checks ([`status.md`](status.md)).

## Set a time limit

A check that never returns holds the boot until systemd's start timeout (90 s by default) and then fails it. Set `TimeoutStartSec` to what the check really needs, so a hang fails quickly and the next try is not far off.

## The rule to copy: Windows' last known good

Windows XP updates its last known good configuration after a successful logon, not when the system has merely started. A desktop product should do the same: its check passes once the logon screen is up, or once its first-boot setup screen is shown. It should not wait for a user to log in, because nobody may, and it should not pass before the display manager can show a screen, because then a broken desktop is blessed.

## First-boot setup

A product often holds its desktop back while a setup phase runs (account creation, a language choice). The checks that must pass in that phase are the ones the setup phase itself needs: the display manager is up and shows the setup screen, the input devices work, and the setup service started. Write the check so that it passes in setup mode and does not require the desktop's usual services. When setup ends and the real desktop starts, the same check, or a stricter one, applies from the next boot on. Do not make a check depend on an account that setup has not created yet: the image would never be blessed.

If setup reboots the machine, each of those boots counts a try until one of them is blessed. A setup that takes more than a few restarts to finish needs its screen check to pass early, so that the first boot of setup is already `GOOD`.

## Variant boots

A boot of a variant (`vintner.variant=<id>` on the command line, for example Safe Mode, [`variants.md`](variants.md)) changes no boot state: `vintner bless` prints `booted variant <id>: nothing changed` and exits 0 without a rename, a counter write or a manifest change (D-D). Two things follow for checks:

- A check that fails in a variant boot has no counter to spend and nothing falls back. The user restarts, and the next boot is the ordinary default again.
- A variant is for repair, so a check can be skipped there: test `vintner.variant=` in `/proc/cmdline`, for example with `ConditionKernelCommandLine=!vintner.variant` on a unit that should not run in a variant (and a different check, or none, in its place). A check that is skipped by its condition counts as passed.

## Short descriptions

systemd cuts status lines at 80 columns on a serial console. It prints a unit as `name - description`, and a long line is cut in the middle, so the unit's name can be lost. In `ci/recovery-fallback` a failing unit with a long description could not be found in the serial log. Keep `Description=` short (about 30 characters), or leave it out so that the line shows the unit name alone. Give the unit a name that says what failed, for example `myproduct-session-check.service`.

## What vintner does not check

vintner does not read the product's units, run them or tell the product which ones exist. A product proves its checks the way `ci/recovery-fallback` F4 does: a deliberately failing check in the image, and a boot log that shows `Dependency failed for boot-complete.target` followed by the fallback.
