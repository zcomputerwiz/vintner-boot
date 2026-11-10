# BIOS boot interface (WP-004)

This is the contract between the static [`grub.cfg`](grub.cfg), the publisher and the vintner bless service on BIOS machines. It implements the same semantics as UEFI: architecture §4 and [ADR-0003](../../docs/adr/0003-uefi-entry-naming-and-boot-counting.md) option B (re-arm). [`ci/bios-trial/`](../../ci/bios-trial/) proves it on Debian 13's GRUB under SeaBIOS.

## Disk and GRUB installation

- GPT with a BIOS Boot Partition (type `21686148-6449-6e6f-744e-656564454649`, about 2 MiB) and the boot storage partition (type `bc13c2ff-59e6-4262-a352-b275fd6f7172`, FAT32, plain, unencrypted).
- GRUB comes from Debian trixie's `grub-pc-bin`. `boot.img` goes into the MBR code area. `core.img` goes into the BIOS Boot Partition.
- `core.img` contains every module `grub.cfg` uses, so GRUB loads no module from disk:

  `biosdisk part_gpt fat search search_fs_uuid normal configfile linux echo test true regexp eval loadenv keystatus serial terminal halt reboot all_video`

  The list lives in [`core-modules.txt`](core-modules.txt), which every image builder reads; `ci/lint/grub-modules.sh` fails if `grub.cfg` uses a module outside it.

- `grub.cfg` sets up the serial terminal only if the `serial` command succeeds, and clears the screen if it does not, so a machine without a serial port shows no error (WO-0010 1.4; checked by `ci/bios-trial/noserial.py`).

- The early configuration embedded in `core.img` is the only per-machine part:

  ```
  search --no-floppy --fs-uuid --set=root <boot storage FAT UUID>
  set prefix=($root)/grub
  ```

## Boot storage layout

| Path | Written by | Content |
|---|---|---|
| `/grub/grub.cfg` | installer | [`grub.cfg`](grub.cfg), byte for byte. Never regenerated |
| `/grub/grubenv` | installer, then GRUB, the publisher and the bless service | GRUB environment block, 1024 bytes |
| `/vintner/bios/os-<gen>/{vmlinuz,initrd.img,slot.cfg}` | publisher | One ordinary image |
| `/vintner/bios/recovery-a/{vmlinuz,initrd.img,slot.cfg}` | recovery updater | Recovery A |
| `/vintner/bios/recovery-b/{vmlinuz,initrd.img,slot.cfg}` | recovery updater | Recovery B |

`slot.cfg` sets `vintner_cmdline` and `vintner_title` with `set`. `vintner_title` is the text of the slot's menu entry (WO-0010 4.1): the publisher writes the installed `PRETTY_NAME` and the publish date, for example `WineExplorer (2026-11-07)`, the installer writes `Recovery Console` and `Recovery Console (backup)` or the product's `--recovery-title`. A slot without it (published before 4.1) is shown as `Generation <gen>`, recovery as `Recovery Console` and `Recovery Console (backup)`. The command lines of recovery A and B carry `vintner.recovery=a` and `vintner.recovery=b`: they tell the recovery image whether to re-arm `VINTNER_T_RA` (`vintner bless --recovery`). Example:

```
set vintner_cmdline="root=UUID=<root fs uuid> ro quiet vintner.bootstorage=PARTUUID=<boot storage partuuid>"
```

With variants (WO-0010 4.3, [`docs/variants.md`](../../docs/variants.md)) an ordinary slot's `slot.cfg` also lists them. `vintner_variants` holds their IDs separated by spaces, and each ID has a title and a full command line:

```
set vintner_variants="safe"
set vintner_vtitle_safe="Safe Mode"
set vintner_vcmdline_safe="root=UUID=<root fs uuid> ro quiet vintner.bootstorage=PARTUUID=<boot storage partuuid> wexmode=safe vintner.variant=safe"
```

IDs are lower case letters, digits and `_` (they are part of GRUB variable names). Recovery slots have no variants.

`<gen>` is the publish generation from ADR-0003: a decimal integer, strictly increasing per installation and never reused.

## grubenv variables

| Variable | Written by | Meaning |
|---|---|---|
| `VINTNER_ORDER` | publisher | Generations to try, newest first, separated by spaces, for example `42 41 40` |
| `VINTNER_T_<gen>` | publisher, GRUB, bless service | Tries left for that ordinary image: a run of `x` characters (`xxx` is 3), because GRUB script has no arithmetic. `0` means bad |
| `VINTNER_T_RA` | recovery updater, GRUB, recovery image | The same for recovery A. Recovery B has no counter |
| `VINTNER_BOOTED` | GRUB, bless service | The image GRUB chose on this boot: `<gen>`, or `RA` for recovery A |
| `VINTNER_NEXT` | `vintner next-boot`, GRUB | A request for the next boot only (WO-0010 4.4): `recovery`, `<gen>`, `<gen>:<id>` (a variant) or `menu:<seconds>`. GRUB removes it |

A counter that is unset or not made of `x` characters counts as bad. A generation in `VINTNER_ORDER` that is not a decimal integer is skipped.

The whole environment text, header included, must end within the first 512 bytes of the 1024-byte block. The publisher checks this before every write and refuses to add a generation that would cross it. A power cut during a write can leave one sector old and the other new. GRUB's `load_env` accepts such a block and silently mixes any variable that straddles the two sectors (`ci/bios-trial`, T1). With every variable in the first sector, a torn block reads as entirely old or entirely new: at worst one try goes uncounted. This assumes the disk writes each 512-byte sector atomically. Ten 6-digit generations with five tries each use well under 512 bytes (T2).

## Boot algorithm

1. `load_env`. If it fails, or `VINTNER_ORDER` is empty, boot recovery A and then recovery B without writing anything.
2. For each generation in `VINTNER_ORDER` whose counter is not bad:
   1. Remove one `x` from its counter. A counter that becomes empty becomes `0`.
   2. Set `VINTNER_BOOTED` to the generation and write both variables with one `save_env`.
   3. `source` the slot's `slot.cfg`, load its kernel and initramfs, and boot.
   4. If loading fails, go on to the next generation in the same boot. Its counter stays decremented.
3. Recovery A, the same way, with `VINTNER_T_RA` and `VINTNER_BOOTED=RA`.
4. Recovery B, without writing anything.

If `save_env` fails, GRUB cannot count, so it stops trying ordinary images and boots recovery A and then recovery B without writing.

The last try still boots: a counter of `x` becomes `0` and the image boots once more. This matches systemd-boot, where `+1` becomes `+0`.

## Menu

The menu is hidden, with no delay. Holding Shift at boot (`keystatus --shift`) shows a menu with one entry per generation in `VINTNER_ORDER`, titled by the slot's `vintner_title`, each followed by an entry for each of its variants (titled by `vintner_vtitle_<id>`: the slot's title, `: ` and the variant's title), plus recovery A and recovery B (titled by their `vintner_title`). The text matches the UEFI menu, and no entry says "vintner". A menu choice counts like an automatic boot. It also boots an image that is already bad, and still records `VINTNER_BOOTED`, as systemd-boot does on UEFI. Recovery B writes nothing. A variant entry counts nothing and writes nothing either: no counter, no `VINTNER_BOOTED` (D-D). It boots the slot's `vmlinuz` and `initrd.img` with the variant's command line from `slot.cfg`.

## Image token

`grub.cfg` adds `vintner.image=<dir>` to the command line of every image it boots: `os-<gen>`, `recovery-a` or `recovery-b`, and a variant boot adds it after the variant's own command line (WO-0010 4.5). It is not in `slot.cfg` and not in the manifest. The kernel then tells which image booted after the bless service has cleared `VINTNER_BOOTED`, for `vintner status` ([`docs/status.md`](../../docs/status.md)). `ci/bios-trial` rows B23 to B25 show it on the guest's `/proc/cmdline`.

## One-shot request

`vintner next-boot` ([`docs/next-boot.md`](../../docs/next-boot.md)) sets `VINTNER_NEXT`. `grub.cfg` reads it right after `load_env` and unsets it, and then:

- `recovery`: counts and boots recovery A while it is not bad (`VINTNER_BOOTED=RA`), else recovery B.
- `<gen>`: counts and boots that generation. A generation that is already bad boots anyway, as a menu choice does, and `VINTNER_BOOTED` is still set.
- `<gen>:<id>`: boots that variant of the generation, counting nothing and setting no `VINTNER_BOOTED` (D-D).
- `menu:<seconds>`: shows the menu as Shift does, but boots its first entry after that many seconds.
- Any other value is dropped.

The variable leaves the grubenv in the same `save_env` as the counter write, so one power cut cannot leave a counted boot with its request still set. Where GRUB writes no counter (variant, recovery B, menu, a value it drops) it saves the cleared variable first. If `save_env` fails, GRUB does not honour the request: one that cannot be cleared would repeat on every boot. A target that cannot be loaded returns to the automatic order of the same boot, with the request already gone. Holding Shift shows the menu as usual and also uses up a pending request.

## Re-arm (bless)

After `boot-complete.target`, the vintner bless service reads `VINTNER_BOOTED`. If it is a generation, the service sets `VINTNER_T_<gen>` back to the configured number of tries and removes `VINTNER_BOOTED`. The recovery image does the same for `VINTNER_T_RA` once its own early checks pass. An image that fails N consecutive boots therefore becomes bad even if it was good before.

## Publisher

The publisher adds a generation by first writing its slot directory. Then, in one write of the environment block, it sets the generation's counter and puts it at the front of `VINTNER_ORDER`. It removes a generation in the reverse order.

## Writing grubenv from Linux

The publisher, the bless service and the recovery image change `grubenv` the same way. They open the file without truncating it, write the whole 1024-byte block in one write, and fsync. They never truncate, recreate or rename it. `grub-editenv` opens the file with `O_TRUNC` (checked with strace on trixie's 2.12), so a power cut during it can leave an empty `grubenv`. The next boot then goes to recovery A.
