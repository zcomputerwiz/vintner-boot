# Installation manifest

The manifest records every boot image on one installation and its state. The publisher, the bless service, the health service and the recovery image read and write it. All of them use the same `vintner` binary (ADR-0002).

The firmware and boot loaders never read the manifest. Boot selection depends only on the files on boot storage and the boot counters in their names or in `grubenv` (architecture §4.1, ADR-0003). The manifest is not covered by any signature. A tampered manifest can mislead reporting and garbage collection, but it cannot make an unsigned image boot (threat model T1, T2).

## Storage

The manifest lives on the boot storage partition, so that recovery can read it without the Debian root:

```text
/vintner/state/manifest-a
/vintner/state/manifest-b
```

Each file is exactly two lines:

1. the manifest as one line of compact JSON, matching [`manifest.schema.json`](manifest.schema.json);
2. the lowercase hex SHA-256 of the bytes of line 1, excluding its newline.

A copy is **valid** if it has both lines, the hash matches, and the JSON parses and validates.

**Reading:** read both copies and use the valid one with the higher `seq`. If neither is valid, the installation is damaged. The health service reports it, the publisher refuses to publish, and recovery offers to rebuild the manifest from the files on boot storage.

**Writing:** every change goes to the copy that does *not* hold the current highest valid `seq`. It is written with `seq` + 1, using these steps:

1. truncate and write the file;
2. `fsync` the file;
3. `fsync` the `/vintner/state` directory.

The copy holding the current state is never written. A power cut therefore leaves either the old state or the new one, never neither (R08). FAT gives no atomic rename-over, which is why two copies are used instead of a rename.

## Fields

The schema is authoritative. This table explains the intent.

| Field | Meaning |
|---|---|
| `format` | Manifest format version, currently `1` |
| `seq` | Write sequence number; see Storage |
| `installation_id` | UUID chosen at installation |
| `machine_id` | `/etc/machine-id` of the installed system, used to detect a cloned or swapped root (threat model T12) |
| `firmware` | `uefi`, `bios`, or `both` for mixed-firmware lab disks (architecture §5) |
| `secure_boot_tier` | `A`, `B`, `C` or `none` (architecture §8) |
| `root` | Root filesystem identity: GPT `partuuid` and filesystem `uuid` |
| `boot_storage` | `partuuid` of the boot storage partition |
| `tries` | Boot counter value used for new and re-armed images (default 3) |
| `retain` | Number of ordinary images kept (default 3, architecture §5 image budget) |
| `next_generation` | The generation the next published image gets. It only ever increases (ADR-0003) |
| `images` | Ordinary images; see below |
| `recovery` | Recovery images `a` and `b`; see below |

An ordinary image record:

| Field | Meaning |
|---|---|
| `generation` | Publish generation, also its `IMAGE_VERSION` and file name stem (ADR-0003) |
| `state` | `STAGED`, `TRYING`, `GOOD` or `FAILED`; see [state-machine.md](state-machine.md) |
| `stale` | `true` when the installed module tree no longer matches `module_tree` (architecture §6.1). Not a state: a stale image keeps its state but does not count as the known-good fallback for R04 |
| `kernel` | `abi` (for example `6.12.43+deb13-amd64`) and the Debian `package` and `version` it came from |
| `module_tree` | `kernel_sha256` (SHA-256 of `/boot/vmlinuz-<abi>` at publish time; it identifies the build and with it the module signing key), `modules_dep_sha256` (SHA-256 of `/lib/modules/<abi>/modules.dep`) and `dkms_sha256` (SHA-256 of the sorted list of `<path> <sha256>` lines for every file under `/lib/modules/<abi>/updates/dkms`, or of the empty string) |
| `cmdline` | Kernel command line embedded in the UKI or written to the BIOS `slot.cfg` |
| `uefi` | Present when `firmware` is `uefi` or `both`. `entry` is the file name stem `vintner-os-<generation>`, without counter or `.efi`; the counter changes on every boot, so the file is found by stem. `sha256` and `size` describe the UKI |
| `bios` | Present when `firmware` is `bios` or `both`. `dir` is `/vintner/bios/os-<generation>`; `files` maps `vmlinuz`, `initrd.img` and `slot.cfg` to their SHA-256 |
| `created` | When the image was staged (RFC 3339, UTC) |
| `first_good`, `last_good` | When the bless service first and last marked it good; `null` until then |

A recovery record has `slot` (`a` or `b`), `version` (its `IMAGE_VERSION`), and the same `uefi`/`bios` file descriptions with stems `vintner-recovery-a` / `vintner-recovery-b` and directories `/vintner/bios/recovery-a` / `-b`.

## Rebuilding

Recovery and `vintner manifest rebuild` reconstruct a manifest from boot storage when both copies are invalid. They do this by reading each vintner UKI's `.osrel` and `.uname` sections, or each BIOS slot's `slot.cfg` and the version string in its `vmlinuz` header. Fields that cannot be recovered from the files are reset to `null`: `module_tree` and the good timestamps. Every rebuilt ordinary image gets state `TRYING`, so nothing is trusted as GOOD until it boots again.
