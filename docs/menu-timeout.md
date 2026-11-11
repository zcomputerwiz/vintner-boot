# Menu timeout

XP's Startup and Recovery dialog sets "Time to display list of operating systems". WO-0010 item 4.7 gives a product the same setting.

```
vintner menu-timeout [--json] [<seconds>]
```

Without a number it shows the setting. With one it sets it: a whole number of seconds from 0 to 3600.

- **0** is the installed setting. The menu is hidden, nothing waits for a key, and a key held at start opens it ([`menu-key.md`](menu-key.md): F8 on UEFI, Shift on BIOS).
- **n above 0** shows the menu for n seconds at every boot, then boots the default entry: the newest generation that is not bad. It is a delay on every start, so a product sets it only when its user asked for it.

It is a different thing from `vintner next-boot menu`, which shows the menu once ([`next-boot.md`](next-boot.md)). A pending `next-boot` request boots at once whatever the timeout is.

## Where it lives

The manifest's `firmware` says which stores to write, whichever firmware booted this system:

| Firmware | Store | Written by |
|---|---|---|
| UEFI | the `timeout` line of `\loader\loader.conf` on the ESP (systemd-boot reads it) | the ESP mounted read-write on demand, the file replaced atomically. Only that line changes; comments and every other setting stay. The command never creates the file, because without it systemd-boot would lose `default vintner-*` |
| BIOS | the grubenv variable `VINTNER_TIMEOUT` on boot storage (`grub.cfg` reads it; `firmware/bios/INTERFACE.md`) | the grubenv, which 0 removes the variable from |

`both` writes both. Setting it takes the same lock as `vintner publish`.

A `LoaderConfigTimeout` EFI variable, which systemd-boot's own menu keys (`+` and `-`) write, beats `loader.conf`. Setting the timeout therefore removes it and says so (`cleared_override`). It is not left to the health check: that check keeps reporting a `LoaderConfigTimeout` that someone set from the menu, and a timeout set with this command never leaves one behind.

## Output and exit codes

`--json` prints one object: `{"ok":true,"firmware":"both","timeout":5,"uefi":5,"bios":5}`. `timeout` is the setting of the loader that booted this system (`uefi` or `bios` alone for an installation with one firmware). `cleared_override` is present when a menu-key override was removed. Failure is `{"ok":false,"code":"…","error":"…"}`.

| Code | Meaning |
|---|---|
| 0 | done |
| 1 | an I/O or state error (boot storage, the ESP, `loader.conf` missing or without a timeout) |
| 2 | a usage error, including a number outside 0 to 3600 |
| 3 | not a vintner installation: no valid manifest |

Flags for tests and the recovery image: `--boot-storage`, `--state-dir`, `--esp` (an already mounted ESP), `--efivars`.

## Reinstalling

`vintner install` deploys the shipped `loader.conf` (`timeout 0`). A platform update that redeploys that file must carry the timeout over; none exists yet.

## Tests

`TestLoaderConfShipped` holds that setting 0 leaves the shipped `loader.conf` as it is and another number changes only its timeout line. `ci/selection-policy` S22 and S23 show systemd-boot showing the menu with `timeout 4` and no key, and not with `timeout 0`. `ci/bios-trial` B27 to B29 show GRUB doing the same from `VINTNER_TIMEOUT`, and a pending request winning over it.
