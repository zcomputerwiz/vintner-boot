# The product's name in the boot menu

A product names its boot entries with `/etc/vintner/product.conf`. WO-0010 item 4.9 adds it. Without the file the name is the installed system's `PRETTY_NAME`, as before.

```
# /etc/vintner/product.conf
name=WineExplorer
```

The entries then read `WineExplorer (2026-11-10)` and, for a variant, `WineExplorer (2026-11-10): Advanced Options` ([`variants.md`](variants.md)).

- One `key=value` per line. Blank lines and lines starting with `#` are ignored. The value is the rest of the line, trimmed, with no quoting. A key given twice or an unknown key is an error. The format is the one of `variants.d`.
- `name` is printable characters, at most 64 bytes, not empty when given.
- A file that does not validate refuses the publish and names the file. The images already published stay bootable.
- `vintner publish --product-file <path>` reads another file (tests, packaging); the default is `<root-dir>/etc/vintner/product.conf`.

## Where it applies

The name goes where the installed `PRETTY_NAME` went: the `PRETTY_NAME` of each UKI's `.osrel` (UEFI) and `vintner_title` and the variant titles of each slot's `slot.cfg` (BIOS). `/etc/os-release` is not touched.

`vintner publish` reads the file at every publish, so a kernel upgrade keeps the name, and a changed name reaches the entries of the next generation published. Entries already published keep the title they were built with: a name change alone does not publish a generation, as the same inputs publish nothing. The recovery entries have their own title (`--recovery-title`, `docs/architecture.md`).
