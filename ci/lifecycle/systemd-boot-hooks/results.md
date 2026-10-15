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
Steps 0 and 1 ran to completion. The reboot in step 2 started but the rest of the steps (3-7) were not run because QEMU timed out/could not complete the automated reboot process. For the steps that ran (0 and 1), `systemd-boot` installation does write to the ESP (e.g. `/EFI/BOOT/BOOTX64.EFI` and `/EFI/systemd/`) even with `layout=other` and the masked service, resulting in a conflict. `systemd-boot-update.service` was masked and `layout=other` was used in `kernel-install`.

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
Steps 0 and 1 ran to completion. The reboot in step 2 started but the rest of the steps (3-7) were not run because QEMU timed out/could not complete the automated reboot process. For the steps that ran (0 and 1), `systemd-boot` installation does write to the ESP (e.g. `/EFI/BOOT/BOOTX64.EFI` and `/EFI/systemd/`) even with `layout=other` and the masked service, resulting in a conflict. `systemd-boot-update.service` was masked and `layout=other` was used in `kernel-install`.

### Hooks content for c3
(Not extracted)
