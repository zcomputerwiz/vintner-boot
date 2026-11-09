// SPDX-License-Identifier: GPL-2.0-or-later

package fsid

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt/gpttest"
)

// fat32 writes the boot sector mformat -F makes for a volume of total sectors.
func fat32(d *gpttest.Disk, off int64, total uint32, serial uint32, label string) {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint16(b[11:], 512)
	b[13] = 8
	binary.LittleEndian.PutUint16(b[14:], 32)
	b[16] = 2
	binary.LittleEndian.PutUint32(b[32:], total)
	binary.LittleEndian.PutUint32(b[36:], (total/8)/128+1)
	binary.LittleEndian.PutUint32(b[67:], serial)
	copy(b[71:82], []byte(label+"           "))
	b[510], b[511] = 0x55, 0xAA
	d.WriteAt(b, off)
}

func TestReadFAT32(t *testing.T) {
	d := gpttest.New(1 << 30)
	fat32(d, 1<<20, 2097152, 0x1234ABCD, "BOOTSTORAGE")
	f, err := ReadFAT(d, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != "FAT32" || f.Label != "BOOTSTORAGE" || f.UUID() != "1234-ABCD" {
		t.Errorf("got %+v uuid %s", f, f.UUID())
	}
	if f.Bytes != 2097152*512 {
		t.Errorf("bytes %d", f.Bytes)
	}
}

func TestReadFATRefusesOthers(t *testing.T) {
	d := gpttest.New(1 << 20)
	if _, err := ReadFAT(d, 0); !errors.Is(err, ErrNotFAT) {
		t.Errorf("blank: %v", err)
	}
	// A LUKS or ext4 volume has no FAT boot sector signature at 510.
	d.WriteAt([]byte("LUKS\xba\xbe"), 0)
	if _, err := ReadFAT(d, 0); !errors.Is(err, ErrNotFAT) {
		t.Errorf("luks: %v", err)
	}
}

func TestReadFATSmallIsNotFAT32(t *testing.T) {
	d := gpttest.New(1 << 24)
	b := make([]byte, 512)
	binary.LittleEndian.PutUint16(b[11:], 512)
	b[13] = 4
	binary.LittleEndian.PutUint16(b[14:], 1)
	b[16] = 2
	binary.LittleEndian.PutUint16(b[17:], 512)
	binary.LittleEndian.PutUint16(b[19:], 20000)
	binary.LittleEndian.PutUint16(b[22:], 20)
	b[510], b[511] = 0x55, 0xAA
	d.WriteAt(b, 0)
	f, err := ReadFAT(d, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != "FAT16" {
		t.Errorf("type %s, want FAT16", f.Type)
	}
}

func TestReadExtUUID(t *testing.T) {
	d := gpttest.New(1 << 22)
	sb := make([]byte, 1024)
	binary.LittleEndian.PutUint16(sb[56:], 0xEF53)
	copy(sb[104:120], []byte{0x4e, 0x9f, 0x54, 0xf3, 0x86, 0x5b, 0x43, 0xfa, 0x8f, 0x1c, 0x9e, 0x64, 0x4d, 0x3a, 0x2b, 0x11})
	d.WriteAt(sb, 4096+1024)
	got, err := ReadExtUUID(d, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if want := "4e9f54f3-865b-43fa-8f1c-9e644d3a2b11"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if _, err := ReadExtUUID(d, 1<<20); !errors.Is(err, ErrNotExt) {
		t.Errorf("blank: %v", err)
	}
}
