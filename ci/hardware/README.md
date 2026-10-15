# Real-hardware firmware behavior checks (WO-0001 Part B)

This directory contains the tools and instructions for verifying how real PC firmware behaves under UEFI and BIOS boot modes, comparing physical hardware against QEMU (OVMF/SeaBIOS) and VirtualBox.

## Deliverables

- `ci/hardware/make-usb.sh`: Generates bootable USB disk images (`uefi.img` and `bios.img`), safely writes them to USB media, and inspects results afterwards.
- `ci/hardware/README.md`: Instructions for human testers, test procedures, and results tables.

## Generated Images

Run the build script to generate both images under `ci/out/hardware/`:

```bash
ci/hardware/make-usb.sh build
```

| Image | Layout | Purpose |
|---|---|---|
| `uefi.img` | GPT disk with ESP (systemd-boot) and boot storage (XBOOTLDR) containing `os-2+3`, `os-1`, recovery A `+3`, and recovery B | Checks B1, B2 |
| `bios.img` | GPT disk with BIOS Boot Partition, boot storage, static GRUB (`firmware/bios/grub.cfg`), and `grubenv` initialized to scenario B1 (`VINTNER_ORDER="2 1"`, `VINTNER_T_2=xxx`, `VINTNER_T_1=xxx`, `VINTNER_T_RA=xxx`) | Checks B3, B4 |

Both images use a lightweight test initramfs built with Debian 13 trixie `busybox-static` and the trixie kernel (`6.12.107+deb13-amd64`). On real hardware, the initramfs prints a banner to the screen (`console=tty0`) displaying the booted test ID, waits 30 seconds so the tester can read and photograph the screen, and then powers off automatically.

## Writing the USB Stick

**Safety Rule:** Never write to an internal disk. Before writing, `make-usb.sh` displays `lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS` and requires typing the exact target block device name.

```bash
# To write the UEFI test image:
sudo ci/hardware/make-usb.sh write uefi /dev/sdX

# To write the BIOS test image:
sudo ci/hardware/make-usb.sh write bios /dev/sdX
```

After each boot test on hardware, read the USB stick back on the workstation:

```bash
# To inspect UEFI boot storage FAT directory:
ci/hardware/make-usb.sh inspect uefi /dev/sdX

# To inspect BIOS boot storage grubenv variables:
ci/hardware/make-usb.sh inspect bios /dev/sdX
```

---

## Test Procedures for Physical Hardware

### Checks B1 & B2: UEFI Machine
**Target:** Any 64-bit x86 UEFI PC with Secure Boot turned off in firmware setup.
1. Write `uefi.img` to the USB stick: `sudo ci/hardware/make-usb.sh write uefi /dev/sdX`.
2. Insert stick into the test PC. Power on and enter the firmware boot menu (e.g., F12, F11, F8, or Esc depending on manufacturer). Select the USB stick (UEFI mode).
3. **Plain boot (B1a):** Boot without pressing or holding any key.
   - *Observe:* Does it boot directly into `os-2` without showing a boot menu?
   - *Observe:* Does the screen display `VINTNER REAL-HARDWARE TEST BOOTED` with `Booted image test_id : os-2`?
   - Wait 30 seconds for automatic power-off.
4. **Firmware FAT Write Check (B2):**
   - Move the stick back to the workstation.
   - Run `ci/hardware/make-usb.sh inspect uefi /dev/sdX`.
   - *Expected:* `vintner-os-2+3.efi` was renamed by systemd-boot to `vintner-os-2+2-1.efi`.
5. **Key-press Menu Check (B1b):**
   - Re-write `uefi.img` or boot again holding the **Space bar** continuously from power-on.
   - *Observe:* Did holding Space open the systemd-boot menu despite `timeout 0`?
   - Test other keys (e.g., Tab, Down arrow, Enter). Record which keys successfully interrupt boot and open the menu.

---

### Checks B3 & B4: BIOS / CSM Machine
**Target:** A legacy PC BIOS machine, or a UEFI PC with CSM (Compatibility Support Module) enabled.
1. Write `bios.img` to the USB stick: `sudo ci/hardware/make-usb.sh write bios /dev/sdX`.
2. Insert stick into the test PC. Power on and select the legacy USB boot option from the boot menu.
3. **Plain boot (B3a):** Boot without pressing or holding any key.
   - *Observe:* Does GRUB load `os-2` automatically?
   - *Observe:* Does the screen display `VINTNER REAL-HARDWARE TEST BOOTED` with `Booted image test_id : os-2`?
   - Wait 30 seconds for automatic power-off.
4. **BIOS save_env Check (B4):**
   - Move the stick back to the workstation.
   - Run `ci/hardware/make-usb.sh inspect bios /dev/sdX`.
   - *Expected:* `VINTNER_T_2=xx` and `VINTNER_BOOTED=2`. This proves GRUB's `save_env` function successfully wrote back to the disk using the real machine's BIOS disk services (INT 13h).
5. **Shift Menu Check (B3b):**
   - Power on while holding the **Shift** key down.
   - *Observe:* Did holding Shift reveal the vintner GRUB menu?

---

### Check B5: Secure Boot LoadImage Failure (Optional)
> [!CAUTION]
> Run B5 only on a **spare test machine** where Secure Boot keys can be replaced and restored to factory defaults. Never run on a machine required for production or daily use.

1. Enroll test platform keys (`PK`, `KEK`, `db`) as generated in `ci/selection-policy/run.sh` scenario S12.
2. Sign `systemd-bootx64.efi` with the test key, but leave `vintner-os-2` unsigned.
3. Power on three times and record firmware reaction to the `LoadImage` security violation:
   - Does firmware advance to the next boot option, show a menu, reboot, or halt?
   - How many tries were decremented on the stick?
4. Clear test keys and restore firmware factory default keys.

---

## Results Table

| ID | Machine (Make, Model, Firmware Version) | Boot Mode | Steps Taken | Observed Behavior & Evidence | Status |
|---|---|---|---|---|---|
| B1 | QEMU (EDK II OVMF 4M `202411-1`) | UEFI (SB Off) | Plain boot, then Space held | Booted `os-2` directly on plain boot. Space key opened systemd-boot menu listing entries. | PASS |
| B2 | QEMU (EDK II OVMF 4M `202411-1`) | UEFI (SB Off) | Inspected FAT after 1 plain boot | `::/EFI/Linux/` shows `vintner-os-2+2-1.efi` (decremented tries from 3 to 2, done 0 to 1). | PASS |
| B3 | QEMU (SeaBIOS `1.16.3`) | BIOS / Legacy | Plain boot | GRUB loaded static `grub.cfg`, selected `os-2`, screen displayed `VINTNER REAL-HARDWARE TEST BOOTED id=os-2`. | PASS |
| B4 | QEMU (SeaBIOS `1.16.3`) | BIOS / Legacy | Inspected `grubenv` after 1 plain boot | `VINTNER_BOOTED=2`, `VINTNER_T_2=xx`, `VINTNER_T_1=xxx`, `VINTNER_T_RA=xxx`. Proves `save_env` written via INT 13h. | PASS |
| B3 | VirtualBox 7.2.20 (Oracle VM VirtualBox BIOS) | BIOS / Legacy | Plain boot in VirtualBox BIOS VM | GRUB booted `os-2`, screen displayed `VINTNER REAL-HARDWARE TEST BOOTED id=os-2`, auto-powered off after 30s. | PASS |
| B4 | VirtualBox 7.2.20 (Oracle VM VirtualBox BIOS) | BIOS / Legacy | Inspected cloned medium `grubenv` | `VINTNER_BOOTED=2`, `VINTNER_T_2=xx`, `VINTNER_T_1=xxx`, `VINTNER_T_RA=xxx`. BIOS disk write verified. | PASS |

### Physical Hardware Observations

*Template for recording physical test machines:*

```markdown
| ID | Machine | Firmware Version | Boot Mode | Steps | Result | Notes |
|---|---|---|---|---|---|---|
| B1 | [Vendor & Model] | [e.g. BIOS v1.15] | UEFI | Plain boot & Space | [PASS/OBSERVED] | [Menu observed / keys tested] |
| B2 | [Vendor & Model] | [e.g. BIOS v1.15] | UEFI | Read stick | [PASS/FAIL] | [vintner-os-2+2-1.efi present] |
| B3 | [Vendor & Model] | [e.g. BIOS v1.15] | Legacy/CSM | Plain boot & Shift | [PASS/OBSERVED] | [Booted os-2 / Shift menu behavior] |
| B4 | [Vendor & Model] | [e.g. BIOS v1.15] | Legacy/CSM | Read grubenv | [PASS/FAIL] | [T_2=xx, BOOTED=2] |
```
