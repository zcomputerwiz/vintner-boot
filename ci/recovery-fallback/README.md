# Broken root recovery fallback test (WO-0004)

Verifies that deliberate corruption of a Debian root partition on both UEFI and BIOS firmwares leads to Recovery A after exactly the configured 3 failed boots, and verifies that Recovery A leaves the root partition completely unaltered (architecture §4, §9, WO-0004).

## Methodology

1. **Base disk:** Built once per firmware with `ci/lib/vintner-base.sh`, with stock Debian bootloader packages purged (Operation P0). Boot storage contains the Debian trixie kernel/initrd and manifest.
2. **UEFI Recovery UKIs:** The production recovery UKIs lack `vintner.recovery_test=1`. On UEFI, the test replaces `/EFI/Linux/vintner-recovery-a+3-0.efi` and `/EFI/Linux/vintner-recovery-b.efi` on boot storage with UKIs built by trixie `ukify` from `recovery/out/vmlinuz` and `recovery/out/initrd.img`:
   - Recovery A: `console=tty0 console=ttyS0,115200 vintner.recovery=1 vintner.recovery_test=1`
   - Recovery B: `console=tty0 console=ttyS0,115200 vintner.recovery=1 vintner.recovery_test=1 vintner.test_slot=b`
   - No `vintner.root=` is supplied: recovery discovers the root filesystem UUID from the active manifest on boot storage.
3. **BIOS Recovery:** The base disk's `slot.cfg` for `recovery-a` and `recovery-b` already contains `vintner.recovery_test=1`.
4. **Case isolation:** For each scenario (`G0`, `F1`, `F2`, `F3`, `F4`), a sparse copy of `base.raw` (`cp --sparse=always`) is modified on the host and booted up to 5 times. The disk copy is deleted upon scenario completion.
5. **Boot cycle:** Each boot runs under QEMU with KVM, serial output logged to file, and `-no-reboot`. Boots terminate either when the guest powers off or when killed at the 120-second timeout.
6. **Stopping rule:** Boots stop after the first boot that runs an image other than `os-1` (Boot 4).
7. **Integrity check:** The root partition SHA-256 hash is computed immediately before Boot 4 and immediately after Boot 4.

## Cases and Failure Definitions

| ID | Case | Host modification | What os-1 boots should do | What makes the check FAIL |
|---|---|---|---|---|
| **D1** | G0 | None (control) | Boot normally, `vintner-bless.service` active, counter re-armed (`+3-0` / `VINTNER_T_1=xxx`). | Any boot running recovery, failing to reach active bless state, counter not re-armed, or not completing 5 boots. |
| **D2** | F1–F4 | F1: zero primary superblock (byte 1024)<br>F2: remove `/usr/lib/systemd/systemd` and `/sbin/init`<br>F3: append missing device to `/etc/fstab`<br>F4: install failing `vintner-test-health.service` | Boots 1–3 run os-1. Counter decrements:<br>UEFI: `+2-1`, `+1-2`, `+0-3`<br>BIOS: `xx`, `x`, `0` with `VINTNER_BOOTED=1`. | Any boot running an image other than os-1, counter sequence mismatching expected degradation, or missing serial failure evidence. |
| **D3** | F1–F4 | As above | Boot 4 runs Recovery A (not Recovery B). Counter after Boot 4:<br>UEFI: `vintner-recovery-a+2-1.efi`<br>BIOS: `VINTNER_T_RA=xx`, `VINTNER_BOOTED=RA`. | Boot 4 running os-1, running Recovery B, failing to boot, or counter not decremented to `+2-1` / `T_RA=xx`. |
| **D4** | F1–F4 | As above | Boot 4 prints `VINTNER-RECOVERY-TEST`.<br>F2, F3, F4: `root=mounted ro=yes`.<br>F1: `root=failed ro=no` or `root=absent ro=no` (OBSERVED). | For F2–F4: missing report or root not mounted read-only. |
| **D5** | F1–F4 | As above | Root partition SHA-256 identical before and after Boot 4. | SHA-256 hash mismatch after Recovery A runs. |
| **D6** | F1–F4 | As above | How boots 1–3 ended (OBSERVED): powered off or killed at 120s timeout. | N/A (OBSERVED). |

## Versions

Taken from `versions.txt`:

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

=== Purged in P0 / Conflicting packages ===
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
```

## Results: UEFI (`fallback-uefi`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| D1 | G0 | PASS | 5 boots: os-1, blessed (vintner-bless.service active); counter re-armed each time (UEFI +3-0); recovery never booted |
| D2 | F1 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: Gave up waiting for root file system device.  Common problems: |
| D2 | F2 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: Target filesystem doesn't have requested /sbin/init. |
| D2 | F3 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: [ TIME ] Timed out waiting for device dev-d…000000-0000-0000-0000-000000000000. |
| D2 | F4 | PASS | os-1 boots 1-3; counters: B1: +2-1, B2: +1-2, B3: +0-3; failure: none; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check. |
| D3 | F1 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D3 | F2 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D3 | F3 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D3 | F4 | PASS | boot 4 ran Recovery A (not B); counter after: vintner-recovery-a+2-1.efi |
| D4 | F1 | OBSERVED | VINTNER-RECOVERY-TEST root=failed ro=no logs=/dev/vda2:/vintner/recovery-logs/20261016T150753 |
| D4 | F2 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T151425 |
| D4 | F3 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T152058 |
| D4 | F4 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T152150 |
| D5 | F1 | PASS | unchanged (sha256=1771ed33d66923e20fb15c29302b45ad181437f1e8c8a77305fdc69506a0b905) |
| D5 | F2 | PASS | unchanged (sha256=27dfa1c5a1fe86633b41574d0c33b411f3a2f900ca4849f285661cdafd6c7a66) |
| D5 | F3 | PASS | unchanged (sha256=efbcc77d4a4734280ecfe6b1303171524b391e738fe99bea15be0d98db403fbe) |
| D5 | F4 | PASS | unchanged (sha256=13dee58078cdbfe949ddb75aab037d61c5139ddaab1a66cb31c8d8fbcd962dde) |
| D6 | F1 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F2 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F3 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F4 | OBSERVED | B1: powered off; B2: powered off; B3: powered off |

## Results: BIOS (`fallback-bios`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| D1 | G0 | PASS | 5 boots: os-1, blessed (vintner-bless.service active); counter re-armed each time (VINTNER_T_1=xxx, no VINTNER_BOOTED); recovery never booted |
| D2 | F1 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: Gave up waiting for root file system device.  Common problems: |
| D2 | F2 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: Target filesystem doesn't have requested /sbin/init. |
| D2 | F3 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: [ TIME ] Timed out waiting for device dev-d…000000-0000-0000-0000-000000000000. |
| D2 | F4 | PASS | os-1 boots 1-3; counters: B1: T_1=xx,BOOTED=1; B2: T_1=x,BOOTED=1; B3: T_1=0,BOOTED=1; failure: none; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check. |
| D3 | F1 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D3 | F2 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D3 | F3 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D3 | F4 | PASS | boot 4 ran Recovery A (not B); grubenv after: VINTNER_T_RA=xx, VINTNER_BOOTED=RA |
| D4 | F1 | OBSERVED | VINTNER-RECOVERY-TEST root=failed ro=no logs=/dev/vda2:/vintner/recovery-logs/20261016T150753 |
| D4 | F2 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T151426 |
| D4 | F3 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T152058 |
| D4 | F4 | PASS | VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261016T152149 |
| D5 | F1 | PASS | unchanged (sha256=6cd9c426445a1dca2bb4b32628ce332f3ff79d050765fc9ff305c1cb1eca8d67) |
| D5 | F2 | PASS | unchanged (sha256=7b065d29ae149c640d15f53743e73cd8db256d153075ec70ecb298a6994182dc) |
| D5 | F3 | PASS | unchanged (sha256=2c1f2a1a83ffec5ac9ab9637c57a0c75eaff7fd8cb21c9c88bfea8f40aec7ff4) |
| D5 | F4 | PASS | unchanged (sha256=57c94d04a69d836b1523d9470c5e745d8f21aeae046a6dcaa7b08bac20a469a6) |
| D6 | F1 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F2 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F3 | OBSERVED | B1: killed at timeout; B2: killed at timeout; B3: killed at timeout |
| D6 | F4 | OBSERVED | B1: powered off; B2: powered off; B3: powered off |
