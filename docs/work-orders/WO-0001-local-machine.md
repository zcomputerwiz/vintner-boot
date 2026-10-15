# WO-0001: End-to-end and real-hardware checks on a local machine

- **For:** an agent with a local Linux workstation (root via `sudo`, KVM, loop devices, a kernel with `vfat` and device-mapper). Part B also needs a person at real hardware.
- **Issued:** 2026-10-15
- **Base branch:** `main`. Work on a new branch per part (`wo-0001-a`, `wo-0001-b`) and open one pull request per part against `main`.

## Why this work order exists

Everything so far was tested in QEMU, either in a cloud container without KVM, root mounts or device-mapper, or in GitHub CI. Three things could not be checked there:

1. **The on-demand boot storage mount (decision D4) has never run on a real kernel.** `src/internal/bootstorage.Mount` failed in the container with `no such device`, because that kernel has no `vfat`.
2. **`vintner bless` has never run inside a booted Debian system.** It has run against temporary directories in unit tests, but never under systemd, after `boot-complete.target`, on a FAT partition that systemd-boot just renamed a file on.
3. **Firmware behaviour on real PCs** may differ from OVMF and SeaBIOS: the hidden menu key, FAT renames by the firmware, and GRUB on a real BIOS.

## Read first

- `docs/architecture.md`: §4 "Health and blessing", §5 "Mounting boot storage", §16 decision D4.
- `docs/adr/0003-uefi-entry-naming-and-boot-counting.md`: entry names, counters, re-arm (option B), findings 1 to 8.
- `schema/manifest.md`: manifest file format (two copies, line 1 compact JSON, line 2 its SHA-256) and `schema/examples/manifest-uefi.json`.
- `integration/systemd-units/vintner-bless.service`, `src/cmd/vintner/bless.go`, `src/internal/bootstorage/`.
- `ci/selection-policy/run.sh` and `ci/bios-trial/run.sh`: how existing tests fetch Debian trixie packages without apt, build UKIs with trixie `ukify`, build GPT disks and boot QEMU. Reuse their patterns.

## Rules

- **Never write to a disk you did not create for this work order.** Before any `dd`, `sfdisk`, `mkfs` or `losetup` against a block device, print `lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS` and check that the target is the loop device or USB stick you mean. Do not touch the machine's internal disks or its firmware settings unless Part B says so.
- **Commit no generated output:** no disk images, `.deb` files, logs or keys. CI rejects paths under `ci/out/` and files ending in `.key` or `.fd`. Put everything generated under `ci/out/`.
- **Take Debian packages from deb.debian.org trixie,** as the existing scripts do. Never use the host's systemd-boot, GRUB or kernel.
- **Scripts:** bash, `shellcheck`-clean, and runnable again from a clean checkout. Where root is needed, say so at the top of the script and use `sudo` only for those steps.
- **Do not change** `src/`, `firmware/`, `schema/`, `docs/architecture.md` or the ADRs. If a test finds a bug there, describe it in the pull request with the evidence, and stop that part rather than working around it.

## Part A: UEFI end-to-end test in a real Debian 13 VM (agent only)

**Goal:** prove decision D4 and `vintner bless` in a real Debian 13 system that boots through systemd-boot and a vintner UKI. "Done" means a script that does all of this and a results table from one run.

**Deliverables:**
- `ci/integration/uefi-bless/run.sh`, which builds the disk, boots it and checks the results.
- `ci/integration/uefi-bless/README.md`, with method, package versions and the results table.

**1. Build the disk** from the Debian 13 `genericcloud` amd64 image, pinned in `ci/lib/fetch-image.sh`, converted to raw.
- **Boot storage partition:** add one of type `bc13c2ff-59e6-4262-a352-b275fd6f7172`, FAT32, about 512 MiB. You may grow the image first.
- **ESP:** replace its loader with trixie `systemd-bootx64.efi`, as both `\EFI\BOOT\BOOTX64.EFI` and `\EFI\vintner\systemd-bootx64.efi`. Copy `firmware/uefi/loader.conf` to `\loader\loader.conf`.
- **The UKI:**
  - Build it with trixie `ukify` and stub from the image's own `/boot/vmlinuz-*` and `/boot/initrd.img-*`.
  - Command line: `root=UUID=<root fs uuid> ro console=ttyS0,115200 vintner.bootstorage=PARTUUID=<boot storage partuuid>`.
  - `.osrel` per ADR-0003: `IMAGE_ID=vintner-os`, `IMAGE_VERSION=1`.
  - Place it on boot storage as `/EFI/Linux/vintner-os-1+3-0.efi`.
- **The manifest:** write `/vintner/state/manifest-a` and `manifest-b` on boot storage in the format of `schema/manifest.md`, with one image, generation 1, state `TRYING`, `firmware` `uefi`, `tries` 3. Build it from `schema/examples/manifest-uefi.json`, and generate the files with a throwaway program that calls `manifest.Save` rather than writing them by hand. Do not commit that program.
- **Inside the root filesystem:**
  - install a static build of `vintner` (`cd src && CGO_ENABLED=0 go build -o vintner ./cmd/vintner`) as `/usr/sbin/vintner`;
  - install `integration/systemd-units/vintner-bless.service` and enable it;
  - mask `systemd-bless-boot.service`;
  - leave `/etc/fstab` alone. Boot storage must not appear in it.

**2. Boot it** with QEMU, KVM, q35, the OVMF 4M firmware (Secure Boot off) and a serial console. Use cloud-init NoCloud, or an equivalent, to get a root shell and run the checks. Then reboot once and check again.

**3. Checks**

Report each as PASS, FAIL or OBSERVED, with the evidence:

| ID | Check | Expected |
|---|---|---|
| A1 | `systemctl status vintner-bless.service` after the first boot | `active (exited)`, exit status 0 |
| A2 | File name on boot storage after the first boot (inspect from the host after power-off, with mtools) | `vintner-os-1+3-0.efi`. systemd-boot renamed it to `+2-1`; bless re-armed it. |
| A3 | Manifest after the first boot | generation 1 `GOOD`, `first_good` and `last_good` set, `seq` one higher than before the boot, written to the other copy |
| A4 | Boot storage mounts visible on the running system: `findmnt -t vfat`, `grep /run/vintner /proc/self/mountinfo` | Only the ESP, at whatever path the cloud image's fstab uses. Boot storage is never mounted in the host namespace. |
| A5 | What `systemd-gpt-auto-generator` did: `ls /run/systemd/generator*/`, `journalctl -b -o short-monotonic \| grep -i gpt-auto` | OBSERVED. Record any `boot.mount`, `efi.mount` or automount units it generated. The expectation is that it skips XBOOTLDR because `/boot` is populated. |
| A6 | `sudo vintner reconcile --dry-run` on the running system | Exit 0, no report lines. It mounts read-only and unmounts: no mount left afterwards. |
| A7 | The second boot, with `vintner-bless.service` masked first | Afterwards the file is `vintner-os-1+2-1.efi`, because systemd-boot counted the boot and nothing re-armed it |
| A8 | After A7, unmask `vintner-bless.service`, remove `vintner.bootstorage=` from the UKI command line and boot | Bless fails with a clear error naming the missing parameter. systemd-boot's count is not re-armed (`+2-1` becomes `+1-2`), and the manifest is unchanged. This is the failure mode to document, not a bug. |

## Part B: Real hardware, with a person present

**Goal:** see how real firmware behaves where QEMU stands in today. The agent builds USB stick images and writes the instructions. A person boots them and reports what the screen showed. The agent records the results.

**Deliverables:**
- `ci/hardware/make-usb.sh`, which writes the images under `ci/out/hardware/`.
- `ci/hardware/README.md`, with the steps for the person and a results table: one row per machine, giving make, model, firmware version and boot mode.

**Test kernel:** reuse the test UKI and initramfs approach of `ci/selection-policy/run.sh`. On real hardware the initramfs must print on the screen (`console=tty0`), wait 30 seconds and then power off, so the person can read it.

**Images:**

| Image | Layout | Purpose |
|---|---|---|
| `uefi.img` | Same as selection-policy scenario S1, with `os-2+3`, `os-1`, recovery A `+3` and recovery B | B1, B2 |
| `bios.img` | The template disk of `ci/bios-trial/run.sh`, with `grubenv` set as in scenario B1 | B3, B4 |

**Writing the stick:** `dd` to a USB stick only after the person confirms the device name from `lsblk`. Every check below reads the stick back afterwards on the workstation, with mtools.

**Checks:**

| ID | Machine | Steps | Record |
|---|---|---|---|
| B1 | UEFI PC, Secure Boot off, booted from the stick through the firmware boot menu | Boot without touching anything. Then boot again holding the space bar from power-on. | Did it boot `os-2` without showing a menu? Did holding space show the systemd-boot menu? Which keys worked (space, any key, none)? |
| B2 | Same | After one plain boot, read the stick | Did the firmware's FAT write work? Expect `vintner-os-2+2-1.efi`. |
| B3 | BIOS PC, or UEFI with CSM, legacy boot from the stick | Boot without touching anything. Then boot holding Shift. | Did GRUB boot `os-2` (screen text)? Did Shift show the vintner menu? |
| B4 | Same | After one plain boot, read `grub/grubenv` from the stick with `grub-editenv list` (trixie binary, as in `ci/bios-trial/run.sh`) | Expect `VINTNER_T_2=xx` and `VINTNER_BOOTED=2`. This proves `save_env` works through the real BIOS disk services. |
| B5 (optional) | A **spare** UEFI PC whose Secure Boot keys may be replaced and restored to factory defaults | Enroll a test key as in `ci/selection-policy/run.sh` (S12): signed systemd-boot, unsigned os-2. Boot three times. | What did the firmware do after systemd-boot returned the `LoadImage` error: next boot option, a menu, a reboot, or a hang? How many tries did each power-on use (read the stick)? Then restore the factory keys. **Never do B5 on a machine someone depends on.** |

Report at least one UEFI machine and one BIOS or CSM machine. More machines, from different vendors, are better.

## Reporting back

- **One pull request per part,** with the results tables pasted into its description.
- **Note anything unexpected**, such as a firmware that renames the boot entry or a key that did not work, as a finding with the evidence, even if the check "passed".
- **If a check cannot be run as written,** say exactly why in the pull request. Do not substitute a different check without saying so.
