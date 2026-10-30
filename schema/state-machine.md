# Image state machine

This document defines the states of an ordinary boot image and the transitions between them. They are the same on both firmware backends (architecture §6, ADR-0003). The boot loader's own records (counters in UEFI file names, `VINTNER_T_<gen>` in `grubenv`) decide what boots. The manifest state ([manifest.md](manifest.md)) records what the userspace tools know.

## States

| State | Meaning | Boot loader view |
|---|---|---|
| `STAGED` | The image is being written: the manifest records it, but its file is not yet in a place the boot loader looks | Invisible |
| `TRYING` | Published with a fresh counter; it has never completed a good boot since publication or since it was last re-armed by an operator | Counted, not bad |
| `GOOD` | Completed at least one good boot. The bless service re-arms its counter after every good boot | Counted, not bad, unless it then failed `tries` times in a row |
| `FAILED` | Its counter reached zero. The health service or publisher records this when it sees the bad counter | Bad: sorts after every non-bad entry (UEFI) or is skipped (BIOS) |

`stale` (architecture §6.1) is a flag on top of these states, not a state of its own. `RECOVERY` is a system condition, "the booted image is a recovery image", not an image state. The health service reports it.

## Transitions

| From | To | Trigger | Writes |
|---|---|---|---|
| (none) | `STAGED` | Publisher starts publishing generation `g` | Manifest: new record; then the image file under `/vintner/staging/` on boot storage |
| `STAGED` | `TRYING` | Image written, verified (hash, size, signature where the tier requires one) and flushed | Rename into place: `/EFI/Linux/vintner-os-<g>+<tries>-0.efi`, or `/vintner/bios/os-<g>/` with `VINTNER_T_<g>` set and `g` prepended to `VINTNER_ORDER`. `fsync` the directory, then the manifest |
| `TRYING`, `GOOD` | `GOOD` | `boot-complete.target` reached on a boot of this image | Re-arm the counter to `tries` (rename to `+<tries>-0`, or set `VINTNER_T_<g>`). Then the manifest: `state`, `first_good` if unset, and `last_good` |
| `TRYING`, `GOOD` | `FAILED` | Counter seen at zero | Manifest only |
| `FAILED` | `TRYING` | Operator runs `vintner image rearm <g>` | Counter set to `tries`; manifest |
| any | (removed) | Garbage collection (below) | BIOS: `grubenv` first (one write unsets the counter and drops the generation from `VINTNER_ORDER`). Then the files, made durable. The record last |

The boot loader decrements the counter itself on every boot of a counted image. Userspace never decrements.

## Reconciliation

The publisher and the health service compare the manifest with boot storage before they do anything else, and repair what an interrupted run left behind:

- **A `STAGED` record whose file is in its final place and verifies** becomes `TRYING`.
- **A `STAGED` BIOS record whose slot files are in place and match the record, with the generation neither in `VINTNER_ORDER` nor holding a counter** was interrupted between the rename and the `grubenv` write. Reconciliation finishes the transition: one `grubenv` write sets `VINTNER_T_<g>` and prepends `g` to `VINTNER_ORDER`; then the record becomes `TRYING`. With firmware `both`, the UKI must also be in place and match.
- **A `STAGED` record whose file is not in its final place** has its staging file and record removed.
- **A file in `/EFI/Linux/` or `/vintner/bios/` that matches `vintner-os-*` but has no record** is reported and left alone (threat model T5).
- **A record whose file is missing** is reported. It is removed from the manifest only by garbage collection or an operator command.
- **A counter at zero on a `TRYING` or `GOOD` record** sets the record to `FAILED`.
- **A `STAGED` record with files in its final place that do not verify** is reported and left alone. For BIOS, verifying includes the generation being in `VINTNER_ORDER` with its counter set.
- **On BIOS, only a counter of exactly `0` is at zero.** An unset or malformed counter, or an unreadable `grubenv`, is reported and changes nothing.

Staging names: generation `g` stages its UKI as `/vintner/staging/vintner-os-<g>.efi` and its BIOS slot as the directory `/vintner/staging/os-<g>/`. `vintner reconcile` implements these rules (`src/internal/state`).

## Garbage collection

Garbage collection runs after a successful publish and never touches recovery images. It removes one image at a time in this order: BIOS `grubenv`, then the image's files (made durable), then its manifest record. A power cut leaves the record in place until the files are gone, so the next publish selects the same image again and finishes the job; no file is left without a record. It keeps:

1. the newest `GOOD` image that is not `stale`;
2. the newest `GOOD` image whose ABI differs from that of image 1, so that a module tree changed in place under one ABI cannot remove every fallback (architecture §6.1);
3. every `TRYING` image;
4. further images, newest first, until `retain` images remain.

`FAILED` images go first, then the oldest. Before removing an image it drops that image's kernel package from the `APT::NeverAutoRemove` list (architecture §6 step 8). If the rules above would keep more than `retain` images, it keeps them all and reports the image budget as exceeded.
