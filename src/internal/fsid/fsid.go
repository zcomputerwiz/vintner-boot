// SPDX-License-Identifier: GPL-2.0-or-later

// Package fsid reads the identity of a filesystem straight from its first
// sectors: the FAT type and volume serial of boot storage and the ESP, and the
// UUID of an ext4 root. Nothing is mounted and nothing is written.
package fsid

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// FAT describes a FAT filesystem.
type FAT struct {
	Type   string // "FAT12", "FAT16" or "FAT32"
	Serial uint32
	Label  string
	Bytes  int64 // size of the filesystem as the boot sector states it
}

// UUID returns the volume serial the way GRUB's search --fs-uuid names it:
// "ABCD-1234", upper case.
func (f FAT) UUID() string { return fmt.Sprintf("%04X-%04X", f.Serial>>16, f.Serial&0xFFFF) }

// ErrNotFAT is returned when the boot sector is not a FAT one.
var ErrNotFAT = errors.New("not a FAT filesystem")

// ReadFAT reads the FAT boot sector at off.
func ReadFAT(r io.ReaderAt, off int64) (*FAT, error) {
	b := make([]byte, 512)
	if _, err := r.ReadAt(b, off); err != nil {
		return nil, fmt.Errorf("fsid: read boot sector: %w", err)
	}
	if b[510] != 0x55 || b[511] != 0xAA {
		return nil, ErrNotFAT
	}
	bps := int64(binary.LittleEndian.Uint16(b[11:13]))
	spc := int64(b[13])
	rsvd := int64(binary.LittleEndian.Uint16(b[14:16]))
	nfats := int64(b[16])
	rootEnt := int64(binary.LittleEndian.Uint16(b[17:19]))
	tot := int64(binary.LittleEndian.Uint16(b[19:21]))
	if tot == 0 {
		tot = int64(binary.LittleEndian.Uint32(b[32:36]))
	}
	spf := int64(binary.LittleEndian.Uint16(b[22:24]))
	if spf == 0 {
		spf = int64(binary.LittleEndian.Uint32(b[36:40]))
	}
	if bps < 512 || bps&(bps-1) != 0 || spc == 0 || spc&(spc-1) != 0 || nfats == 0 || tot == 0 || spf == 0 {
		return nil, ErrNotFAT
	}
	rootSectors := (rootEnt*32 + bps - 1) / bps
	data := tot - rsvd - nfats*spf - rootSectors
	if data <= 0 {
		return nil, ErrNotFAT
	}
	clusters := data / spc
	f := &FAT{Bytes: tot * bps}
	switch {
	case clusters < 4085:
		f.Type = "FAT12"
	case clusters < 65525:
		f.Type = "FAT16"
	default:
		f.Type = "FAT32"
	}
	if f.Type == "FAT32" {
		f.Serial = binary.LittleEndian.Uint32(b[67:71])
		f.Label = strings.TrimRight(string(b[71:82]), " \x00")
	} else {
		f.Serial = binary.LittleEndian.Uint32(b[39:43])
		f.Label = strings.TrimRight(string(b[43:54]), " \x00")
	}
	return f, nil
}

// ErrNotExt is returned when there is no ext2/3/4 superblock.
var ErrNotExt = errors.New("not an ext2/3/4 filesystem")

// ReadExtUUID returns the UUID of the ext2/3/4 filesystem that starts at off,
// in lower-case text form.
func ReadExtUUID(r io.ReaderAt, off int64) (string, error) {
	sb := make([]byte, 1024)
	if _, err := r.ReadAt(sb, off+1024); err != nil {
		return "", fmt.Errorf("fsid: read superblock: %w", err)
	}
	if binary.LittleEndian.Uint16(sb[56:58]) != 0xEF53 {
		return "", ErrNotExt
	}
	u := sb[104:120]
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}
