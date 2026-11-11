# Boot variants

A **variant** is an extra boot entry that a product declares: the same kernel and initramfs as an ordinary image, with extra command-line tokens. Safe Mode (`wexmode=safe`), a basic-video mode (`nomodeset`) and boot logging are variants. WO-0010 item 4.3 adds them; decisions D-C and D-D in the work order fix how.

## Declaring a variant

A product ships `/etc/vintner/variants.d/<id>.conf`. `vintner publish` reads every `*.conf` there.

```
# /etc/vintner/variants.d/safe.conf
title=Safe Mode
tokens=wexmode=safe nomodeset
```

- `<id>` is the file name without `.conf`: lower case letters, digits and `_`, starting with a letter, at most 24 characters. It is part of file names, GRUB variable names and the command line.
- One `key=value` per line. Blank lines and lines starting with `#` are ignored. The value is the rest of the line, trimmed, with no quoting. A key given twice or an unknown key is an error.
- `title` is the name in the boot menu: printable characters, at most 64 bytes.
- `tokens` are extra kernel command-line tokens, separated by blanks. `root=` and every `vintner.` token are vintner's and are refused, as are quote, backslash and `$` (they would need quoting in GRUB). The tokens are not interpreted.

A declaration that does not validate refuses the publish and names the file. The images already published stay bootable, and a quiet drop would hide the product's mistake.

vintner adds `vintner.variant=<id>` to the variant's command line. A product that must tell a variant boot from an ordinary one reads that token from `/proc/cmdline`. The command line is the generation's own command line, then the tokens, then `vintner.variant=<id>`:

```
root=UUID=… ro vintner.bootstorage=PARTUUID=… wexmode=safe nomodeset vintner.variant=safe
```

## What the publisher does

Each publish builds the variants of the new generation together with it:

- **UEFI:** one more UKI per variant, `/EFI/Linux/vintner_variant-os-<gen>-<id>.efi`, signed in tier A like any other image. It has **no boot counter** (D-C): it never becomes bad, and no boot of it spends a try of the ordinary image. Its name does not start with `vintner-`, so `loader.conf`'s `default vintner-*` cannot select it, and with every ordinary image bad recovery A still boots without input. Its `.osrel` carries the ordinary `IMAGE_ID`, the version `<gen>~<id>` and the name `<ordinary name>: <title>`, where the ordinary name is the product's name and the publish date (4.1). systemd-boot sorts versions newest first and `~` sorts before the end of the string, so each variant is listed directly after its generation's ordinary entry and before the next older generation. ADR-0003 has the details.
- **BIOS:** `slot.cfg` of the generation lists the variants (`vintner_variants`, `vintner_vtitle_<id>`, `vintner_vcmdline_<id>`), and the Shift menu shows them directly under their generation. See `firmware/bios/INTERFACE.md`.
- The manifest records each variant with its title, its command line and, on UEFI, its file and hash. A change to a declaration is an ordinary change: the next publish makes a new generation.
- Garbage collection removes a generation's variant files with it, found by name. The health service treats variant files as vintner's: a variant file that no record lists is reported as an unlisted vintner file.
- The image budget counts the variants. If the ordinary image fits but the variants do not, the publisher says so on stderr and publishes the ordinary image without them. It never drops a `GOOD` image or a recovery to make room.

## A variant boot changes nothing (D-D)

Booting a variant re-arms, promotes or demotes no image. A `GOOD` image stays `GOOD`, a bad image stays bad, and a `TRYING` image keeps exactly the tries it had.

- On UEFI a variant has no counter, so systemd-boot renames nothing and sets no `LoaderBootCountPath`.
- On BIOS GRUB's variant entry writes neither a counter nor `VINTNER_BOOTED`.
- `vintner bless` (the bless service and the health service) does nothing when the kernel command line has `vintner.variant=`: no rename, no grubenv write, no manifest write, no reconciliation. A `VINTNER_BOOTED` that a failed ordinary boot left just before stays, so that the next ordinary boot counts the failure.

## Choosing a variant

- From the boot menu: UEFI shows every variant entry in systemd-boot's menu (press F8 or any key at start); BIOS lists them under their generation in the Shift menu ([`menu-key.md`](menu-key.md)).
- From the running system: `vintner next-boot variant <id>` (WO-0010 4.4) restarts into that variant of the generation that would boot next, once.
