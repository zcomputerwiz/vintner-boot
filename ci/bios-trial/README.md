# BIOS trial and fallback test

`run.sh` proves the static BIOS configuration ([`firmware/bios/grub.cfg`](../../firmware/bios/grub.cfg)) on Debian 13's own GRUB under SeaBIOS. It checks grubenv boot counting, re-arm and fallback to recovery, as defined in [`firmware/bios/INTERFACE.md`](../../firmware/bios/INTERFACE.md), architecture §4 and ADR-0003 option B.

```
ci/bios-trial/run.sh
```

**Needs:** `curl xz-utils dpkg cpio gzip fdisk mtools python3 qemu-system-x86`. No root. Uses KVM when `/dev/kvm` is writable, TCG otherwise. CI runs it in `.github/workflows/qemu-tests.yml`.

## What it does

1. **Fetches packages.** It downloads trixie `grub-pc-bin`, `grub-common`, `busybox-static`, the kernel that `linux-image-amd64` points to, and the C library and libraries GRUB's host tools need. It unpacks them without apt or root. `grub-mkimage` and `grub-editenv` run on trixie's own `ld.so`, never the host's GRUB.
2. **Builds a test initramfs.** Its `/init` prints `VINTNER-TEST-BOOTED id=<vintner.test_id>` to ttyS0, then powers off, or reboots when `vintner.test_action=reboot`.
3. **Builds a template disk.** The disk is GPT with a 2 MiB BIOS Boot Partition and a FAT32 boot storage partition. Boot storage holds `/grub/grub.cfg` and four slots: `os-1`, `os-2`, `recovery-a` and `recovery-b`.
   - `core.img` is built with `grub-mkimage`. It contains every module `grub.cfg` needs, plus an early config that finds boot storage by FAT UUID.
   - A short Python step then does what `grub-bios-setup` does for GPT: `boot.img` goes into the MBR code area and `core.img` into the BIOS Boot Partition, with the two block pointers set. `grub-bios-setup` itself cannot run on an image file, because it insists on opening the host's root device.
4. **Runs the scenarios.** Each scenario starts from a fresh copy of the template and writes `grubenv` with trixie `grub-editenv`. Each boot is one QEMU run with `-no-reboot`. A reboot by the test kernel ends that run, and the harness starts the next one.

   After a boot, the harness reads `grubenv` back with mtools. `bless` simulates the vintner bless service on the host: it sets `VINTNER_T_<VINTNER_BOOTED>=xxx` and unsets `VINTNER_BOOTED`.

## Scenarios

All scenarios start with `VINTNER_ORDER="2 1"` and three tries per counter unless stated.

| Scenario | Setup and steps | Expected |
|---|---|---|
| B1 newest boots | One boot, then bless | `os-2`. Before bless, `T_2=xx` and `BOOTED=2`. After bless, `T_2=xxx` |
| B2 failing candidate | `os-2` reboots every time, no bless | `os-2 os-2 os-2 os-1`; `T_2=0` |
| B3 good image starts failing | `os-2` boots twice with bless, then reboots every time | `os-2` ×5, then `os-1`; `T_2=0` (the case ADR-0003 option B exists for) |
| B4 all ordinary bad | `T_2=0 T_1=0` | `ra`; `T_RA=xx`, `BOOTED=RA` |
| B5 recovery A bad too | `T_2=0 T_1=0 T_RA=0` | `rb`; `grubenv` unchanged byte for byte |
| B6 corrupt grubenv | The 1024-byte block is overwritten with `?` | `ra`; `grubenv` unchanged byte for byte |
| B7 missing kernel | `os-2/vmlinuz` deleted | `os-1` in the same boot; `T_2=xx`, `T_1=xx`, `BOOTED=1` |
| B8 Shift menu | Shift held down through QMP for 20 s, then Enter | Observation only: whether the menu appears and what Enter on the first entry boots |
| B9 empty grubenv | 0-byte `grubenv`, as a power cut during `grub-editenv` can leave it | `ra`; nothing written |
| T1 torn block | Host only: the first sector of one block with the second sector of another, with a variable straddling them | Observation only: what `grub-editenv list` (the same parser as `load_env`) reads |
| T2 vintner variables in first sector | Host only: 10 six-digit generations, five tries each | The second sector holds only padding |

Results go to `ci/out/bios-trial/summary.tsv`, with one serial log per boot.

B8 sends Shift as repeated key-down events with no key-up, as a held key does. HMP `sendkey` releases the key after each press, so GRUB sampled Shift as up two times in three.

GRUB's `load_env` and Debian's `grub-editenv` share one parser (`grub-core/lib/envblk.c`). That is why T1 and T2 need no boot.

There is no random power-cut test under QEMU. A SIGKILL cannot tear a single guest write request, so random kills can only produce the sector combinations T1 already covers. Tears on real hardware remain open (architecture §12, risk 3).

## Results

One run under TCG, `qemu-system-x86_64` 8.2.2, about 3 minutes:

```
scenario	booted	final grubenv (VINTNER_*)	result
B1-newest-boots	os-2	VINTNER_ORDER=2 1;VINTNER_T_1=xxx;VINTNER_T_2=xxx;VINTNER_T_RA=xxx	PASS
B2-failing-candidate	os-2 os-2 os-2 os-1	VINTNER_BOOTED=1;VINTNER_ORDER=2 1;VINTNER_T_1=xx;VINTNER_T_2=0;VINTNER_T_RA=xxx	PASS
B3-good-image-starts-failing	os-2 os-2 os-2 os-2 os-2 os-1	VINTNER_BOOTED=1;VINTNER_ORDER=2 1;VINTNER_T_1=xx;VINTNER_T_2=0;VINTNER_T_RA=xxx	PASS
B4-all-ordinary-bad	ra	VINTNER_BOOTED=RA;VINTNER_ORDER=2 1;VINTNER_T_1=0;VINTNER_T_2=0;VINTNER_T_RA=xx	PASS
B5-recovery-a-bad	rb	VINTNER_ORDER=2 1;VINTNER_T_1=0;VINTNER_T_2=0;VINTNER_T_RA=0	PASS
B6-corrupt-grubenv	ra		PASS
B7-missing-kernel	os-1	VINTNER_BOOTED=1;VINTNER_ORDER=2 1;VINTNER_T_1=xx;VINTNER_T_2=xx;VINTNER_T_RA=xxx	PASS
B8-shift-menu (menu shown)	os-2	VINTNER_BOOTED=2;VINTNER_ORDER=2 1;VINTNER_T_1=xxx;VINTNER_T_2=xx;VINTNER_T_RA=xxx	OBSERVED
B9-empty-grubenv	ra		PASS
T1-torn-old-new	-	GEN=old;PAYLOAD=aaaa...bbbb	OBSERVED
T1-torn-new-old	-	GEN=new;PAYLOAD=bbbb...aaaa	OBSERVED
T2-vintner-variables-in-first-sector	-	449 bytes of 1024 used	PASS
```

Trixie packages: `grub-pc-bin` and `grub-common` 2.12-9+deb13u2, `linux-image-6.12.107+deb13-amd64` 6.12.107-1, `busybox-static` 1:1.37.0-6+b9.

Findings:

- **The algorithm works as written on GRUB 2.12.** That covers per-generation variable names through `eval`, the `x`-run counters through `regexp`, and continuing to the next slot when `linux` fails.
- **B6 and B9:** `load_env` fails with `error: invalid environment block.` and GRUB boots recovery A without writing.
- **B8:** Shift shows the menu with `vintner os 2`, `vintner os 1`, `vintner recovery A` and `vintner recovery B`. Enter on the first entry boots os-2 and counts it. This confirms that `menuentry` passes its extra arguments to the entry body as `$2`.
- **T1:** a torn block loads without error. A variable that straddles the sectors gets a mixed value, and nothing reports it. `firmware/bios/INTERFACE.md` therefore keeps every variable in the first sector.
- **T2:** Debian's `grub-editenv` adds a 69-byte warning line after the header, which counts against the first sector.
