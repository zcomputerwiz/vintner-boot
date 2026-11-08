// SPDX-License-Identifier: GPL-2.0-or-later

package gpt

import (
	"errors"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt/gpttest"
)

func sample() []gpttest.Part {
	return []gpttest.Part{
		{Type: TypeBIOSBoot, UUID: "2B0F98D3-E62A-48B4-B260-DFDFDA5B8B3F", FirstLBA: 2048, LastLBA: 8191, Name: "bios"},
		{Type: TypeESP, UUID: "B558256E-E822-4787-9BD2-C4C47DFB54D9", FirstLBA: 8192, LastLBA: 262143, Name: "ESP"},
		{Type: TypeBootStorage, UUID: "2D11FDB7-C9B6-475F-8336-EBDB5B830355", FirstLBA: 262144, LastLBA: 4456447, Attrs: 1 << 63, Name: "bootstorage"},
		{Type: TypeLinuxRoot, UUID: "FD202E72-0241-4C3B-953D-FFE506AE6CFC", FirstLBA: 4456448, LastLBA: 8000000, Name: "root"},
	}
}

func TestReadAtParsesPartitions(t *testing.T) {
	d := gpttest.Image(sample(), 8388608)
	tab, err := ReadAt(d, d.Size)
	if err != nil {
		t.Fatal(err)
	}
	if tab.SectorSize != 512 || len(tab.Partitions) != 4 {
		t.Fatalf("got sector size %d and %d partitions", tab.SectorSize, len(tab.Partitions))
	}
	bs := tab.ByType(TypeBootStorage)
	if len(bs) != 1 {
		t.Fatalf("boot storage partitions: %d", len(bs))
	}
	p := bs[0]
	if p.Number != 3 || p.UUID != "2d11fdb7-c9b6-475f-8336-ebdb5b830355" {
		t.Errorf("boot storage partition: %+v", p)
	}
	if p.Attrs != 1<<63 || p.Name != "bootstorage" {
		t.Errorf("attrs/name: %+v", p)
	}
	if got := p.Size(512); got != int64(4456447-262144+1)*512 {
		t.Errorf("size %d", got)
	}
	if got := p.Offset(512); got != 262144*512 {
		t.Errorf("offset %d", got)
	}
	if _, ok := tab.ByUUID("FD202E72-0241-4C3B-953D-FFE506AE6CFC"); !ok {
		t.Error("ByUUID is case sensitive")
	}
}

func TestReadAtRefusesWhatIsNotAPlainGPT(t *testing.T) {
	cases := map[string]func() *gpttest.Disk{
		"blank disk": func() *gpttest.Disk { return gpttest.New(1 << 20) },
		"mbr beside gpt": func() *gpttest.Disk {
			d := gpttest.Image(sample(), 8388608)
			gpttest.WithMBRPartition(d, 0x83)
			return d
		},
		"corrupt entry array": func() *gpttest.Disk {
			d := gpttest.Image(sample(), 8388608)
			gpttest.CorruptEntry(d)
			return d
		},
	}
	for name, mk := range cases {
		d := mk()
		if _, err := ReadAt(d, d.Size); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	d := gpttest.New(1 << 20)
	if _, err := ReadAt(d, d.Size); !errors.Is(err, ErrNotGPT) {
		t.Errorf("blank disk: %v, want ErrNotGPT", err)
	}
}

func TestReadAtFindsA4096ByteSectorTable(t *testing.T) {
	d := gpttest.ImageSS([]gpttest.Part{
		{Type: TypeBootStorage, UUID: "2D11FDB7-C9B6-475F-8336-EBDB5B830355", FirstLBA: 256, LastLBA: 262399},
	}, 1048576, 4096)
	tab, err := ReadAt(d, d.Size)
	if err != nil {
		t.Fatal(err)
	}
	if tab.SectorSize != 4096 || len(tab.Partitions) != 1 || tab.Partitions[0].Offset(4096) != 256*4096 {
		t.Errorf("table %+v", tab)
	}
}
