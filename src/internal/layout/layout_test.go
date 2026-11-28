// SPDX-License-Identifier: GPL-2.0-or-later

package layout

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt/gpttest"
)

const (
	uBIOS = "2b0f98d3-e62a-48b4-b260-dfdfda5b8b3f"
	uESP  = "b558256e-e822-4787-9bd2-c4c47dfb54d9"
	uBS   = "2d11fdb7-c9b6-475f-8336-ebdb5b830355"
	uRoot = "fd202e72-0241-4c3b-953d-ffe506aefc6c"
	// Sectors: BIOS Boot 3 MiB at 2048, ESP 124 MiB at 8192, boot storage 1 GiB
	// at 262144, root from 2359296.
	bsFirst   = 262144
	bsSectors = 2097152
	rootFirst = 2359296
)

func part(typ, uuid string, first, last uint64) gpttest.Part {
	return gpttest.Part{Type: typ, UUID: uuid, FirstLBA: first, LastLBA: last}
}

func bsPart(sectors uint64) gpttest.Part {
	return part(gpt.TypeBootStorage, uBS, bsFirst, bsFirst+sectors-1)
}

func fat(d *gpttest.Disk, off int64, total uint32, serial uint32) {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint16(b[11:], 512)
	b[13] = 8
	binary.LittleEndian.PutUint16(b[14:], 32)
	b[16] = 2
	binary.LittleEndian.PutUint32(b[32:], total)
	binary.LittleEndian.PutUint32(b[36:], (total/8)/128+1)
	binary.LittleEndian.PutUint32(b[67:], serial)
	b[510], b[511] = 0x55, 0xAA
	d.WriteAt(b, off)
}

func ext(d *gpttest.Disk, off int64) {
	sb := make([]byte, 1024)
	binary.LittleEndian.PutUint16(sb[56:], 0xEF53)
	copy(sb[104:120], []byte{0x4e, 0x9f, 0x54, 0xf3, 0x86, 0x5b, 0x43, 0xfa, 0x8f, 0x1c, 0x9e, 0x64, 0x4d, 0x3a, 0x2b, 0x11})
	d.WriteAt(sb, off+1024)
}

// build makes a disk from parts and gives every partition of a known type a
// filesystem of the right kind and size.
func build(parts ...gpttest.Part) *gpttest.Disk {
	d := gpttest.Image(parts, 8388608)
	for _, p := range parts {
		off := int64(p.FirstLBA) * 512
		sectors := uint32(p.LastLBA - p.FirstLBA + 1)
		switch p.Type {
		case gpt.TypeESP:
			fat(d, off, sectors, 0xAAAA0001)
		case gpt.TypeBootStorage:
			fat(d, off, sectors, 0x1234ABCD)
		case gpt.TypeLinuxRoot:
			ext(d, off)
		}
	}
	return d
}

func good() *gpttest.Disk {
	return build(
		part(gpt.TypeBIOSBoot, uBIOS, 2048, 8191),
		part(gpt.TypeESP, uESP, 8192, 262143),
		bsPart(bsSectors),
		part(gpt.TypeLinuxRoot, uRoot, rootFirst, 8000000))
}

func rules(fs []Finding) string {
	var r []string
	for _, f := range fs {
		r = append(r, f.Rule)
	}
	return strings.Join(r, ",")
}

func check(fw string, d *gpttest.Disk, mod func(*Request)) (*Layout, []Finding) {
	req := Request{Firmware: fw, Disk: d, DiskSize: d.Size}
	if mod != nil {
		mod(&req)
	}
	return Check(req)
}

func TestAcceptsTheCILayout(t *testing.T) {
	for _, fw := range []string{"uefi", "bios", "both"} {
		l, fs := check(fw, good(), nil)
		if len(fs) != 0 {
			t.Fatalf("%s: %v", fw, fs)
		}
		if l.RootUUID != "4e9f54f3-865b-43fa-8f1c-9e644d3a2b11" || l.BootFS.UUID() != "1234-ABCD" || l.BootStorage.UUID != uBS {
			t.Errorf("%s: %+v", fw, l)
		}
	}
}

func TestRefusals(t *testing.T) {
	root := part(gpt.TypeLinuxRoot, uRoot, rootFirst, 8000000)
	bios := part(gpt.TypeBIOSBoot, uBIOS, 2048, 8191)
	esp := part(gpt.TypeESP, uESP, 8192, 262143)
	cases := []struct {
		name string
		fw   string
		mk   func() *gpttest.Disk
		mod  func(*Request)
		want string
	}{
		{"no GPT", "both", func() *gpttest.Disk { return gpttest.New(1 << 30) }, nil, "L1"},
		{"mbr beside gpt", "both", func() *gpttest.Disk { d := good(); gpttest.WithMBRPartition(d, 0x83); return d }, nil, "L1"},
		{"boot storage is not FAT", "both", func() *gpttest.Disk {
			d := good()
			d.WriteAt(make([]byte, 512), bsFirst*512) // LUKS, ext4 or blank: no FAT boot sector
			return d
		}, nil, "L3"},
		{"boot storage is FAT16", "both", func() *gpttest.Disk {
			d := good()
			b := make([]byte, 512)
			binary.LittleEndian.PutUint16(b[11:], 512)
			b[13] = 64 // 32768 clusters: FAT16
			binary.LittleEndian.PutUint16(b[14:], 1)
			b[16] = 2
			binary.LittleEndian.PutUint16(b[17:], 512)
			binary.LittleEndian.PutUint32(b[32:], bsSectors)
			binary.LittleEndian.PutUint16(b[22:], 64)
			b[510], b[511] = 0x55, 0xAA
			d.WriteAt(b, bsFirst*512)
			return d
		}, nil, "L3"},
		{"boot storage too small", "both", func() *gpttest.Disk {
			return build(bios, esp, bsPart(bsSectors/2), root) // 512 MiB
		}, nil, "L4"},
		{"no ESP for uefi", "uefi", func() *gpttest.Disk { return build(bsPart(bsSectors), root) }, nil, "L5"},
		{"ESP too small", "uefi", func() *gpttest.Disk {
			return build(part(gpt.TypeESP, uESP, 8192, 8192+1023), bsPart(bsSectors), root) // 512 KiB
		}, nil, "L5"},
		{"no BIOS boot partition for bios", "bios", func() *gpttest.Disk { return build(bsPart(bsSectors), root) }, nil, "L6"},
		{"two boot storage partitions", "both", func() *gpttest.Disk {
			return build(bios, esp, bsPart(bsSectors),
				part(gpt.TypeBootStorage, "9d11fdb7-c9b6-475f-8336-ebdb5b830355", rootFirst, 4456447),
				part(gpt.TypeLinuxRoot, uRoot, 4456448, 8000000))
		}, nil, "L2"},
		{"no boot storage", "both", func() *gpttest.Disk { return build(bios, esp, root) }, nil, "L2"},
		{"root is not ext4", "both", func() *gpttest.Disk {
			d := good()
			d.WriteAt(make([]byte, 1024), rootFirst*512+1024)
			return d
		}, nil, "L7"},
		{"root named but not on this disk", "both", good, func(r *Request) { r.RootPartUUID = "00000000-0000-0000-0000-000000000001" }, "L7"},
		{"root is the boot storage", "both", good, func(r *Request) { r.RootPartUUID = uBS }, "L7"},
		{"two roots and none named", "both", func() *gpttest.Disk {
			return build(bios, esp, bsPart(bsSectors), root, part(gpt.TypeLinuxRoot, "aaaaaaaa-c9b6-475f-8336-ebdb5b830355", 8000001, 8100000))
		}, nil, "L7"},
		{"unknown firmware", "arm", good, nil, "L0"},
	}
	for _, c := range cases {
		_, fs := check(c.fw, c.mk(), c.mod)
		if !strings.Contains(rules(fs), c.want) {
			t.Errorf("%s: findings %q, want %s", c.name, rules(fs), c.want)
		}
	}
}

func TestTheOtherFirmwaresPartitionIsNotNeeded(t *testing.T) {
	root := part(gpt.TypeLinuxRoot, uRoot, rootFirst, 8000000)
	if _, fs := check("bios", build(part(gpt.TypeBIOSBoot, uBIOS, 2048, 8191), bsPart(bsSectors), root), nil); len(fs) != 0 {
		t.Errorf("bios without ESP: %v", fs)
	}
	if _, fs := check("uefi", build(part(gpt.TypeESP, uESP, 8192, 262143), bsPart(bsSectors), root), nil); len(fs) != 0 {
		t.Errorf("uefi without BIOS Boot Partition: %v", fs)
	}
}

func TestNamingAPartitionOverridesTheTypeSearch(t *testing.T) {
	bios := part(gpt.TypeBIOSBoot, uBIOS, 2048, 8191)
	esp := part(gpt.TypeESP, uESP, 8192, 262143)
	d := build(bios, esp, bsPart(bsSectors),
		part(gpt.TypeBootStorage, "9d11fdb7-c9b6-475f-8336-ebdb5b830355", rootFirst, 4456447),
		part(gpt.TypeLinuxRoot, uRoot, 4456448, 8000000))
	l, fs := check("both", d, func(r *Request) { r.BootStoragePartUUID = uBS })
	if len(fs) != 0 {
		t.Fatalf("findings: %v", fs)
	}
	if l.BootStorage.UUID != uBS {
		t.Errorf("boot storage %s", l.BootStorage.UUID)
	}
}

func TestBIOSNeedsFiveHundredTwelveByteSectors(t *testing.T) {
	// A disk with 4096-byte sectors, partitions given in 4 KiB sectors.
	parts := []gpttest.Part{
		part(gpt.TypeBIOSBoot, uBIOS, 256, 1023),
		part(gpt.TypeESP, uESP, 1024, 32767),
		part(gpt.TypeBootStorage, uBS, 32768, 32768+262144-1),
		part(gpt.TypeLinuxRoot, uRoot, 294912, 1000000),
	}
	mk := func() *gpttest.Disk {
		d := gpttest.ImageSS(parts, 1048576, 4096)
		fat(d, 1024*4096, 253952, 1)
		fat(d, 32768*4096, 2097152, 2)
		ext(d, 294912*4096)
		return d
	}
	if _, fs := check("uefi", mk(), nil); len(fs) != 0 {
		t.Errorf("uefi on 4096-byte sectors: %v", fs)
	}
	for _, fw := range []string{"bios", "both"} {
		_, fs := check(fw, mk(), nil)
		if !strings.Contains(rules(fs), "L6") {
			t.Errorf("%s on 4096-byte sectors: findings %q, want L6", fw, rules(fs))
		}
	}
}

const rootInnerUUID = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"

// containerRoot is good() with the root partition holding a container
// (what is written at its start or 4 KiB in) in place of an ext4 filesystem.
func containerRoot(sig []byte, at int64) *gpttest.Disk {
	d := good()
	d.WriteAt(make([]byte, 2048), rootFirst*512+1024) // wipe the ext4 superblock
	d.WriteAt(sig, rootFirst*512+at)
	return d
}

func TestContainerRoots(t *testing.T) {
	luks := []byte("LUKS\xba\xbe\x00\x02")
	md := []byte{0xfc, 0x4e, 0x2b, 0xa9}
	lvm := append(append([]byte("LABELONE"), make([]byte, 16)...), []byte("LVM2 001")...)
	for _, c := range []struct {
		name string
		sig  []byte
		at   int64
		kind string
	}{{"LUKS", luks, 0, "LUKS"}, {"LVM", lvm, 512, "LVM"}, {"md", md, 4096, "md"}} {
		// Without the filesystem UUID the container is refused, and the finding says what to do.
		_, fs := check("both", containerRoot(c.sig, c.at), nil)
		if rules(fs) != "L7" || !strings.Contains(fs[0].Text, "--root-fs-uuid") || !strings.Contains(fs[0].Text, c.kind) {
			t.Errorf("%s without --root-fs-uuid: %v", c.name, fs)
		}
		// With it the layout is accepted and records the inner UUID, lower case.
		l, fs := check("both", containerRoot(c.sig, c.at), func(r *Request) { r.RootFSUUID = strings.ToUpper(rootInnerUUID) })
		if len(fs) != 0 {
			t.Fatalf("%s with --root-fs-uuid: %v", c.name, fs)
		}
		if l.RootUUID != rootInnerUUID || l.RootContainer != c.kind || l.Root.UUID != uRoot {
			t.Errorf("%s: RootUUID %q container %q root partition %q", c.name, l.RootUUID, l.RootContainer, l.Root.UUID)
		}
		// A malformed UUID is refused.
		_, fs = check("both", containerRoot(c.sig, c.at), func(r *Request) { r.RootFSUUID = "not-a-uuid" })
		if rules(fs) != "L7" {
			t.Errorf("%s with a malformed UUID: %v", c.name, fs)
		}
	}
}

func TestRootFSUUIDDoesNotOverrideAPlainRoot(t *testing.T) {
	// The ext4 UUID on the disk is what counts: naming another one for a plain partition is an error,
	// and naming the right one changes nothing.
	_, fs := check("both", good(), func(r *Request) { r.RootFSUUID = rootInnerUUID })
	if rules(fs) != "L7" {
		t.Errorf("a UUID that does not match the ext4 root: %v", fs)
	}
	l, fs := check("both", good(), func(r *Request) { r.RootFSUUID = "4E9F54F3-865B-43FA-8F1C-9E644D3A2B11" })
	if len(fs) != 0 || l.RootContainer != "" || l.RootUUID != "4e9f54f3-865b-43fa-8f1c-9e644d3a2b11" {
		t.Errorf("the matching UUID: %v %+v", fs, l)
	}
}

func TestUnknownRootContentIsStillRefused(t *testing.T) {
	// Btrfs, XFS and blank partitions are not containers, whatever UUID the caller names.
	d := good()
	d.WriteAt(make([]byte, 2048), rootFirst*512+1024)
	_, fs := check("both", d, func(r *Request) { r.RootFSUUID = rootInnerUUID })
	if rules(fs) != "L7" {
		t.Errorf("blank root with --root-fs-uuid: %v", fs)
	}
}
