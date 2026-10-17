# WO-0005: The publisher: kernel and initramfs updates become new boot images

- **For:** an agent with a local Linux environment that has root via `sudo`, KVM, loop devices with partition scanning (`losetup -P`), Go 1.24 and outbound network access.
- **Issued:** 2026-10-16
- **Status:** done: `vintner publish`, the hooks and `ci/publish/`, reviewed and run in CI (`publish-uefi`, `publish-bios`). Results and findings in `ci/publish/README.md`.
- **Base branch:** `main`. Work on branch `wo-0005` and open one pull request against `main`.

## Why

Phase 2 of the architecture (§9) ends when "kernel upgrade and initramfs-only regeneration create a bootable candidate, preserving known-good".

Today nothing publishes. The only ordinary image on a vintner disk is os-1, which `ci/lib/vintner-base.sh` writes when it builds the disk. The kernel and initramfs hook directories under `integration/` hold only READMEs. That is why WO-0003 saw installing a kernel change nothing vintner owns.

This work order adds `vintner publish`, the Debian hooks that call it, and a CI test that proves the phase 2 gate on both firmwares.

## Read first

- `docs/architecture.md`:
  - §4 "Boot architecture" and §4.1 (UEFI selection, BIOS slots);
  - §5 (boot storage, image budget);
  - §6 and §6.1: the publishing protocol and module-tree coherence. This work order implements §6 steps 1 to 5 and 8. Steps 6 and 7 already exist: the boot loaders count tries, `vintner bless` re-arms.
- `schema/state-machine.md`: states, transitions, reconciliation, garbage collection. **The transitions table and the garbage-collection rules are the specification.**
- `schema/manifest.md` and `schema/manifest.schema.json`: the record you write, and how the two manifest copies are written.
- `docs/adr/0002-implementation-language.md`: Go, standard library only, static binary, hooks as POSIX `sh` shims with no logic.
- `docs/adr/0003-uefi-entry-naming-and-boot-counting.md`: file names, `.osrel` fields and counters.
- `firmware/bios/INTERFACE.md`: BIOS slot layout, the `grubenv` variables, the 512-byte rule, and how Linux writes `grubenv`.
- The code you build on:
  - `src/internal/manifest`: `Load`, `Save`, `Validate`;
  - `src/internal/state`: `Scan`, `Reconcile`, `StagingNames`, `Keep`;
  - `src/internal/bls`: `Find`, `Parse`;
  - `src/internal/grubenv`: `Read`, `Set`, `Unset`, `Write`, which already refuse to cross the first sector;
  - `src/internal/uki`: `Read`;
  - `src/internal/bootstorage`: `Device`, `Mount`;
  - `src/cmd/vintner/bless.go`, for how a command finds and mounts boot storage.
- `ci/lib/vintner-base.sh`, `ci/lib/vintner-qemu.sh` and `ci/lifecycle/packages/README.md`: the base disk and its report service. Use them; do not copy their code.

## Rules

The WO-0003 and WO-0004 rules still apply:

- **Disks:** print `lsblk` before every `losetup`, and only touch disks you created.
- **Output:** commit no generated output; everything goes under `ci/out/`.
- **Evidence:** copy it from output, never hardcode it. Missing evidence is a FAIL.
- **Comparisons:** compare whole values, never substrings.
- **Every check must be able to fail.** The README says what output would make each check FAIL. The control case C0 proves the hooks publish nothing when nothing changed.
- **Expected values:** do not adjust them to fit. If the system does something else, report it with the evidence.
- **Executable bit:** set it in git.
- **Versions:** take them from the run's `versions.txt`.
- **Pull request body:** pass it as a file.

And for the Go code:

- **Libraries:** standard library only. Build with `CGO_ENABLED=0`. Every new file starts with `// SPDX-License-Identifier: GPL-2.0-or-later`.
- **Checks:** `gofmt`, `go vet` and `go test ./...` pass in `src/`.
- **Scope:** you may add packages under `src/internal/` and change `src/cmd/vintner/`. Existing internal packages may be extended, but their tests must still pass unchanged. Do not change `schema/`, `firmware/`, `recovery/`, `ci/lib/`, `docs/architecture.md` or the ADRs. If the work needs a change there, stop and report it with the reason.

## Task

**Deliverables:**
- `vintner publish` in `src/cmd/vintner/`, with its logic in a new package `src/internal/publish` and unit tests;
- `integration/kernel-hooks/zz-vintner` and `integration/initramfs-hooks/zz-vintner`, with their READMEs updated;
- `ci/publish/run.sh <uefi|bios>` and `ci/publish/README.md`, with the method, versions and both results tables;
- two CI rows: `publish-uefi` and `publish-bios`.

### 1. `vintner publish --kernel <abi>`

Publishes `/boot/vmlinuz-<abi>` with `/boot/initrd.img-<abi>` as a new ordinary image. Do the steps below in this order. Where they say "refuse", print the reason to stderr and exit non-zero without changing boot storage.

1. **Not a vintner system, or nothing to publish yet.** Exit 0 with a one-line message, and change nothing, if:
   - `/proc/cmdline` has no `vintner.bootstorage=`; or
   - `/boot/initrd.img-<abi>` does not exist yet. The initramfs hook publishes once it does: Debian runs kernel hooks around initramfs creation in an order you must not rely on.

   Refuse if `/boot/vmlinuz-<abi>` does not exist.
2. **Lock.** Take an exclusive `flock` on `/run/vintner/publish.lock`, creating the directory, and wait for it. Hold it to the end. Overlapping hook runs then serialise. `vintner bless` and `vintner reconcile` also write the manifest, so make them take the same lock.
3. **Boot storage and manifest:**
   - mount boot storage read-write as `bless` does, if it is not mounted;
   - load the manifest, and refuse if no copy is valid;
   - refuse if `boot_storage.partuuid` is not the partition `vintner.bootstorage=` names;
   - refuse if `secure_boot_tier` is not `none`. Signing is phase 4.
4. **Reconcile.** Run `state.Scan` and `state.Reconcile`, apply its actions, and print its reports. Then continue.
5. **Inputs:**
   - **Command line:** take it from the newest `GOOD` image, or the newest image if none is `GOOD`. Refuse if it does not contain:
     - the word `root=UUID=<root.uuid>` or the word `root=PARTUUID=<root.partuuid>`;
     - exactly one `vintner.bootstorage=PARTUUID=<boot_storage.partuuid>`.
   - **Package and version:** the Debian package that owns `/boot/vmlinuz-<abi>` (`dpkg-query -S`) and its version. Refuse if none owns it.
   - **Module-tree identity:** as `schema/manifest.md` defines it.
6. **Unchanged input.** Take the newest image with the same ABI. If its state is not `FAILED`, and all of the following are identical, print "unchanged" and go to step 12:
   - kernel SHA-256 (`module_tree.kernel_sha256`);
   - command line;
   - initramfs. Compare it with `bios.files."initrd.img"` when there is a BIOS record. Otherwise compare it with the `.initrd` section of the published UKI, read with `debug/pe`.

   This is how one apt run that triggers several hooks for the same kernel publishes once.
7. **Check that `grubenv` has room.** Let `<g>` be `next_generation`. On BIOS (`firmware` `bios` or `both`), the new `grubenv`, with `VINTNER_T_<g>` and `<g>` added to the front of `VINTNER_ORDER`, must end within the first sector. Refuse otherwise.
8. **Build** the image files on the root filesystem, under a fresh directory in `/var/tmp/`. Not `/run`: it is a small tmpfs, and a UKI is tens of megabytes. Remove the directory when the command ends.
   - **UEFI** (`firmware` `uefi` or `both`): run `ukify build` with:
     - the kernel and initramfs;
     - `--stub=/usr/lib/systemd/boot/efi/linuxx64.efi.stub`;
     - the command line and `--uname=<abi>`;
     - an `--os-release` file. It is `/etc/os-release` with `IMAGE_ID=vintner-os`, `IMAGE_VERSION=<g>`, and `PRETTY_NAME` set to the original plus `, <abi>`, as ADR-0003 says.

     Refuse if `ukify` or the stub is missing.
   - **BIOS:** copy the kernel and initramfs, and write `slot.cfg` as `INTERFACE.md` shows.

   Hash every file. Refuse if boot storage lacks room for them.
9. **Record `STAGED`.** Add the image record to the manifest with state `STAGED`, all hashes and sizes, `created`, and `next_generation` set to `<g>+1`, in one `Save`. From here on `<g>` is never reused.
10. **Stage and verify:**
    - copy the files to their staging names on boot storage (`state.StagingNames`);
    - `fsync` each file and the staging directory;
    - read each file back from boot storage and compare its SHA-256 with the record;
    - for the UKI, check with `uki.Read` that `IMAGE_ID`, `IMAGE_VERSION`, `.uname` and `.cmdline` are the expected values.

    On any mismatch, remove the staging files, remove the record with a `Save`, and refuse.
11. **Publish:**
    - **UEFI:** rename the UKI to `/EFI/Linux/vintner-os-<g>+<tries>-0.efi`, then `fsync` `/EFI/Linux` and the staging directory.
    - **BIOS:** rename the staging directory to `/vintner/bios/os-<g>`, then `fsync` both parents. Then write `grubenv` once, as `INTERFACE.md` "Writing grubenv from Linux" says, with `VINTNER_T_<g>` set to `tries` `x` characters and `<g>` at the front of `VINTNER_ORDER`.
    - **Manifest:** set the record to `TRYING`.
12. **Stale flags.** For every ordinary image with a `module_tree`, set `stale` to whether the installed tree still matches it. A missing kernel, `modules.dep` or module directory counts as not matching. An image whose `module_tree` is `null` keeps its flag. Save the manifest if any flag changed.
13. **Garbage collection.** Run `state.Keep`. For each image to remove, in this order:
    - remove its record (`Save`);
    - on BIOS, remove its generation from `VINTNER_ORDER` and unset its counter, in one `grubenv` write;
    - remove its BIOS directory and its UKI (found by stem with `bls.Find`).

    Report an exceeded budget.
14. **Protect the kernel packages of retained images.** Rewrite `/etc/apt/apt.conf.d/50vintner-retained-kernels` so that its `APT::NeverAutoRemove` list holds exactly the kernel packages of the images left, each as an anchored, escaped regular expression. Write a temporary file in the same directory, `fsync` it, rename it into place, and `fsync` the directory.
15. Unmount boot storage if this command mounted it, and release the lock.

Print one line per step that changed something, so the hook output in the apt log says what happened.

The publisher never removes or renames a recovery image, never writes the ESP and never writes EFI variables.

**Unit tests** in `src/internal/publish`:
- Put the UKI builder, the package lookup and the clock behind small interfaces, so that tests run without `ukify`, `dpkg` or root, on boot storage and root directories under `t.TempDir()`.
- Cover at least:
  - the full publish on `uefi`, `bios` and `both`, comparing the resulting files, `grubenv` and manifest whole;
  - each refusal in steps 1, 3, 5, 7 and 10, each leaving boot storage byte-identical;
  - the "unchanged" case;
  - a crash after step 9 and after step 10, simulated by stopping there, followed by `vintner reconcile`, which must leave a consistent state;
  - stale flags;
  - garbage collection with the `NeverAutoRemove` file.

### 2. The hooks

POSIX `sh`, no logic beyond passing arguments (ADR-0002):

- `integration/kernel-hooks/zz-vintner`, for `/etc/kernel/postinst.d/`: `exec vintner publish --kernel "$1"`. Debian passes the ABI as `$1`.
- `integration/initramfs-hooks/zz-vintner`, for `/etc/initramfs/post-update.d/`: the same. `update-initramfs` passes the ABI as `$1`.

There is no `postrm.d` hook in this work order. Published images are copies on boot storage and survive the removal of their kernel package. Step 12 flags an image whose module tree is gone, and garbage collection then keeps a fallback with a different ABI. Say in the hooks' README why there is no `postrm.d` hook.

Do not add the hooks to the base disk in `ci/lib/`. The lifecycle test (WO-0003) expects package operations to change nothing vintner owns. Moving it to the publisher is a later change.

### 3. The test: `ci/publish/run.sh <uefi|bios>`

**Setup:**
1. Build the base with `ci/lib/vintner-base.sh`. Its manifest has `firmware` `both`, so the publisher writes both a UKI and a BIOS slot on either firmware.
2. Prepare one disk from it with what the publisher needs:
   - first, `systemd-ukify` and `systemd-boot-efi` installed in the guest with `apt-get` (not `systemd-boot`, which is a conflicting package, architecture §5);
   - then the two hooks copied to their directories on the guest root.
3. Boot it once so that os-1 is blessed `GOOD`. Each case below starts from a copy of this disk.

Boot under the firmware given to `run.sh` as the lifecycle test does: guest operations through the seed, the report service powering off, and an inspection of the disk on the host after every boot.

Before the cases, check that the prepared disk has exactly one image, os-1, `GOOD`, and nothing under `/vintner/staging/`.

**Read on the host after every boot:**
- the manifest (both copies; use the host-built `vintner manifest show`);
- the names in `/EFI/Linux/` on boot storage;
- `grub-editenv list`;
- the files under `/vintner/bios/`;
- the guest's `/etc/apt/apt.conf.d/50vintner-retained-kernels`.

**Which image booted:**
- UEFI: the report's `LOADER_ENTRY_SELECTED`.
- BIOS: GRUB's last `vintner: trying <gen>` line on the serial console.

**Cases:**

| ID | Guest operation | Then |
|---|---|---|
| C0 | `true` (control) | reboot once |
| K1 | `apt-get -y install linux-image-amd64`, a second kernel with a new ABI | reboot once |
| K2 | add a line to `/etc/initramfs-tools/conf.d/vintner-test`, then `update-initramfs -u -k "$(uname -r)"` | reboot once |
| K3 | K2 twice in one operation, then `vintner publish --kernel "$(uname -r)"` | reboot once |
| U1 | wait for `vintner-bless.service` to finish, list every file on boot storage with its SHA-256, run `vintner publish --kernel "$(uname -r)"` with nothing changed, list again | none |
| G1 | K2 then reboot, four times in a row | — |
| R1 | K1, then `apt-get -y purge` the new kernel's package (generation 2's `kernel.package`), then K2, all in one operation, still running os-1's kernel | reboot once |

**Checks:**

| ID | Case | Expected |
|---|---|---|
| E1 | C0 | No image added: `next_generation` and the generations in `images` unchanged; no file added under `/EFI/Linux/`, `/vintner/bios/` or `/vintner/staging/`; `VINTNER_ORDER` unchanged; os-1 booted |
| E2 | K1, K2 | Exactly one new image, generation 2:<br>- state `TRYING`; `kernel.abi`, `kernel.package` and `module_tree` matching the guest's files;<br>- UEFI file `vintner-os-2+3-0.efi`;<br>- BIOS directory `os-2` whose three files match the manifest hashes;<br>- `VINTNER_ORDER=2 1` and `VINTNER_T_2=xxx`;<br>- `/vintner/staging/` empty;<br>- `NeverAutoRemove` listing exactly the packages of images 1 and 2 |
| E3 | K1, K2 | The next boot ran generation 2, and after it the manifest has image 2 `GOOD` with `first_good` set |
| E4 | K1, K2 | os-1 preserved: its UKI and BIOS files have the hashes its record had before the operation; its record is unchanged apart from `last_good`, which the operation's own boot updates; `state` is still `GOOD` and `stale` still `false`; it is still in `VINTNER_ORDER` with `VINTNER_T_1=xxx` and in `/EFI/Linux/` as `vintner-os-1+3-0.efi` |
| E5 | K3 | Exactly two new generations, 2 and 3, one per changed initramfs. The extra `vintner publish` printed "unchanged" and added none |
| E6 | U1 | Exit 0, "unchanged" printed, and the two listings identical |
| E7 | G1 | After each round, the images kept are exactly those `schema/state-machine.md` "Garbage collection" keeps with `retain` 3. Every removed image's record, UKI, BIOS directory, `VINTNER_ORDER` entry and counter are gone. The `NeverAutoRemove` file lists exactly the kept images' packages |
| E8 | R1 | Generation 2 is still on boot storage and in the manifest after the operation, and `stale: true`. Generation 3 (K2's) is `TRYING` and os-1 is `GOOD`. The reboot ran generation 3 |
| E9 | all | Every boot ran an ordinary image; none fell back to recovery. Report how each boot ended |

`results.tsv` has one row per check and case: ID, case, status, evidence. Each row is PASS or FAIL against the expected column. The script exits 0 when every row passes.

### 4. CI

Add `publish-uefi` and `publish-bios` to `.github/workflows/qemu-tests.yml`, with the same setup as the lifecycle rows. Both must pass in the pull request's CI, and each should finish within 60 minutes. `go test ./...` runs in the existing `go` job.

## Reporting back

- **The pull request:** both results tables, with evidence copied from the CI run, and the unit test coverage of `src/internal/publish`.
- **Findings:** anything that does not match the expected tables, with evidence. Report in particular:
  - the order in which Debian ran the kernel and initramfs hooks during K1, from the apt log;
  - how many times `vintner publish` ran, and how many of those runs printed "unchanged".

  Do not adjust the expected values to fit; say what happened.
- **A step that cannot be done as written:** say exactly why. Do not work around it in `schema/`, `firmware/` or `ci/lib/`.
