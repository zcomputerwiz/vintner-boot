# Publication interruption integration test (WO-0007)

Tests that a power cut at any durable point of a publish leaves a bootable system, that reconciliation rules bring the manifest back to a consistent state, and that subsequent publication succeeds, across both UEFI and BIOS.

```bash
ci/publish-interrupt/run.sh <uefi|bios>
```

CI runs it as `interrupt-uefi` and `interrupt-bios` in `.github/workflows/qemu-tests.yml`.

## Method

1. **Base disk:** built once per run by `ci/lib/vintner-base.sh`, with `systemd-ukify` and `systemd-boot-efi` installed, the kernel and initramfs hooks copied to `/etc/kernel/postinst.d/zz-vintner` and `/etc/initramfs/post-update.d/zz-vintner`, `/usr/sbin/vintner` updated with a build tagged `vintner_testpoints`, and `os-1` booted once so that it is blessed `GOOD`. Pre-checks verify that the guest binary matches the host-built tagged binary hash, and that a default build contains neither `VINTNER_TEST_INTERRUPT` nor `vintner-testpoint`.
2. **Garbage-collection disk:** starts from the base disk and runs two consecutive rounds of harmless `/etc/initramfs-tools/conf.d/` change, `update-initramfs -u -k "$(uname -r)"`, and one bless boot. Holds generations 1, 2, and 3, all `GOOD`, with `next_generation` 4, so the next publish collects generation 1.
3. **Interrupt mechanism:** in tagged builds, `src/internal/testpoint` checks `VINTNER_TEST_INTERRUPT`. When matched:
   - Writes `<2>vintner-testpoint: <name>` to `/dev/kmsg`, which the kernel emits to the serial console.
   - Writes `b` to `/proc/sysrq-trigger`, resetting the VM immediately without syncing or unmounting.
   - QEMU exits due to `-no-reboot`.
4. **Cases:**
   - **C0 (control):** variable unset; publish completes normally and powers off.
   - **T1 (`staged-recorded`):** power cut right after manifest records `STAGED` in step 9.
   - **T2 (`staging-written`):** power cut after last staging copy, before `syncDir(stagingDir)` in step 10.
   - **T3 (`staging-verified`):** power cut after readback checks in step 10.
   - **T4 (`renamed`):** power cut after renames and directory syncs, before BIOS grubenv write or UEFI `TRYING` save in step 11.
   - **T5 (`grubenv-written`):** power cut after BIOS grubenv write, before `TRYING` save in step 11.
   - **T6 (`trying-recorded`):** power cut right after manifest records `TRYING` in step 11.
   - **M1 (`manifest-written#1`):** power cut in `manifest.Save` between file write and `f.Sync()` during the `STAGED` commit.
   - **M2 (`manifest-written#2`):** power cut in `manifest.Save` between file write and `f.Sync()` during the `TRYING` commit.
   - **G1 (`gc-record-removed`):** power cut right after manifest without collected generation 1 is saved in step 13.
   - **G2 (`gc-grubenv-written`):** power cut after BIOS grubenv update in step 13, before generation 1 files are deleted.
5. **Each case sequence:**
   1. **Operation boot:** conf.d modified and `VINTNER_TEST_INTERRUPT=<point> update-initramfs -u -k "$(uname -r)"` runs. VM resets at the test point and QEMU exits.
   2. **Boot 1:** plain boot without operations, powers off.
   3. **Recovery publish:** conf.d modified again and plain `update-initramfs -u -k "$(uname -r)"` runs to completion.
   4. **Boot 2:** plain boot without operations, powers off.
6. **Host inspections:** read on host after every boot: manifest (`Load` picked copy and parsed JSON), names under `/EFI/Linux/` and `/vintner/bios/`, `/vintner/staging/`, `grub-editenv list`, file SHA-256 hashes, and `/etc/apt/apt.conf.d/50vintner-retained-kernels` on the root partition.

## Checks

| ID | Case | Expected | What would make it FAIL |
|---|---|---|---|
| P1 | all but C0 | Operation boot log has `[<uptime>] vintner-testpoint: <name>` and `sysrq: Resetting`; no `vintner publish: published` for `p` (unless testpoint is after that line in G1/G2); no `reboot: Power down`. C0: no testpoint line, prints `published generation 2` | Missing testpoint line; missing `sysrq: Resetting`; premature publish line; clean `reboot: Power down` in interrupted cases; testpoint in C0 or missing publish line in C0. |
| P2 | all | Boot 1 boots the expected image in §4 table; never recovery A or B | Booted image does not match table (e.g. os-1 instead of os-2, or vice versa); boot identified as recovery or unknown. |
| P3 | all | Boot 1's booted image is blessed: state `GOOD`, `last_good` advanced, counter re-armed (UEFI `+3-0`, BIOS `xxx` with no `VINTNER_BOOTED`) | State not `GOOD`; `last_good` missing or not strictly later than baseline; counter not re-armed or `VINTNER_BOOTED` present. |
| P4 | all | Every image that was `GOOD` before the case (and retained) is still `GOOD`, and its files have matching SHA-256 hashes, at every read | Any retained image lost, state changed from `GOOD`, or file hash mismatch across reads. |
| P5 | all | After boot 1, record of `p`, `next_generation`, staging empty, and BIOS order/counters match §4 table | Staging not empty; `p` state mismatch; `next_generation` mismatch; BIOS order or counters mismatch. |
| P6 | all | Recovery publish exits 0 and prints `published generation <n>` with `<n>` equal to `next_generation` after boot 1. Boot 2 boots `<n>`, which is `GOOD` and re-armed | Non-zero exit; generation mismatch; boot 2 fails to boot `<n>`; image `<n>` not `GOOD` or not re-armed. |
| P7 | all | After recovery publish, `50vintner-retained-kernels` lists exactly the kernel packages of the images the manifest then holds | Missing packages, extra packages, or syntax error in retained kernels configuration. |
| P8 | all | Reconciliation reports, state changes, and unrecorded files after boot 2. OBSERVED | Always OBSERVED. Summarizes observable behavior across boots. |
| P9 | all | No boot ran recovery A or B | Any boot in the case runs recovery A or B. |

## Fidelity of the power cut

Writing `b` to `/proc/sysrq-trigger` causes the Linux kernel to immediately issue an emergency reset without syncing mounted filesystems or unmounting. This drops dirty pages that have not yet reached disk. As noted in `docs/architecture.md` §12 (risk 3) and `docs/work-orders/WO-0007-publish-interruption.md`, it does not model a physical drive losing its internal write cache or tearing a sector, and no attempt is made to simulate drive-level hardware faults.

## Versions

*(To be updated from CI run)*

## Results

*(To be updated from CI run)*

### UEFI (`interrupt-uefi`)

*(Pending CI)*

### BIOS (`interrupt-bios`)

*(Pending CI)*

## Findings

*(To be updated from CI run with verbatim evidence)*
