# WO-0004: A broken Debian root falls back to recovery, on both firmwares

- **For:** an agent with a local Linux environment that has root via `sudo`, KVM, loop devices with partition scanning (`losetup -P`), `debugfs` (e2fsprogs) and outbound network access.
- **Issued:** 2026-10-16
- **Status:** ready. Both prerequisites are on `main`: recovery finds the root from the manifest when the command line names none, and `ci/lib/vintner-base.sh` builds the base disk.
- **Base branch:** `main`. Work on branch `wo-0004` and open one pull request against `main`.

## Why

Phase 1 ends when "a fresh installation and a deliberately corrupted Debian root can still select recovery" (architecture §9). What is tested so far:

- **BIOS:** `ci/integration/bios-bless` (C7, C8) covers one failure: os-1 pointing at a root that does not exist.
- **UEFI:** only `ci/selection-policy` covers falling back to recovery, and only with a toy kernel.

Nothing yet breaks a real Debian root in the ways a real machine breaks. That is:

- an unreadable file system;
- a missing init;
- a boot that stops in emergency mode;
- a health check that fails.

Nothing yet shows, on both firmwares, that these lead to recovery A after exactly the configured number of tries.

## Read first

- `docs/architecture.md`:
  - §4 "Health and blessing": health checks are units ordered `Before=boot-complete.target` and pulled in by it; a boot that never reaches `boot-complete.target` is not blessed.
  - §9, phase 1.
- `docs/adr/0003-uefi-entry-naming-and-boot-counting.md`: counters, the last try, and why a bad entry sorts after recovery A.
- `firmware/bios/INTERFACE.md`: the grubenv counters (`xxx`, `xx`, `x`, `0`).
- `ci/lib/vintner-base.sh` and `ci/lib/vintner-qemu.sh`: build the dual-firmware base disk, with the stock loaders already removed (P0), and run QEMU. `vintner-base.sh <uefi|bios> <out-dir>` writes `<out-dir>/base.env` with the paths, partition offsets and UUIDs to source; `ci/lifecycle/packages/run.sh` shows the use. Use them; do not copy their code.
- `ci/lifecycle/packages/README.md`: what the base disk contains, and the report service that powers the guest off.
- `recovery/BUILDING.md`: `vintner.recovery_test=1` prints `VINTNER-RECOVERY-TEST root=<absent|mounted|failed> ro=<yes|no> logs=<path>` on `ttyS0` and powers off.
- `ci/integration/bios-bless/run.sh`: the root partition hash check (C9) to repeat.

## Rules

The WO-0003 rules still apply:

- **Disks:** print `lsblk` before every `losetup`, and only touch disks you created.
- **Output:** commit no generated output; everything goes under `ci/out/`.
- **Scope:** do not change `src/`, `firmware/`, `schema/`, `recovery/`, `integration/`, `docs/architecture.md`, the ADRs, or `ci/lib/`. If a check finds a bug there, report it with the evidence. Do not work around it.
- **Evidence:** copy it from output, never hardcode it. Missing evidence is a FAIL.
- **Comparisons:** compare whole values, never substrings.
- **Every check must be able to fail.** The README says what output would make each check FAIL, and the control case G0 below proves the harness itself does not cause a fallback.
- **Executable bit:** set it in git.
- **Versions:** take them from the run's `versions.txt`.
- **Pull request body:** pass it as a file.

## Task

**Deliverables:**
- `ci/recovery-fallback/run.sh <uefi|bios>`;
- `ci/recovery-fallback/README.md`, with the method, versions and both results tables;
- two CI rows: `fallback-uefi` and `fallback-bios`.

### 1. The disks

Build the base once per run with `ci/lib/vintner-base.sh`. Then, for each case, make a sparse copy of `base.raw` (`cp --sparse=always`), change it on the host as the case says, and boot it. Delete the copy when the case is done.

**Recovery UKIs, UEFI only.** The base disk carries the production recovery UKIs, which have no `vintner.recovery_test=1`, so they would not report and power off. In each case's copy, replace them on boot storage with UKIs built by trixie `ukify` from `recovery/out/vmlinuz` and `recovery/out/initrd.img`:

| UKI | Command line |
|---|---|
| A | `console=tty0 console=ttyS0,115200 vintner.recovery=1 vintner.recovery_test=1` |
| B | the same, plus `vintner.test_slot=b` |

Keep the base disk's file names.

Do **not** add `vintner.root=`: on UEFI the recovery image must find the root from the manifest, and this test is where that is proved.

On BIOS, the slots' `slot.cfg` already have `vintner.recovery_test=1`.

### 2. Cases

| ID | Change to the disk copy, made on the host | What each os-1 boot should do |
|---|---|---|
| G0 | none (control) | boot normally and be blessed |
| F1 | zero the root file system's primary superblock: 1024 bytes at byte 1024 of the root partition | stop in the initramfs, unable to mount the root |
| F2 | remove `/usr/lib/systemd/systemd` and `/sbin/init` from the root file system (`debugfs -w`, or a loop mount) | kernel panic, "No working init found" |
| F3 | append `/dev/disk/by-uuid/00000000-0000-0000-0000-000000000000 /mnt/missing ext4 defaults 0 2` to `/etc/fstab` | stop in emergency mode |
| F4 | install and enable `vintner-test-health.service`: `Type=oneshot`, `ExecStart=/bin/false`, `Before=boot-complete.target`, `[Install] RequiredBy=boot-complete.target` | reach multi-user, but `boot-complete.target` fails, so bless does not run |

### 3. Boots

Boot each case up to 5 times, under the firmware given to `run.sh`, with the serial console to a file and `-no-reboot`.

A boot ends in one of two ways:
- the guest powers off;
- after 120 seconds under KVM, QEMU is killed. This is a power cut, the way a person resets a hung machine. Record which way each boot ended.

Stop after the first boot that is not os-1.

Before the first boot that is not os-1, read the root partition's sha256 on the host, and read it again after that boot.

**Which image each boot ran:** from the kernel's `Command line:` line on the serial console.
- os-1: has `vintner.bootstorage=` and no `vintner.recovery`.
- Recovery A: has `vintner.recovery=1` and no `vintner.test_slot=b`. On BIOS it also follows a `vintner: trying recovery-a` line.

**Counters:** read them on the host after every boot.
- UEFI: the names of the os-1 and recovery A UKIs on boot storage.
- BIOS: `grub-editenv list`.

### 4. Checks

| ID | Check | Expected |
|---|---|---|
| D1 | G0: every boot | os-1, blessed (report shows `vintner-bless.service` active), counter re-armed each time (UEFI `+3-0`, BIOS `VINTNER_T_1=xxx` and no `VINTNER_BOOTED`); 5 boots, never recovery |
| D2 | F1 to F4: boots 1 to 3 | os-1 each time. Counter after each: UEFI `+2-1`, `+1-2`, `+0-3`; BIOS `xx`, `x`, `0`, with `VINTNER_BOOTED=1` |
| D3 | F1 to F4: boot 4 | Recovery A, and not recovery B. Recovery A's counter is then UEFI `+2-1`; BIOS `VINTNER_T_RA=xx`, `VINTNER_BOOTED=RA` |
| D4 | F1 to F4: boot 4's report | F2, F3, F4: `VINTNER-RECOVERY-TEST root=mounted ro=yes`. F1: `root=absent ro=no` or `root=failed ro=no`, whichever the run shows; OBSERVED |
| D5 | F1 to F4: root partition sha256 before and after boot 4 | unchanged |
| D6 | F1 to F4: how boots 1 to 3 ended | OBSERVED: powered off, or killed at the timeout |

Record each case's serial evidence for D2: the line that shows why the boot failed.
- F1: initramfs-tools' "Gave up waiting for root file system device", or the mount error.
- F2: the panic line.
- F3: the emergency-mode line.
- F4: the failed `vintner-test-health.service` and `boot-complete.target` lines.

`results.tsv` has one row per check and case: ID, case, status, evidence.

**PASS rules:**
- D1 to D5 PASS when the evidence matches exactly.
- D4 for F1 and D6 are OBSERVED.
- The script exits 0 when every row that can PASS does.

### 5. CI

Add `fallback-uefi` and `fallback-bios` to `.github/workflows/qemu-tests.yml`, with the same setup as the lifecycle rows. Both must pass in the pull request's CI. Each should finish within 60 minutes.

## Reporting back

- **The pull request:** both results tables, with evidence copied from the CI run.
- **Findings:** report anything that does not match the expected table with its serial evidence, first of all a fallback that happens earlier or later than boot 4. Do not adjust the expected counts to fit; say what happened.
- **A check that cannot be run as written:** say exactly why.
