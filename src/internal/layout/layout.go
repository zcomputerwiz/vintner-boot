// SPDX-License-Identifier: GPL-2.0-or-later

// Package layout checks a disk against the layouts that vintner install
// accepts (examples/disk-layouts/README.md, docs/accepted-platforms.md). It
// reads the partition table and the first sectors of the filesystems; it never
// writes and never mounts.
package layout

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/fsid"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
)

// Minimum sizes (examples/disk-layouts/README.md).
const (
	MinBootStorage = 1 << 30
	MinESP         = 2 << 20 // the loader files need about 1 MiB; a Windows ESP is 100 MiB or more
	MinBIOSBoot    = 1 << 20
)

// Request says what to check.
type Request struct {
	Firmware string // "uefi", "bios" or "both"
	Disk     io.ReaderAt
	DiskSize int64
	// RootPartUUID, BootStoragePartUUID and ESPPartUUID name the partitions
	// when the disk is ambiguous. Empty means: the one partition of that type.
	RootPartUUID        string
	BootStoragePartUUID string
	ESPPartUUID         string
}

// Finding is one broken rule.
type Finding struct {
	Rule string // L0 ... L7
	Text string
}

func (f Finding) String() string { return f.Rule + ": " + f.Text }

// Layout is what was found. Fields are nil or empty when the rule that needs
// them failed.
type Layout struct {
	Table       *gpt.Table
	Root        *gpt.Partition
	RootUUID    string // the root filesystem UUID
	BootStorage *gpt.Partition
	BootFS      *fsid.FAT
	ESP         *gpt.Partition
	ESPFS       *fsid.FAT
	BIOSBoot    *gpt.Partition
}

// Check returns the layout and every rule it breaks. No findings means the
// layout is accepted.
func Check(req Request) (*Layout, []Finding) {
	var fs []Finding
	add := func(rule, format string, a ...any) { fs = append(fs, Finding{rule, fmt.Sprintf(format, a...)}) }
	l := &Layout{}

	switch req.Firmware {
	case "uefi", "bios", "both":
	default:
		add("L0", "firmware %q is not uefi, bios or both", req.Firmware)
		return l, fs
	}

	tab, err := gpt.ReadAt(req.Disk, req.DiskSize)
	if err != nil {
		if errors.Is(err, gpt.ErrNotGPT) {
			add("L1", "the disk has no GPT (an MBR/DOS partition table is not accepted)")
		} else {
			add("L1", "%v", err)
		}
		return l, fs
	}
	l.Table = tab
	ss := tab.SectorSize

	pick := func(typ, uuid, what, rule string) *gpt.Partition {
		if uuid != "" {
			p, ok := tab.ByUUID(uuid)
			if !ok {
				add(rule, "no partition with PARTUUID %s on this disk (the %s must be on the same disk)", uuid, what)
				return nil
			}
			return &p
		}
		ps := tab.ByType(typ)
		switch len(ps) {
		case 0:
			add(rule, "no %s partition (type %s) on this disk", what, typ)
			return nil
		case 1:
			return &ps[0]
		}
		var ids []string
		for _, p := range ps {
			ids = append(ids, p.UUID)
		}
		add(rule, "%d %s partitions (%s); name one", len(ps), what, strings.Join(ids, ", "))
		return nil
	}

	// Boot storage: one, FAT32, big enough, plain.
	l.BootStorage = pick(gpt.TypeBootStorage, req.BootStoragePartUUID, "boot storage (XBOOTLDR)", "L2")
	if p := l.BootStorage; p != nil {
		if p.Type != gpt.TypeBootStorage {
			add("L2", "partition %d (%s) is type %s, not the boot storage type %s", p.Number, p.UUID, p.Type, gpt.TypeBootStorage)
		}
		if p.Size(ss) < MinBootStorage {
			add("L4", "boot storage is %d MiB; the minimum is %d MiB", p.Size(ss)>>20, MinBootStorage>>20)
		}
		f, err := fsid.ReadFAT(req.Disk, p.Offset(ss))
		switch {
		case err != nil:
			add("L3", "boot storage (partition %d) is not a FAT filesystem: it must be plain, unencrypted FAT32 (%v)", p.Number, err)
		case f.Type != "FAT32":
			add("L3", "boot storage is %s; it must be FAT32", f.Type)
		default:
			l.BootFS = f
		}
	}

	// ESP.
	if req.Firmware != "bios" {
		l.ESP = pick(gpt.TypeESP, req.ESPPartUUID, "EFI System Partition", "L5")
		if p := l.ESP; p != nil {
			if p.Size(ss) < MinESP {
				add("L5", "the EFI System Partition is %d KiB; the loader files need at least %d MiB", p.Size(ss)>>10, MinESP>>20)
			}
			if f, err := fsid.ReadFAT(req.Disk, p.Offset(ss)); err != nil {
				add("L5", "the EFI System Partition (partition %d) is not a FAT filesystem (%v)", p.Number, err)
			} else {
				l.ESPFS = f
			}
		}
	}

	// BIOS Boot Partition.
	if req.Firmware != "uefi" {
		if ss != 512 {
			add("L6", "BIOS boot needs 512-byte sectors; this disk has %d-byte sectors", ss)
		}
		l.BIOSBoot = pick(gpt.TypeBIOSBoot, "", "BIOS Boot Partition", "L6")
		if p := l.BIOSBoot; p != nil && p.Size(ss) < MinBIOSBoot {
			add("L6", "the BIOS Boot Partition is %d KiB; the GRUB core image needs at least %d MiB", p.Size(ss)>>10, MinBIOSBoot>>20)
		}
	}

	// Root: on this disk, ext4, not boot storage.
	if req.RootPartUUID == "" {
		ps := tab.ByType(gpt.TypeLinuxRoot)
		if len(ps) == 1 {
			l.Root = &ps[0]
		} else {
			add("L7", "found %d partitions of the x86-64 root type %s; name the root with --root-partuuid", len(ps), gpt.TypeLinuxRoot)
		}
	} else {
		l.Root = pick(gpt.TypeLinuxRoot, req.RootPartUUID, "root", "L7")
	}
	if p := l.Root; p != nil {
		if l.BootStorage != nil && p.UUID == l.BootStorage.UUID {
			add("L7", "the root and boot storage are the same partition")
		}
		u, err := fsid.ReadExtUUID(req.Disk, p.Offset(ss))
		if err != nil {
			add("L7", "the root (partition %d) is not an ext4 filesystem on a plain partition (%v)", p.Number, err)
		} else {
			l.RootUUID = u
		}
	}
	return l, fs
}
