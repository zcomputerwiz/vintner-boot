# The menu key

Windows XP users press F8 at start to see the boot options. Vintner Boot starts without a menu and without a delay, and a key held at start opens it. WO-0010 item 4.8 documents which key. A product that tells its users "press F8" must know where it holds.

| Firmware | Menu key | Tested |
|---|---|---|
| UEFI | **F8**, or any other key | `ci/selection-policy` row S21 (real systemd-boot under OVMF); reference hardware is WO-0010 6.1 |
| BIOS | **Shift** (hold it) | `ci/bios-trial` row B8 (Shift opens the menu), row B26 (F8 does not) |

## UEFI

`firmware/uefi/loader.conf` sets `timeout 0`, which hides the menu but, unlike `menu-disabled`, still lets a key press at start open it. systemd-boot treats any key as that request, F8 included. Press it repeatedly from power-on, because the loader looks for a key only while it starts. A product may say "press F8" on UEFI.

The firmware's own boot menu is a different key (F12, F11, Esc and others, depending on the maker) and is outside Vintner Boot.

## BIOS

GRUB's `keystatus` reads only the Shift, Ctrl and Alt modifier flags, and a menu that is hidden with no delay never waits for a key. There is no stock GRUB way to see F8 at start without a pause. The spike tried the two ways GRUB has: `keystatus` (no F8) and a timed, interruptible wait (a delay, which R10 forbids). On BIOS the key is therefore **Shift, held from power-on until the menu appears**. `ci/bios-trial` row B26 holds that F8 pressed through the first 20 seconds opens nothing and the newest image boots at once.

A product that supports both firmwares should say "hold Shift or press F8", or choose the wording from `vintner status --json` (`booted_firmware` is the firmware of this boot).

Adding F8 on BIOS would need a GRUB module of our own that polls the keyboard without waiting. That is a change to the loader payload and is not part of this item.

## Without a key

`vintner next-boot menu` shows the menu on the next boot without any key ([`next-boot.md`](next-boot.md)). It is the way for a product that cannot rely on the user's timing.

`vintner menu-timeout <seconds>` makes the menu show at every boot for that long, with no key ([`menu-timeout.md`](menu-timeout.md)); 0, the installed setting, keeps it hidden.
