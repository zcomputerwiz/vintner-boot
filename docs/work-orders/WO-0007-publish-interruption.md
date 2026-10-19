# WO-0007: A power cut at any point of a publish leaves a bootable system

- **For:** an agent with a local Linux environment that has root via `sudo`, KVM, loop devices with partition scanning (`losetup -P`), Go 1.24 and outbound network access.
- **Issued:** 2026-10-17
- **Status:** done. Delivered in #48 together with the vfat rename fixes (`bls.RenameSync`); results in `ci/publish-interrupt/README.md`.
- **Base branch:** `main`. Work on branch `wo-0007` and open one pull request against `main`.

## Why

Phase 4 of the architecture (§9) ends when "100% of scripted publication interruption points preserve bootable fallback/recovery in tested configurations". §6 steps 3 and 4 require the publisher to flush in a documented order and to publish by an atomic rename or a state-record commit, and to "validate behavior under FAT filesystem limitations and abrupt power loss". `schema/state-machine.md` "Reconciliation" lists the rules that repair what an interrupted run left behind.

Nothing has tested any of this yet. WO-0005 showed that a complete publish works, and WO-0006 that a bad candidate falls back. This work order cuts the power at every durable step of a publish, on both firmwares. It checks three things:

- the machine still boots a good image without anyone touching it;
- the reconciliation rules bring the manifest back to a consistent state;
- the next publish works.

Secure Boot, the other half of phase 4, is a later work order.

## Read first

- `docs/architecture.md` §6 (steps 3 to 8) and §9, phase 4.
- `schema/state-machine.md`, "Reconciliation" and "Garbage collection". The expected table in section 4 follows these rules exactly.
- `schema/manifest.md`: the two manifest copies and how `Load` picks one.
- `src/internal/publish/publish.go`: the steps are numbered in comments. Also read `manifest.Save` in `src/internal/manifest/manifest.go`.
- `src/cmd/vintner/bless.go`: since WO-0006, bless runs the reconciliation rules after a successful bless and prints what changed.
- `ci/publish/` and `ci/candidate-fallback/`: how to prepare a disk, run guest operations, identify the booted image and read boot storage afterwards. Reuse their approach and the identification rules of `ci/candidate-fallback/inspector.py`.

## Rules

The rules of WO-0003 to WO-0006 still apply:

- **Disks:** print `lsblk` before every `losetup`, and only touch disks you created.
- **Output:** commit no generated output; everything goes under `ci/out/`.
- **Evidence:** copy it from output, never hardcode it. Missing evidence is a FAIL.
- **Comparisons:** compare whole values, never substrings.
- **Every check must be able to fail.** The README says what output would make each check FAIL. The control case C0 proves that the harness and the test build do not themselves cause anything the checks look for.
- **Expected values:** do not adjust them to fit. Where the system does something else, report it with the evidence. In particular, do not change the publisher, the reconciliation rules or bless to make a case pass.
- **Executable bit:** set it in git.
- **Versions:** take them from the run's `versions.txt`.
- **Pull request body:** pass it as a file.
- **Go:** standard library only, `CGO_ENABLED=0`, SPDX header on new files; `gofmt`, `go vet` and `go test ./...` pass in `src/`.
- **Scope:** you may make the changes section 1 lists, and add `ci/publish-interrupt/` and the CI rows. Change nothing else, in particular not `schema/`, `firmware/`, `recovery/`, `integration/`, `ci/lib/`, `docs/architecture.md` or the ADRs. If the work needs a change elsewhere, stop and report it with the reason.

## Task

**Deliverables:**
- the test points in section 1, with unit tests;
- `ci/publish-interrupt/run.sh <uefi|bios>` and `ci/publish-interrupt/README.md`, with the method, versions and both results tables;
- two CI rows: `interrupt-uefi` and `interrupt-bios`.

### 1. Test points

Add the package `src/internal/testpoint` with one function, `Hit(name string)`.

- **Default build:** `Hit` does nothing, and the binary contains neither the variable name `VINTNER_TEST_INTERRUPT` nor the string `vintner-testpoint`.
- **Build tag `vintner_testpoints`:** `Hit` reads the environment variable `VINTNER_TEST_INTERRUPT`, whose value is `<name>` or `<name>#<n>`. The `#<n>` form means the n-th hit of that name in this process; without it, the first hit. On a match `Hit` does three things:
  1. writes `<2>vintner-testpoint: <name>` to `/dev/kmsg`;
  2. writes `b` to `/proc/sysrq-trigger`, which resets the machine at once, without syncing or unmounting;
  3. if either write fails, panics.

Put the build-tagged code in a file of its own, so that the default build cannot reach it.

Call `Hit` at exactly these places, one line each, with no other change to the surrounding code:

| Name | File | Place |
|---|---|---|
| `staged-recorded` | `publish.go` | step 9, right after the manifest that records `STAGED` is saved |
| `staging-written` | `publish.go` | step 10, after the last staging copy and, on BIOS, the slot directory's sync, and before `syncDir(stagingDir)` |
| `staging-verified` | `publish.go` | step 10, after the readback checks, before step 11 |
| `renamed` | `publish.go` | step 11, after the renames and their directory syncs, before the BIOS `grubenv` write; on UEFI, before the `TRYING` save |
| `grubenv-written` | `publish.go` | step 11, on the BIOS path, after the `grubenv` write, before the `TRYING` save |
| `trying-recorded` | `publish.go` | step 11, right after the `TRYING` save |
| `gc-record-removed` | `publish.go` | step 13, right after the manifest without the collected record is saved |
| `gc-grubenv-written` | `publish.go` | step 13, on the BIOS path, after the garbage-collection `grubenv` write, before the files are removed |
| `manifest-written` | `manifest.go` | in `Save`, after the write, before `f.Sync()` |

**Unit tests:**
- A default-build test that `Hit` returns for every name with `VINTNER_TEST_INTERRUPT` set.
- A tagged test that replaces the two writes with fakes and shows:
  - a match on the bare name;
  - a match on `name#2`, but not on the first hit;
  - no match on a different name, or on a name that merely contains the set value.
- Add `go test -tags vintner_testpoints ./internal/testpoint/` to the `go` job in `.github/workflows/lint.yml`.

### 2. Disks

Prepare two disks once per run. Each case starts from a copy of one of them.

- **Base disk:** as `ci/publish/run.sh` prepares it, plus a `vintner` binary built with `-tags vintner_testpoints` and copied to `/usr/sbin/vintner`. Boot os-1 once so that it is `GOOD`. Before any case, check two things and FAIL the run if either fails:
  - the guest's `/usr/sbin/vintner` has the hash of the tagged binary you built;
  - a default build of the same source contains neither `VINTNER_TEST_INTERRUPT` nor `vintner-testpoint`.
- **Garbage-collection disk:** the base disk after two further rounds of the same steps:
  1. a harmless change under `/etc/initramfs-tools/conf.d/`;
  2. `update-initramfs -u -k "$(uname -r)"`;
  3. one boot, so that the new image is blessed.

  The result holds generations 1, 2 and 3, all `GOOD`, so the next publish (generation 4) collects generation 1.

The base disk's manifest has `firmware: both`, so every publish writes both the UKI and the BIOS slot and updates `grubenv`, whichever firmware the run boots with. Every test point is therefore reached on both firmwares.

Every publish in this work order is caused by a change to a file under `/etc/initramfs-tools/conf.d/`, followed by `update-initramfs -u -k "$(uname -r)"`. The content of the file is different each time, so that step 6 never finds the input unchanged.

### 3. Each case

1. **Operation boot.** Write a new harmless conf.d file and run `VINTNER_TEST_INTERRUPT=<point> update-initramfs -u -k "$(uname -r)"`. The machine resets at the test point, and QEMU exits because of `-no-reboot`.
2. **Boot 1.** Boot with no operation and let it power off.
3. **Recovery publish.** An operation boot with a further, different conf.d file and a plain `update-initramfs -u -k "$(uname -r)"`, with no test point.
4. **Boot 2.** Boot with no operation and let it power off.

Kill any boot that has not ended after 120 seconds and record that it was killed. Identify each boot as `ci/candidate-fallback` does. Read on the host, after every boot:
- the manifest, both copies, with which one `Load` picks;
- the names under `/EFI/Linux/` and `/vintner/bios/`;
- `/vintner/staging/`;
- `grub-editenv list`;
- the hashes of every file a record lists;
- `/etc/apt/apt.conf.d/50vintner-retained-kernels` on the root.

| ID | Disk | Test point | Firmware |
|---|---|---|---|
| C0 | base | none (the variable unset) | both |
| T1 | base | `staged-recorded` | both |
| T2 | base | `staging-written` | both |
| T3 | base | `staging-verified` | both |
| T4 | base | `renamed` | both |
| T5 | base | `grubenv-written` | both |
| T6 | base | `trying-recorded` | both |
| M1 | base | `manifest-written#1` (the `STAGED` save) | both |
| M2 | base | `manifest-written#2` (the `TRYING` save) | both |
| G1 | garbage collection | `gc-record-removed` | both |
| G2 | garbage collection | `gc-grubenv-written` | both |

The `#1` and `#2` of M1 and M2 assume that the publish saves the manifest for the first time at step 9. That is true when step 4 finds nothing to repair. Check it from the operation boot's output, and report it if it is not.

### 4. Expected outcome

`p` is the generation being published: 2 on the base disk, 4 on the garbage-collection disk.

| Case | Boot 1 boots | Record of `p` after boot 1 | Other state after boot 1 |
|---|---|---|---|
| C0 | `p` | `GOOD` | none |
| T1, T2, T3 | the newest image `GOOD` before the case (os-1) | removed (reconciliation rule 2); `next_generation` 3 | `/vintner/staging/` empty |
| T4, UEFI | `p` (its UKI is in place with `+3-0`) | `GOOD` (bless marks the booted record `GOOD`) | `p` is not in `VINTNER_ORDER`, so bless reports its BIOS counter as unset (rule 7) |
| T4, BIOS | os-1 (`p` is not in `VINTNER_ORDER`) | `STAGED`, reported as in place but not verifying (rule 6) | `/vintner/bios/os-2/` present |
| T5, T6 | `p` | `GOOD` | none |
| M1 | os-1 | either removed with `next_generation` 3 (the save reached the disk) or absent with `next_generation` 2 (it did not); report which | `/vintner/staging/` empty |
| M2 | `p` | `GOOD` | report which manifest copy `Load` picked after the operation boot |
| G1 | `p` | `GOOD` | generation 1 has no record; its files remain and bless reports them (rule 3); on BIOS `VINTNER_ORDER` still lists 1 |
| G2 | `p` | `GOOD` | generation 1 has no record, is not in `VINTNER_ORDER` and has no `VINTNER_T_1`; its files remain and bless reports them |

The recovery publish in every case publishes the generation equal to `next_generation` after boot 1. Boot 2 boots that generation, and it is blessed.

### 5. Checks

| ID | Case | Expected |
|---|---|---|
| P1 | all but C0 | The operation boot's serial log has the whole line `[<uptime>] vintner-testpoint: <name>` for the case's test point, and after it a `sysrq: Resetting` line. It has no `vintner publish: published` line for `p`, unless the test point comes after that line. QEMU exited without a `reboot: Power down` line. C0: no `vintner-testpoint` line at all, and the publish printed `published generation 2` |
| P2 | all | Boot 1 boots the image in the table, identified as in WO-0006; never recovery A or B |
| P3 | all | Boot 1's image is blessed: its record is `GOOD` with `last_good` later than before the case, and its counter is re-armed (UEFI `+3-0`, BIOS `xxx` with no `VINTNER_BOOTED`) |
| P4 | all | Every image that was `GOOD` before the case is still `GOOD`, and its files have the hashes in its record from before the case, at every read |
| P5 | all | After boot 1, the record of `p`, `next_generation`, `/vintner/staging/` and, on BIOS, `VINTNER_ORDER` and the `VINTNER_T_<g>` variables are as in the table (M1: one of the two outcomes, consistently) |
| P6 | all | The recovery publish exits 0 and prints `published generation <n>` with `<n>` equal to `next_generation` after boot 1. Boot 2 boots generation `<n>`, and it is `GOOD` and re-armed afterwards |
| P7 | all | After the recovery publish, `50vintner-retained-kernels` lists exactly the kernel packages of the images the manifest then holds |
| P8 | all | Every reconciliation report and state change that bless printed on boots 1 and 2, and that the recovery publish printed. Also any file under `/EFI/Linux/`, `/vintner/bios/` or `/vintner/staging/`, and any generation in `VINTNER_ORDER`, without a record after boot 2. OBSERVED |
| P9 | all | No boot ran recovery A or B |

`results.tsv` has one row per check and case: ID, case, status, evidence. P1 to P7 and P9 are PASS or FAIL; P8 is OBSERVED. The script exits 0 when every PASS/FAIL row is present and passes.

### 6. CI

Add `interrupt-uefi` and `interrupt-bios` to `.github/workflows/qemu-tests.yml`, with the same setup as the `candidate-*` rows. Both must pass in the pull request's CI, and each should finish within 60 minutes.

## Reporting back

- **The pull request:**
  - both results tables, with evidence copied from the CI run;
  - for each case, the test point's serial line and how the operation boot ended.
- **Findings:** report the following, with evidence, and do not adjust the expected values to fit:
  - anything that does not match sections 4 and 5, in particular any case where boot 1 does not boot an image that was `GOOD` or completely published, or where the recovery publish fails;
  - any state the rules leave behind after boot 2 (P8), such as a `STAGED` record that stays or files without a record. Say for each one whether a later publish or garbage collection removes it;
  - which manifest copy `Load` picked in M1 and M2.
- **Fidelity of the power cut:** `sysrq b` drops what the guest kernel has not yet written. It does not model a drive that loses its own write cache or tears a sector (architecture §12, risk 3). Say so in the README and do not try to model it.
- **A step that cannot be done as written:** say exactly why. Do not work around it outside the scope above.
