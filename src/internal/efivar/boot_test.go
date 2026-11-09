// SPDX-License-Identifier: GPL-2.0-or-later

package efivar

import (
	"bytes"
	"encoding/binary"
	"testing"
)

var testPart = PartitionRef{Number: 2, FirstLBA: 4096, Sectors: 262144, Signature: "11223344-5566-7788-99aa-bbccddeeff00"}

func TestParseGUIDMixedEndian(t *testing.T) {
	g, err := ParseGUID(testPart.Signature)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x44, 0x33, 0x22, 0x11, 0x66, 0x55, 0x88, 0x77, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00}
	if !bytes.Equal(g[:], want) {
		t.Fatalf("got % x want % x", g, want)
	}
	if _, err := ParseGUID("nope"); err == nil {
		t.Fatal("a bad GUID was accepted")
	}
}

func TestDevicePathLayout(t *testing.T) {
	dp, err := DevicePath(testPart, `\EFI\vintner\a.efi`)
	if err != nil {
		t.Fatal(err)
	}
	if dp[0] != 4 || dp[1] != 1 || binary.LittleEndian.Uint16(dp[2:]) != 42 {
		t.Fatalf("bad hard drive node: % x", dp[:4])
	}
	if binary.LittleEndian.Uint32(dp[4:]) != 2 || binary.LittleEndian.Uint64(dp[8:]) != 4096 || binary.LittleEndian.Uint64(dp[16:]) != 262144 {
		t.Fatal("partition number, start or size wrong")
	}
	if dp[40] != 2 || dp[41] != 2 {
		t.Fatal("not a GPT signature")
	}
	if dp[42] != 4 || dp[43] != 4 {
		t.Fatal("file path node missing")
	}
	fpLen := int(binary.LittleEndian.Uint16(dp[44:]))
	if want := 4 + 2*(len(`\EFI\vintner\a.efi`)+1); fpLen != want {
		t.Fatalf("file path node length %d, want %d", fpLen, want)
	}
	if !bytes.Equal(dp[len(dp)-4:], []byte{0x7f, 0xff, 4, 0}) {
		t.Fatal("no end node")
	}
}

func TestLoadOptionRoundTrip(t *testing.T) {
	dp, _ := DevicePath(testPart, `\EFI\vintner\a.efi`)
	desc, got, err := ParseLoadOption(LoadOption("Vintner", dp))
	if err != nil || desc != "Vintner" || !bytes.Equal(got, dp) {
		t.Fatalf("round trip: %q %v", desc, err)
	}
	if _, _, err := ParseLoadOption([]byte{1, 0}); err == nil {
		t.Fatal("a short option was accepted")
	}
}

func TestSetFirstBootEntry(t *testing.T) {
	dir := t.TempDir()
	other := LoadOption("Windows", []byte{0x7f, 0xff, 4, 0})
	if err := Set(dir, "Boot0000", GlobalVariable, BootAttrs, other); err != nil {
		t.Fatal(err)
	}
	if err := setBootOrder(dir, []uint16{0}); err != nil {
		t.Fatal(err)
	}
	dp, _ := DevicePath(testPart, `\EFI\vintner\a.efi`)

	n, created, err := SetFirstBootEntry(dir, "Vintner", dp)
	if err != nil || !created || n != 1 {
		t.Fatalf("first call: n=%d created=%v err=%v", n, created, err)
	}
	order, _ := BootOrder(dir)
	if len(order) != 2 || order[0] != 1 || order[1] != 0 {
		t.Fatalf("BootOrder %v: vintner must be first, Windows kept after it", order)
	}
	// the other system's entry is untouched
	if _, data, err := Get(dir, "Boot0000", GlobalVariable); err != nil || !bytes.Equal(data, other) {
		t.Fatal("another system's entry was changed")
	}

	// again: same entry, nothing new
	n2, created2, err := SetFirstBootEntry(dir, "Vintner", dp)
	if err != nil || created2 || n2 != 1 {
		t.Fatalf("second call: n=%d created=%v err=%v", n2, created2, err)
	}
	// firmware moved Windows first: vintner goes back to the front, without a duplicate
	if err := setBootOrder(dir, []uint16{0, 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SetFirstBootEntry(dir, "Vintner", dp); err != nil {
		t.Fatal(err)
	}
	order, _ = BootOrder(dir)
	if len(order) != 2 || order[0] != 1 {
		t.Fatalf("BootOrder %v after the firmware reordered it", order)
	}
}

func TestSetFirstBootEntryNoBootOrder(t *testing.T) {
	dir := t.TempDir()
	dp, _ := DevicePath(testPart, `\EFI\vintner\a.efi`)
	if _, _, err := SetFirstBootEntry(dir, "Vintner", dp); err != nil {
		t.Fatal(err)
	}
	if order, _ := BootOrder(dir); len(order) != 1 || order[0] != 0 {
		t.Fatalf("BootOrder %v", order)
	}
}
