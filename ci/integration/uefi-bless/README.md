# UEFI end-to-end blessing test in Debian 13

`run.sh` proves decision D4 (on-demand boot storage mounting, [architecture §5](../../docs/architecture.md)) and `vintner bless` ([architecture §4](../../docs/architecture.md), [ADR-0003](../../docs/adr/0003-uefi-entry-naming-and-boot-counting.md)) inside a real Debian 13 (Trixie) cloud image booted through OVMF 4M and systemd-boot.

It exercises:
1. Mounting boot storage on demand by partition UUID (`vintner.bootstorage=PARTUUID=...`).
2. Execution of `vintner-bless.service` under systemd after `boot-complete.target`.
3. FAT re-arming of the booted UKI from `+2-1` back to `+3-0` on real kernel `vfat`.
4. Manifest state transitions (`TRYING` -> `GOOD`, updating `first_good`/`last_good`, advancing `seq` across alternating copies `manifest-a` and `manifest-b`).
5. Absence of boot storage mounts in the host mount namespace (`findmnt -t vfat`).
6. Behavior of `systemd-gpt-auto-generator` regarding the boot storage partition type.
7. Verification that `vintner reconcile --dry-run` runs cleanly without side effects.
8. Boot counter decrementing without re-arming when `vintner-bless.service` is masked (`+3-0` -> `+2-1`).
9. Failure mode when `vintner.bootstorage=` is omitted from the kernel command line: clear error message, failure to re-arm (`+2-1` -> `+1-2`), and manifest left untouched.

```bash
ci/integration/uefi-bless/run.sh
```

**Requirements:** Root via `sudo`, KVM (or TCG fallback), Debian 13 packages (`systemd-boot-efi`, `systemd-ukify`), host tools (`qemu-system-x86`, `ovmf`, `qemu-utils`, `mtools`, `dosfstools`, `e2fsprogs`, `cloud-image-utils`, `golang-go`, `python3`, `shellcheck`, `gdisk`). Output goes to `ci/out/integration/uefi-bless/`.

## What it does

1. **Fetches packages:** Downloads trixie `systemd-boot-efi` and `systemd-ukify` from `deb.debian.org` and unpacks them into `ci/out/integration/uefi-bless/root/` without apt or root.
2. **Builds static vintner binary:** Compiles `src/cmd/vintner` statically (`CGO_ENABLED=0`) as `/usr/sbin/vintner`.
3. **Builds disk image:**
   - Starts from `debian-13-genericcloud-amd64-20261001-2618.qcow2` cached in `ci/out/cache/` (pinned in `ci/lib/fetch-image.sh`).
   - Converts to raw and extends by 1 GiB.
   - Appends a 512 MiB FAT32 boot storage partition with type GUID `bc13c2ff-59e6-4262-a352-b275fd6f7172`.
   - Populates ESP with trixie `systemd-bootx64.efi` (as `\EFI\BOOT\BOOTX64.EFI` and `\EFI\vintner\systemd-bootx64.efi`) and `firmware/uefi/loader.conf`.
4. **Configures guest root filesystem:**
   - Prints `lsblk` device safety table before creating loop devices.
   - Mounts guest rootfs, copies static `/usr/sbin/vintner`, installs `integration/systemd-units/vintner-bless.service` into `/usr/lib/systemd/system/`, enables it via `/etc/systemd/system/basic.target.wants/`, masks `systemd-bless-boot.service` and `systemd-networkd-wait-online.service`.
   - Confirms `/etc/fstab` does not contain boot storage.
   - Installs a guest check runner unit (`vintner-guest-checks.service`) that logs check evidence to the serial console and powers off.
5. **Builds UKI & Initial Manifest:**
   - Builds UKI `vintner-os-1+3-0.efi` with trixie `ukify` using guest kernel `vmlinuz` and `initrd.img`, with cmdline `root=UUID=<uuid> ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=<uuid>`.
   - Places UKI at `/EFI/Linux/vintner-os-1+3-0.efi` on boot storage.
   - Generates initial manifests (`manifest-a` seq 1 `TRYING`, `manifest-b` seq 2 `TRYING`) using a throwaway Go program calling `manifest.Save`.
6. **Executes three VM boots:**
   - **Boot 1 (bless enabled):** systemd-boot decrements UKI to `+2-1`. On boot, `vintner-bless.service` mounts boot storage, validates state, re-arms UKI to `+3-0`, and writes `manifest-a` (seq 3 `GOOD`). Checks A1–A6 evaluate.
   - **Boot 2 (bless masked):** `vintner-bless.service` is masked by linking to `/dev/null`. systemd-boot decrements UKI from `+3-0` to `+2-1`. Bless does not run; UKI remains `+2-1`. Check A7 evaluates.
   - **Boot 3 (missing bootstorage parameter):** `vintner-bless.service` is unmasked, and the UKI is rebuilt without `vintner.bootstorage=`. systemd-boot decrements UKI from `+2-1` to `+1-2`. `vintner bless` fails with a descriptive error naming the missing parameter. UKI is not re-armed (`+1-2`), and manifest remains unchanged (`seq 3`). Check A8 evaluates.

## Package versions

- `systemd-boot-efi`: `257.13-1~deb13u1`
- `systemd-ukify`: `257.13-1~deb13u1`
- Guest kernel: `6.12.111+deb13-cloud-amd64` (from `debian-13-genericcloud-amd64-20261001-2618.qcow2`)

## Results (2026-10-15, QEMU KVM)

| ID | Check | Expected | Status | Evidence |
|---|---|---|---|---|
| A1 | `systemctl status vintner-bless.service` after first boot | `active (exited)`, exit status 0 | PASS | `Active: active (exited) since Thu 2026-10-15 17:02:27 UTC; 253ms ago; Process: 607 ExecStart=/usr/sbin/vintner bless (code=exited, status=0/SUCCESS);` |
| A2 | File name on boot storage after first boot | `vintner-os-1+3-0.efi` | PASS | `Files in ::/EFI/Linux: vintner-os-1+3-0.efi` |
| A3 | Manifest after first boot | generation 1 GOOD, first_good/last_good set, seq+1 | PASS | `manifest-a seq=3 (state=GOOD, first_good set), manifest-b seq=2 (TRYING)` |
| A4 | Boot storage mounts visible on running system | Only ESP mounted; boot storage not in host namespace | PASS | `findmnt showed only /boot/efi; /run/vintner not in mountinfo` |
| A5 | What `systemd-gpt-auto-generator` did | OBSERVED (skips XBOOTLDR because /boot populated) | OBSERVED | `-.mount; boot-efi.mount; (skipped boot storage XBOOTLDR since /boot populated)` |
| A6 | `sudo vintner reconcile --dry-run` | Exit 0, no report lines, unmounted afterwards | PASS | `vintner reconcile --dry-run exited 0 with no action or report lines` |
| A7 | Second boot with `vintner-bless.service` masked | `vintner-os-1+2-1.efi` (boot counted, not rearmed) | PASS | `Files in ::/EFI/Linux: vintner-os-1+2-1.efi (decremented by systemd-boot to +2-1, not rearmed)` |
| A8 | Third boot with unmasked bless but missing `vintner.bootstorage=` | Bless fails naming parameter; file +1-2; manifest unchanged | PASS | `Bless failed naming missing parameter; file decremented to +1-2; manifest unchanged` |
