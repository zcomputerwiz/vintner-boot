// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
)

// Disk is the whole disk, for the BIOS boot sector and the BIOS Boot Partition.
type Disk interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
}

// cacheDropper is implemented by a Disk that can throw away the kernel's cached
// copy of the device, so that a read-back comes from the device.
type cacheDropper interface{ DropCache() error }

// EarlyConfig returns the configuration built into core.img: it finds boot
// storage by its FAT volume serial and sets the prefix to its /grub directory.
func EarlyConfig(fatUUID string) string {
	return "search --no-floppy --fs-uuid --set=root " + fatUUID + "\nset prefix=($root)/grub\n"
}

// BuildCore runs grub-mkimage on the payload's i386-pc directory and returns
// core.img. The modules are the ones in the payload's core-modules.txt, which
// ci/lint/grub-modules.sh keeps in step with grub.cfg.
func BuildCore(ctx context.Context, r Runner, p *payload.Payload, fatUUID string) ([]byte, error) {
	mods, err := os.ReadFile(p.Path(payload.CoreModules))
	if err != nil {
		return nil, err
	}
	names := strings.Fields(string(mods))
	if len(names) == 0 {
		return nil, errors.New("core-modules.txt lists no modules")
	}
	tmp, err := os.MkdirTemp("", "vintner-core")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	early := filepath.Join(tmp, "early.cfg")
	if err := os.WriteFile(early, []byte(EarlyConfig(fatUUID)), 0o600); err != nil {
		return nil, err
	}
	out := filepath.Join(tmp, "core.img")
	args := append([]string{"-O", "i386-pc", "-d", p.Path(payload.GrubDir), "-p", "/grub", "-c", early, "-o", out}, names...)
	if o, err := r.Run(ctx, "grub-mkimage", args...); err != nil {
		return nil, fmt.Errorf("grub-mkimage: %w: %s", err, strings.TrimSpace(string(o)))
	}
	core, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if len(core) < 512 {
		return nil, fmt.Errorf("grub-mkimage wrote %d bytes", len(core))
	}
	return core, nil
}

// Embedded is what EmbedGRUB wrote, for the read-back check.
type Embedded struct {
	MBRPrefix []byte // bytes 0 to 0x1B7 of the boot sector
	Core      []byte // the core image as written, padded to whole sectors, with its blocklist
	LBA       uint64 // first sector of the BIOS Boot Partition
}

// EmbedGRUB does what grub-bios-setup does for a GPT disk with a BIOS Boot
// Partition: it writes core.img (padded to whole sectors, with the blocklist
// of its own continuation patched in) to the start of the partition, and
// puts GRUB's boot.img in the boot sector, keeping the disk signature and the
// partition table in bytes 0x1B8 to 0x1FF. The boot sector then points at the
// partition. The disk must have 512-byte sectors (layout rule L6).
func EmbedGRUB(d Disk, bp gpt.Partition, bootImg, core []byte) (*Embedded, error) {
	const ss = 512
	if len(bootImg) != ss {
		return nil, fmt.Errorf("boot.img is %d bytes, not %d", len(bootImg), ss)
	}
	c := append([]byte(nil), core...)
	c = append(c, make([]byte, (ss-len(c)%ss)%ss)...)
	if int64(len(c)) > bp.Size(ss) {
		return nil, fmt.Errorf("core.img is %d bytes and the BIOS Boot Partition %d", len(c), bp.Size(ss))
	}
	if n := len(c)/ss - 1; n > 0xFFFF {
		return nil, fmt.Errorf("core.img is %d sectors; the boot blocklist holds at most 65536", len(c)/ss)
	}
	lba := bp.FirstLBA
	// The first sector of core.img (diskboot.img) ends with the blocklist of the
	// rest of the image: its first sector and how many follow.
	binary.LittleEndian.PutUint64(c[0x1f4:], lba+1)
	binary.LittleEndian.PutUint16(c[0x1fc:], uint16(len(c)/ss-1))

	mbr := make([]byte, ss)
	if _, err := d.ReadAt(mbr, 0); err != nil {
		return nil, fmt.Errorf("read boot sector: %w", err)
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, errors.New("the boot sector has no 0x55AA signature (no protective MBR): refusing to write boot code the BIOS would not run")
	}
	copy(mbr[0:3], bootImg[0:3])
	copy(mbr[0x5a:0x1b8], bootImg[0x5a:0x1b8])
	binary.LittleEndian.PutUint64(mbr[0x5c:], lba)
	mbr[0x66], mbr[0x67] = 0x90, 0x90

	// Core first, then the boot sector, each flushed to the device. The core image
	// is written over whatever the BIOS Boot Partition held, which a stock GRUB
	// boot sector would load: from the first write on, booting has changed.
	if _, err := d.WriteAt(c, int64(lba)*ss); err != nil {
		return nil, fmt.Errorf("write core.img: %w", err)
	}
	if err := d.Sync(); err != nil {
		return nil, fmt.Errorf("flush core.img: %w", err)
	}
	if _, err := d.WriteAt(mbr, 0); err != nil {
		return nil, fmt.Errorf("write boot sector: %w", err)
	}
	if err := d.Sync(); err != nil {
		return nil, fmt.Errorf("flush the boot sector: %w", err)
	}
	return &Embedded{MBRPrefix: append([]byte(nil), mbr[:0x1b8]...), Core: c, LBA: lba}, nil
}

// VerifyEmbedded reads the disk back and compares it with what EmbedGRUB wrote.
func VerifyEmbedded(d Disk, e *Embedded) error {
	const ss = 512
	if c, ok := d.(cacheDropper); ok {
		if err := c.DropCache(); err != nil {
			return err
		}
	}
	mbr := make([]byte, 0x1b8)
	if _, err := d.ReadAt(mbr, 0); err != nil {
		return err
	}
	if !bytes.Equal(mbr, e.MBRPrefix) {
		return errors.New("the boot sector does not read back as written")
	}
	core := make([]byte, len(e.Core))
	if _, err := d.ReadAt(core, int64(e.LBA)*ss); err != nil {
		return err
	}
	if !bytes.Equal(core, e.Core) {
		return errors.New("core.img does not read back as written")
	}
	return nil
}
