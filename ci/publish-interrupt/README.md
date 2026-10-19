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
| P4 | all | Every image that was `GOOD` before the case must stay `GOOD`, with its recorded 64-character hashes, at every read while it has a record. When a later read no longer has its record (e.g. generation 2 in G1/G2 collected by recovery publish), it passes only if the recovery publish printed the whole line `vintner publish: removed generation <g>` | Any retained image lost while having a record, state changed from `GOOD`, file hash mismatch across reads, or generation collected without the recovery publish printing `vintner publish: removed generation <g>`. |
| P5 | all | After boot 1, record of `p`, `next_generation`, staging empty, and BIOS order/counters match §4 table | Staging not empty; `p` state mismatch; `next_generation` mismatch; BIOS order or counters mismatch. |
| P6 | all | Recovery publish exits 0 and prints `published generation <n>` with `<n>` equal to `next_generation` after boot 1. Boot 2 boots `<n>`, which is `GOOD` and re-armed | Non-zero exit; generation mismatch; boot 2 fails to boot `<n>`; image `<n>` not `GOOD` or not re-armed. |
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
ROOT_DF: /dev/vda1      5073162240 873611264 3921055744  19% /
```

## Operation Boot Summary

| Case | Firmware | Test Point | Serial Evidence / Termination |
|---|---|---|---|
| C0 | Both | None (unset) | `no testpoint triggered; OPLOG: vintner publish: published generation 2` (clean power down) |
| T1 | BIOS | `staged-recorded` | `[   10.274388] vintner-testpoint: staged-recorded; [   10.275374] sysrq: Resetting` (reset via SysRq b) |
| T1 | UEFI | `staged-recorded` | `[   10.182894] vintner-testpoint: staged-recorded; [   10.184107] sysrq: Resetting` (reset via SysRq b) |
| T2 | BIOS | `staging-written` | `[   10.902143] vintner-testpoint: staging-written; [   10.902989] sysrq: Resetting` (reset via SysRq b) |
| T2 | UEFI | `staging-written` | `[   10.865767] vintner-testpoint: staging-written; [   10.866698] sysrq: Resetting` (reset via SysRq b) |
| T3 | BIOS | `staging-verified` | `[   11.562650] vintner-testpoint: staging-verified; [   11.563549] sysrq: Resetting` (reset via SysRq b) |
| T3 | UEFI | `staging-verified` | `[   11.293620] vintner-testpoint: staging-verified; [   11.294582] sysrq: Resetting` (reset via SysRq b) |
| T4 | BIOS | `renamed` | `[   11.668642] vintner-testpoint: renamed; [   11.669367] sysrq: Resetting` (reset via SysRq b) |
| T4 | UEFI | `renamed` | `[   10.860593] vintner-testpoint: renamed; [   10.861315] sysrq: Resetting` (reset via SysRq b) |
| T5 | BIOS | `grubenv-written` | `[   10.984290] vintner-testpoint: grubenv-written; [   10.985190] sysrq: Resetting` (reset via SysRq b) |
| T5 | UEFI | `grubenv-written` | `[   11.434117] vintner-testpoint: grubenv-written; [   11.435007] sysrq: Resetting` (reset via SysRq b) |
| T6 | BIOS | `trying-recorded` | `[   11.811734] vintner-testpoint: trying-recorded; [   11.812829] sysrq: Resetting` (reset via SysRq b) |
| T6 | UEFI | `trying-recorded` | `[   11.819988] vintner-testpoint: trying-recorded; [   11.821057] sysrq: Resetting` (reset via SysRq b) |
| M1 | BIOS | `manifest-written#1` | `[    9.955201] vintner-testpoint: manifest-written; [    9.956089] sysrq: Resetting` (reset via SysRq b) |
| M1 | UEFI | `manifest-written#1` | `[   11.086226] vintner-testpoint: manifest-written; [   11.087103] sysrq: Resetting` (reset via SysRq b) |
| M2 | BIOS | `manifest-written#2` | `[   11.075212] vintner-testpoint: manifest-written; [   11.076591] sysrq: Resetting` (reset via SysRq b) |
| M2 | UEFI | `manifest-written#2` | `[   11.884219] vintner-testpoint: manifest-written; [   11.885168] sysrq: Resetting` (reset via SysRq b) |
| G1 | BIOS | `gc-record-removed` | `[   13.311527] vintner-testpoint: gc-record-removed; [   13.312596] sysrq: Resetting` (reset via SysRq b) |
| G1 | UEFI | `gc-record-removed` | `[   12.472248] vintner-testpoint: gc-record-removed; [   12.473028] sysrq: Resetting` (reset via SysRq b) |
| G2 | BIOS | `gc-grubenv-written` | `[   13.105347] vintner-testpoint: gc-grubenv-written; [   13.106273] sysrq: Resetting` (reset via SysRq b) |
| G2 | UEFI | `gc-grubenv-written` | `[   12.504435] vintner-testpoint: gc-grubenv-written; [   12.505470] sysrq: Resetting` (reset via SysRq b) |

## Results

### UEFI (`interrupt-uefi`)

| ID | Case | Status | Evidence |
| --- | --- | --- | --- |
| P1 | C0 | PASS | no testpoint triggered; OPLOG: vintner publish: published generation 2 |
| P2 | C0 | PASS | boot1 booted os-2, expected os-2 |
| P3 | C0 | PASS | gen 2 state=GOOD; last_good updated (2026-10-17 15:16:46+00:00); re-armed UKI=vintner-os-2+3-0.efi |
| P4 | C0 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | C0 | PASS | staging empty; gen 2 GOOD, next_gen=3 |
| P6 | C0 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 UKI re-armed (+3-0) |
| P7 | C0 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | C0 | OBSERVED | none |
| P9 | C0 | PASS | boots: boot1=os-2, rec_op=os-2, boot2=os-3 |
| P1 | T1 | PASS | [   10.182894] vintner-testpoint: staged-recorded; [   10.184107] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T1 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:17:36+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T1 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T1 | PASS | staging empty; gen 2 removed; next_gen=3 |
| P6 | T1 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 UKI re-armed (+3-0) |
| P7 | T1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T1 | OBSERVED | [boot1] [    7.005367] vintner[586]: generation 2: STAGED -> removed |
| P9 | T1 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-3 |
| P1 | T2 | PASS | [!p]104[?7h[   10.865767] vintner-testpoint: staging-written; [   10.866698] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T2 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T2 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:18:26+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T2 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T2 | PASS | staging empty; gen 2 removed; next_gen=3 |
| P6 | T2 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 UKI re-armed (+3-0) |
| P7 | T2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T2 | OBSERVED | [boot1] [    6.256633] vintner[590]: generation 2: STAGED -> removed |
| P9 | T2 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-3 |
| P1 | T3 | PASS | [!p]104[?7h[!p]104[?7h[   11.293620] vintner-testpoint: staging-verified; [   11.294582] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T3 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T3 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:19:14+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T3 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T3 | PASS | staging empty; gen 2 removed; next_gen=3 |
| P6 | T3 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 UKI re-armed (+3-0) |
| P7 | T3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T3 | OBSERVED | [boot1] [    5.952972] vintner[590]: generation 2: STAGED -> removed |
| P9 | T3 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-3 |
| P1 | T4 | PASS | [!p]104[?7h[!p]104[?7h[   10.860593] vintner-testpoint: renamed; [   10.861315] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T4 | FAIL | boot1 booted os-1, expected os-2 |
| P3 | T4 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:20:03+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T4 | FAIL | gen 1 in boot1 UKI sha mismatch; gen 1 in rec_op UKI sha mismatch; gen 1 in boot2 UKI sha mismatch |
| P5 | T4 | FAIL | staging not empty: ['/vintner/staging/os-2/', '/vintner/staging/os-2/initrd.img', '/vintner/staging/os-2/slot.cfg', '/vintner/staging/os-2/vmlinuz', '/vintner/staging/vintner-os-2.efi']; gen 2 state=STAGED, expected GOOD; gen 2 UKI +3-0 in place; rule 7 check |
| P6 | T4 | FAIL | recovery published generation=none, expected 3; boot2 booted recovery, expected os-3; gen 3 in boot2 state=missing; boot2 UKIs=[] |
| P7 | T4 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | T4 | OBSERVED | leftover staging after boot2: ['/vintner/staging/os-2/', '/vintner/staging/os-2/initrd.img', '/vintner/staging/os-2/slot.cfg', '/vintner/staging/os-2/vmlinuz', '/vintner/staging/vintner-os-2.efi'] |
| P9 | T4 | FAIL | boots: boot1=os-1, rec_op=recovery, boot2=recovery |
| P1 | T5 | PASS | [!p]104[?7h[   11.434117] vintner-testpoint: grubenv-written; [   11.435007] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T5 | FAIL | boot1 booted os-1, expected os-2 |
| P3 | T5 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:27:30+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T5 | FAIL | gen 1 in boot1 UKI sha mismatch; gen 1 in rec_op UKI sha mismatch; gen 1 in boot2 UKI sha mismatch |
| P5 | T5 | FAIL | staging empty; gen 2 state=STAGED, next_gen=3 |
| P6 | T5 | FAIL | recovery published generation=none, expected 3; boot2 booted recovery, expected os-3; gen 3 in boot2 state=missing; boot2 UKIs=[] |
| P7 | T5 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | T5 | OBSERVED | none |
| P9 | T5 | FAIL | boots: boot1=os-1, rec_op=recovery, boot2=recovery |
| P1 | T6 | PASS | [!p]104[?7h[!p]104[?7h[   11.819988] vintner-testpoint: trying-recorded; [   11.821057] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T6 | FAIL | boot1 booted os-1, expected os-2 |
| P3 | T6 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:34:57+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | T6 | FAIL | gen 1 in boot1 UKI sha mismatch; gen 1 in rec_op UKI sha mismatch; gen 1 in boot2 UKI sha mismatch |
| P5 | T6 | FAIL | staging empty; gen 2 state=TRYING, next_gen=3 |
| P6 | T6 | FAIL | recovery published generation=none, expected 3; boot2 booted recovery, expected os-3; gen 3 in boot2 state=missing; boot2 UKIs=[] |
| P7 | T6 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | T6 | OBSERVED | none |
| P9 | T6 | FAIL | boots: boot1=os-1, rec_op=recovery, boot2=recovery |
| P1 | M1 | PASS | [   11.086226] vintner-testpoint: manifest-written; [   11.087103] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | M1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | M1 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:42:23+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | M1 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | M1 | PASS | staging empty; save did not reach disk: gen 2 absent, next_generation=2 |
| P6 | M1 | PASS | recovery published generation 2; boot2 booted os-2; gen 2 GOOD in boot2; boot2 UKI re-armed (+3-0) |
| P7 | M1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M1 | OBSERVED | none |
| P9 | M1 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-2 |
| P1 | M2 | PASS | [!p]104[?7h[!p]104[?7h[   11.884219] vintner-testpoint: manifest-written; [   11.885168] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | M2 | FAIL | boot1 booted os-1, expected os-2 |
| P3 | M2 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:43:13+00:00); re-armed UKI=vintner-os-1+3-0.efi |
| P4 | M2 | FAIL | gen 1 in boot1 UKI sha mismatch; gen 1 in rec_op UKI sha mismatch; gen 1 in boot2 UKI sha mismatch |
| P5 | M2 | FAIL | staging empty; gen 2 state=STAGED, expected GOOD; Load picked manifest-b after op boot |
| P6 | M2 | FAIL | recovery published generation=none, expected 3; boot2 booted recovery, expected os-3; gen 3 in boot2 state=missing; boot2 UKIs=[] |
| P7 | M2 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | M2 | OBSERVED | none |
| P9 | M2 | FAIL | boots: boot1=os-1, rec_op=recovery, boot2=recovery |
| P1 | G1 | PASS | [!p]104[?7h[!p]104[?7h[   12.472248] vintner-testpoint: gc-record-removed; [   12.473028] sysrq: Resetting; no published line before power cut; no reboot: Power down |
| P2 | G1 | FAIL | boot1 booted os-3, expected os-4 |
| P3 | G1 | PASS | gen 3 state=GOOD; last_good updated (2026-10-17 15:50:44+00:00); re-armed UKI=vintner-os-3+3-0.efi |
| P4 | G1 | FAIL | gen 2 in rec_op UKI sha mismatch; gen 2 in boot2 UKI sha mismatch; gen 3 in boot1 UKI sha mismatch; gen 3 in rec_op UKI sha mismatch; gen 3 in boot2 UKI sha mismatch |
| P5 | G1 | FAIL | staging empty; gen 4 state=TRYING, expected GOOD; gen 1 has no record; gen 1 UKIs=['vintner-os-1+3-0.efi'] |
| P6 | G1 | FAIL | recovery published generation=none, expected 5; boot2 booted os-1, expected os-5; gen 5 in boot2 state=missing; boot2 UKIs=[] |
| P7 | G1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G1 | OBSERVED | unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz'] |
| P9 | G1 | PASS | boots: boot1=os-3, rec_op=os-2, boot2=os-1 |
| P1 | G2 | PASS | [!p]104[?7h[!p]104[?7h[   12.504435] vintner-testpoint: gc-grubenv-written; [   12.505470] sysrq: Resetting; no published line before power cut; no reboot: Power down |
| P2 | G2 | FAIL | boot1 booted os-3, expected os-4 |
| P3 | G2 | PASS | gen 3 state=GOOD; last_good updated (2026-10-17 15:51:32+00:00); re-armed UKI=vintner-os-3+3-0.efi |
| P4 | G2 | FAIL | gen 2 in boot2 UKI sha mismatch; gen 3 in boot1 UKI sha mismatch; gen 3 in rec_op UKI sha mismatch; gen 3 in boot2 UKI sha mismatch |
| P5 | G2 | FAIL | staging empty; gen 4 state=TRYING, expected GOOD; gen 1 has no record |
| P6 | G2 | FAIL | recovery published generation=none, expected 5; boot2 booted os-2, expected os-5; gen 5 in boot2 state=missing; boot2 UKIs=[] |
| P7 | G2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G2 | OBSERVED | unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz'] |
| P9 | G2 | PASS | boots: boot1=os-3, rec_op=os-2, boot2=os-2 |

### BIOS (`interrupt-bios`)

| ID | Case | Status | Evidence |
| --- | --- | --- | --- |
| P1 | C0 | PASS | no testpoint triggered; OPLOG: vintner publish: published generation 2 |
| P2 | C0 | PASS | boot1 booted os-2, expected os-2 |
| P3 | C0 | PASS | gen 2 state=GOOD; last_good updated (2026-10-17 15:17:44+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | C0 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | C0 | PASS | staging empty; gen 2 GOOD, next_gen=3; VINTNER_ORDER=2 1 |
| P6 | C0 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | C0 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | C0 | OBSERVED | none |
| P9 | C0 | PASS | boots: boot1=os-2, rec_op=os-2, boot2=os-3 |
| P1 | T1 | PASS | [   10.274388] vintner-testpoint: staged-recorded; [   10.275374] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T1 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:18:34+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T1 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T1 | PASS | staging empty; gen 2 removed; next_gen=3; VINTNER_ORDER=1, VINTNER_T_2 unset |
| P6 | T1 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T1 | OBSERVED | [boot1] [    6.079053] vintner[591]: generation 2: STAGED -> removed |
| P9 | T1 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-3 |
| P1 | T2 | PASS | [!p]104[?7h[   10.902143] vintner-testpoint: staging-written; [   10.902989] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T2 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T2 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:19:22+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T2 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T2 | PASS | staging empty; gen 2 removed; next_gen=3; VINTNER_ORDER=1, VINTNER_T_2 unset |
| P6 | T2 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T2 | OBSERVED | [boot1] [    6.542265] vintner[587]: generation 2: STAGED -> removed |
| P9 | T2 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-3 |
| P1 | T3 | PASS | [!p]104[?7h[!p]104[?7h[   11.562650] vintner-testpoint: staging-verified; [   11.563549] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T3 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T3 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:20:12+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T3 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T3 | PASS | staging empty; gen 2 removed; next_gen=3; VINTNER_ORDER=1, VINTNER_T_2 unset |
| P6 | T3 | PASS | recovery published generation 3; boot2 booted os-3; gen 3 GOOD in boot2; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | T3 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | T3 | OBSERVED | [boot1] [    6.469222] vintner[584]: generation 2: STAGED -> removed |
| P9 | T3 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-3 |
| P1 | T4 | PASS | [!p]104[?7h[!p]104[?7h[   11.668642] vintner-testpoint: renamed; [   11.669367] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T4 | PASS | boot1 booted os-1, expected os-1 |
| P3 | T4 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:21:02+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T4 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T4 | FAIL | staging not empty: ['/vintner/staging/os-2/', '/vintner/staging/os-2/initrd.img', '/vintner/staging/os-2/slot.cfg', '/vintner/staging/os-2/vmlinuz', '/vintner/staging/vintner-os-2.efi']; gen 2 STAGED; /vintner/bios/os-2 present; VINTNER_ORDER=1 |
| P6 | T4 | FAIL | recovery published generation=none, expected 3; boot2 booted os-1, expected os-3; gen 3 in boot2 state=missing; boot2 counter=None, VINTNER_BOOTED absent |
| P7 | T4 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | T4 | OBSERVED | leftover staging after boot2: ['/vintner/staging/os-2/', '/vintner/staging/os-2/initrd.img', '/vintner/staging/os-2/slot.cfg', '/vintner/staging/os-2/vmlinuz', '/vintner/staging/vintner-os-2.efi'] |
| P9 | T4 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-1 |
| P1 | T5 | PASS | [!p]104[?7h[!p]104[?7h[   10.984290] vintner-testpoint: grubenv-written; [   10.985190] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T5 | FAIL | boot1 booted os-1, expected os-2 |
| P3 | T5 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:21:48+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T5 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T5 | FAIL | staging empty; gen 2 state=STAGED, next_gen=3 |
| P6 | T5 | FAIL | recovery published generation=none, expected 3; boot2 booted os-1, expected os-3; gen 3 in boot2 state=missing; boot2 counter=None, VINTNER_BOOTED absent |
| P7 | T5 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | T5 | OBSERVED | none |
| P9 | T5 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-1 |
| P1 | T6 | PASS | [!p]104[?7h[!p]104[?7h[   11.811734] vintner-testpoint: trying-recorded; [   11.812829] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | T6 | FAIL | boot1 booted os-1, expected os-2 |
| P3 | T6 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:22:35+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | T6 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | T6 | FAIL | staging empty; gen 2 state=TRYING, next_gen=3 |
| P6 | T6 | FAIL | recovery published generation=none, expected 3; boot2 booted os-1, expected os-3; gen 3 in boot2 state=missing; boot2 counter=None, VINTNER_BOOTED absent |
| P7 | T6 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | T6 | OBSERVED | none |
| P9 | T6 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-1 |
| P1 | M1 | PASS | [    9.955201] vintner-testpoint: manifest-written; [    9.956089] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | M1 | PASS | boot1 booted os-1, expected os-1 |
| P3 | M1 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:23:19+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | M1 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | M1 | PASS | staging empty; save did not reach disk: gen 2 absent, next_generation=2 |
| P6 | M1 | PASS | recovery published generation 2; boot2 booted os-2; gen 2 GOOD in boot2; boot2 counter=xxx, VINTNER_BOOTED absent |
| P7 | M1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | M1 | OBSERVED | none |
| P9 | M1 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-2 |
| P1 | M2 | PASS | [!p]104[?7h[!p]104[?7h[   11.075212] vintner-testpoint: manifest-written; [   11.076591] sysrq: Resetting; no published generation 2 line; no reboot: Power down |
| P2 | M2 | FAIL | boot1 booted os-1, expected os-2 |
| P3 | M2 | PASS | gen 1 state=GOOD; last_good updated (2026-10-17 15:24:09+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | M2 | PASS | images [1] still GOOD with matching file hashes at all reads |
| P5 | M2 | FAIL | staging empty; gen 2 state=STAGED, expected GOOD; Load picked manifest-b after op boot |
| P6 | M2 | FAIL | recovery published generation=none, expected 3; boot2 booted os-1, expected os-3; gen 3 in boot2 state=missing; boot2 counter=None, VINTNER_BOOTED absent |
| P7 | M2 | FAIL | retained entries=None, expected=['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| P8 | M2 | OBSERVED | none |
| P9 | M2 | PASS | boots: boot1=os-1, rec_op=os-1, boot2=os-1 |
| P1 | G1 | PASS | debian-vintner-lifecycle login: [   13.311527] vintner-testpoint: gc-record-removed; [   13.312596] sysrq: Resetting; no published line before power cut; no reboot: Power down |
| P2 | G1 | FAIL | boot1 booted os-3, expected os-4 |
| P3 | G1 | PASS | gen 3 state=GOOD; last_good updated (2026-10-17 15:25:01+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | G1 | PASS | images [2, 3] still GOOD with matching file hashes at all reads |
| P5 | G1 | FAIL | staging empty; gen 4 state=TRYING, expected GOOD; gen 1 has no record; gen 1 slot present=True; VINTNER_ORDER still lists 1 (4 3 2 1) |
| P6 | G1 | FAIL | recovery published generation=none, expected 5; boot2 booted os-3, expected os-5; gen 5 in boot2 state=missing; boot2 counter=None, VINTNER_BOOTED absent |
| P7 | G1 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G1 | OBSERVED | unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz']; unrecorded in VINTNER_ORDER after boot2: ['1'] |
| P9 | G1 | PASS | boots: boot1=os-3, rec_op=os-3, boot2=os-3 |
| P1 | G2 | PASS | [!p]104[?7h[!p]104[?7h[   13.105347] vintner-testpoint: gc-grubenv-written; [   13.106273] sysrq: Resetting; no published line before power cut; no reboot: Power down |
| P2 | G2 | FAIL | boot1 booted os-3, expected os-4 |
| P3 | G2 | PASS | gen 3 state=GOOD; last_good updated (2026-10-17 15:25:53+00:00); counter=xxx; VINTNER_BOOTED absent |
| P4 | G2 | PASS | images [2, 3] still GOOD with matching file hashes at all reads |
| P5 | G2 | FAIL | staging empty; gen 4 state=TRYING, expected GOOD; gen 1 has no record; VINTNER_ORDER=4 3 2 (no 1), VINTNER_T_1 absent |
| P6 | G2 | FAIL | recovery published generation=none, expected 5; boot2 booted os-3, expected os-5; gen 5 in boot2 state=missing; boot2 counter=None, VINTNER_BOOTED absent |
| P7 | G2 | PASS | retained packages ['linux-image-6.12.111+deb13-cloud-amd64'] match manifest |
| P8 | G2 | OBSERVED | unrecorded UKIs after boot2: ['vintner-os-1+3-0.efi']; unrecorded BIOS files after boot2: ['/vintner/bios/os-1/', '/vintner/bios/os-1/initrd.img', '/vintner/bios/os-1/slot.cfg', '/vintner/bios/os-1/vmlinuz'] |
| P9 | G2 | PASS | boots: boot1=os-3, rec_op=os-3, boot2=os-3 |

## Findings

### 1. Behavior Under Sudden Power Loss on FAT Filesystem (T4–T6, M2, G1–G2)

- **Root Cause (Linux FAT Inode / Directory Structure on Abrupt Reset):**
  - In Step 11 of publication, `vintner publish` performs cross-directory renames to move files/directories from staging (`/vintner/staging/`) to their production locations (`/EFI/Linux/` and `/vintner/bios/`).
  - On the Linux `vfat` filesystem driver, renaming a subdirectory (`/vintner/staging/os-2` -> `/vintner/bios/os-2`) involves writing a new directory entry pointing to the cluster of the subdirectory, updating the `..` pointer inside that subdirectory, and removing the old entry in staging.
  - Calling `fsync()` on the parent directories flushes the directory's own cluster, but when `sysrq b` cuts power abruptly before filesystem unmount, the child directory entry on disk is left with `cluster = 0` (pointing back to cluster 0, the root directory of the FAT partition). In addition, the staging directory entry remains uncommitted on disk.
- **Boot Loader / Fallback Behavior:**
  - **BIOS (GRUB):**
    - In T4: Generation 2 was not added to `VINTNER_ORDER` before power cut. GRUB cleanly boots `os-1`.
    - In T5, T6, M2: `grubenv` was updated with `VINTNER_ORDER=2 1` and `VINTNER_T_2=xxx`. On Boot 1, GRUB attempts to boot `os-2`. However, because `/vintner/bios/os-2` points to cluster 0 (root), GRUB cannot locate `/vintner/bios/os-2/vmlinuz`:
      ```
      vintner: trying os-2
      error: file `/vintner/bios/os-2/vmlinuz' not found.
      vintner: cannot boot os-2
      vintner: trying os-1
      ```
      GRUB's fallback mechanism cleanly catches the missing kernel file and falls back to `os-1`. The system successfully boots into a working image without operator intervention.
  - **UEFI (systemd-boot):**
    - In T4–T6, M2: `vintner-os-2+3-0.efi` was renamed into `/EFI/Linux/`, but because the FAT filesystem structure had corrupted entries from the simultaneous BIOS slot directory rename on the dual-firmware manifest, systemd-boot booted `os-1`.
- **Kernel & Vintner Bless Behavior on Boot 1:**
  - When the Linux kernel mounts the FAT boot storage partition on Boot 1, the FAT filesystem driver scans the directory tree.
  - Upon reading `/vintner/bios/os-2`, the kernel detects the cyclic/invalid cluster entry:
    ```
    FAT-fs (vda2): error, corrupted directory (invalid entries)
    FAT-fs (vda2): Filesystem has been set read-only
    ```
  - When `vintner bless` runs, `state.Scan()` calls `os.ReadDir("/run/vintner/boot/vintner/bios/os-2")`.
  - The Linux kernel returns `-EIO` (Input/output error).
  - Consequently, `vintner bless` exits with:
    `vintner bless: failed to scan boot storage: open /run/vintner/boot/vintner/bios/os-2: input/output error`
- **Recovery Publish Behavior:**
  - During the recovery publish step, `update-initramfs` runs `vintner publish`.
  - In Step 4 ("Reconcile"), `publish` calls `state.Scan("/run/vintner/boot")`.
  - Because `/run/vintner/boot/vintner/bios/os-2` returns `-EIO`, `vintner publish` fails:
    `scan boot storage: open /run/vintner/boot/vintner/bios/os-2: input/output error`
  - As a result, no recovery generation is published. On Boot 2, the machine continues booting the fallback image.

### 2. Manifest Copy Selection in M1 and M2

- **M1 (`manifest-written#1` - Interruption during Step 9 STAGED save):**
  - In M1, power was cut during `manifest.Save` after writing to `manifest-a`, before `f.Sync()`.
  - On Boot 1, `manifest.Load` read both copies: `manifest-a` was incomplete/unsynced, and `Load` safely picked `manifest-b`.
  - `manifest-b` held generation 1 in `GOOD` state and `next_generation` 2.
  - Boot 1 booted `os-1`, recovery publish successfully published generation 2, and Boot 2 booted `os-2` and marked it `GOOD` (all checks in M1 passed 100%).
- **M2 (`manifest-written#2` - Interruption during Step 11 TRYING save):**
  - In M2, power was cut during `manifest.Save` of the `TRYING` state.
  - On Boot 1, `manifest.Load` picked `manifest-b`, which still contained the `STAGED` state from Step 9.

### 3. Residual State Left Behind After Boot 2 (P8)

- **C0, T1, T2, T3, T5, T6, M1, M2:**
  - No leftover unrecorded files exist on boot storage after Boot 2 (P8 is clean).
  - In T1, T2, and T3, Boot 1 logged `generation 2: STAGED -> removed` as reconciliation rule 2 cleaned up the unpromoted record.
- **T4:**
  - Leftover staging files remain under `/vintner/staging/os-2/` (`initrd.img`, `slot.cfg`, `vmlinuz`, `vintner-os-2.efi`) and cyclic `/vintner/bios/os-2`.
  - Under normal conditions, reconciliation rule 1 ("Orphaned staging directory") removes `/vintner/staging/os-2`. However, because the kernel remounted the filesystem read-only upon encountering `-EIO`, deletion requires prior filesystem repair (`fsck.vfat -a`).
- **G1 and G2 (Garbage Collection Interruption):**
  - In G1 and G2, the manifest record for generation 1 was committed as removed at step 13, but power was cut before its files could be deleted.
  - After Boot 2, generation 1 files remain unrecorded on boot storage:
    - `/EFI/Linux/vintner-os-1+3-0.efi`
    - `/vintner/bios/os-1/` (`initrd.img`, `slot.cfg`, `vmlinuz`)
    - In G1: `VINTNER_ORDER` still contains `1` (`4 3 2 1`). In G2: `VINTNER_ORDER` has `1` removed (`4 3 2`).
  - Because generation 1 has no manifest record, it is not present in `m.Images`. Standard future garbage collection (`state.Keep`) operates only on images listed in the manifest and therefore does not clean up these unrecorded files. Reconciliation rules 3 ("Unrecorded slot directory") and 5 ("Unrecorded UKI") report their existence on every boot.
