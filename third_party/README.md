# Third-party dependencies

This directory contains metadata and pins for upstream projects that `vintner-boot` depends on or references. To comply with our architectural decision to keep never-copied references, we maintain only this index, never duplicating upstream source.

## `pins.tsv` columns

*   **name**: An internal short name for the component.
*   **kind**: `git` for a remote repository, `debian` for a Debian source package.
*   **location**: The git clone URL or Debian source package name.
*   **ref**: For `git`, a tag or branch name; for `debian`, the precise package version string.
*   **commit**: For `git`, the 40-character SHA hash of the tagged commit (not the tag object itself, but the peeled `^{}` commit). For `debian`, this is `-`.
*   **spdx**: The SPDX license expression applying to the component.
*   **role**: How the component is used (e.g., `adopt`, `evaluate`, or `reference`), as defined in architecture.md §7. Note that branches (e.g., master) may record `branch snapshot` here.

## Bumping a pin

To bump a dependency:

1.  Edit the corresponding row in `pins.tsv` with the new `ref` and `commit` (or Debian version). Use tools like `git ls-remote` or the Debian sources API to obtain the correct values.
2.  Run `sh verify.sh` from the `third_party/` directory (or the root). The script will exit 0 and print `OK` for every row if all pins match upstream reality.
3.  Commit the updated TSV file.

## License summary

| Project | SPDX | Usage |
|---|---|---|
| systemd | LGPL-2.1-or-later | adopt (payload) |
| grub / grub2 | GPL-3.0-or-later | adopt (payload) |
| shim / shim-signed | BSD-2-Clause | adopt (payload) |
| limine | BSD-2-Clause | evaluate |
| rauc | LGPL-2.1-only | evaluate |
| dracut | GPL-2.0-or-later | evaluate |
| plymouth | GPL-2.0-or-later | adopt (payload) |
| u-root | BSD-3-Clause | evaluate |
| u-boot | GPL-2.0-or-later | reference only |
| heads | GPL-2.0-only | reference only |
| petitboot | GPL-2.0-only | reference only |
| reactos | GPL-2.0-or-later | reference only |
| initramfs-tools | GPL-2.0-or-later | adopt (build tool) |
| ovmf (edk2) | BSD-2-Clause-Patent | reference only |
| linux | GPL-2.0-only | reference only |

This summary is consistent with the licensing notes in architecture.md §7.
