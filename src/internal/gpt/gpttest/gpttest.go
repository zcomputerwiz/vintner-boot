// SPDX-License-Identifier: GPL-2.0-or-later

// Package gpttest builds sparse disk images with a GPT for tests. It is not
// used by the vintner binary.
package gpttest

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Part describes one partition of a test disk.
type Part struct {
	Type     string // type GUID text
	UUID     string // partition GUID text
	FirstLBA uint64
	LastLBA  uint64
	Attrs    uint64
	Name     string
}

const pageSize = 4096

// Disk is a sparse in-memory disk: pages never written read as zeros.
type Disk struct {
	Size  int64
	pages map[int64][]byte
}

// New returns an empty disk of the given size in bytes.
func New(size int64) *Disk { return &Disk{Size: size, pages: map[int64][]byte{}} }

// ReadAt implements io.ReaderAt.
func (d *Disk) ReadAt(p []byte, off int64) (int, error) {
	if off >= d.Size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off+int64(n) < d.Size {
		pg := (off + int64(n)) / pageSize
		in := int((off + int64(n)) % pageSize)
		c := pageSize - in
		if c > len(p)-n {
			c = len(p) - n
		}
		if page, ok := d.pages[pg]; ok {
			copy(p[n:n+c], page[in:in+c])
		} else {
			for i := 0; i < c; i++ {
				p[n+i] = 0
			}
		}
		n += c
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// WriteAt implements io.WriterAt.
func (d *Disk) WriteAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		pg := (off + int64(n)) / pageSize
		in := int((off + int64(n)) % pageSize)
		c := pageSize - in
		if c > len(p)-n {
			c = len(p) - n
		}
		page, ok := d.pages[pg]
		if !ok {
			page = make([]byte, pageSize)
			d.pages[pg] = page
		}
		copy(page[in:], p[n:n+c])
		n += c
	}
	if end := off + int64(len(p)); end > d.Size {
		d.Size = end
	}
	return n, nil
}

// GUIDBytes converts the text form of a GUID to its on-disk bytes.
func GUIDBytes(s string) []byte {
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		panic("gpttest: bad GUID " + s)
	}
	raw := make([]byte, 16)
	for i := range raw {
		v, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		if err != nil {
			panic("gpttest: bad GUID " + s)
		}
		raw[i] = byte(v)
	}
	out := make([]byte, 16)
	out[0], out[1], out[2], out[3] = raw[3], raw[2], raw[1], raw[0]
	out[4], out[5] = raw[5], raw[4]
	out[6], out[7] = raw[7], raw[6]
	copy(out[8:], raw[8:])
	return out
}

// Image builds a disk of totalLBAs 512-byte sectors holding a protective MBR
// and a GPT with the given partitions.
func Image(parts []Part, totalLBAs uint64) *Disk { return ImageSS(parts, totalLBAs, 512) }

// ImageSS is Image for a disk with ss-byte sectors (512 or 4096).
func ImageSS(parts []Part, totalLBAs uint64, ss int) *Disk {
	d := New(int64(totalLBAs) * int64(ss))
	mbr := make([]byte, 512)
	mbr[446+4] = 0xEE
	binary.LittleEndian.PutUint32(mbr[446+8:], 1)
	binary.LittleEndian.PutUint32(mbr[446+12:], uint32(totalLBAs-1))
	mbr[510], mbr[511] = 0x55, 0xAA
	d.WriteAt(mbr, 0)

	const nEnt, eSize = 128, 128
	arr := make([]byte, nEnt*eSize)
	for i, p := range parts {
		e := arr[i*eSize : (i+1)*eSize]
		copy(e[0:16], GUIDBytes(p.Type))
		copy(e[16:32], GUIDBytes(p.UUID))
		binary.LittleEndian.PutUint64(e[32:], p.FirstLBA)
		binary.LittleEndian.PutUint64(e[40:], p.LastLBA)
		binary.LittleEndian.PutUint64(e[48:], p.Attrs)
		for j, u := range utf16.Encode([]rune(p.Name)) {
			binary.LittleEndian.PutUint16(e[56+2*j:], u)
		}
	}
	hdr := make([]byte, ss)
	copy(hdr[0:8], "EFI PART")
	binary.LittleEndian.PutUint32(hdr[8:], 0x00010000)
	binary.LittleEndian.PutUint32(hdr[12:], 92)
	binary.LittleEndian.PutUint64(hdr[24:], 1)
	binary.LittleEndian.PutUint64(hdr[32:], totalLBAs-1)
	binary.LittleEndian.PutUint64(hdr[40:], 34)
	binary.LittleEndian.PutUint64(hdr[48:], totalLBAs-34)
	copy(hdr[56:72], GUIDBytes("11111111-2222-3333-4444-555555555555"))
	binary.LittleEndian.PutUint64(hdr[72:], 2)
	binary.LittleEndian.PutUint32(hdr[80:], nEnt)
	binary.LittleEndian.PutUint32(hdr[84:], eSize)
	binary.LittleEndian.PutUint32(hdr[88:], crc32.ChecksumIEEE(arr))
	binary.LittleEndian.PutUint32(hdr[16:], crc32.ChecksumIEEE(hdr[:92]))
	d.WriteAt(hdr, int64(ss))
	d.WriteAt(arr, int64(2*ss))
	return d
}

// CorruptEntry flips a byte of the entry array so its CRC no longer matches.
func CorruptEntry(d *Disk) {
	b := make([]byte, 1)
	d.ReadAt(b, 1024+5)
	b[0] ^= 0xFF
	d.WriteAt(b, 1024+5)
}

// WithMBRPartition returns an error-free disk whose MBR also holds a
// partition of the given type (a hybrid or mixed table).
func WithMBRPartition(d *Disk, typ byte) {
	mbr := make([]byte, 512)
	d.ReadAt(mbr, 0)
	mbr[446+16+4] = typ
	d.WriteAt(mbr, 0)
}

// String describes a part for test failure messages.
func (p Part) String() string { return fmt.Sprintf("%s %d-%d", p.Name, p.FirstLBA, p.LastLBA) }

// Save writes the disk to a sparse file at path.
func (d *Disk) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(d.Size); err != nil {
		return err
	}
	for pg, page := range d.pages {
		if _, err := f.WriteAt(page, pg*pageSize); err != nil {
			return err
		}
	}
	return nil
}

// Sync implements the flush a real disk has; memory has nothing to flush.
func (d *Disk) Sync() error { return nil }
