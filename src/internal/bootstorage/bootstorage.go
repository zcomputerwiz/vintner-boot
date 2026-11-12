// SPDX-License-Identifier: GPL-2.0-or-later

// Package bootstorage finds the boot storage partition and mounts it on
// demand (architecture §5, decision D4). It is never in fstab.
package bootstorage

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	var err error
	if rw {
		err = mountRetry(dev, MountPoint, "vfat", flags, "umask=0077,flush")
	} else {
		err = mountReadOnly(dev, MountPoint, flags, "umask=0077,flush")
	}
	if err != nil {
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
	if err := mountReadOnly(dev, ESPMountPoint, flags, "umask=0077"); err != nil {
		return nil, fmt.Errorf("bootstorage: mount ESP %s: %w", dev, err)
	}
	return func() error { return syscall.Unmount(ESPMountPoint, 0) }, nil
}

// MountESPWritable mounts the ESP read-write at ESPMountPoint, for the one
// command that edits loader.conf (vintner menu-timeout), and returns a function
// that unmounts it.
func MountESPWritable(dev string) (func() error, error) {
	if err := os.MkdirAll(ESPMountPoint, 0o700); err != nil {
		return nil, err
	}
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	if err := mountRetry(dev, ESPMountPoint, "vfat", flags, "umask=0077,flush"); err != nil {
		return nil, fmt.Errorf("bootstorage: mount ESP %s: %w", dev, err)
	}
	return func() error { return syscall.Unmount(ESPMountPoint, 0) }, nil
}

// Test seams.
var (
	mountFn       = syscall.Mount
	sleepFn       = time.Sleep
	mountInfoPath = "/proc/self/mountinfo"
	devNumberFn   = devNumber
)

// Boot storage and the ESP are FAT file systems that the kernel mounts once: a
// read-only and a read-write mount of the same device cannot exist at the same
// time, and the second one fails with EBUSY, in any mount namespace. When it
// is one of vintner's own commands that holds the other mount (vintner status,
// called by a product early in boot, against publish, from a kernel hook), it
// holds it for a moment, so the loser retries for a few seconds before it gives
// up (WO-0010 4.5).
const (
	mountTries = 25
	mountWait  = 200 * time.Millisecond
)

func mountRetry(dev, target, fstype string, flags uintptr, data string) error {
	return mountLoop(dev, target, fstype, flags, data, false)
}

// mountReadOnly is mountRetry for a read-only mount. When the EBUSY comes from a
// mount that someone else holds read-write, for good (systemd-gpt-auto's /efi,
// a /boot/efi line in fstab), waiting does not help: it binds that mount at
// target and makes the bind read-only. The other mount is not touched, and
// nothing can be written through ours.
func mountReadOnly(dev, target string, flags uintptr, data string) error {
	return mountLoop(dev, target, "vfat", flags|syscall.MS_RDONLY, data, true)
}

func mountLoop(dev, target, fstype string, flags uintptr, data string, bindReadOnly bool) error {
	var err error
	for i := 0; i < mountTries; i++ {
		if err = mountFn(dev, target, fstype, flags, data); !errors.Is(err, syscall.EBUSY) {
			return err
		}
		if bindReadOnly {
			if berr := bindExisting(dev, target, flags); berr == nil {
				return nil
			}
		}
		sleepFn(mountWait)
	}
	return err
}

// bindExisting binds the existing mount of dev's file system at target, then
// remounts the bind read-only. It fails if dev is not mounted whole, as a
// vfat file system, in this mount namespace.
func bindExisting(dev, target string, flags uintptr) error {
	num, err := devNumberFn(dev)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return err
	}
	src := existingMount(string(data), num, target)
	if src == "" {
		return errors.New("bootstorage: no existing mount to bind")
	}
	if err := mountFn(src, target, "", syscall.MS_BIND, ""); err != nil {
		return err
	}
	ro := flags | syscall.MS_REMOUNT | syscall.MS_BIND | syscall.MS_RDONLY
	if err := mountFn("", target, "", ro, ""); err != nil {
		_ = syscall.Unmount(target, 0)
		return err
	}
	return nil
}

// existingMount returns the mount point of the newest mount in mountinfo of
// the vfat file system on device num ("major:minor") that shows its whole root
// directory, other than target itself; "" if there is none.
func existingMount(mountinfo, num, target string) string {
	found := ""
	for _, line := range strings.Split(mountinfo, "\n") {
		f := strings.Fields(line)
		// id parent major:minor root mountpoint options [optional fields] - fstype source superoptions
		if len(f) < 10 || f[2] != num || f[3] != "/" {
			continue
		}
		sep := 6
		for sep < len(f) && f[sep] != "-" {
			sep++
		}
		if sep+1 >= len(f) || f[sep+1] != "vfat" {
			continue
		}
		if mp := unescapeMountInfo(f[4]); mp != target {
			found = mp
		}
	}
	return found
}

// unescapeMountInfo undoes the octal escapes (\040 is a space) of mountinfo paths.
func unescapeMountInfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// devNumber returns "major:minor" of the block device that dev names, as
// mountinfo prints it.
func devNumber(dev string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(dev, &st); err != nil {
		return "", err
	}
	rdev := uint64(st.Rdev)
	major := (rdev>>8)&0xfff | (rdev>>32)&^uint64(0xfff)
	minor := rdev&0xff | (rdev>>12)&^uint64(0xff)
	return fmt.Sprintf("%d:%d", major, minor), nil
}
