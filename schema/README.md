Language-neutral definitions shared by the installed system and the recovery image (architecture §11, WP-002):

- [manifest.md](manifest.md): the installation manifest, how it is stored on boot storage as two checksummed copies, and what each field means.
- [manifest.schema.json](manifest.schema.json): the JSON Schema (draft 2020-12) for the manifest, format 1. [examples/manifest-uefi.json](examples/manifest-uefi.json) validates against it.
- [state-machine.md](state-machine.md): image states (`STAGED`, `TRYING`, `GOOD`, `FAILED`), transitions, reconciliation after interrupted runs, and garbage collection.

The UEFI entry names and `.osrel` fields are in [ADR-0003](../docs/adr/0003-uefi-entry-naming-and-boot-counting.md). The BIOS `grubenv` interface will be in `firmware/bios/INTERFACE.md`.
