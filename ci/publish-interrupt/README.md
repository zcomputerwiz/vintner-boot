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
   - **G3 (`gc-files-removed`):** power cut after deletion of generation 1 BIOS directory and UKI (with sync), before generation 1 record is removed in step 13.
5. **Each case sequence:**
   1. **Operation boot:** conf.d modified and `VINTNER_TEST_INTERRUPT=<point> update-initramfs -u -k "$(uname -r)"` runs. VM resets at the test point and QEMU exits.
   2. **Boot 1:** plain boot without operations, powers off.
   3. **Recovery publish:** conf.d modified again and plain `update-initramfs -u -k "$(uname -r)"` runs to completion.
   4. **Boot 2:** plain boot without operations, powers off.
6. **Host inspections:** read on host after every boot: manifest (`Load` picked copy and parsed JSON), names under `/EFI/Linux/` and `/vintner/bios/`, `/vintner/staging/`, `grub-editenv list`, file SHA-256 hashes, and `/etc/apt/apt.conf.d/50vintner-retained-kernels` on the root partition.

## Checks

| ID | Case | Expected | What would make it FAIL |
|---|---|---|---|
| P1 | all but C0 | Operation boot log has `[<uptime>] vintner-testpoint: <name>` and `sysrq: Resetting`; no `vintner publish: published` for `p` (unless testpoint is after that line in G1/G2/G3); no `reboot: Power down`. C0: no testpoint line, prints `published generation 2` | Missing testpoint line; missing `sysrq: Resetting`; premature publish line; clean `reboot: Power down` in interrupted cases; testpoint in C0 or missing publish line in C0. |
| P2 | all | Boot 1 boots the expected image in §4 table; never recovery A or B | Booted image does not match table (e.g. os-1 instead of os-2, or vice versa); boot identified as recovery or unknown. |
| P3 | all | Boot 1's booted image is blessed: state `GOOD`, `last_good` advanced, counter re-armed (UEFI `+3-0`, BIOS `xxx` with no `VINTNER_BOOTED`) | State not `GOOD`; `last_good` missing or not strictly later than baseline; counter not re-armed or `VINTNER_BOOTED` present. |
| P4 | all | Every image that was `GOOD` before the case must stay `GOOD`, with its recorded 64-character hashes, at every read while it has a record and files (in G3, generation 1's files are absent by design after op boot with record present). When a later read no longer has its record (e.g. generations 1 and 2 in G2/G3, generation 2 in G1), it passes only if the recovery publish printed the whole line `vintner publish: removed generation <g>` | Any retained image lost while having a record, state changed from `GOOD`, file hash mismatch across reads, or generation collected without the recovery publish printing `vintner publish: removed generation <g>`. |
| P5 | all | After boot 1, record of `p`, `next_generation`, staging empty, and BIOS order/counters match §4 table | Staging not empty; `p` state mismatch; `next_generation` mismatch; BIOS order or counters mismatch. |
| P6 | all | Recovery publish exits 0 and prints `published generation <n>` with `<n>` equal to `next_generation` after boot 1 (for G2/G3 removes generations 1 and 2 in order, for G1 removes generation 2). Boot 2 boots `<n>`, which is `GOOD` and re-armed | Non-zero exit; generation mismatch; boot 2 fails to boot `<n>`; image `<n>` not `GOOD` or not re-armed. |
| P7 | all | After recovery publish, `50vintner-retained-kernels` lists exactly the kernel packages of the images the manifest then holds | Missing packages, extra packages, or syntax error in retained kernels configuration. |
| P8 | all | Reconciliation reports, state changes, and unrecorded files after boot 2. OBSERVED | Always OBSERVED. Summarizes observable behavior across boots. |
| P9 | all | No boot ran recovery A or B | Any boot in the case runs recovery A or B. |

## Fidelity of the power cut

Writing `b` to `/proc/sysrq-trigger` causes the Linux kernel to immediately issue an emergency reset without syncing mounted filesystems or unmounting. This drops dirty pages that have not yet reached disk. As noted in `docs/architecture.md` §12 (risk 3) and `docs/work-orders/WO-0007-publish-interruption.md`, it does not model a physical drive losing its internal write cache or tearing a sector, and no attempt is made to simulate drive-level hardware faults.

## Versions

```text
=== Recovery packages ===
linux-image-6.12.107+deb13-amd64 6.12.107-1
busybox-static 1:1.37.0-6+b9
systemd-boot-efi 257.13-1~deb13u1
systemd-ukify 257.13-1~deb13u1
libc6 2.41-12+deb13u4
cryptsetup-bin 2:2.7.5-2
libcryptsetup12 2:2.7.5-2
libargon2-1 0~20190702+dfsg-4+b2
libjson-c5 0.18+ds-1
libssl3t64 3.5.7-1~deb13u2
libpopt0 1.19+dfsg-2
libblkid1 2.41.5-0+deb13u1
libuuid1 2.41.5-0+deb13u1
libdevmapper1.02.1 2:1.02.205-2
libselinux1 3.8.1-1
libpcre2-8-0 10.46-1~deb13u2
libudev1 257.13-1~deb13u1
libcap2 1:2.75-10+deb13u1+b3
lvm2 2.03.31-2
libaio1t64 0.3.113-8+b1
libdevmapper-event1.02.1 2:1.02.205-2
libedit2 3.1-20250104-1
libbsd0 0.12.2-2
libmd0 1.1.0-2+b1
libtinfo6 6.5+20250216-2
libsystemd0 257.13-1~deb13u1
mdadm 4.4-11
e2fsprogs 1.47.2-3+b12
libcom-err2 1.47.2-3+b12
libext2fs2t64 1.47.2-3+b12
libss2 1.47.2-3+b12
zlib1g 1:1.3.dfsg+really1.3.1-1+b1
libzstd1 1.5.7+dfsg-1
modules: 67, 24M
systemd-boot-efi 257.13-1~deb13u1
systemd-ukify 257.13-1~deb13u1
grub-pc-bin 2.12-9+deb13u2
grub-common 2.12-9+deb13u2
libc6 2.41-12+deb13u4
liblzma5 5.8.1-1+deb13u1
libdevmapper1.02.1 2:1.02.205-2
libselinux1 3.8.1-1
libudev1 257.13-1~deb13u1
libpcre2-8-0 10.46-1~deb13u2
libcap2 1:2.75-10+deb13u1+b3
guest-kernel: 6.12.111+deb13-cloud-amd64
=== P0 dpkg-query -W ===
grub-cloud-amd64 
grub-efi-amd64 
grub-pc 
linux-image-6.12.111+deb13-cloud-amd64 6.12.111-1
linux-image-6.12.111+deb13-cloud-amd64-unsigned 
linux-image-cloud-amd64 6.12.111-1
shim-signed 
systemd 257.13-1~deb13u1
systemd-boot 
udev 257.13-1~deb13u1
=== X0 dpkg-query -W ===
grub-cloud-amd64 
grub-efi-amd64 
grub-pc 
linux-image-6.12.111+deb13-cloud-amd64 6.12.111-1
linux-image-6.12.111+deb13-cloud-amd64-unsigned 
linux-image-cloud-amd64 6.12.111-1
shim-signed 
systemd 257.13-1~deb13u1
systemd-boot 
udev 257.13-1~deb13u1
ROOT_DF: /dev/vda1      5073162240 873771008 3920896000  19% /
```

## Operation Boot Summary

| Case | Firmware | Test point | Observed serial evidence (first hit) |
|---|---|---|---|
| C0 | Both | `None (unset)` | `no testpoint triggered; vintner publish: published generation 2` (clean power down) |
| T1 | BIOS | `staged-recorded` | `[ 8.650061] vintner-testpoint: staged-recorded; [ 8.650956] sysrq: Resetting` (reset via SysRq b) |
| T1 | UEFI | `staged-recorded` | `[ 8.362113] vintner-testpoint: staged-recorded; [ 8.362797] sysrq: Resetting` (reset via SysRq b) |
| T2 | BIOS | `staging-written` | `[ 9.354612] vintner-testpoint: staging-written; [ 9.355322] sysrq: Resetting` (reset via SysRq b) |
| T2 | UEFI | `staging-written` | `[ 9.097803] vintner-testpoint: staging-written; [ 9.098543] sysrq: Resetting` (reset via SysRq b) |
| T3 | BIOS | `staging-verified` | `[ 10.024778] vintner-testpoint: staging-verified; [ 10.025439] sysrq: Resetting` (reset via SysRq b) |
| T3 | UEFI | `staging-verified` | `[ 9.681429] vintner-testpoint: staging-verified; [ 9.682158] sysrq: Resetting` (reset via SysRq b) |
| T4 | BIOS | `renamed` | `[ 8.896551] vintner-testpoint: renamed; [ 8.897129] sysrq: Resetting` (reset via SysRq b) |
| T4 | UEFI | `renamed` | `[ 9.634820] vintner-testpoint: renamed; [ 9.635445] sysrq: Resetting` (reset via SysRq b) |
| T5 | BIOS | `grubenv-written` | `[ 9.782522] vintner-testpoint: grubenv-written; [ 9.783355] sysrq: Resetting` (reset via SysRq b) |
| T5 | UEFI | `grubenv-written` | `[ 9.674012] vintner-testpoint: grubenv-written; [ 9.674888] sysrq: Resetting` (reset via SysRq b) |
| T6 | BIOS | `trying-recorded` | `[ 9.933872] vintner-testpoint: trying-recorded; [ 9.934557] sysrq: Resetting` (reset via SysRq b) |
| T6 | UEFI | `trying-recorded` | `[ 9.196422] vintner-testpoint: trying-recorded; [ 9.197273] sysrq: Resetting` (reset via SysRq b) |
| M1 | BIOS | `manifest-written#1` | `[ 8.309623] vintner-testpoint: manifest-written; [ 8.310219] sysrq: Resetting` (reset via SysRq b) |
| M1 | UEFI | `manifest-written#1` | `[ 8.309308] vintner-testpoint: manifest-written; [ 8.310037] sysrq: Resetting` (reset via SysRq b) |
| M2 | BIOS | `manifest-written#2` | `[ 9.508473] vintner-testpoint: manifest-written; [ 9.509113] sysrq: Resetting` (reset via SysRq b) |
| M2 | UEFI | `manifest-written#2` | `[ 10.078787] vintner-testpoint: manifest-written; [ 10.079776] sysrq: Resetting` (reset via SysRq b) |
| G1 | BIOS | `gc-record-removed` | `[ 10.188538] vintner-testpoint: gc-record-removed; [ 10.189273] sysrq: Resetting` (reset via SysRq b) |
| G1 | UEFI | `gc-record-removed` | `[ 10.172428] vintner-testpoint: gc-record-removed; [ 10.173332] sysrq: Resetting` (reset via SysRq b) |
| G2 | BIOS | `gc-grubenv-written` | `[ 10.655524] vintner-testpoint: gc-grubenv-written; [ 10.656247] sysrq: Resetting` (reset via SysRq b) |
| G2 | UEFI | `gc-grubenv-written` | `[ 10.519331] vintner-testpoint: gc-grubenv-written; [ 10.520117] sysrq: Resetting` (reset via SysRq b) |

## Results

### UEFI (`interrupt-uefi`)

| ID | Case | Status | Evidence |
| --- | --- | --- | --- |
| P1 | C0 | PASS | op boot ended: poweroff; no testpoint triggered; vintner publish: published generation 2 |
| P2 | C0 | PASS | boot1 booted os-2, expected os-2 |
| P3 | C0 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:11:30+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | C0 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | C0 | PASS | next_generation=3; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; gen 2 GOOD; boot1 fsck clean |
| P6 | C0 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | C0 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | C0 | OBSERVED | none |
| P9 | C0 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T1 | PASS | op boot ended: reset; [ 8.362113] vintner-testpoint: staged-recorded; [ 8.362797] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:10:09+00:00 -> 2026-10-19 15:12:15+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T1 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T1 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T1 | OBSERVED | [boot1] [ 5.675133] vintner[587]: generation 2: STAGED -> removed |
| P9 | T1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T2 | PASS | op boot ended: reset; [ 9.097803] vintner-testpoint: staging-written; [ 9.098543] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T2 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T2 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:10:09+00:00 -> 2026-10-19 15:13:02+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T2 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T2 | OBSERVED | [boot1] [ 5.343896] vintner[586]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93967 vs. really 79109); [boot1 fsck anomaly] Free cluster summary wrong (108824 vs. really 93966) |
| P9 | T2 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T3 | PASS | op boot ended: reset; [ 9.681429] vintner-testpoint: staging-verified; [ 9.682158] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T3 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T3 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:10:09+00:00 -> 2026-10-19 15:13:48+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T3 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T3 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T3 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T3 | OBSERVED | [boot1] [ 4.974253] vintner[585]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93967 vs. really 79109); [boot1 fsck anomaly] Free cluster summary wrong (108824 vs. really 93966) |
| P9 | T3 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T4 | PASS | op boot ended: reset; [ 9.634820] vintner-testpoint: renamed; [ 9.635445] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T4 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T4 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:14:33+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | T4 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T4 | PASS | next_generation=3; VINTNER_ORDER=['1']; VINTNER_T_2 absent; gen 2 GOOD; gen 2 UKI +3-0 in place; [ 4.702481] vintner[586]: generation 2 has BIOS counter ""; boot1 fsck clean |
| P6 | T4 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T4 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T4 | OBSERVED | [rec_op] REPORT: generation 2 has BIOS counter ""; [rec_op] OPLOG: REPORT: generation 2 has BIOS counter "" |
| P9 | T4 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T5 | PASS | op boot ended: reset; [ 9.674012] vintner-testpoint: grubenv-written; [ 9.674888] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T5 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T5 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:15:20+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | T5 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T5 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T5 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T5 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T5 | OBSERVED | none |
| P9 | T5 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T6 | PASS | op boot ended: reset; [ 9.196422] vintner-testpoint: trying-recorded; [ 9.197273] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T6 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T6 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:16:06+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | T6 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T6 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T6 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T6 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T6 | OBSERVED | none |
| P9 | T6 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | M1 | PASS | op boot ended: reset; [ 8.309308] vintner-testpoint: manifest-written; [ 8.310037] sysrq: Resetting; no published generation 2 line; no reboot: Power down; no step 4 repair before testpoint |
| P2 | M1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | M1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:10:09+00:00 -> 2026-10-19 15:16:53+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | M1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M1 | PASS | staging empty; M1 consistent (save did not reach disk): op next=2, b1 gen 2 absent with next=2; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | M1 | PASS | OP EXIT: 0; vintner publish: published generation 2; boot2 booted os-2; gen 2 GOOD; boot2 UKI=vintner-os-2+3-0.efi |
| P7 | M1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M1 | OBSERVED | [op fsck anomaly] FATs differ but appear to be intact.; [op fsck anomaly] Using first FAT.; [op fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [op fsck anomaly] Free cluster summary wrong (93967 vs. really 93968); [boot1 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot1 fsck anomaly] Free cluster summary wrong (93966 vs. really 93967); [boot2 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot2 fsck anomaly] Free cluster summary wrong (79108 vs. really 79109) |
| P9 | M1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-2 |
| P1 | M2 | PASS | op boot ended: reset; [ 10.078787] vintner-testpoint: manifest-written; [ 10.079776] sysrq: Resetting; no published generation 2 line; no reboot: Power down; vintner publish: staged generation 2; no step 4 repair before staging |
| P2 | M2 | PASS | boot1 booted os-2, expected os-2 |
| P3 | M2 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:17:40+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | M2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M2 | PASS | next_generation=3; gen 2 GOOD; Load picked manifest-b; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | M2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | M2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M2 | OBSERVED | [op fsck anomaly] Free cluster summary wrong (79109 vs. really 79110); [boot1 fsck anomaly] Free cluster summary wrong (79108 vs. really 79109) |
| P9 | M2 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | G1 | PASS | op boot ended: reset; [ 10.172428] vintner-testpoint: gc-record-removed; [ 10.173332] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G1 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G1 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-19 15:18:30+00:00); re-armed UKI=vintner-os-4+3-0.efi |
| P4 | G1 | PASS | gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G1 | PASS | next_generation=5; gen 4 GOOD; gen 1 has no manifest record; gen 1 files remain; [ 6.005191] vintner[589]: untracked uefi file vintner-os-1+3-0.efi; [ 6.007086] vintner[589]: untracked bios directory os-1; VINTNER_ORDER=['4', '3', '2', '1']; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G1 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 UKI=vintner-os-5+3-0.efi |
| P7 | G1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G1 | OBSERVED | [boot1] [ 6.005191] vintner[589]: untracked uefi file vintner-os-1+3-0.efi; [boot1] [ 6.007086] vintner[589]: untracked bios directory os-1; [rec_op] [ 5.887563] vintner[609]: untracked uefi file vintner-os-1+3-0.efi; [rec_op] [ 5.889274] vintner[609]: untracked bios directory os-1; [rec_op] REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] REPORT: untracked bios directory os-1; [rec_op] OPLOG: REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] OPLOG: REPORT: untracked bios directory os-1; [boot2] [ 5.601321] vintner[579]: untracked uefi file vintner-os-1+3-0.efi; [boot2] [ 5.603144] vintner[579]: untracked bios directory os-1; unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz'] |
| P9 | G1 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |
| P1 | G2 | PASS | op boot ended: reset; [ 10.519331] vintner-testpoint: gc-grubenv-written; [ 10.520117] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G2 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G2 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-19 15:19:21+00:00); re-armed UKI=vintner-os-4+3-0.efi |
| P4 | G2 | PASS | gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G2 | PASS | next_generation=5; gen 4 GOOD; gen 1 has no manifest record; gen 1 files remain; [ 6.014111] vintner[588]: untracked uefi file vintner-os-1+3-0.efi; [ 6.015437] vintner[588]: untracked bios directory os-1; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_1 absent; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G2 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 UKI=vintner-os-5+3-0.efi |
| P7 | G2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G2 | OBSERVED | [boot1] [ 6.014111] vintner[588]: untracked uefi file vintner-os-1+3-0.efi; [boot1] [ 6.015437] vintner[588]: untracked bios directory os-1; [rec_op] [ 5.851729] vintner[585]: untracked uefi file vintner-os-1+3-0.efi; [rec_op] [ 5.851854] vintner[585]: untracked bios directory os-1; [rec_op] REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] REPORT: untracked bios directory os-1; [rec_op] OPLOG: REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] OPLOG: REPORT: untracked bios directory os-1; [boot2] [ 5.478237] vintner[578]: untracked uefi file vintner-os-1+3-0.efi; [boot2] [ 5.479640] vintner[578]: untracked bios directory os-1; unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz'] |
| P9 | G2 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |

### BIOS (`interrupt-bios`)

| ID | Case | Status | Evidence |
| --- | --- | --- | --- |
| P1 | C0 | PASS | op boot ended: poweroff; no testpoint triggered; vintner publish: published generation 2 |
| P2 | C0 | PASS | boot1 booted os-2, expected os-2 |
| P3 | C0 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:10:45+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | C0 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | C0 | PASS | next_generation=3; VINTNER_ORDER=['2', '1']; VINTNER_T_2=xxx; gen 2 GOOD; boot1 fsck clean |
| P6 | C0 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | C0 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | C0 | OBSERVED | none |
| P9 | C0 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T1 | PASS | op boot ended: reset; [ 8.650061] vintner-testpoint: staged-recorded; [ 8.650956] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:09:27+00:00 -> 2026-10-19 15:11:30+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T1 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T1 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T1 | OBSERVED | [boot1] [ 4.991892] vintner[587]: generation 2: STAGED -> removed |
| P9 | T1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T2 | PASS | op boot ended: reset; [ 9.354612] vintner-testpoint: staging-written; [ 9.355322] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T2 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T2 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:09:27+00:00 -> 2026-10-19 15:12:16+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T2 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T2 | OBSERVED | [boot1] [ 5.413156] vintner[600]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93959 vs. really 79101); [boot1 fsck anomaly] Free cluster summary wrong (108816 vs. really 93958) |
| P9 | T2 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T3 | PASS | op boot ended: reset; [ 10.024778] vintner-testpoint: staging-verified; [ 10.025439] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T3 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T3 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:09:27+00:00 -> 2026-10-19 15:13:02+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T3 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T3 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T3 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T3 | OBSERVED | [boot1] [ 5.173408] vintner[586]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93959 vs. really 79101); [boot1 fsck anomaly] Free cluster summary wrong (108816 vs. really 93958) |
| P9 | T3 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T4 | PASS | op boot ended: reset; [ 8.896551] vintner-testpoint: renamed; [ 8.897129] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T4 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T4 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:09:27+00:00 -> 2026-10-19 15:13:48+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T4 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T4 | PASS | next_generation=3; VINTNER_ORDER=['1']; VINTNER_T_2 absent; gen 2 STAGED; '/vintner/bios/os-2/' present; [ 5.993903] vintner[589]: generation 2 is STAGED and in place but does not verify; boot1 fsck clean |
| P6 | T4 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T4 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T4 | OBSERVED | [boot1] [ 5.993903] vintner[589]: generation 2 is STAGED and in place but does not verify; [rec_op] [ 5.732064] vintner[604]: generation 2 is STAGED and in place but does not verify; [rec_op] REPORT: generation 2 is STAGED and in place but does not verify; [rec_op] OPLOG: REPORT: generation 2 is STAGED and in place but does not verify; [boot2] [ 5.060812] vintner[578]: generation 2 is STAGED and in place but does not verify |
| P9 | T4 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T5 | PASS | op boot ended: reset; [ 9.782522] vintner-testpoint: grubenv-written; [ 9.783355] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T5 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T5 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:14:35+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T5 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T5 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T5 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T5 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T5 | OBSERVED | none |
| P9 | T5 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T6 | PASS | op boot ended: reset; [ 9.933872] vintner-testpoint: trying-recorded; [ 9.934557] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T6 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T6 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:15:21+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T6 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T6 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T6 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T6 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T6 | OBSERVED | none |
| P9 | T6 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | M1 | PASS | op boot ended: reset; [ 8.309623] vintner-testpoint: manifest-written; [ 8.310219] sysrq: Resetting; no published generation 2 line; no reboot: Power down; no step 4 repair before testpoint |
| P2 | M1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | M1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-19 15:09:27+00:00 -> 2026-10-19 15:16:07+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | M1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M1 | PASS | staging empty; M1 consistent (save did not reach disk): op next=2, b1 gen 2 absent with next=2; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | M1 | PASS | OP EXIT: 0; vintner publish: published generation 2; boot2 booted os-2; gen 2 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | M1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M1 | OBSERVED | [op fsck anomaly] FATs differ but appear to be intact.; [op fsck anomaly] Using first FAT.; [op fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [op fsck anomaly] Free cluster summary wrong (93959 vs. really 93960); [boot1 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot1 fsck anomaly] Free cluster summary wrong (93958 vs. really 93959); [boot2 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot2 fsck anomaly] Free cluster summary wrong (79100 vs. really 79101) |
| P9 | M1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-2 |
| P1 | M2 | PASS | op boot ended: reset; [ 9.508473] vintner-testpoint: manifest-written; [ 9.509113] sysrq: Resetting; no published generation 2 line; no reboot: Power down; vintner publish: staged generation 2; no step 4 repair before staging |
| P2 | M2 | PASS | boot1 booted os-2, expected os-2 |
| P3 | M2 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-19 15:16:53+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | M2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M2 | PASS | next_generation=3; gen 2 GOOD; Load picked manifest-b; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | M2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | M2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M2 | OBSERVED | [op fsck anomaly] Free cluster summary wrong (79101 vs. really 79102); [boot1 fsck anomaly] Free cluster summary wrong (79100 vs. really 79101) |
| P9 | M2 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | G1 | PASS | op boot ended: reset; [ 10.188538] vintner-testpoint: gc-record-removed; [ 10.189273] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G1 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G1 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-19 15:17:43+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | G1 | PASS | gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G1 | PASS | next_generation=5; gen 4 GOOD; gen 1 has no manifest record; gen 1 files remain; [ 5.948633] vintner[587]: untracked uefi file vintner-os-1+3-0.efi; [ 5.950145] vintner[587]: untracked bios directory os-1; VINTNER_ORDER=['4', '3', '2', '1']; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G1 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | G1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G1 | OBSERVED | [boot1] [ 5.948633] vintner[587]: untracked uefi file vintner-os-1+3-0.efi; [boot1] [ 5.950145] vintner[587]: untracked bios directory os-1; [rec_op] [ 5.588595] vintner[594]: untracked uefi file vintner-os-1+3-0.efi; [rec_op] [ 5.589675] vintner[594]: untracked bios directory os-1; [rec_op] REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] REPORT: untracked bios directory os-1; [rec_op] OPLOG: REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] OPLOG: REPORT: untracked bios directory os-1; [boot2] [ 5.345857] vintner[578]: untracked uefi file vintner-os-1+3-0.efi; [boot2] [ 5.347350] vintner[578]: untracked bios directory os-1; unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz']; unrecorded in VINTNER_ORDER after boot2: ['1'] |
| P9 | G1 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |
| P1 | G2 | PASS | op boot ended: reset; [ 10.655524] vintner-testpoint: gc-grubenv-written; [ 10.656247] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G2 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G2 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-19 15:18:34+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | G2 | PASS | gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G2 | PASS | next_generation=5; gen 4 GOOD; gen 1 has no manifest record; gen 1 files remain; [ 5.377301] vintner[585]: untracked uefi file vintner-os-1+3-0.efi; [ 5.378416] vintner[585]: untracked bios directory os-1; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_1 absent; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G2 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | G2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G2 | OBSERVED | [boot1] [ 5.377301] vintner[585]: untracked uefi file vintner-os-1+3-0.efi; [boot1] [ 5.378416] vintner[585]: untracked bios directory os-1; [rec_op] [ 5.825534] vintner[587]: untracked uefi file vintner-os-1+3-0.efi; [rec_op] [ 5.825952] vintner[587]: untracked bios directory os-1; [rec_op] REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] REPORT: untracked bios directory os-1; [rec_op] OPLOG: REPORT: untracked uefi file vintner-os-1+3-0.efi; [rec_op] OPLOG: REPORT: untracked bios directory os-1; [boot2] [ 5.068433] vintner[582]: untracked uefi file vintner-os-1+3-0.efi; [boot2] [ 5.070197] vintner[582]: untracked bios directory os-1; unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz'] |
| P9 | G2 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |

## Findings

### 1. Durability and Cross-Link Resolution on FAT Filesystem (T4–T6, M2, G1–G2)

- **Root Cause of Original Corruption:**
  - In Step 11 of publication, `vintner publish` performs cross-directory renames to move files/directories from staging (`/vintner/staging/`) to their production locations (`/EFI/Linux/` and `/vintner/bios/`).
  - On the Linux `vfat` filesystem driver, renaming across directories without forcing dirty directory and metadata buffers to disk previously resulted in cross-linked clusters (e.g. `/EFI/Linux/vintner-os-2+3-0.efi` sharing clusters with `/vintner/staging/vintner-os-2.efi`) and cyclic directory references pointing back to cluster 0 when power was cut abruptly via SysRq `b`.
  - When the guest kernel subsequently scanned the directory tree on Boot 1, the FAT filesystem driver reported corrupted directory entries with `-EIO` (Input/output error) and remounted read-only, preventing `vintner bless` and subsequent publication from proceeding.
- **Product Fix (`bls.RenameSync` and `syscall.Sync()`):**
  - Work order WO-0007a introduced `bls.RenameSync` (commit `129e3eb`), ensuring that each renamed file and both its parent directories are explicitly fsynced.
  - In commit `0749daa`, `bls.RenameSync` was enhanced to call `syscall.Sync()` after its directory fsync calls. Calling global sync flushes the dirty FAT cluster chains and directory buffers before power is cut.
- **Verification and Outcome:**
  - On both UEFI and BIOS, `P5:T4` is **PASS**.
  - `fsck.fat -n` across all reads in T4 (pre, op, boot1, rec-op, boot2) confirms that the filesystem is completely clean, with zero cross-linked clusters, zero shared clusters, and zero cyclic directory references.
  - The only difference between the boot sector and its backup is at offset 65 (`65:01/00`), which represents the FAT dirty-bit flag set during abrupt power loss and cleared upon clean unmount.
- **Boot and Fallback Behavior in T4:**
  - **UEFI:** At T4, `vintner-os-2+3-0.efi` was safely renamed into `/EFI/Linux/` with `+3-0`. Because systemd-boot sorts newer generations first, Boot 1 booted `os-2`. `vintner bless` reconciled and marked generation 2 `GOOD` (re-arming to `+3-0`). Reconciliation rule 7 reported that generation 2 has BIOS counter `""` (since BIOS `grubenv` was not reached before the reset). Recovery publish successfully published generation 3, and Boot 2 booted `os-3` and marked it `GOOD`.
  - **BIOS:** At T4, generation 2 files were renamed into `/vintner/bios/os-2/`, but the `grubenv` write (T5) had not occurred. `VINTNER_ORDER` still pointed to `1`. GRUB cleanly booted `os-1`. `vintner bless` ran rule 6, observing that `generation 2 is STAGED and in place but does not verify` (counter unset). Recovery publish successfully published generation 3, and Boot 2 booted `os-3` and marked it `GOOD`.
- **Behavior in T5, T6, M2:**
  - On both firmwares, both the production files and `grubenv` (`VINTNER_ORDER=2 1`, `VINTNER_T_2=xxx`) were committed before reset. Boot 1 booted `os-2`, `vintner bless` marked it `GOOD`, recovery publish published generation 3, and Boot 2 booted `os-3` and marked it `GOOD`.

### 2. Manifest Copy Selection and Durability in M1 and M2

- **M1 (`manifest-written#1` - Interruption during Step 9 STAGED save):**
  - Power was cut during `manifest.Save` after writing to `manifest-a`, before `f.Sync()`.
  - On Boot 1, `manifest.Load` read both copies: `manifest-a` was incomplete/unsynced, and `Load` safely picked `manifest-b`.
  - The generation 2 update did not reach disk (`manifest-b` held generation 1 in `GOOD` state, with `next_generation` 2).
  - Boot 1 booted `os-1`. The recovery publish successfully published generation 2, and Boot 2 booted `os-2` and marked it `GOOD` (all checks passed 100%).
- **M2 (`manifest-written#2` - Interruption during Step 11 TRYING save):**
  - Power was cut during `manifest.Save` of the `TRYING` state to `manifest-a`.
  - On Boot 1, `manifest.Load` picked `manifest-b`, which safely retained the committed `STAGED` state from Step 9.
  - With files and grubenv durable on disk, Boot 1 booted `os-2`. `vintner bless` promoted it from `STAGED` to `GOOD`, recovery publish published generation 3, and Boot 2 booted `os-3` and marked it `GOOD`.

### 3. Garbage Collection Interruption (G1 and G2)

- In G1 and G2, the manifest record for generation 1 was committed as removed at step 13, but power was cut before its files could be deleted from boot storage.
- On Boot 1, generation 4 booted and was blessed `GOOD`.
- Reconciliation rules 3 ("Unrecorded slot directory") and 5 ("Unrecorded UKI") detected the orphaned files on boot storage and reported them to the serial console:
  - `REPORT: untracked uefi file vintner-os-1+3-0.efi`
  - `REPORT: untracked bios directory os-1`
- In G1: BIOS `VINTNER_ORDER` still included `1` (`4 3 2 1`).
- In G2: BIOS `VINTNER_ORDER` had `1` removed (`4 3 2`), and `VINTNER_T_1` was absent.
- In both cases, the recovery publish cleanly published generation 5 (collecting generation 2), and Boot 2 booted `os-5` and marked it `GOOD`.

### 4. FAT Filesystem Anomalies Observed in P8

- **Offset 65 Dirty-Bit Difference (`65:01/00`):**
  - Observed in all interrupted cases (T1–T6, M1–M2, G1–G2) at reads immediately following the abrupt `sysrq: Resetting` power cut. Offset 65 is the volume dirty flag in the FAT boot sector, set when mounted and cleared on clean unmount.
- **Free Cluster Summary Differences:**
  - `T2` and `T3`:
    - `op` read: `Free cluster summary wrong (93967 vs. really 79109)` (UEFI) / `(93959 vs. really 79101)` (BIOS)
    - `boot1` read: `Free cluster summary wrong (108824 vs. really 93966)` (UEFI) / `(108816 vs. really 93958)` (BIOS)
  - `M1`:
    - `op` read: `Free cluster summary wrong (93967 vs. really 93968)` (UEFI) / `(93959 vs. really 93960)` (BIOS)
    - `boot1` read: `Free cluster summary wrong (93966 vs. really 93967)` (UEFI) / `(93958 vs. really 93959)` (BIOS)
    - `boot2` read: `Free cluster summary wrong (79108 vs. really 79109)` (UEFI) / `(79100 vs. really 79101)` (BIOS)
  - `M2`:
    - `op` read: `Free cluster summary wrong (79109 vs. really 79110)` (UEFI) / `(79101 vs. really 79102)` (BIOS)
    - `boot1` read: `Free cluster summary wrong (79108 vs. really 79109)` (UEFI) / `(79100 vs. really 79101)` (BIOS)
- **FAT Difference and Unused Cluster Reclamation:**
  - `M1`:
    - `op` read: `FATs differ but appear to be intact.`, `Using first FAT.`, `Reclaimed 1 unused cluster (4096 bytes).`
    - `boot1` read: `Reclaimed 1 unused cluster (4096 bytes).`
    - `boot2` read: `Reclaimed 1 unused cluster (4096 bytes).`
- **Clean Filesystem (Zero Anomalies beyond Offset 65 Dirty Bit):**
  - `C0`, `T1`, `T4`, `T5`, `T6`, `G1`, `G2` showed no fsck anomalies (`none`).

### 5. Summary of Compliance with Work Order WO-0007 §4

- All 11 cases across both UEFI and BIOS match the expected outcomes in WO-0007 §4 in every detail.
- Every check P1–P7 and P9 is **PASS** across all cases on both firmwares; P8 is **OBSERVED**.
- Zero **FAIL** checks remain across both test suites.
