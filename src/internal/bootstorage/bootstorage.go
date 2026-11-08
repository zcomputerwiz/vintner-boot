// SPDX-License-Identifier: GPL-2.0-or-later

// Package bootstorage finds the boot storage partition and mounts it on
// demand (architecture §5, decision D4). It is never in fstab.
package bootstorage

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// MountPoint is where the vintner services mount boot storage, inside their
// own mount namespace (PrivateMounts=yes).
const MountPoint = "/run/vintner/boot"

// Device returns the boot storage device that the kernel command line names
// with vintner.bootstorage=PARTUUID=<uuid>.
func Device(cmdline string) (string, error) {
	for _, f := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(f, "vintner.bootstorage=PARTUUID="); ok && v != "" {
			return "/dev/disk/by-partuuid/" + strings.ToLower(v), nil
		}
	}
	return "", errors.New("bootstorage: no vintner.bootstorage=PARTUUID= on the kernel command line")
}

// Mount mounts dev at MountPoint, read-only unless rw, and returns a function
// that unmounts it.
func Mount(dev string, rw bool) (func() error, error) {
	if err := os.MkdirAll(MountPoint, 0o700); err != nil {
		return nil, err
	}
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	if !rw {
		flags |= syscall.MS_RDONLY
	}
	if err := syscall.Mount(dev, MountPoint, "vfat", flags, "umask=0077,flush"); err != nil {
		return nil, fmt.Errorf("bootstorage: mount %s: %w", dev, err)
	}
	return func() error { return syscall.Unmount(MountPoint, 0) }, nil
}

// ESPMountPoint is where the vintner services mount the EFI System Partition,
// read-only, inside their own mount namespace.
const ESPMountPoint = "/run/vintner/esp"

// ESPDevice returns the device of the ESP with the given GPT partition UUID.
func ESPDevice(partuuid string) (string, error) {
	if partuuid == "" || strings.ContainsAny(partuuid, "/ ") {
		return "", errors.New("bootstorage: invalid ESP partuuid")
	}
	return "/dev/disk/by-partuuid/" + strings.ToLower(partuuid), nil
}

// MountESP mounts the ESP read-only at ESPMountPoint and returns a function
// that unmounts it. vintner never writes the ESP through this path.
func MountESP(dev string) (func() error, error) {
	if err := os.MkdirAll(ESPMountPoint, 0o700); err != nil {
		return nil, err
	}
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC | syscall.MS_RDONLY)
	if err := syscall.Mount(dev, ESPMountPoint, "vfat", flags, "umask=0077"); err != nil {
		return nil, fmt.Errorf("bootstorage: mount ESP %s: %w", dev, err)
	}
	return func() error { return syscall.Unmount(ESPMountPoint, 0) }, nil
}
