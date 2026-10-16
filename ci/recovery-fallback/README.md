# Broken-root recovery fallback test (WO-0004)

Breaks a Debian root in four ways and checks, on UEFI and on BIOS, that the machine tries os-1 exactly three times, then boots recovery A, and that recovery A leaves the root partition byte-identical (architecture §4 "Health and blessing", §9 phase 1).

```
ci/recovery-fallback/run.sh <uefi|bios>
```

CI runs it as `fallback-uefi` and `fallback-bios` in `.github/workflows/qemu-tests.yml`.

## Method

1. **Base disk:** built once per run by `ci/lib/vintner-base.sh`, with the stock loaders already purged (P0). See `ci/lifecycle/packages/README.md` for what it contains and for the report service that powers the guest off once the boot finishes.
2. **Recovery UKIs (UEFI only):** the base disk's recovery UKIs do not report and power off, so each case's disk gets UKIs built by trixie `ukify` from `recovery/out/vmlinuz` and `recovery/out/initrd.img`, under the base disk's file names (`vintner-recovery-a+3-0.efi`, `vintner-recovery-b.efi`):
   - A: `console=tty0 console=ttyS0,115200 vintner.recovery=1 vintner.recovery_test=1`
   - B: the same plus `vintner.test_slot=b`

   Neither has `vintner.root=`: recovery has to find the root from the manifest on boot storage. On BIOS the base disk's recovery `slot.cfg` already carries `vintner.recovery_test=1` and `vintner.root=`.
3. **Cases:** each case boots a sparse copy of `base.raw`, changed on the host first and deleted afterwards.

   | Case | Change made on the host |
   |---|---|
   | G0 | none (control) |
   | F1 | 1024 zero bytes at byte 1024 of the root partition (the primary superblock) |
   | F2 | `/usr/lib/systemd/systemd` and `/sbin/init` removed |
   | F3 | `/dev/disk/by-uuid/00000000-0000-0000-0000-000000000000 /mnt/missing ext4 defaults 0 2` appended to `/etc/fstab` |
   | F4 | `vintner-test-health.service` (`Type=oneshot`, `ExecStart=/bin/false`, `Before=boot-complete.target`, `RequiredBy=boot-complete.target`) installed and enabled |

4. **Boots:** up to 5 per case under KVM, with `-no-reboot` and the serial console to a file. A boot ends when the guest powers off or, after 120 seconds, when QEMU is killed: a power cut. The case stops after the first boot that is not os-1. In F1–F4 the root partition's sha256 is read on the host before every boot and again after the first boot that is not os-1.
5. **Which image booted:** from the kernel's `Command line:`, compared word by word.
   - os-1: has `vintner.bootstorage=` and no `vintner.recovery…` word.
   - UEFI recovery A: has `vintner.recovery=1` and no `vintner.test_slot=b`.
   - BIOS recovery A: has `vintner.recovery_test=1` and no `vintner.test_slot=b`, after GRUB's `vintner: trying recovery-a`. The BIOS `slot.cfg` has no `vintner.recovery=1` (finding 6).
6. **Counters:** read on the host after every boot: the os-1 and recovery A UKI names on boot storage (UEFI), or `grub-editenv list` (BIOS).

## Checks

| ID | Check | PASS when | Fails on |
|---|---|---|---|
| D1 | G0, every boot | 5 boots, all os-1, report shows `BLESS_ACTIVE: active`, counter re-armed each time (UEFI `+3-0`; BIOS `VINTNER_T_1=xxx` and no `VINTNER_BOOTED`) | any other image, bless not active, any other counter, fewer than 5 boots |
| D2 | F1–F4, boots 1–3 | os-1 each time; counter after each UEFI `+2-1`, `+1-2`, `+0-3`, BIOS `T_1` `xx`, `x`, `0` with `VINTNER_BOOTED=1`; the case's failure line on every one of the three boots | any other image or counter, or a boot without its failure line |
| D3 | F1–F4, boot 4 | recovery A, not B; recovery A's counter then UEFI `+2-1`, BIOS `VINTNER_T_RA=xx` and `VINTNER_BOOTED=RA` | any other image or counter |
| D4 | F1–F4, boot 4's report | F2–F4: `root=mounted ro=yes`, compared whole. On UEFI also the line `root from the manifest on boot storage: UUID=<the base root's UUID>`, compared to the end of the UUID. F1 is OBSERVED. | other fields, no report, or (UEFI) no such manifest line, F1 included |
| D5 | F1–F4 | root partition sha256 equal before and after boot 4 | any difference |
| D6 | F1–F4, boots 1–3 | OBSERVED: powered off, or killed at the timeout | — |

Failure lines for D2:

| Case | Line |
|---|---|
| F1 | initramfs-tools' `Gave up waiting for root file system device`, or the mount error |
| F2 | initramfs-tools' `Target filesystem doesn't have requested`, or the kernel's `No working init found`; whether a `Kernel panic` line follows is recorded |
| F3 | `You are in emergency mode` |
| F4 | `Failed to start vintner-test-health.service` and `Dependency failed for boot-complete.target` |

The script exits 0 when every row that can PASS does.

## Versions

From the run's `versions.txt`:

- Host, fetched from trixie: `systemd-boot-efi` and `systemd-ukify` `257.13-1~deb13u1`; `grub-pc-bin` and `grub-common` `2.12-9+deb13u2`.
- Guest (os-1): kernel `6.12.111+deb13-cloud-amd64` (`linux-image-cloud-amd64` `6.12.111-1`), `systemd` and `udev` `257.13-1~deb13u1`.
- Recovery: `linux-image-6.12.107+deb13-amd64` `6.12.107-1`, `busybox-static` `1:1.37.0-6+b9`.

## Results

GitHub Actions, KVM, 2026-10-16: run 38951457063 on commit `b723618`. Copied from each job's `results.tsv`.

### UEFI (`run.sh uefi`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| D1 | G0 | PASS | 5 boots: os-1, blessed (vintner-bless.service active); counter re-armed each time (UEFI +3-0); recovery never booted |
| D2 | F1 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: Gave up waiting for root file system device.  Common problems: |
| D2 | F2 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: Target filesystem doesn't have requested /sbin/init.; kernel panic: none |
| D2 | F3 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: You are in emergency mode. After logging in, type "journalctl -xb" to view |
| D2 | F4 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check. |
| D3 | F1 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D3 | F2 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D3 | F3 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D3 | F4 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D4 | F1 | OBSERVED | root from the manifest on boot storage: UUID=4e9f54f3-865c-43fa-9b87-69ad69d4dd9a; VINTNER-RECOVERY-TEST root=failed ro=no logs=/dev/vda2:/vintner/recovery-logs/20261016T190807 |
| D4 | F2 | PASS | root from the manifest on boot storage: UUID=4e9f54f3-865c-43fa-9b87-69ad69d4dd9a; VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T191437 |
| D4 | F3 | PASS | root from the manifest on boot storage: UUID=4e9f54f3-865c-43fa-9b87-69ad69d4dd9a; VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T192107 |
| D4 | F4 | PASS | root from the manifest on boot storage: UUID=4e9f54f3-865c-43fa-9b87-69ad69d4dd9a; VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T192200 |
| D5 | F1 | PASS | unchanged (sha256=59a534bc178e391358253420c5d0e5a663f2092ea6d2c10aeb4afc93880edfb5) |
| D5 | F2 | PASS | unchanged (sha256=97e7c8322888a48305188c783011671f0e55583bb1847162589486bafaac8c2d) |
| D5 | F3 | PASS | unchanged (sha256=063c5906f6a339b5eb49623ea79d1893b6ea96e0907769c3145a0b5984fdbd18) |
| D5 | F4 | PASS | unchanged (sha256=16ff7ae7fb1957131f8c18e0155eee71ef4dd235c6114a16fee8d699b6327ea0) |
| D6 | F1 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F2 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F3 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F4 | OBSERVED | B1: powered off; B2: powered off; B3: powered off |

### BIOS (`run.sh bios`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| D1 | G0 | PASS | 5 boots: os-1, blessed (vintner-bless.service active); counter re-armed each time (VINTNER_T_1=xxx, no VINTNER_BOOTED); recovery never booted |
| D2 | F1 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: Gave up waiting for root file system device.  Common problems: |
| D2 | F2 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: Target filesystem doesn't have requested /sbin/init.; kernel panic: none |
| D2 | F3 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: You are in emergency mode. After logging in, type "journalctl -xb" to view |
| D2 | F4 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check. |
| D3 | F1 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D3 | F2 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D3 | F3 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D3 | F4 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D4 | F1 | OBSERVED | VINTNER-RECOVERY-TEST root=failed ro=no logs=/dev/vda2:/vintner/recovery-logs/20261016T190754 |
| D4 | F2 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T191422 |
| D4 | F3 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T192049 |
| D4 | F4 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T192134 |
| D5 | F1 | PASS | unchanged (sha256=17792062b6aea7b0ecf663aea42d5cb2bbc7979d0ae2cfdd753c3e2314a3bc7c) |
| D5 | F2 | PASS | unchanged (sha256=b90f69e0602f7433bfc2f951d225f3b7a22f0ae7ffdc246d8a9cd9e5fefd4fe2) |
| D5 | F3 | PASS | unchanged (sha256=7fbb24ebde11ac7e6b27b1342820f417203eb541bd33a20cab882b7a3b071664) |
| D5 | F4 | PASS | unchanged (sha256=40bafb97a1e9e7b80b1e6c3d4df36a2f5330400935e099ef30ad6c3b9fc3677f) |
| D6 | F1 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F2 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F3 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F4 | OBSERVED | B1: powered off; B2: powered off; B3: powered off |

## Findings

1. **Recovery A on boot 4, every time, on both firmwares.** In all four broken cases os-1 was tried exactly three times, its counter went `+2-1`, `+1-2`, `+0-3` (UEFI) or `xx`, `x`, `0` (BIOS), and boot 4 was recovery A; recovery B never ran. The control G0 booted os-1 five times and was re-armed after each boot.
2. **A hang counts the same as a clean failure.** F1, F2 and F3 never finished booting and were killed at 120 seconds; F4 reached multi-user and powered off normally. The counter fell identically in both, because systemd-boot and GRUB spend the try before the kernel starts and only a blessed boot restores it.
3. **UEFI recovery finds the root from the manifest.** With no `vintner.root=` on its command line, recovery named the base root's UUID from the manifest in all four cases, mounted it read-only in F2–F4, and reported `root=failed` in F1, where the manifest still names the root but its superblock is gone.
4. **Recovery leaves the root untouched.** The root partition's sha256 was the same before and after recovery A in every case on both firmwares, F1's damaged file system included.
5. **A missing init does not panic on Debian (F2).** The work order expected the kernel's `No working init found` panic. Instead initramfs-tools printed `Target filesystem doesn't have requested /sbin/init.` and no kernel panic followed; the boot hung until the timeout. Its init script falls back to the first of `/sbin/init`, `/etc/init`, `/bin/init` and `/bin/sh` that exists, so with `/sbin/init` removed it reaches `/bin/sh` rather than the kernel's panic.
6. **The BIOS recovery command line has no `vintner.recovery=1`.** The base disk's recovery `slot.cfg` passes `vintner.recovery_test=1` and `vintner.root=` only, so the work order's rule for recognising recovery A ("has `vintner.recovery=1`") holds on UEFI alone. BIOS recovery A is recognised by GRUB's `vintner: trying recovery-a` and `vintner.recovery_test=1`.
7. **systemd shortens status lines to 80 columns on the serial console.** A unit with a `Description=` is printed as `name - description`, and a long line is cut in the middle, so the unit name can be lost: in this PR's first run F3's line read `Timed out waiting for device dev-d…000000-0000-0000-0000-000000000000.`, and F4's failed unit, then with a description, could not be found. The F4 test unit therefore has no `Description=`; a health check meant to be found in serial logs needs a short description or none.
