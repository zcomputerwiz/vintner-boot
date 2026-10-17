# WO-0006: A bad candidate falls back to the known-good image, on both firmwares

- **For:** an agent with a local Linux environment that has root via `sudo`, KVM, loop devices with partition scanning (`losetup -P`), Go 1.24 and outbound network access.
- **Issued:** 2026-10-17
- **Status:** waiting on WO-0005 (the publisher and `ci/publish/`) being on `main`. Start when it is.
- **Base branch:** `main`. Work on branch `wo-0006` and open one pull request against `main`.

## Why

Phase 3 of the architecture (§9) ends when we can "inject failed kernel, incomplete boot, forced power cycle and hung boot; correct fallback after bounded attempts".

WO-0004 showed the last line of defence: when every ordinary image is broken, recovery A boots. Phase 3 is the common case. The publisher (WO-0005) adds a new image as a `TRYING` candidate while the previous image stays `GOOD`. When the candidate is bad, the machine must try it exactly `tries` times, then boot the previous `GOOD` image without anyone touching it, and never fall through to recovery while a good ordinary image exists.

One piece is missing in the code: nothing records a candidate as `FAILED`. `schema/state-machine.md` says the counter reaching zero sets the record to `FAILED`, and `state.Reconcile` implements that rule, but no boot-time service runs it. This work order adds that to `vintner bless`.

## Read first

- `docs/architecture.md`:
  - §4 "Health and blessing";
  - §6 steps 5 to 7;
  - §9, phase 3.
- `schema/state-machine.md`: the `TRYING`/`GOOD` → `FAILED` transition and "Reconciliation".
- `docs/adr/0003-uefi-entry-naming-and-boot-counting.md`: why a bad entry sorts after every non-bad one, including after older ordinary images.
- `firmware/bios/INTERFACE.md`: GRUB skips a generation whose counter is `0` and tries the next one in `VINTNER_ORDER`.
- `src/cmd/vintner/bless.go` and `src/internal/state` (`Scan`, `Reconcile`).
- `ci/publish/run.sh`, `ci/publish/inspector.py` and `ci/publish/README.md`: how to prepare a disk with the publisher and hooks installed, run guest operations, and read the disk afterwards. Reuse their approach; put shared helpers you need in `ci/publish/` only if both tests use them, otherwise keep them in the new directory.
- `ci/recovery-fallback/README.md`: how WO-0004 records each boot's image, its counters and how it ended.

## Rules

The rules of WO-0003 to WO-0005 still apply:

- **Disks:** print `lsblk` before every `losetup`, and only touch disks you created.
- **Output:** commit no generated output; everything goes under `ci/out/`.
- **Evidence:** copy it from output, never hardcode it. Missing evidence is a FAIL.
- **Comparisons:** compare whole values, never substrings.
- **Every check must be able to fail.** The README says what output would make each check FAIL. The control case G0 proves the harness itself does not cause a fallback.
- **Expected values:** do not adjust them to fit. If the system does something else, report it with the evidence.
- **Executable bit:** set it in git.
- **Versions:** take them from the run's `versions.txt`.
- **Pull request body:** pass it as a file.
- **Go:** standard library only, `CGO_ENABLED=0`, SPDX header on new files; `gofmt`, `go vet` and `go test ./...` pass in `src/`.
- **Scope:** you may change `src/cmd/vintner/bless.go` and its tests as section 1 says, and add `ci/candidate-fallback/` and the CI rows. Do not change `src/internal/`, `schema/`, `firmware/`, `recovery/`, `integration/`, `ci/lib/`, `docs/architecture.md` or the ADRs. If the work needs a change there, stop and report it with the reason.

## Task

**Deliverables:**
- the change to `vintner bless` in section 1, with unit tests;
- `ci/candidate-fallback/run.sh <uefi|bios>` and `ci/candidate-fallback/README.md`, with the method, versions and both results tables;
- two CI rows: `candidate-uefi` and `candidate-bios`.

### 1. `vintner bless` records failed images

After `vintner bless` has re-armed the booted image and saved the manifest, and while it still holds the publish lock, it runs `state.Scan` and `state.Reconcile` on boot storage, applies the actions, saves the manifest if anything changed, and prints each report and each record it changed (for example `generation 2: TRYING -> FAILED`).

It does this only after a successful bless, so it runs once per good boot. A failure in this step is printed and makes `vintner bless` exit non-zero, but never undoes the re-arm that came before it.

**Unit tests:** a manifest with a `GOOD` booted image and a `TRYING` candidate whose counter is at zero (UEFI `+0-3`; BIOS `VINTNER_T_<g>=0`) ends with the booted image `GOOD` and re-armed and the candidate `FAILED`; a candidate with tries left stays `TRYING`; boot storage is otherwise unchanged.

### 2. The disks

Prepare one disk as `ci/publish/run.sh` does: the base disk from `ci/lib/vintner-base.sh`, `systemd-ukify` and `systemd-boot-efi` installed, the hooks copied, and os-1 booted once so that it is `GOOD`. Each case starts from a copy of this disk.

Each broken case publishes its candidate, generation 2, through the real publisher: the guest operation makes a change under `/etc/initramfs-tools/`, then runs `update-initramfs -u -k "$(uname -r)"`, and the hook publishes. Then, in the same operation, it removes the change again **without** rebuilding, so that only the candidate carries it. os-1's own copies on boot storage are untouched throughout; that is part of what the test proves.

| ID | Change put into the candidate's initramfs | What each boot of generation 2 should do |
|---|---|---|
| G0 | a harmless file in `/etc/initramfs-tools/conf.d/` (control) | boot normally and be blessed |
| B1 | an `init-top` script that crashes the kernel: `echo c > /proc/sysrq-trigger` | kernel panic before the root is mounted |
| B2 | an `init-bottom` script that creates `/run/vintner-test-break`, plus, on the root, `vintner-test-health.service` (`Type=oneshot`, `ConditionPathExists=/run/vintner-test-break`, `ExecStart=/bin/false`, `Before=boot-complete.target`, `[Install] RequiredBy=boot-complete.target`, no `Description=`) | reach multi-user, but `boot-complete.target` fails, so bless does not run. On os-1 the condition is false, the unit is skipped and the boot is blessed |
| B3 | an `init-top` script that never returns: `sleep infinity` | hang until the 120-second kill |
| B4 | none: a G0-style candidate, but each of its boots is killed by the host 1 second after the kernel's `Command line:` line appears on the serial console | a forced power cycle on every try |

B2's unit stays on the root for the whole case. A healthy boot powers off within about 10 seconds, so B4 waits for the kernel line rather than a fixed time: the boot loader has spent the try by then, and the boot has not been blessed. Report the last serial line before each kill.

### 3. Boots

Boot each case up to 6 times after the operation boot, under the firmware given to `run.sh`, with the serial console to a file and `-no-reboot`. A boot ends when the guest powers off or when it is killed: at 120 seconds, or as described above for B4's tries. Record how each boot ended.

**Which image booted:** UEFI, the report's `LOADER_ENTRY_SELECTED` where the report ran, otherwise the kernel's `Command line:` and the UKI that systemd-boot renamed (the counter that changed on boot storage); BIOS, GRUB's last `vintner: trying <gen>` line. Say in the README how each boot was identified.

**Read on the host after every boot:** the manifest, the names in `/EFI/Linux/`, `grub-editenv list`, and the files under `/vintner/bios/`.

### 4. Checks

| ID | Case | Expected |
|---|---|---|
| H1 | G0 | Boot 1 is generation 2, blessed: generation 2 `GOOD`, UEFI `vintner-os-2+3-0.efi`, BIOS `VINTNER_T_2=xxx`; every later boot is generation 2 as well; recovery never boots |
| H2 | B1 to B4 | Boots 1 to 3 are generation 2. Its counter after each: UEFI `+2-1`, `+1-2`, `+0-3`; BIOS `xx`, `x`, `0` |
| H3 | B1 to B4 | Boot 4 is generation 1 (os-1), not recovery A or B, and is blessed: os-1 `GOOD` with `last_good` later than before the case, UEFI `vintner-os-1+3-0.efi`, BIOS `VINTNER_T_1=xxx` and no `VINTNER_BOOTED` |
| H4 | B1 to B4 | After boot 4: generation 2 is `FAILED` in the manifest, its UEFI file is still `vintner-os-2+0-3.efi` and its BIOS counter still `0`; `vintner bless` printed the transition on the serial console |
| H5 | B1 to B4 | Boots 5 and 6 are os-1 again; generation 2 is never tried again (its counter and name do not change) |
| H6 | B1 to B4 | os-1's UKI and BIOS files have the hashes its record had before the case, at every read |
| H7 | B1 to B4 | How boots 1 to 3 ended, and each case's serial evidence for why: B1 the panic line, B2 the failed `vintner-test-health.service` and `boot-complete.target` lines, B3 the last line before the kill, B4 the last line before each kill. OBSERVED |
| H8 | all | No boot ran recovery A or B |

`results.tsv` has one row per check and case: ID, case, status, evidence. H1 to H6 and H8 are PASS or FAIL; H7 is OBSERVED. The script exits 0 when every PASS/FAIL row is present and passes.

### 5. CI

Add `candidate-uefi` and `candidate-bios` to `.github/workflows/qemu-tests.yml`, with the same setup as the `publish-*` rows. Both must pass in the pull request's CI, and each should finish within 60 minutes.

## Reporting back

- **The pull request:** both results tables, with evidence copied from the CI run, and the unit test coverage of `src/cmd/vintner`.
- **Findings:** anything that does not match the expected tables, with evidence. Report in particular:
  - any case where the fallback happened earlier or later than boot 4, or landed on recovery while os-1 was good;
  - B1: whether the kernel rebooted, halted or hung after the panic, and how that ended the boot;

  Do not adjust the expected values to fit; say what happened.
- **A step that cannot be done as written:** say exactly why. Do not work around it outside the scope above.
