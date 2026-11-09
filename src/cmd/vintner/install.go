// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bootstorage"
	"github.com/zcomputerwiz/vintner-boot/src/internal/install"
	"github.com/zcomputerwiz/vintner-boot/src/internal/layout"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
)

// installTestHook lets tests replace the external programs; it is nil in the binary.
var installTestHook func(*install.FullOptions)

func cmdInstall(args []string) {
	osExit(runInstall(args, os.Stdout, os.Stderr))
}

// runInstall implements vintner install and returns its exit status: 0 when
// everything checked out or was deployed, 1 when a rule, the payload check or a
// step failed, 2 for a usage error.
func runInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vintner install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	disk := fs.String("disk", "", "the whole disk that holds the layout, for example /dev/vda (required)")
	firmware := fs.String("firmware", "", "uefi, bios or both (required)")
	target := fs.String("target", "/", "the root of the target system, mounted")
	espDir := fs.String("esp", "", "the mounted ESP (default <target>/boot/efi)")
	bsDir := fs.String("boot-storage", "", "the mounted boot storage (default: mount it by PARTUUID at "+bootstorage.MountPoint+")")
	rootUUID := fs.String("root-partuuid", "", "PARTUUID of the root partition (default: the one partition of the x86-64 root type)")
	bsUUID := fs.String("boot-storage-partuuid", "", "PARTUUID of boot storage (default: the one partition of the XBOOTLDR type)")
	espUUID := fs.String("esp-partuuid", "", "PARTUUID of the EFI System Partition (default: the one partition of the ESP type)")
	payloadDir := fs.String("payload-dir", payload.DefaultDir, "directory of the vintner-platform payloads")
	check := fs.Bool("check", false, "only check the layout and the payloads; change nothing")
	kernel := fs.String("kernel", "", "kernel ABI of generation 1 (default: the newest kernel with an initramfs in <target>/boot)")
	cmdlineFile := fs.String("cmdline-file", "", "the product's kernel options (default <target>/etc/kernel/cmdline)")
	resume := fs.Bool("resume", false, "continue an installation that published generation 1 and failed afterwards: take booting over, remove the stock loaders, verify")
	updateInitramfs := fs.Bool("update-initramfs", false, "when the kernel's initramfs is older than the target's initramfs-tools hooks, run update-initramfs -u -k <abi> in the target instead of refusing")
	sbTier := fs.String("secure-boot", "none", "Secure Boot tier: none, or A (a machine key in the firmware's db, no shim): creates the key, signs the loader and the recovery images")
	sbDir := fs.String("secure-boot-dir", "", "where the machine key is kept (default <target>/etc/vintner/secureboot)")
	sbEnrolled := fs.Bool("secure-boot-enrolled", false, "tier A: the machine certificate is in the firmware's db (use when the installer cannot read the firmware's variables)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *disk == "" || *firmware == "" {
		fmt.Fprintln(stderr, "vintner install: --disk and --firmware are required")
		fs.Usage()
		return 2
	}

	flags := os.O_RDONLY
	if !*check && *firmware != "uefi" {
		flags = os.O_RDWR // BIOS: the boot sector and the BIOS Boot Partition
	}
	f, err := os.OpenFile(*disk, flags, 0)
	if err != nil {
		fmt.Fprintf(stderr, "vintner install: %v\n", err)
		return 1
	}
	defer f.Close()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Fprintf(stderr, "vintner install: %v\n", err)
		return 1
	}

	l, findings := layout.Check(layout.Request{
		Firmware: *firmware, Disk: f, DiskSize: size,
		RootPartUUID: *rootUUID, BootStoragePartUUID: *bsUUID, ESPPartUUID: *espUUID,
	})
	ok := true
	if l.Table != nil {
		ss := l.Table.SectorSize
		if p := l.BootStorage; p != nil {
			fmt.Fprintf(stdout, "boot storage: partition %d, PARTUUID %s, %d MiB\n", p.Number, p.UUID, p.Size(ss)>>20)
		}
		if p := l.ESP; p != nil {
			fmt.Fprintf(stdout, "ESP:          partition %d, PARTUUID %s, %d MiB\n", p.Number, p.UUID, p.Size(ss)>>20)
		}
		if p := l.BIOSBoot; p != nil {
			fmt.Fprintf(stdout, "BIOS boot:    partition %d, %d KiB\n", p.Number, p.Size(ss)>>10)
		}
		if p := l.Root; p != nil {
			fmt.Fprintf(stdout, "root:         partition %d, PARTUUID %s, filesystem UUID %s\n", p.Number, p.UUID, l.RootUUID)
		}
	}
	for _, fd := range findings {
		fmt.Fprintf(stderr, "vintner install: refused: %s\n", fd)
		ok = false
	}
	pl, err := payload.Open(*payloadDir)
	if err != nil {
		fmt.Fprintf(stderr, "vintner install: refused: %v\n", err)
		ok = false
	}
	if !ok {
		return 1
	}
	if *check {
		fmt.Fprintln(stdout, "layout and payloads are accepted")
		return 0
	}

	// Deploy.
	opts := install.FullOptions{KernelABI: *kernel, CmdlineFile: *cmdlineFile, UpdateInitramfs: *updateInitramfs, Options: install.Options{
		Firmware: *firmware, Layout: l, Payload: pl,
		TargetDir: *target, ESPDir: *espDir, BootStorageDir: *bsDir,
		SecureBootTier: *sbTier, SecureBootDir: *sbDir, SecureBootEnrolled: *sbEnrolled,
		Log: stdout,
	}}
	if *firmware != "bios" && opts.ESPDir == "" {
		opts.ESPDir = filepath.Join(*target, "boot", "efi")
	}
	if *firmware != "uefi" {
		opts.Disk = blockDisk{f}
	}
	if opts.BootStorageDir == "" {
		unmount, err := bootstorage.Mount("/dev/disk/by-partuuid/"+l.BootStorage.UUID, true)
		if err != nil {
			fmt.Fprintf(stderr, "vintner install: mount boot storage: %v\n", err)
			return 1
		}
		defer func() {
			if err := unmount(); err != nil {
				fmt.Fprintf(stderr, "vintner install: unmount boot storage: %v\n", err)
			}
		}()
		opts.BootStorageDir = bootstorage.MountPoint
	}
	if installTestHook != nil {
		installTestHook(&opts)
	}
	fmt.Fprintln(stdout, "installing:")
	var res *install.Result
	if *resume {
		res, err = install.Resume(context.Background(), opts)
	} else {
		res, err = install.Install(context.Background(), opts)
	}
	if err != nil {
		fmt.Fprintf(stderr, "vintner install: %v\n", err)
		var se *install.Error
		if errors.As(err, &se) && len(se.Changed) > 0 {
			fmt.Fprintln(stderr, "vintner install: changed before the failure:")
			for _, c := range se.Changed {
				fmt.Fprintln(stderr, "  "+c)
			}
		}
		fmt.Fprintln(stderr, "vintner install: the lines above are everything that was changed. Booting changes at the first \"created UEFI boot entry\", \"wrote EFI/BOOT/BOOTX64.EFI\" or \"writing core.img\" line; all come after generation 1 is published")
		if errors.As(err, &se) && se.Resumable {
			fmt.Fprintln(stderr, "vintner install: generation 1 is published; run the same command again with --resume to finish")
		}
		return 1
	}
	if *sbTier == "A" {
		if *sbDir == "" {
			*sbDir = filepath.Join(*target, "etc", "vintner", "secureboot")
		}
		fmt.Fprintf(stdout, "Secure Boot tier A: the machine certificate is %s; out-of-tree kernel modules cannot load with Secure Boot on (no shim, no MOK list)\n", filepath.Join(*sbDir, "cert.der"))
	}
	fmt.Fprintf(stdout, "installed: %d changes; installation %s; generation 1 is TRYING and is blessed GOOD by the first boot that reaches boot-complete.target\n", len(res.Changed), res.Manifest.InstallationID)
	return 0
}

// blockDisk is the disk opened for the BIOS boot code. DropCache asks the
// kernel to flush and discard its cached copy of the device (BLKFLSBUF), so that
// the read-back after the write comes from the device. A regular file, as in
// tests, has no such cache to drop.
type blockDisk struct{ *os.File }

const blkflsbuf = 0x1261

// DropCache implements the optional interface that install.VerifyEmbedded uses.
func (b blockDisk) DropCache() error {
	if err := b.Sync(); err != nil {
		return err
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, b.Fd(), blkflsbuf, 0); errno != 0 && errno != syscall.ENOTTY {
		return fmt.Errorf("flush the device cache: %w", errno)
	}
	return nil
}
