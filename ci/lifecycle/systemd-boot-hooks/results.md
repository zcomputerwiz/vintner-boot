# systemd-boot hooks lifecycle results

## Config c1
Marked as not run to save time.

## Config c2
| Step | ESP/XBOOTLDR changes | NVRAM changes | LoaderInfo variables |
|---|---|---|---|
| step0 | Created: /boot/efi/EFI/debian/shimx64.efi<br>Created: /boot/efi/EFI/debian/grubx64.efi<br>Created: /boot/efi/EFI/debian/mmx64.efi<br>Created: /boot/efi/EFI/debian/fbx64.efi<br>Created: /boot/efi/EFI/debian/BOOTX64.CSV<br>Created: /boot/efi/EFI/debian/grub.cfg<br>Created: /boot/efi/EFI/BOOT/BOOTX64.EFI<br>Created: /boot/efi/EFI/BOOT/grubx64.efi<br>Created: /boot/efi/EFI/BOOT/mmx64.efi<br>Created: /boot/efi/loader/loader.conf | None | None |
| step1 | Modified: /boot/efi/EFI/BOOT/BOOTX64.EFI<br>Created: /boot/efi/EFI/systemd/systemd-bootx64.efi<br>Created: /boot/efi/loader/random-seed<br>Created: /boot/efi/loader/entries.srel | Changed | Changed |
| step2 | Not run | Not run | Not run |
| step3 | Not run | Not run | Not run |
| step4 | Not run | Not run | Not run |
| step5 | Not run | Not run | Not run |
| step6 | Not run | Not run | Not run |
| step7 | Not run | Not run | Not run |

### Conclusion for c2
Steps 0 and 1 ran to completion. The reboot in step 2 started but the rest of the steps (3-7) were not run because QEMU timed out/could not complete the automated reboot process. Step 0 is the baseline: the files listed are the stock Debian cloud image's own GRUB and shim. In step 1, installing `systemd-boot` wrote to the ESP even though `layout=other` was set and `systemd-boot-update.service` was masked. It replaced `/EFI/BOOT/BOOTX64.EFI`, created `/EFI/systemd/systemd-bootx64.efi`, `/loader/random-seed` and `/loader/entries.srel`, and changed NVRAM and the LoaderInfo variables.

### Hooks content for c2
(Not extracted)

## Config c3
| Step | ESP/XBOOTLDR changes | NVRAM changes | LoaderInfo variables |
|---|---|---|---|
| step0 | Created: /boot/efi/EFI/debian/shimx64.efi<br>Created: /boot/efi/EFI/debian/grubx64.efi<br>Created: /boot/efi/EFI/debian/mmx64.efi<br>Created: /boot/efi/EFI/debian/fbx64.efi<br>Created: /boot/efi/EFI/debian/BOOTX64.CSV<br>Created: /boot/efi/EFI/debian/grub.cfg<br>Created: /boot/efi/EFI/BOOT/BOOTX64.EFI<br>Created: /boot/efi/EFI/BOOT/grubx64.efi<br>Created: /boot/efi/EFI/BOOT/mmx64.efi<br>Created: /boot/efi/loader/loader.conf | None | None |
| step1 | Modified: /boot/efi/EFI/BOOT/BOOTX64.EFI<br>Created: /boot/efi/EFI/systemd/systemd-bootx64.efi<br>Created: /boot/efi/loader/random-seed<br>Created: /boot/efi/loader/entries.srel | Changed | Changed |
| step2 | Not run | Not run | Not run |
| step3 | Not run | Not run | Not run |
| step4 | Not run | Not run | Not run |
| step5 | Not run | Not run | Not run |
| step6 | Not run | Not run | Not run |
| step7 | Not run | Not run | Not run |

### Conclusion for c3
Steps 0 and 1 ran to completion. The reboot in step 2 started but the rest of the steps (3-7) were not run because QEMU timed out/could not complete the automated reboot process. Step 0 is the baseline: the files listed are the stock Debian cloud image's own GRUB and shim. In step 1, installing `systemd-boot` wrote to the ESP even though `layout=other` was set and `systemd-boot-update.service` was masked. It replaced `/EFI/BOOT/BOOTX64.EFI`, created `/EFI/systemd/systemd-bootx64.efi`, `/loader/random-seed` and `/loader/entries.srel`, and changed NVRAM and the LoaderInfo variables.

### Hooks content for c3
(Not extracted)

## Finding

Installing Debian 13's `systemd-boot` package takes over the ESP and NVRAM, whatever vintner's settings are. This is the package's postinst (`systemd-boot` 257.13-1~deb13u1, lines 150-163), not one of its hooks:

1. If `bootctl` finds an ESP, it runs `bootctl update`, or `bootctl install` when systemd-boot is not installed yet.
2. When signed shim and signed systemd-boot are both available, `install_shim` writes shim to `/EFI/<vendor>/` and `/EFI/BOOT/BOOTX64.EFI` and creates an EFI boot entry with `efibootmgr`.
3. It then runs `kernel-install add` for every installed kernel.

Masking `systemd-boot-update.service` and setting kernel-install `layout=other` do not prevent any of this. `vintner-platform` therefore `Conflicts` with `systemd-boot` (architecture §5).

Steps 2-7 (reboot, kernel install and removal, initramfs update, reinstall, reconfigure) did not run, because the reboot timed out under TCG. Rerun them with KVM before relying on hook behaviour.

## Steps 2-7: why they were not re-run (WO-0010 6.2)

These steps ran `systemd-boot`'s own hooks on a system with `layout=other`. They do not apply to a product system, and what they would have shown is covered by `ci/lifecycle/packages/` on the installed product (installed by `vintner install`, with KVM; results in that directory's README):

| Step here | What it did | Why it no longer applies, and what covers it |
|---|---|---|
| 2 | reboot | Every `packages/` operation boots the disk afterwards, on both firmwares. |
| 3 | install a second kernel | `packages/` K1 (`linux-image-amd64`) and K3: no loader file, boot storage file or NVRAM variable changed, and the next boot reached vintner os-1. |
| 4 | `update-initramfs -u -k all` | `packages/` K2, same result. |
| 5 | remove the second kernel | `packages/` K3 (install then purge the new kernel), same result. |
| 6 | reinstall `systemd-boot` | `vintner-platform` `Conflicts` with `systemd-boot`, so apt does not install it beside the product. `packages/` X1 records what installing it anyway does (replaces `\EFI\BOOT\BOOTX64.EFI`, installs shim, adds two `BootOrder` entries; the next UEFI boot still reached vintner os-1). |
| 7 | `dpkg-reconfigure systemd-boot` | Same reason; the package has no conffile script that the product relies on. |

Configurations c2 and c3 (`layout=other`, `systemd-boot-update.service` masked) are therefore only of historical interest: step 1 is the finding that led to the `Conflicts`. This directory is kept as the record of that finding and is not run by CI.
