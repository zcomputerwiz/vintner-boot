# BIOS end-to-end blessing and recovery test in Debian 13

`run.sh` proves vintner's BIOS boot path, static GRUB (`firmware/bios/grub.cfg`), grubenv boot counting and re-arming via `vintner bless` ([architecture §4](../../docs/architecture.md), [ADR-0003](../../docs/adr/0003-uefi-entry-naming-and-boot-counting.md)), the 512-byte first sector rule ([firmware/bios/INTERFACE.md](../../firmware/bios/INTERFACE.md)), and automatic fallback to recovery A when an ordinary image fails 3 times, inside a real Debian 13 (Trixie) cloud image booted through SeaBIOS (WO-0002).

It exercises:
1. Embedding Debian trixie's GRUB (`core.img` in the BIOS Boot Partition and `boot.img` in MBR code area) with early configuration discovering boot storage by FAT UUID.
2. Static GRUB configuration with all modules pre-embedded, requiring no disk module loads.
3. Execution of `vintner-bless.service` under systemd after `boot-complete.target`.
4. Re-arming the booted ordinary generation in `grubenv` (`VINTNER_T_1` from `xx` back to `xxx`, clearing `VINTNER_BOOTED`).
5. Manifest state transitions (`TRYING` -> `GOOD`, updating `first_good`/`last_good`, advancing `seq` across alternating copies `manifest-a` and `manifest-b`).
6. Absence of boot storage mounts in the host mount namespace (`findmnt -rn -t vfat`, `grep /run/vintner /proc/self/mountinfo`).
7. Compliance with the 512-byte first sector rule: the 1024-byte `grubenv` file keeps all variable assignments within the first 512 bytes, with bytes 512..1023 padded with `#`.
8. Boot counter decrementing without re-arming when `vintner-bless.service` is masked (`xxx` -> `xx`).
9. Degradation sequence across repeated boot failures: `VINTNER_T_1` decrements from `xx` -> `x` -> `0`, simulating an image failing to mount its root (`rootdelay=5 panic=5`).
10. Automatic recovery fallback on Boot 6: GRUB skips the bad generation (`0`), transitions to recovery A, decrements `VINTNER_T_RA` from `xxx` to `xx`, records `VINTNER_BOOTED=RA`, boots the recovery kernel and initramfs, mounts the real Debian root read-only (`ro,noload`), and powers off leaving the root filesystem byte-identical.

```bash
ci/integration/bios-bless/run.sh
```

**Requirements:** Root via `sudo` (for loop mounts and partition inspection), KVM (or TCG fallback), Debian 13 packages (`grub-pc-bin`, `grub-common`), host tools (`qemu-system-x86`, `qemu-utils`, `mtools`, `sfdisk`, `cloud-image-utils`, `golang-go`, `python3`, `shellcheck`). Output goes to `ci/out/integration/bios-bless/`.

## What it does

1. **Fetches packages:** Downloads trixie `grub-pc-bin`, `grub-common`, `libc6`, and library dependencies from `deb.debian.org` and unpacks them into `ci/out/integration/bios-bless/root/` without apt or root. Records package versions in `ci/out/integration/bios-bless/versions.txt`.
2. **Builds static vintner binary:** Compiles `src/cmd/vintner` statically (`CGO_ENABLED=0`) as `/usr/sbin/vintner`.
3. **Builds recovery image:** Produces `recovery/out/vmlinuz` and `recovery/out/initrd.img` via `recovery/build.sh`.
4. **Builds disk image:**
   - Starts from `debian-13-genericcloud-amd64-20261001-2618.qcow2` cached in `ci/out/cache/` (pinned in `ci/lib/fetch-image.sh`).
   - Converts to raw and extends by 1 GiB.
   - Appends a 512 MiB FAT32 boot storage partition with type GUID `bc13c2ff-59e6-4262-a352-b275fd6f7172`.
   - Generates early configuration searching for boot storage by FAT UUID, builds `core.img` with `grub-mkimage` embedding all necessary modules, and embeds `boot.img` and `core.img` into MBR and BIOS Boot Partition (partition 14, LBA 2048) as done in `ci/bios-trial/run.sh`.
   - Populates boot storage with `firmware/bios/grub.cfg`, initial `grubenv` (`VINTNER_ORDER=1`, `VINTNER_T_1=xxx`, `VINTNER_T_RA=xxx`), recovery slots (`recovery-a`, `recovery-b`), and ordinary image slot `os-1`.
5. **Configures guest root filesystem:**
   - Prints `lsblk` device safety table before creating loop devices.
   - Mounts guest rootfs, copies static `/usr/sbin/vintner`, installs `integration/systemd-units/vintner-bless.service`, enables it in `basic.target.wants/`, masks `systemd-bless-boot.service` and `systemd-networkd-wait-online.service`.
   - Confirms `/etc/fstab` does not contain boot storage.
   - Installs a guest check runner unit (`vintner-guest-checks.service`) ordered `After=multi-user.target vintner-bless.service` that logs check evidence to serial console and powers off.
6. **Generates initial manifests:**
   - Computes SHA256 of `vmlinuz`, `initrd.img`, and `slot.cfg` for `os-1`, `recovery-a`, and `recovery-b`.
   - Generates initial manifests (`manifest-a` seq 1 `TRYING`, `manifest-b` seq 2 `TRYING`) with a throwaway Go program calling `manifest.Save`.
7. **Executes six VM boots under SeaBIOS:**
   - **Boot 1 (normal, bless enabled):** GRUB decrements `VINTNER_T_1` to `xx` and sets `VINTNER_BOOTED=1`. After `boot-complete.target`, `vintner-bless.service` runs, mounts boot storage on demand, re-arms `VINTNER_T_1=xxx`, unsets `VINTNER_BOOTED`, and updates manifest to `GOOD` (`seq 3`). Evaluates checks C1–C5.
   - **Boot 2 (bless masked):** `vintner-bless.service` is masked. GRUB decrements `VINTNER_T_1` to `xx` and sets `VINTNER_BOOTED=1`. Bless does not run; grubenv retains `T_1=xx` and `BOOTED=1`. Evaluates check C6.
   - **Boots 3 to 5 (broken image degradation):** Host resets `VINTNER_T_1=xxx` and unmasks bless. Modifies `os-1/slot.cfg` to point root to non-existent UUID with `rootdelay=5 panic=5`. Across Boots 3, 4, and 5, initramfs kernel panic causes SeaBIOS to exit under `-no-reboot`. `VINTNER_T_1` transitions `xx` -> `x` -> `0`, with `VINTNER_BOOTED=1` each time. Evaluates check C7.
   - **Boot 6 (recovery fallback):** GRUB sees `VINTNER_T_1=0` (bad) and skips os-1. GRUB selects recovery A, decrements `VINTNER_T_RA` from `xxx` to `xx`, sets `VINTNER_BOOTED=RA`, and boots recovery A kernel and initramfs. Recovery mounts the real root read-only (`ro,noload`), outputs `VINTNER-RECOVERY-TEST root=mounted ro=yes`, and shuts down. Evaluates check C8 and confirms root filesystem SHA256 is unchanged (C9).

## Package versions

Taken directly from `versions.txt`:

- `grub-pc-bin`: `2.12-9+deb13u2`
- `grub-common`: `2.12-9+deb13u2`
- `libc6`: `2.41-12+deb13u4`
- `liblzma5`: `5.8.1-1+deb13u1`
- `libdevmapper1.02.1`: `2:1.02.205-2`
- `libselinux1`: `3.8.1-1`
- `libudev1`: `257.13-1~deb13u1`
- `libpcre2-8-0`: `10.46-1~deb13u2`
- `libcap2`: `1:2.75-10+deb13u1+b3`
- Guest kernel: `6.12.111+deb13-cloud-amd64` (from `debian-13-genericcloud-amd64-20261001-2618.qcow2`)
- Recovery kernel: `6.12.107+deb13-amd64` (from `recovery/build.sh`)

## Results (2026-10-15, QEMU KVM)

| ID | After | Check | Expected | Status | Evidence |
|---|---|---|---|---|---|
| C1 | boot 1 | `systemctl status vintner-bless.service` | `active (exited)`, status 0 | PASS | `Active: active (exited) since Thu 2026-10-15 18:29:47 UTC; 184ms ago;    Process: 584 ExecStart=/usr/sbin/vintner bless (code=exited, status=0/SUCCESS)` |
| C2 | boot 1 | grubenv | `VINTNER_T_1=xxx` and no `VINTNER_BOOTED` (re-armed) | PASS | `VINTNER_ORDER=1;VINTNER_T_1=xxx;VINTNER_T_RA=xxx` |
| C3 | boot 1 | manifest | generation 1 `GOOD`, `first_good`/`last_good` set, `seq+1` | PASS | `manifest-a seq=3 state=GOOD first_good=2026-10-15T18:29:47Z; manifest-b seq=2` |
| C4 | boot 1 | `findmnt -rn -t vfat; grep /run/vintner mountinfo` | Boot storage not mounted anywhere | PASS | `vfat mounts: /boot/efi /dev/vda15; /run/vintner mounts: none` |
| C5 | boot 1 | grubenv file on host | exactly 1024 bytes, second 512 bytes all `#` | PASS | `size=1024 bytes; bytes 512..1023 all '#' padding` |
| C6 | boot 2 | grubenv | `VINTNER_T_1=xx`, `VINTNER_BOOTED=1` (counted, not rearmed) | PASS | `VINTNER_BOOTED=1;VINTNER_ORDER=1;VINTNER_T_1=xx;VINTNER_T_RA=xxx` |
| C7 | boots 3 to 5 | grubenv after each | `VINTNER_T_1` goes `xx`, then `x`, then `0`; `BOOTED=1` | PASS | `B3: VINTNER_BOOTED=1;VINTNER_T_1=xx (Gave up waiting for root file system device.  Common problems:); B4: VINTNER_BOOTED=1;VINTNER_T_1=x (Gave up waiting for root file system device.  Common problems:); B5: VINTNER_BOOTED=1;VINTNER_T_1=0 (Gave up waiting for root file system device.  Common problems:)` |
| C8 | boot 6 | serial log and grubenv | Boots recovery A, `root=mounted ro=yes`; `T_RA=xx`, `BOOTED=RA` | PASS | `vintner: trying recovery-a; VINTNER-RECOVERY-TEST root=mounted ro=yes logs=/dev/vda2:/vintner/recovery-logs/20261015T184350; grubenv: VINTNER_BOOTED=RA;VINTNER_ORDER=1;VINTNER_T_1=0;VINTNER_T_RA=xx` |
| C9 | all | root filesystem | Recovery boot left root partition unchanged (sha256) | PASS | `before: 64db0d8510bf081b72f81316d3f29333f541b466ea7492b8dd655a6ea2608383; after: 64db0d8510bf081b72f81316d3f29333f541b466ea7492b8dd655a6ea2608383 (byte-identical)` |
