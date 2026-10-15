# Boot Writers Locator

This script (`find-boot-writers.sh`) is used to find every Debian 13 (trixie) package whose maintainer scripts or kernel/initramfs hooks can write a bootloader, boot entries, NVRAM, or regenerate the initramfs.

It downloads the indexes and scans them for potential packages matching specific paths or having dependencies on specific boot-related packages. It then downloads these packages and statically analyzes their maintainer scripts and hooks for the relevant command invocations.

## How to Run

To regenerate the TSV report locally, run:

```bash
bash ci/lifecycle/find-boot-writers.sh
```

The output will be produced exactly as `ci/lifecycle/boot-writers-trixie.tsv`.

## Environment Checksums

The report was generated against the Debian `trixie` suite Release file dated:
**Sat, 12 Sep 2026 07:55:41 UTC**

The SHA256 values of the relevant indexes used are:
- `main/Contents-amd64.gz`: `3d3e6930908db0b53f780dd7397b9589e27db88b59205c331a76d76d6726e419`
- `main/binary-amd64/Packages.xz`: `7778d3e3f303b7ddb8ce0fe7c8d57473a076c6bf2e8f241f75421d2396352498`
- `contrib/Contents-amd64.gz`: `47874ed85dd50f13fc4123b2237380cb769e654c0e8c38ff3af9125c8f5200e3`
- `contrib/binary-amd64/Packages.xz`: `926e365110c83a7bc974c2700a82cbd833a4ce1ea6b4134c97b6aba85e5ecb84`
- `non-free/Contents-amd64.gz`: `39a9afe8fd412e2d4ee935c7322eb6a4d975f8d589d5a1b5d1c1dca39c1d877c`
- `non-free/binary-amd64/Packages.xz`: `873a1152a9641bbbd32ae06d9bea40c0eb9486caeea340a786d4ab84cb6de6c5`
- `non-free-firmware/Contents-amd64.gz`: `2d55195f1bbb4cd56b8e7d91f3c4fb1ffb47a20e041319ee55272468d54b1b4b`
- `non-free-firmware/binary-amd64/Packages.xz`: `a05bfaf35b64eb8271e374f617b3b75b5ba24dacfa86fd7ddbec5a60d99a17da`


## Summary Table

| Package | Commands | Locations | Categories |
|---|---|---|---|
| amd64-microcode | update-initramfs | maintainer script | regenerates initramfs only |
| aoetools | update-initramfs | maintainer script | regenerates initramfs only |
| azure-vm-utils | update-initramfs | maintainer script | regenerates initramfs only |
| bcache-tools | update-initramfs | maintainer script | regenerates initramfs only |
| bilibop-lockfs | update-initramfs | maintainer script | regenerates initramfs only |
| bilibop-rules | update-initramfs | maintainer script | regenerates initramfs only |
| bootcd | update-initramfs | maintainer script | regenerates initramfs only |
| brltty | update-initramfs | maintainer script | regenerates initramfs only |
| btrfs-progs | update-initramfs | maintainer script | regenerates initramfs only |
| busybox | update-initramfs | maintainer script | regenerates initramfs only |
| busybox-static | update-initramfs | maintainer script | regenerates initramfs only |
| clevis-dracut | update-initramfs | maintainer script | regenerates initramfs only |
| clevis-initramfs | update-initramfs | maintainer script | regenerates initramfs only |
| cloud-initramfs-dyn-netconf | update-initramfs | maintainer script | regenerates initramfs only |
| cloud-initramfs-growroot | update-initramfs | maintainer script | regenerates initramfs only |
| cloud-initramfs-rescuevol | update-initramfs | maintainer script | regenerates initramfs only |
| cryptsetup-initramfs | update-initramfs | maintainer script | regenerates initramfs only |
| cryptsetup-nuke-password | update-initramfs | maintainer script | regenerates initramfs only |
| dmsetup | update-initramfs | maintainer script | regenerates initramfs only |
| dracut | dracut, update-initramfs | maintainer script | regenerates initramfs only |
| dracut-core | dracut | hook, maintainer script | regenerates initramfs only |
| dropbear-initramfs | update-initramfs | maintainer script | regenerates initramfs only |
| fuse3 | update-initramfs | maintainer script | regenerates initramfs only |
| glx-alternative-nvidia | update-initramfs | maintainer script | regenerates initramfs only |
| grml-rescueboot | update-grub | maintainer script | writes boot config |
| grub-cloud-amd64 | grub-install, update-grub | maintainer script | writes loader/NVRAM, writes boot config |
| grub-common | grub-mkconfig | hook | writes boot config |
| grub-coreboot | grub-install, update-grub | maintainer script | writes loader/NVRAM, writes boot config |
| grub-efi-amd64 | grub-install, update-grub | maintainer script | writes loader/NVRAM, writes boot config |
| grub-efi-ia32 | grub-install, update-grub | maintainer script | writes loader/NVRAM, writes boot config |
| grub-ieee1275 | grub-install, update-grub | maintainer script | writes loader/NVRAM, writes boot config |
| grub-ipxe | update-grub | maintainer script | writes boot config |
| grub-pc | grub-install, update-grub | maintainer script | writes loader/NVRAM, writes boot config |
| grub-xen | grub-install, update-grub | maintainer script | writes loader/NVRAM, writes boot config |
| grub2-common | update-grub | hook | writes boot config |
| initramfs-tools | update-initramfs | maintainer script | regenerates initramfs only |
| intel-microcode | update-initramfs | maintainer script | regenerates initramfs only |
| iscsiuio | update-initramfs | maintainer script | regenerates initramfs only |
| kdump-tools | update-grub, update-initramfs | hook, maintainer script | writes boot config, regenerates initramfs only |
| klibc-utils | update-initramfs | maintainer script | regenerates initramfs only |
| kmod | update-initramfs | maintainer script | regenerates initramfs only |
| kxc | update-initramfs | maintainer script | regenerates initramfs only |
| live-boot-initramfs-tools | update-initramfs | maintainer script | regenerates initramfs only |
| live-tools | update-initramfs | maintainer script | regenerates initramfs only |
| lvm2 | update-initramfs | maintainer script | regenerates initramfs only |
| mandos-client | dracut, update-initramfs | hook, maintainer script | regenerates initramfs only |
| mdadm | update-grub, update-initramfs | maintainer script | writes boot config, regenerates initramfs only |
| memtest86+ | update-grub | maintainer script | writes boot config |
| miniramfs | update-initramfs | maintainer script | regenerates initramfs only |
| multipath-tools-boot | update-initramfs | maintainer script | regenerates initramfs only |
| nbd-client | update-initramfs | maintainer script | regenerates initramfs only |
| ntfs-3g | update-initramfs | maintainer script | regenerates initramfs only |
| open-iscsi | update-initramfs | maintainer script | regenerates initramfs only |
| osk-sdl | update-initramfs | maintainer script | regenerates initramfs only |
| ostree-boot | update-initramfs | maintainer script | regenerates initramfs only |
| overlayroot | update-initramfs | maintainer script | regenerates initramfs only |
| plymouth | update-initramfs | maintainer script | regenerates initramfs only |
| qcom-phone-utils | update-initramfs | maintainer script | regenerates initramfs only |
| refind | efibootmgr | maintainer script | writes loader/NVRAM |
| reiserfsprogs | update-initramfs | maintainer script | regenerates initramfs only |
| sg3-utils-udev | update-initramfs | maintainer script | regenerates initramfs only |
| shim-signed | grub-install | maintainer script | writes loader/NVRAM |
| systemd | kernel-install | hook | writes boot config |
| systemd-boot | bootctl, efibootmgr, kernel-install | hook, maintainer script | writes loader/NVRAM, writes boot config |
| tboot | grub-mkconfig | hook | writes boot config |
| thin-provisioning-tools | update-initramfs | maintainer script | regenerates initramfs only |
| toybox | update-initramfs | maintainer script | regenerates initramfs only |
| udev | update-initramfs | maintainer script | regenerates initramfs only |
| unl0kr | update-initramfs | maintainer script | regenerates initramfs only |
| v86d | update-initramfs | maintainer script | regenerates initramfs only |
| xfsprogs | update-initramfs | maintainer script | regenerates initramfs only |
| yubikey-luks | update-initramfs | maintainer script | regenerates initramfs only |
| zfs-initramfs | update-initramfs | maintainer script | regenerates initramfs only |
