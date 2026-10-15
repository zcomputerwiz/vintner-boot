# WO-0002: BIOS end-to-end test in a real Debian 13 VM

- **For:** an agent with a local Linux environment that has root via `sudo`, KVM and loop devices with partition scanning (`losetup -P`).
- **Issued:** 2026-10-15
- **Status:** done: `ci/integration/bios-bless/`, reviewed and run in CI.
- **Base branch:** `main`. Work on branch `wo-0002` and open one pull request against `main`.

## Why

WO-0001 Part A (`ci/integration/uefi-bless/`) proved `vintner bless` and the on-demand boot storage mount (decision D4) on UEFI. The BIOS path has never run inside a real booted Debian system:

- our static GRUB (`firmware/bios/grub.cfg` and a `core.img` with every module embedded);
- the grubenv counters;
- `vintner bless` re-arming them through `src/internal/grubenv`.

The fallback chain on BIOS has also only been tested with a toy kernel (`ci/bios-trial/`): an ordinary image fails three times, and recovery A then mounts the real Debian root read-only.

## Read first

- `firmware/bios/INTERFACE.md`: grubenv variables, slot layout, the first-sector rule, and how Linux-side writers rewrite grubenv in place.
- `ci/bios-trial/run.sh`:
  - how to fetch trixie GRUB and run its tools on trixie's own `ld.so`;
  - how to build `core.img` with the early config;
  - how to embed `boot.img` and `core.img` (the Python step).
  
  Reuse these steps; do not use the host's GRUB or `grub-install`.
- `ci/integration/uefi-bless/run.sh` and its README: the structure to mirror (disk build, guest check service, three boots, results table). Note the fixes made in review: the guest check service is ordered `After=vintner-bless.service`, and on failure the script prints the boot logs.
- `recovery/BUILDING.md`: `recovery/build.sh` produces `recovery/out/vmlinuz` and `initrd.img`; `vintner.root=` and `vintner.recovery_test=1`.
- `schema/manifest.md`: BIOS image records (`bios.dir`, `bios.files` with the SHA-256 of `vmlinuz`, `initrd.img` and `slot.cfg`).

## Rules

These are the WO-0001 rules plus four lessons from its review.

- **Disks:** never write to a disk you did not create for this work order. Print `lsblk` before every `losetup` and check the target is your loop device.
- **Generated output:** commit none (images, debs, logs, keys). Put it all under `ci/out/`.
- **Scope:** do not change `src/`, `firmware/`, `schema/`, `recovery/`, `docs/architecture.md` or the ADRs. If a check finds a bug there, report it in the pull request with the evidence. Do not work around it.
- **Evidence must be copied from output,** from the guest's serial log or from the disk read back on the host. Never hardcode an evidence string or a conclusion. A check whose evidence is missing is a FAIL, not a PASS with explanatory text.
- **Scripts must be executable in git:** `git update-index --chmod=+x <file>`. Check with `git ls-files -s`.
- **Versions** in the README and the pull request come from the script's own `versions.txt`, never from memory.
- **Pull request body:** write it to a file and pass that file, so the shell does not mangle backticks. If it renders with stray control characters, fix it.

## Task

**Deliverables:** `ci/integration/bios-bless/run.sh` and `ci/integration/bios-bless/README.md`, with the method, versions and the results table from one run.

**1. Disk.** Start from the pinned Debian 13 `genericcloud` image (`ci/lib/fetch-image.sh`), converted to raw.
- **GRUB:** the image already has a BIOS Boot Partition (partition 14). Embed the trixie `core.img` there, and `boot.img` into the MBR, exactly as `ci/bios-trial/run.sh` does. The early config finds boot storage by its FAT UUID.
- **Boot storage:** append a boot storage partition (type `bc13c2ff-59e6-4262-a352-b275fd6f7172`, FAT32, about 512 MiB), as Part A does.
- **Contents of boot storage:**
  - `/grub/grub.cfg`: `firmware/bios/grub.cfg`, byte for byte.
  - `/grub/grubenv`: `VINTNER_ORDER=1`, `VINTNER_T_1=xxx`, `VINTNER_T_RA=xxx`, written with trixie `grub-editenv`.
  - `/vintner/bios/os-1/`:
    - `vmlinuz` and `initrd.img` from the image's own `/boot`;
    - `slot.cfg` with `set vintner_cmdline="root=UUID=<root fs uuid> ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=<boot storage partuuid>"`.
  - `/vintner/bios/recovery-a/` and `/vintner/bios/recovery-b/`:
    - `vmlinuz` and `initrd.img` from `recovery/build.sh`;
    - `slot.cfg` with `set vintner_cmdline="console=ttyS0 vintner.recovery_test=1 vintner.root=UUID=<root fs uuid>"`. For recovery B, add `vintner.test_slot=b` so the two can be told apart in the serial log.
  - `/vintner/state/manifest-a` and `manifest-b`, generated with a throwaway program that calls `manifest.Save`. Contents: `firmware` `bios`, `tries` 3, one image (generation 1, `TRYING`, a `bios` record with the real SHA-256 of the three slot files), and recovery records `a` and `b`. Do not commit the program.
- **Inside the root filesystem:**
  - `/usr/sbin/vintner`, a static build of `src/`;
  - `integration/systemd-units/vintner-bless.service`, enabled;
  - `systemd-bless-boot.service` masked;
  - a guest check service ordered `After=multi-user.target vintner-bless.service` that prints the checks to `ttyS0` and powers off.

**2. Boots.** Use QEMU with KVM, q35, SeaBIOS (the QEMU default, no `-bios`), the serial console to a file, and `-no-reboot`.
- **Boot 1:** normal.
- **Boot 2:** with `vintner-bless.service` masked.
- **Boots 3 to 5:** first set `VINTNER_T_1=xxx` on the host, as a good boot would, and unmask bless. Then make os-1 fail before `boot-complete.target`: in its `slot.cfg`, point `root=` at a UUID that does not exist and add `rootdelay=5 panic=5`. initramfs-tools then gives up on the root and reboots, which ends the QEMU run under `-no-reboot`. This stands in for a broken image.
- **Boot 6:** the next boot, with nothing changed.

**3. Checks.** Read grubenv back on the host after every boot with trixie `grub-editenv list`.

| ID | After | Check | Expected |
|---|---|---|---|
| C1 | boot 1 | `systemctl status vintner-bless.service` | `active (exited)`, status 0 |
| C2 | boot 1 | grubenv | `VINTNER_T_1=xxx` and no `VINTNER_BOOTED`. GRUB wrote `xx` and `BOOTED=1`, then bless re-armed it. |
| C3 | boot 1 | manifest | generation 1 `GOOD`, `first_good` and `last_good` set, `seq` one higher |
| C4 | boot 1 | `findmnt -rn -t vfat -o TARGET,SOURCE`; `grep /run/vintner /proc/self/mountinfo` | Boot storage is not mounted anywhere. Record the vfat mounts you see, such as the cloud image's ESP. |
| C5 | boot 1 | grubenv file on the host | exactly 1024 bytes, the second 512 bytes all `#` |
| C6 | boot 2 | grubenv | `VINTNER_T_1=xx`, `VINTNER_BOOTED=1` (counted, not re-armed) |
| C7 | boots 3 to 5 | grubenv after each | `VINTNER_T_1` goes `xx`, then `x`, then `0`; `VINTNER_BOOTED=1` each time. Record the serial log lines that show why each boot failed. |
| C8 | boot 6 | serial log and grubenv | GRUB skips os-1 (counter `0`) and boots recovery A (`vintner: trying recovery-a`), which prints `VINTNER-RECOVERY-TEST root=mounted ro=yes`. Afterwards `VINTNER_T_RA=xx` and `VINTNER_BOOTED=RA`. |
| C9 | all | root filesystem | Record whether the recovery boot changed the root partition (sha256 of the partition before and after boot 6). Expect unchanged. |

**4. CI.** Add a `bios-bless` row to `.github/workflows/qemu-tests.yml` in the same way as `uefi-bless`. That row needs Go, `cloud-image-utils`, `qemu-utils` and the cached image, plus `recovery/build.sh` run first. It must pass in the pull request's CI, not only locally.

## Reporting back

- **The pull request** has the results table pasted in its description, with evidence copied from the run.
- **Anything unexpected** is reported as a finding with the evidence. Examples: GRUB printing errors, a counter not changing, the recovery boot touching the root.
- **A check that cannot be run as written:** say exactly why. Do not substitute another check.
