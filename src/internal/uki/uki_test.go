// SPDX-License-Identifier: GPL-2.0-or-later

package uki

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeMinimalPE(path string, osrel, cmdline, uname []byte, pad bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// DOS Header (64 bytes)
	dosHeader := make([]byte, 64)
	dosHeader[0] = 'M'
	dosHeader[1] = 'Z'
	binary.LittleEndian.PutUint32(dosHeader[60:], 64) // e_lfanew
	if _, err := f.Write(dosHeader); err != nil {
		return err
	}

	// PE Header (4 bytes)
	if _, err := f.Write([]byte("PE\x00\x00")); err != nil {
		return err
	}

	// FileHeader (20 bytes)
	fileHeader := make([]byte, 20)
	binary.LittleEndian.PutUint16(fileHeader[0:], 0x8664) // Machine = AMD64

	numSec := 0
	if osrel != nil {
		numSec++
	}
	if cmdline != nil {
		numSec++
	}
	if uname != nil {
		numSec++
	}

	binary.LittleEndian.PutUint16(fileHeader[2:], uint16(numSec)) // NumberOfSections
	optHeaderSize := uint16(240)
	binary.LittleEndian.PutUint16(fileHeader[16:], optHeaderSize) // SizeOfOptionalHeader
	binary.LittleEndian.PutUint16(fileHeader[18:], 0x0200)        // Characteristics
	if _, err := f.Write(fileHeader); err != nil {
		return err
	}

	// Optional header (fake)
	optHeader := make([]byte, optHeaderSize)
	binary.LittleEndian.PutUint16(optHeader[0:], 0x20b) // PE32+
	binary.LittleEndian.PutUint32(optHeader[108:], 16)  // NumberOfRvaAndSizes
	if _, err := f.Write(optHeader); err != nil {
		return err
	}

	var offset uint32 = 64 + 4 + 20 + uint32(optHeaderSize) + uint32(numSec*40) // End of headers

	type section struct {
		name string
		data []byte
	}
	var sections []section
	if osrel != nil {
		sections = append(sections, section{".osrel", osrel})
	}
	if cmdline != nil {
		sections = append(sections, section{".cmdline", cmdline})
	}
	if uname != nil {
		sections = append(sections, section{".uname", uname})
	}

	// Write Section Headers (40 bytes each)
	for _, sec := range sections {
		secHeader := make([]byte, 40)
		copy(secHeader[0:8], sec.name)

		size := uint32(len(sec.data))
		rawSize := size
		if pad {
			rawSize += 5 // simulate padding
		}

		binary.LittleEndian.PutUint32(secHeader[8:], size)     // VirtualSize
		binary.LittleEndian.PutUint32(secHeader[12:], offset)  // VirtualAddress (fake)
		binary.LittleEndian.PutUint32(secHeader[16:], rawSize) // SizeOfRawData
		binary.LittleEndian.PutUint32(secHeader[20:], offset)  // PointerToRawData

		if _, err := f.Write(secHeader); err != nil {
			return err
		}
		offset += rawSize
	}

	// Write Data
	for _, sec := range sections {
		if _, err := f.Write(sec.data); err != nil {
			return err
		}
		if pad {
			if _, err := f.Write(make([]byte, 5)); err != nil {
				return err
			}
		}
	}

	return nil
}

func TestRead(t *testing.T) {
	pePath := filepath.Join("testdata", "test.pe")

	osrelData := []byte("ID=test\nIMAGE_ID=test-img\nVERSION_ID=1.0\nIMAGE_VERSION=1.1\nKEY1=\"val1\"\nKEY2='val2\\''\n")
	cmdlineData := []byte("console=ttyS0")
	unameData := []byte("6.1.0-amd64")

	info, err := Read(pePath)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(info.OSRel, osrelData) {
		t.Errorf("OSRel mismatch: got %q, want %q", info.OSRel, osrelData)
	}
	if !bytes.Equal(info.Cmdline, cmdlineData) {
		t.Errorf("Cmdline mismatch: got %q, want %q", info.Cmdline, cmdlineData)
	}
	if !bytes.Equal(info.Uname, unameData) {
		t.Errorf("Uname mismatch: got %q, want %q", info.Uname, unameData)
	}

	expectedOSRelease := map[string]string{
		"ID":            "test",
		"IMAGE_ID":      "test-img",
		"VERSION_ID":    "1.0",
		"IMAGE_VERSION": "1.1",
		"KEY1":          "val1",
		"KEY2":          "val2'",
	}
	if !reflect.DeepEqual(info.OSRelease, expectedOSRelease) {
		t.Errorf("OSRelease mismatch: got %v, want %v", info.OSRelease, expectedOSRelease)
	}
}

func TestRead_MissingSections(t *testing.T) {
	dir := t.TempDir()
	pePath := filepath.Join(dir, "empty.efi")

	// Create PE with no sections
	if err := writeMinimalPE(pePath, nil, nil, nil, false); err != nil {
		t.Fatalf("writeMinimalPE failed: %v", err)
	}

	info, err := Read(pePath)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(info.OSRelease) != 0 {
		t.Errorf("expected empty OSRelease, got %v", info.OSRelease)
	}
	if len(info.OSRel) != 0 {
		t.Errorf("expected empty OSRel, got %q", info.OSRel)
	}
	if len(info.Cmdline) != 0 {
		t.Errorf("expected empty Cmdline, got %q", info.Cmdline)
	}
	if len(info.Uname) != 0 {
		t.Errorf("expected empty Uname, got %q", info.Uname)
	}
}

func TestSortKey(t *testing.T) {
	tests := []struct {
		name      string
		osRelease map[string]string
		expected  string
	}{
		{
			name:      "image_id present",
			osRelease: map[string]string{"IMAGE_ID": "img1", "ID": "base1"},
			expected:  "img1",
		},
		{
			name:      "image_id absent, id present",
			osRelease: map[string]string{"ID": "base1"},
			expected:  "base1",
		},
		{
			name:      "both absent",
			osRelease: map[string]string{},
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &Info{OSRelease: tt.osRelease}
			if got := info.SortKey(); got != tt.expected {
				t.Errorf("SortKey() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		name      string
		osRelease map[string]string
		expected  string
	}{
		{
			name:      "all present",
			osRelease: map[string]string{"IMAGE_VERSION": "1", "VERSION": "2", "VERSION_ID": "3", "BUILD_ID": "4"},
			expected:  "1",
		},
		{
			name:      "version present",
			osRelease: map[string]string{"VERSION": "2", "VERSION_ID": "3", "BUILD_ID": "4"},
			expected:  "2",
		},
		{
			name:      "version_id present",
			osRelease: map[string]string{"VERSION_ID": "3", "BUILD_ID": "4"},
			expected:  "3",
		},
		{
			name:      "build_id present",
			osRelease: map[string]string{"BUILD_ID": "4"},
			expected:  "4",
		},
		{
			name:      "none present",
			osRelease: map[string]string{},
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &Info{OSRelease: tt.osRelease}
			if got := info.Version(); got != tt.expected {
				t.Errorf("Version() = %v, want %v", got, tt.expected)
			}
		})
	}
}
