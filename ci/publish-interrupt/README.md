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
| T1 | BIOS | `staged-recorded` | `[ 10.734716] vintner-testpoint: staged-recorded; [ 10.735789] sysrq: Resetting` (reset via SysRq b) |
| T1 | UEFI | `staged-recorded` | `[ 8.972077] vintner-testpoint: staged-recorded; [ 8.972885] sysrq: Resetting` (reset via SysRq b) |
| T2 | BIOS | `staging-written` | `[ 11.084806] vintner-testpoint: staging-written; [ 11.085752] sysrq: Resetting` (reset via SysRq b) |
| T2 | UEFI | `staging-written` | `[ 10.299831] vintner-testpoint: staging-written; [ 10.300671] sysrq: Resetting` (reset via SysRq b) |
| T3 | BIOS | `staging-verified` | `[ 11.076128] vintner-testpoint: staging-verified; [ 11.076918] sysrq: Resetting` (reset via SysRq b) |
| T3 | UEFI | `staging-verified` | `[ 10.568076] vintner-testpoint: staging-verified; [ 10.568888] sysrq: Resetting` (reset via SysRq b) |
| T4 | BIOS | `renamed` | `[ 11.617889] vintner-testpoint: renamed; [ 11.618528] sysrq: Resetting` (reset via SysRq b) |
| T4 | UEFI | `renamed` | `[ 10.888808] vintner-testpoint: renamed; [ 10.889549] sysrq: Resetting` (reset via SysRq b) |
| T5 | BIOS | `grubenv-written` | `[ 10.918479] vintner-testpoint: grubenv-written; [ 10.919522] sysrq: Resetting` (reset via SysRq b) |
| T5 | UEFI | `grubenv-written` | `[ 10.754124] vintner-testpoint: grubenv-written; [ 10.754934] sysrq: Resetting` (reset via SysRq b) |
| T6 | BIOS | `trying-recorded` | `[ 11.253214] vintner-testpoint: trying-recorded; [ 11.254308] sysrq: Resetting` (reset via SysRq b) |
| T6 | UEFI | `trying-recorded` | `[ 10.842553] vintner-testpoint: trying-recorded; [ 10.843349] sysrq: Resetting` (reset via SysRq b) |
| M1 | BIOS | `manifest-written#1` | `[ 10.355581] vintner-testpoint: manifest-written; [ 10.356491] sysrq: Resetting` (reset via SysRq b) |
| M1 | UEFI | `manifest-written#1` | `[ 9.254859] vintner-testpoint: manifest-written; [ 9.255619] sysrq: Resetting` (reset via SysRq b) |
| M2 | BIOS | `manifest-written#2` | `[ 11.604283] vintner-testpoint: manifest-written; [ 11.605642] sysrq: Resetting` (reset via SysRq b) |
| M2 | UEFI | `manifest-written#2` | `[ 11.405102] vintner-testpoint: manifest-written; [ 11.406234] sysrq: Resetting` (reset via SysRq b) |
| G1 | BIOS | `gc-record-removed` | `[ 12.978043] vintner-testpoint: gc-record-removed; [ 12.979109] sysrq: Resetting` (reset via SysRq b) |
| G1 | UEFI | `gc-record-removed` | `[ 11.725649] vintner-testpoint: gc-record-removed; [ 11.726458] sysrq: Resetting` (reset via SysRq b) |
| G2 | BIOS | `gc-grubenv-written` | `[ 12.823979] vintner-testpoint: gc-grubenv-written; [ 12.825083] sysrq: Resetting` (reset via SysRq b) |
| G2 | UEFI | `gc-grubenv-written` | `[ 12.515616] vintner-testpoint: gc-grubenv-written; [ 12.516474] sysrq: Resetting` (reset via SysRq b) |
| G3 | BIOS | `gc-files-removed` | `[ 12.704101] vintner-testpoint: gc-files-removed; [ 12.705051] sysrq: Resetting` (reset via SysRq b) |
| G3 | UEFI | `gc-files-removed` | `[ 12.212922] vintner-testpoint: gc-files-removed; [ 12.213612] sysrq: Resetting` (reset via SysRq b) |

## Results

Observed from CI run [41089608589](https://github.com/zcomputerwiz/vintner-boot/actions/runs/41089608589) on commit `89183ac`. Note that per-run values such as timestamps and kernel uptimes vary on every run.

### UEFI (`interrupt-uefi`)

| ID | Case | Status | Evidence |
| --- | --- | --- | --- |
| P1 | C0 | PASS | op boot ended: poweroff; no testpoint triggered; vintner publish: published generation 2 |
| P2 | C0 | PASS | boot1 booted os-2, expected os-2 |
| P3 | C0 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:30:22+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | C0 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | C0 | PASS | next_generation=3; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; gen 2 GOOD; boot1 fsck clean |
| P6 | C0 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | C0 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | C0 | OBSERVED | none |
| P9 | C0 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T1 | PASS | op boot ended: reset; [ 8.972077] vintner-testpoint: staged-recorded; [ 8.972885] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:56+00:00 -> 2026-10-30 16:31:12+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T1 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T1 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T1 | OBSERVED | [boot1] [ 6.384610] vintner[584]: generation 2: STAGED -> removed |
| P9 | T1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T2 | PASS | op boot ended: reset; [ 10.299831] vintner-testpoint: staging-written; [ 10.300671] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T2 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T2 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:56+00:00 -> 2026-10-30 16:32:01+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T2 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T2 | OBSERVED | [boot1] [ 5.660060] vintner[606]: ACTION: remove_staging vintner-os-2.efi; [boot1] [ 5.661854] vintner[606]: ACTION: remove_staging os-2; [boot1] [ 5.662967] vintner[606]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93963 vs. really 79105); [boot1 fsck anomaly] Free cluster summary wrong (108820 vs. really 93962) |
| P9 | T2 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T3 | PASS | op boot ended: reset; [ 10.568076] vintner-testpoint: staging-verified; [ 10.568888] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T3 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T3 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:56+00:00 -> 2026-10-30 16:32:53+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T3 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T3 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T3 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T3 | OBSERVED | [boot1] [ 5.958525] vintner[595]: ACTION: remove_staging vintner-os-2.efi; [boot1] [ 5.960363] vintner[595]: ACTION: remove_staging os-2; [boot1] [ 5.961367] vintner[595]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93963 vs. really 79105); [boot1 fsck anomaly] Free cluster summary wrong (108820 vs. really 93962) |
| P9 | T3 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T4 | PASS | op boot ended: reset; [ 10.888808] vintner-testpoint: renamed; [ 10.889549] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T4 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T4 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:33:45+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | T4 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T4 | PASS | next_generation=3; [ 5.836053] vintner[593]: ACTION: arm_bios generation 2; gen 2 GOOD; gen 2 UKI +3-0 in place; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; rule 7 report absent; boot1 fsck clean |
| P6 | T4 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T4 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T4 | OBSERVED | [boot1] [ 5.836053] vintner[593]: ACTION: arm_bios generation 2 |
| P9 | T4 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T5 | PASS | op boot ended: reset; [ 10.754124] vintner-testpoint: grubenv-written; [ 10.754934] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T5 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T5 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:34:38+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | T5 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T5 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T5 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T5 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T5 | OBSERVED | none |
| P9 | T5 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T6 | PASS | op boot ended: reset; [ 10.842553] vintner-testpoint: trying-recorded; [ 10.843349] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T6 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T6 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:35:30+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | T6 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T6 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T6 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | T6 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T6 | OBSERVED | none |
| P9 | T6 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | M1 | PASS | op boot ended: reset; [ 9.254859] vintner-testpoint: manifest-written; [ 9.255619] sysrq: Resetting; no published generation 2 line; no reboot: Power down; no step 4 repair before testpoint |
| P2 | M1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | M1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:56+00:00 -> 2026-10-30 16:36:21+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | M1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M1 | PASS | staging empty; M1 consistent (save did not reach disk): op next=2, b1 gen 2 absent with next=2; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | M1 | PASS | OP EXIT: 0; vintner publish: published generation 2; boot2 booted os-2; gen 2 GOOD; boot2 UKI=vintner-os-2+3-0.efi |
| P7 | M1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M1 | OBSERVED | [op fsck anomaly] FATs differ but appear to be intact.; [op fsck anomaly] Using first FAT.; [op fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [op fsck anomaly] Free cluster summary wrong (93963 vs. really 93964); [boot1 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot1 fsck anomaly] Free cluster summary wrong (93962 vs. really 93963); [boot2 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot2 fsck anomaly] Free cluster summary wrong (79104 vs. really 79105) |
| P9 | M1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-2 |
| P1 | M2 | PASS | op boot ended: reset; [ 11.405102] vintner-testpoint: manifest-written; [ 11.406234] sysrq: Resetting; no published generation 2 line; no reboot: Power down; vintner publish: staged generation 2; no step 4 repair before staging |
| P2 | M2 | PASS | boot1 booted os-2, expected os-2 |
| P3 | M2 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:37:14+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | M2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M2 | PASS | next_generation=3; gen 2 GOOD; Load picked manifest-b; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | M2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 UKI=vintner-os-3+3-0.efi |
| P7 | M2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M2 | OBSERVED | [op fsck anomaly] Free cluster summary wrong (79105 vs. really 79106); [boot1 fsck anomaly] Free cluster summary wrong (79104 vs. really 79105) |
| P9 | M2 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | G1 | PASS | op boot ended: reset; [ 11.725649] vintner-testpoint: gc-record-removed; [ 11.726458] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G1 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G1 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-30 16:38:14+00:00); re-armed UKI=vintner-os-4+3-0.efi |
| P4 | G1 | PASS | gen 1 record and files absent in op for G1 (removed at gc-record-removed); gen 1 record and files absent in boot1 for G1 (removed at gc-record-removed); gen 1 record and files absent in rec_op for G1 (removed at gc-record-removed); gen 1 record and files absent in boot2 for G1 (removed at gc-record-removed); gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [1, 2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G1 | PASS | next_generation=5; gen 4 GOOD; gen 1 has no manifest record; gen 1 files gone; gen 1 not in VINTNER_ORDER; VINTNER_T_1 absent; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_4=xxx; rule 3 report absent; boot1 fsck clean |
| P6 | G1 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 UKI=vintner-os-5+3-0.efi; vintner publish: removed generation 2 |
| P7 | G1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G1 | OBSERVED | none |
| P9 | G1 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |
| P1 | G2 | PASS | op boot ended: reset; [ 12.515616] vintner-testpoint: gc-grubenv-written; [ 12.516474] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G2 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G2 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-30 16:39:16+00:00); re-armed UKI=vintner-os-4+3-0.efi |
| P4 | G2 | PASS | gen 1 removed in rec_op with recovery publish log: vintner publish: removed generation 1; gen 1 removed in boot2 with recovery publish log: vintner publish: removed generation 1; gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [1, 2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G2 | PASS | next_generation=5; gen 4 GOOD; gen 1 still GOOD; gen 1 files remain; gen 1 not in VINTNER_ORDER; VINTNER_T_1 absent; [ 6.248717] vintner[588]: generation 1 has BIOS counter ""; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G2 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 UKI=vintner-os-5+3-0.efi; removed gen 1 then gen 2 in order |
| P7 | G2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G2 | OBSERVED | [rec_op] REPORT: generation 1 has BIOS counter ""; [rec_op] OPLOG: REPORT: generation 1 has BIOS counter "" |
| P9 | G2 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |
| P1 | G3 | PASS | op boot ended: reset; [ 12.212922] vintner-testpoint: gc-files-removed; [ 12.213612] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G3 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G3 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-30 16:40:22+00:00); re-armed UKI=vintner-os-4+3-0.efi |
| P4 | G3 | PASS | gen 1 files absent as expected in op for G3 with record present; gen 1 files absent as expected in boot1 for G3 with record present; gen 1 removed in rec_op with recovery publish log: vintner publish: removed generation 1; gen 1 removed in boot2 with recovery publish log: vintner publish: removed generation 1; gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [1, 2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G3 | PASS | next_generation=5; gen 4 GOOD; gen 1 still GOOD; gen 1 files gone; gen 1 not in VINTNER_ORDER; VINTNER_T_1 absent; [ 6.646045] vintner[603]: missing files for generation 1; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G3 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 UKI=vintner-os-5+3-0.efi; removed gen 1 then gen 2 in order |
| P7 | G3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G3 | OBSERVED | [boot1] [ 6.646045] vintner[603]: missing files for generation 1; [rec_op] [ 6.245048] vintner[587]: missing files for generation 1; [rec_op] REPORT: missing files for generation 1; [rec_op] REPORT: generation 1 has BIOS counter ""; [rec_op] OPLOG: REPORT: missing files for generation 1; [rec_op] OPLOG: REPORT: generation 1 has BIOS counter "" |
| P9 | G3 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |

### BIOS (`interrupt-bios`)

| ID | Case | Status | Evidence |
| --- | --- | --- | --- |
| P1 | C0 | PASS | op boot ended: poweroff; no testpoint triggered; vintner publish: published generation 2 |
| P2 | C0 | PASS | boot1 booted os-2, expected os-2 |
| P3 | C0 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:29:28+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | C0 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | C0 | PASS | next_generation=3; VINTNER_ORDER=['2', '1']; VINTNER_T_2=xxx; gen 2 GOOD; boot1 fsck clean |
| P6 | C0 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | C0 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | C0 | OBSERVED | none |
| P9 | C0 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T1 | PASS | op boot ended: reset; [ 10.734716] vintner-testpoint: staged-recorded; [ 10.735789] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:06+00:00 -> 2026-10-30 16:30:19+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T1 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T1 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T1 | OBSERVED | [boot1] [ 6.897866] vintner[591]: generation 2: STAGED -> removed |
| P9 | T1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T2 | PASS | op boot ended: reset; [ 11.084806] vintner-testpoint: staging-written; [ 11.085752] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T2 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T2 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:06+00:00 -> 2026-10-30 16:31:11+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T2 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T2 | OBSERVED | [boot1] [ 6.570941] vintner[587]: ACTION: remove_staging vintner-os-2.efi; [boot1] [ 6.573660] vintner[587]: ACTION: remove_staging os-2; [boot1] [ 6.574898] vintner[587]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93957 vs. really 79099); [boot1 fsck anomaly] Free cluster summary wrong (108814 vs. really 93956) |
| P9 | T2 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T3 | PASS | op boot ended: reset; [ 11.076128] vintner-testpoint: staging-verified; [ 11.076918] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T3 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T3 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:06+00:00 -> 2026-10-30 16:32:02+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T3 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T3 | PASS | next_generation=3; staging empty; gen 2 removed; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | T3 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T3 | OBSERVED | [boot1] [ 6.386539] vintner[585]: ACTION: remove_staging vintner-os-2.efi; [boot1] [ 6.388161] vintner[585]: ACTION: remove_staging os-2; [boot1] [ 6.390401] vintner[585]: generation 2: STAGED -> removed; [op fsck anomaly] Free cluster summary wrong (93957 vs. really 79099); [boot1 fsck anomaly] Free cluster summary wrong (108814 vs. really 93956) |
| P9 | T3 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-3 |
| P1 | T4 | PASS | op boot ended: reset; [ 11.617889] vintner-testpoint: renamed; [ 11.618528] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T4 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T4 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:06+00:00 -> 2026-10-30 16:32:53+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T4 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T4 | PASS | next_generation=3; [ 6.814889] vintner[604]: ACTION: arm_bios generation 2; gen 2 TRYING; '/vintner/bios/os-2/' present; VINTNER_ORDER=['2', '1']; VINTNER_T_2=xxx; rule 6 report absent; boot1 fsck clean |
| P6 | T4 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent; gen 2 GOOD; no STAGED records remain |
| P7 | T4 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T4 | OBSERVED | [boot1] [ 6.814889] vintner[604]: ACTION: arm_bios generation 2; [boot1] [ 6.920382] vintner[604]: generation 2: STAGED -> TRYING |
| P9 | T4 | PASS | op=os-1; boot1=os-1; rec_op=os-2; boot2=os-3 |
| P1 | T5 | PASS | op boot ended: reset; [ 10.918479] vintner-testpoint: grubenv-written; [ 10.919522] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T5 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T5 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:33:48+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T5 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T5 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T5 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T5 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T5 | OBSERVED | none |
| P9 | T5 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | T6 | PASS | op boot ended: reset; [ 11.253214] vintner-testpoint: trying-recorded; [ 11.254308] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T6 | PASS | boot1 booted os-2, expected os-2 |
| P3 | T6 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:34:39+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T6 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | T6 | PASS | next_generation=3; gen 2 GOOD; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | T6 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T6 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T6 | OBSERVED | none |
| P9 | T6 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | M1 | PASS | op boot ended: reset; [ 10.355581] vintner-testpoint: manifest-written; [ 10.356491] sysrq: Resetting; no published generation 2 line; no reboot: Power down; no step 4 repair before testpoint |
| P2 | M1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | M1 | PASS | generation 2 had no record before case; gen 1 state=GOOD; last_good advanced (2026-10-30 16:28:06+00:00 -> 2026-10-30 16:35:30+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | M1 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M1 | PASS | staging empty; M1 consistent (save did not reach disk): op next=2, b1 gen 2 absent with next=2; VINTNER_ORDER=['1']; VINTNER_T_2 absent; boot1 fsck clean |
| P6 | M1 | PASS | OP EXIT: 0; vintner publish: published generation 2; boot2 booted os-2; gen 2 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | M1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M1 | OBSERVED | [op fsck anomaly] FATs differ but appear to be intact.; [op fsck anomaly] Using first FAT.; [op fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [op fsck anomaly] Free cluster summary wrong (93957 vs. really 93958); [boot1 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot1 fsck anomaly] Free cluster summary wrong (93956 vs. really 93957); [boot2 fsck anomaly] Reclaimed 1 unused cluster (4096 bytes).; [boot2 fsck anomaly] Free cluster summary wrong (79098 vs. really 79099) |
| P9 | M1 | PASS | op=os-1; boot1=os-1; rec_op=os-1; boot2=os-2 |
| P1 | M2 | PASS | op boot ended: reset; [ 11.604283] vintner-testpoint: manifest-written; [ 11.605642] sysrq: Resetting; no published generation 2 line; no reboot: Power down; vintner publish: staged generation 2; no step 4 repair before staging |
| P2 | M2 | PASS | boot1 booted os-2, expected os-2 |
| P3 | M2 | PASS | generation 2 had no record before case; gen 2 state=GOOD; last_good set (2026-10-30 16:36:22+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | M2 | PASS | images [1] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | M2 | PASS | next_generation=3; gen 2 GOOD; Load picked manifest-b; VINTNER_ORDER starts with 2; VINTNER_T_2=xxx; boot1 fsck clean |
| P6 | M2 | PASS | OP EXIT: 0; vintner publish: published generation 3; boot2 booted os-3; gen 3 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | M2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M2 | OBSERVED | [op fsck anomaly] Free cluster summary wrong (79099 vs. really 79100); [boot1 fsck anomaly] Free cluster summary wrong (79098 vs. really 79099) |
| P9 | M2 | PASS | op=os-1; boot1=os-2; rec_op=os-2; boot2=os-3 |
| P1 | G1 | PASS | op boot ended: reset; [ 12.978043] vintner-testpoint: gc-record-removed; [ 12.979109] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G1 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G1 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-30 16:37:19+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | G1 | PASS | gen 1 record and files absent in op for G1 (removed at gc-record-removed); gen 1 record and files absent in boot1 for G1 (removed at gc-record-removed); gen 1 record and files absent in rec_op for G1 (removed at gc-record-removed); gen 1 record and files absent in boot2 for G1 (removed at gc-record-removed); gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [1, 2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G1 | PASS | next_generation=5; gen 4 GOOD; gen 1 has no manifest record; gen 1 files gone; gen 1 not in VINTNER_ORDER; VINTNER_T_1 absent; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_4=xxx; rule 3 report absent; boot1 fsck clean |
| P6 | G1 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent; vintner publish: removed generation 2 |
| P7 | G1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G1 | OBSERVED | none |
| P9 | G1 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |
| P1 | G2 | PASS | op boot ended: reset; [ 12.823979] vintner-testpoint: gc-grubenv-written; [ 12.825083] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G2 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G2 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-30 16:38:17+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | G2 | PASS | gen 1 removed in rec_op with recovery publish log: vintner publish: removed generation 1; gen 1 removed in boot2 with recovery publish log: vintner publish: removed generation 1; gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [1, 2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G2 | PASS | next_generation=5; gen 4 GOOD; gen 1 still GOOD; gen 1 files remain; gen 1 not in VINTNER_ORDER; VINTNER_T_1 absent; [ 7.370483] vintner[585]: generation 1 has BIOS counter ""; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G2 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent; removed gen 1 then gen 2 in order |
| P7 | G2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G2 | OBSERVED | [rec_op] REPORT: generation 1 has BIOS counter ""; [rec_op] OPLOG: REPORT: generation 1 has BIOS counter "" |
| P9 | G2 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |
| P1 | G3 | PASS | op boot ended: reset; [ 12.704101] vintner-testpoint: gc-files-removed; [ 12.705051] sysrq: Resetting; vintner publish: published generation 4; no reboot: Power down |
| P2 | G3 | PASS | boot1 booted os-4, expected os-4 |
| P3 | G3 | PASS | generation 4 had no record before case; gen 4 state=GOOD; last_good set (2026-10-30 16:39:16+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | G3 | PASS | gen 1 files absent as expected in op for G3 with record present; gen 1 files absent as expected in boot1 for G3 with record present; gen 1 removed in rec_op with recovery publish log: vintner publish: removed generation 1; gen 1 removed in boot2 with recovery publish log: vintner publish: removed generation 1; gen 2 removed in rec_op with recovery publish log: vintner publish: removed generation 2; gen 2 removed in boot2 with recovery publish log: vintner publish: removed generation 2; images [1, 2, 3] still GOOD with matching 64-char hashes at all reads (or properly removed) |
| P5 | G3 | PASS | next_generation=5; gen 4 GOOD; gen 1 still GOOD; gen 1 files gone; gen 1 not in VINTNER_ORDER; VINTNER_T_1 absent; [ 7.086171] vintner[588]: missing files for generation 1; VINTNER_ORDER=['4', '3', '2']; VINTNER_T_4=xxx; boot1 fsck clean |
| P6 | G3 | PASS | OP EXIT: 0; vintner publish: published generation 5; boot2 booted os-5; gen 5 GOOD; boot2 counter=xxx, VINTNER_BOOTED absent; removed gen 1 then gen 2 in order |
| P7 | G3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G3 | OBSERVED | [boot1] [ 7.086171] vintner[588]: missing files for generation 1; [rec_op] [ 7.160133] vintner[585]: missing files for generation 1; [rec_op] REPORT: missing files for generation 1; [rec_op] REPORT: generation 1 has BIOS counter ""; [rec_op] OPLOG: REPORT: missing files for generation 1; [rec_op] OPLOG: REPORT: generation 1 has BIOS counter "" |
| P9 | G3 | PASS | op=os-3; boot1=os-4; rec_op=os-4; boot2=os-5 |

## Findings and Observations (WO-0007b)

### 1. BIOS Arming Completion in T4 (Rule 1b)

- In case **T4** (`renamed`), power was cut after slot files were renamed into place, before the BIOS `grubenv` write or UEFI manifest save in step 11.
- **UEFI:** On Boot 1, the UKI was in place and booted. `vintner bless` marked generation 2 `GOOD` and applied rule 1b: finished the BIOS arming transition by setting `VINTNER_T_2=xxx` and prepending `2` to `VINTNER_ORDER` (`2 1`). It logged `ACTION: arm_bios generation 2`. The old rule 7 report (`generation 2 has BIOS counter ""`) was absent.
- **BIOS:** On Boot 1, `os-1` booted because `VINTNER_ORDER` still held `1`. `vintner bless` applied rule 1b: set `VINTNER_T_2=xxx`, prepended `2` to `VINTNER_ORDER` (`2 1`), logged `ACTION: arm_bios generation 2`, and armed generation 2 as `TRYING`. The old rule 6 report (`generation 2 is STAGED and in place but does not verify`) was absent. On the recovery publish, generation 3 was published (`next_generation` was 3), and Boot 2 booted `os-3`. Generation 2 remained held (`GOOD` or `TRYING`), and no `STAGED` records remained.

### 2. Restartable Garbage Collection Order (G1, G2, G3)

- Garbage collection executes removal in safe restartable order:
  1. BIOS `grubenv` write (`gc-grubenv-written`), dropping the generation from `VINTNER_ORDER` and unsetting its counter;
  2. Removal of BIOS slot directory and UKI followed by `syscall.Sync()` (`gc-files-removed`);
  3. Removal of the manifest record (`gc-record-removed`), followed by printing `vintner publish: removed generation <g>`.
- **G1 (`gc-record-removed`):** Interrupted after record removal. Generation 1 files and record are absent after op boot. No rule 3 untracked report was emitted. Recovery publish cleanly removed only generation 2.
- **G2 (`gc-grubenv-written`):** Interrupted after `grubenv` update. Generation 1 files and record remain in place. `vintner bless` reported `generation 1 has BIOS counter ""`. Recovery publish repeated collection of generation 1 and then removed generation 2, printing `vintner publish: removed generation 1` and `vintner publish: removed generation 2` in order.
- **G3 (`gc-files-removed`):** Interrupted after file deletion and sync, before record removal. Generation 1 record remains while its files are absent. `vintner bless` reported `missing files for generation 1`. Recovery publish repeated collection of generation 1 and then removed generation 2 in order.

### 3. Summary of Compliance with Work Order WO-0007b

- All 12 cases across both UEFI and BIOS match the expected outcomes in WO-0007b in every detail.
- Every check P1–P7 and P9 is **PASS** across all cases on both firmwares ($9 	imes 12 = 108$ pairs per firmware); P8 is **OBSERVED**.
- All 216 rows pass across both test suites.
