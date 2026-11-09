// SPDX-License-Identifier: GPL-2.0-or-later

// Package gpt reads a GUID partition table. It never writes one: the product's
// installer partitions (WO-0010 decision D-A), and vintner only checks the
// result against examples/disk-layouts/README.md.
package gpt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"unicode/utf16"
)

// Partition type GUIDs (upper case, the canonical text form).
const (
	TypeESP         = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
	TypeBIOSBoot    = "21686148-6449-6E6F-744E-656564454649"
	TypeBootStorage = "BC13C2FF-59E6-4262-A352-B275FD6F7172" // XBOOTLDR
	TypeLinuxRoot   = "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709" // x86-64 root
	TypeLinuxData   = "0FC63DAF-8483-4772-8E79-3D69D8477DE4"
)

// ErrNotGPT is returned when the disk has no GPT (an MBR/DOS table, or none).
var ErrNotGPT = errors.New("no GPT on this disk")

// Partition is one used entry of the table.
type Partition struct {
	Number   int    // 1-based index in the entry array
	Type     string // type GUID, upper case
	UUID     string // partition GUID (PARTUUID), lower case
	FirstLBA uint64
	LastLBA  uint64
	Attrs    uint64
	Name     string
}

// Table is a parsed GPT.
type Table struct {
	SectorSize int
	TotalLBAs  uint64 // from the disk size, 0 if unknown
	DiskGUID   string
	Partitions []Partition
}

// Size returns the partition's size in bytes.
func (p Partition) Size(sectorSize int) int64 {
	return int64(p.LastLBA-p.FirstLBA+1) * int64(sectorSize)
}

// Offset returns the partition's byte offset on the disk.
func (p Partition) Offset(sectorSize int) int64 {
	return int64(p.FirstLBA) * int64(sectorSize)
}

// ByType returns the partitions of one type GUID, in table order.
func (t *Table) ByType(typeGUID string) []Partition {
	var out []Partition
	for _, p := range t.Partitions {
		if p.Type == typeGUID {
			out = append(out, p)
		}
	}
	return out
}

// ByUUID returns the partition with this PARTUUID (any case).
func (t *Table) ByUUID(partuuid string) (Partition, bool) {
	for _, p := range t.Partitions {
		if strings.EqualFold(p.UUID, partuuid) {
			return p, true
		}
	}
	return Partition{}, false
}

// guid formats the 16 on-disk bytes (three little-endian fields, then two
// big-endian runs) as text, lower case.
func guid(b []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x",
		binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint16(b[4:6]),
		binary.LittleEndian.Uint16(b[6:8]), b[8:10], b[10:16])
}

// ReadAt reads the table of the disk behind r, which holds size bytes, trying
// 512 and then 4096 byte sectors. A disk whose MBR holds real partitions
// next to the GPT, or whose header or entry array fails its CRC, is refused.
func ReadAt(r io.ReaderAt, size int64) (*Table, error) {
	var lastErr error = ErrNotGPT
	for _, ss := range []int{512, 4096} {
		t, err := readSS(r, size, ss)
		if err == nil {
			return t, nil
		}
		if !errors.Is(err, ErrNotGPT) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func readSS(r io.ReaderAt, size int64, ss int) (*Table, error) {
	hdr := make([]byte, ss)
	if _, err := r.ReadAt(hdr, int64(ss)); err != nil {
		return nil, ErrNotGPT
	}
	if !bytes.Equal(hdr[0:8], []byte("EFI PART")) {
		return nil, ErrNotGPT
	}
	hsize := binary.LittleEndian.Uint32(hdr[12:16])
	if hsize < 92 || int(hsize) > ss {
		return nil, fmt.Errorf("gpt: header size %d is not valid", hsize)
	}
	want := binary.LittleEndian.Uint32(hdr[16:20])
	c := make([]byte, hsize)
	copy(c, hdr[:hsize])
	binary.LittleEndian.PutUint32(c[16:20], 0)
	if crc32.ChecksumIEEE(c) != want {
		return nil, errors.New("gpt: header CRC does not match")
	}

	// The protective MBR must not describe real partitions beside the GPT.
	mbr := make([]byte, 512)
	if _, err := r.ReadAt(mbr, 0); err != nil {
		return nil, fmt.Errorf("gpt: read MBR: %w", err)
	}
	if mbr[510] == 0x55 && mbr[511] == 0xAA {
		for i := 0; i < 4; i++ {
			typ := mbr[446+16*i+4]
			if typ != 0x00 && typ != 0xEE {
				return nil, fmt.Errorf("gpt: the MBR holds a partition of type 0x%02X beside the GPT", typ)
			}
		}
	}

	entLBA := binary.LittleEndian.Uint64(hdr[72:80])
	n := binary.LittleEndian.Uint32(hdr[80:84])
	esz := binary.LittleEndian.Uint32(hdr[84:88])
	arrCRC := binary.LittleEndian.Uint32(hdr[88:92])
	if esz < 128 || esz > 4096 || n == 0 || n > 4096 {
		return nil, fmt.Errorf("gpt: entry array of %d entries of %d bytes is not valid", n, esz)
	}
	arr := make([]byte, int(n)*int(esz))
	if _, err := r.ReadAt(arr, int64(entLBA)*int64(ss)); err != nil {
		return nil, fmt.Errorf("gpt: read entries: %w", err)
	}
	if crc32.ChecksumIEEE(arr) != arrCRC {
		return nil, errors.New("gpt: entry array CRC does not match")
	}

	t := &Table{SectorSize: ss, DiskGUID: guid(hdr[56:72])}
	if size > 0 {
		t.TotalLBAs = uint64(size / int64(ss))
	}
	for i := 0; i < int(n); i++ {
		e := arr[i*int(esz) : (i+1)*int(esz)]
		if allZero(e[0:16]) {
			continue
		}
		first := binary.LittleEndian.Uint64(e[32:40])
		last := binary.LittleEndian.Uint64(e[40:48])
		if last < first {
			return nil, fmt.Errorf("gpt: entry %d ends before it starts", i+1)
		}
		u16 := make([]uint16, 0, 36)
		for j := 56; j+1 < 128; j += 2 {
			v := binary.LittleEndian.Uint16(e[j : j+2])
			if v == 0 {
				break
			}
			u16 = append(u16, v)
		}
		t.Partitions = append(t.Partitions, Partition{
			Number:   i + 1,
			Type:     strings.ToUpper(guid(e[0:16])),
			UUID:     guid(e[16:32]),
			FirstLBA: first,
			LastLBA:  last,
			Attrs:    binary.LittleEndian.Uint64(e[48:56]),
			Name:     string(utf16.Decode(u16)),
		})
	}
	return t, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
