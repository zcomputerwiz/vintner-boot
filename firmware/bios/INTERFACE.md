# BIOS boot interface (WP-004)

This is the contract between the static [`grub.cfg`](grub.cfg), the publisher and the vintner bless service on BIOS machines. It implements the same semantics as UEFI: architecture §4 and [ADR-0003](../../docs/adr/0003-uefi-entry-naming-and-boot-counting.md) option B (re-arm). [`ci/bios-trial/`](../../ci/bios-trial/) proves it on Debian 13's GRUB under SeaBIOS.

## Disk and GRUB installation

- GPT with a BIOS Boot Partition (type `21686148-6449-6e6f-744e-656564454649`, about 2 MiB) and the boot storage partition (type `bc13c2ff-59e6-4262-a352-b275fd6f7172`, FAT32, plain, unencrypted).
- GRUB comes from Debian trixie's `grub-pc-bin`. `boot.img` goes into the MBR code area. `core.img` goes into the BIOS Boot Partition.
- `core.img` contains every module `grub.cfg` uses, so GRUB loads no module from disk:

  `biosdisk part_gpt fat search search_fs_uuid normal configfile linux echo test true regexp eval loadenv keystatus serial terminal halt reboot`

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

`slot.cfg` sets only `vintner_cmdline` (and optionally `vintner_title`) with `set`, for example:

```
set vintner_cmdline="root=UUID=<root fs uuid> ro quiet"
```

`<gen>` is the publish generation from ADR-0003: a decimal integer, strictly increasing per installation and never reused.

## grubenv variables

| Variable | Written by | Meaning |
|---|---|---|
| `VINTNER_ORDER` | publisher | Generations to try, newest first, separated by spaces, for example `42 41 40` |
| `VINTNER_T_<gen>` | publisher, GRUB, bless service | Tries left for that ordinary image: a run of `x` characters (`xxx` is 3), because GRUB script has no arithmetic. `0` means bad |
| `VINTNER_T_RA` | recovery updater, GRUB, recovery image | The same for recovery A. Recovery B has no counter |
| `VINTNER_BOOTED` | GRUB, bless service | The image GRUB chose on this boot: `<gen>`, or `RA` for recovery A |

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

The menu is hidden, with no delay. Holding Shift at boot (`keystatus --shift`) shows a menu with one entry per generation in `VINTNER_ORDER`, plus recovery A and recovery B. A menu choice counts like an automatic boot. It also boots an image that is already bad, and still records `VINTNER_BOOTED`, as systemd-boot does on UEFI. Recovery B writes nothing.

## Re-arm (bless)

After `boot-complete.target`, the vintner bless service reads `VINTNER_BOOTED`. If it is a generation, the service sets `VINTNER_T_<gen>` back to the configured number of tries and removes `VINTNER_BOOTED`. The recovery image does the same for `VINTNER_T_RA` once its own early checks pass. An image that fails N consecutive boots therefore becomes bad even if it was good before.

## Publisher

The publisher adds a generation by first writing its slot directory. Then, in one write of the environment block, it sets the generation's counter and puts it at the front of `VINTNER_ORDER`. It removes a generation in the reverse order.

## Writing grubenv from Linux

The publisher, the bless service and the recovery image change `grubenv` the same way. They open the file without truncating it, write the whole 1024-byte block in one write, and fsync. They never truncate, recreate or rename it. `grub-editenv` opens the file with `O_TRUNC` (checked with strace on trixie's 2.12), so a power cut during it can leave an empty `grubenv`. The next boot then goes to recovery A.
