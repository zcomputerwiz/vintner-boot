// SPDX-License-Identifier: GPL-2.0-or-later

package efivar

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unicode/utf16"
)

// Boot variables are non-volatile and readable at boot time and run time.
const BootAttrs uint32 = 0x7

const loadOptionActive uint32 = 1

// PartitionRef names a GPT partition in the device path of a boot entry.
type PartitionRef struct {
	Number    uint32
	FirstLBA  uint64
	Sectors   uint64
	Signature string // the partition's PARTUUID
}

// ParseGUID turns the text form of a GUID into its 16 byte, mixed-endian wire form.
func ParseGUID(s string) ([16]byte, error) {
	var out [16]byte
	h := strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 || len(s) != 36 {
		return out, fmt.Errorf("efivar: %q is not a GUID", s)
	}
	out[0], out[1], out[2], out[3] = b[3], b[2], b[1], b[0]
	out[4], out[5] = b[5], b[4]
	out[6], out[7] = b[7], b[6]
	copy(out[8:], b[8:])
	return out, nil
}

func utf16z(s string) []byte {
	u := append(utf16.Encode([]rune(s)), 0)
	b := make([]byte, 2*len(u))
	for i, r := range u {
		binary.LittleEndian.PutUint16(b[2*i:], r)
	}
	return b
}

// DevicePath builds the device path of a file on a GPT partition: a hard drive
// media node, the file path node, and the end node. The file path uses
// backslashes, as in \EFI\vintner\systemd-bootx64.efi.
func DevicePath(p PartitionRef, file string) ([]byte, error) {
	sig, err := ParseGUID(p.Signature)
	if err != nil {
		return nil, err
	}
	hd := make([]byte, 42)
	hd[0], hd[1] = 4, 1 // media, hard drive
	binary.LittleEndian.PutUint16(hd[2:], 42)
	binary.LittleEndian.PutUint32(hd[4:], p.Number)
	binary.LittleEndian.PutUint64(hd[8:], p.FirstLBA)
	binary.LittleEndian.PutUint64(hd[16:], p.Sectors)
	copy(hd[24:], sig[:])
	hd[40], hd[41] = 2, 2 // GPT, signature is a GUID
	name := utf16z(file)
	fp := make([]byte, 4+len(name))
	fp[0], fp[1] = 4, 4 // media, file path
	binary.LittleEndian.PutUint16(fp[2:], uint16(len(fp)))
	copy(fp[4:], name)
	end := []byte{0x7f, 0xff, 4, 0}
	return bytes.Join([][]byte{hd, fp, end}, nil), nil
}

// LoadOption encodes an EFI_LOAD_OPTION with no optional data.
func LoadOption(description string, devicePath []byte) []byte {
	desc := utf16z(description)
	b := make([]byte, 6, 6+len(desc)+len(devicePath))
	binary.LittleEndian.PutUint32(b, loadOptionActive)
	binary.LittleEndian.PutUint16(b[4:], uint16(len(devicePath)))
	b = append(b, desc...)
	return append(b, devicePath...)
}

// ParseLoadOption returns the description and device path of a load option.
func ParseLoadOption(b []byte) (description string, devicePath []byte, err error) {
	if len(b) < 6 {
		return "", nil, errors.New("efivar: load option too short")
	}
	fpl := int(binary.LittleEndian.Uint16(b[4:]))
	rest := b[6:]
	end := -1
	for i := 0; i+1 < len(rest); i += 2 {
		if rest[i] == 0 && rest[i+1] == 0 {
			end = i
			break
		}
	}
	if end < 0 || len(rest) < end+2+fpl {
		return "", nil, errors.New("efivar: malformed load option")
	}
	u := make([]uint16, end/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(rest[2*i:])
	}
	return string(utf16.Decode(u)), rest[end+2 : end+2+fpl], nil
}

func bootName(n uint16) string { return fmt.Sprintf("Boot%04X", n) }

// BootOrder reads the BootOrder variable; a missing one is an empty order.
func BootOrder(dir string) ([]uint16, error) {
	_, data, err := Get(dir, "BootOrder", GlobalVariable)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data)%2 != 0 {
		return nil, errors.New("efivar: BootOrder has an odd length")
	}
	o := make([]uint16, len(data)/2)
	for i := range o {
		o[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	return o, nil
}

func setBootOrder(dir string, o []uint16) error {
	b := make([]byte, 2*len(o))
	for i, n := range o {
		binary.LittleEndian.PutUint16(b[2*i:], n)
	}
	return Set(dir, "BootOrder", GlobalVariable, BootAttrs, b)
}

// BootEntry is a Boot#### variable.
type BootEntry struct {
	Number      uint16
	Description string
	DevicePath  []byte
}

// BootEntries lists the Boot#### variables in dir.
func BootEntries(dir string) ([]BootEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []BootEntry
	for _, de := range des {
		name := de.Name()
		var n uint16
		if len(name) < 8 || !strings.HasPrefix(name, "Boot") {
			continue
		}
		if _, err := fmt.Sscanf(name[4:8], "%04X", &n); err != nil || name[8:] != "-"+string(GlobalVariable) || bootName(n) != name[:8] {
			continue
		}
		_, data, err := Get(dir, name[:8], GlobalVariable)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		desc, dp, err := ParseLoadOption(data)
		if err != nil {
			continue // not ours to judge: another system's malformed entry
		}
		out = append(out, BootEntry{Number: n, Description: desc, DevicePath: dp})
	}
	return out, nil
}

// SetFirstBootEntry makes the entry with this description and device path the
// first in BootOrder. An entry that already has both is reused (running it
// again changes nothing else); otherwise the lowest free Boot#### number is
// taken. Other entries stay in BootOrder after it, in their order. It returns
// the entry number and whether a new variable was written.
func SetFirstBootEntry(dir, description string, devicePath []byte) (uint16, bool, error) {
	entries, err := BootEntries(dir)
	if err != nil {
		return 0, false, err
	}
	var num uint16
	found, used := false, map[uint16]bool{}
	for _, e := range entries {
		used[e.Number] = true
		if !found && e.Description == description && bytes.Equal(e.DevicePath, devicePath) {
			num, found = e.Number, true
		}
	}
	created := false
	if !found {
		for used[num] {
			num++
			if num == 0xffff {
				return 0, false, errors.New("efivar: no free Boot#### number")
			}
		}
		if err := Set(dir, bootName(num), GlobalVariable, BootAttrs, LoadOption(description, devicePath)); err != nil {
			return 0, false, err
		}
		created = true
	}
	order, err := BootOrder(dir)
	if err != nil {
		return num, created, err
	}
	next := []uint16{num}
	for _, n := range order {
		if n != num {
			next = append(next, n)
		}
	}
	if len(order) == len(next) && order[0] == num {
		return num, created, nil
	}
	return num, created, setBootOrder(dir, next)
}

const (
	// VintnerDescription is the description of the Boot#### entry vintner creates.
	VintnerDescription = "Vintner Boot"
	// VintnerLoaderPath is the file that entry starts, on the ESP.
	VintnerLoaderPath = `\EFI\vintner\systemd-bootx64.efi`
)

// FindEntry returns the first entry with this description whose device path
// ends in the file path node of file.
func FindEntry(entries []BootEntry, description, file string) (BootEntry, bool) {
	want := utf16z(file)
	for _, e := range entries {
		if e.Description != description {
			continue
		}
		if i := bytes.Index(e.DevicePath, want); i >= 4 && e.DevicePath[i-4] == 4 && e.DevicePath[i-3] == 4 {
			return e, true
		}
	}
	return BootEntry{}, false
}

// BootOrderReport says what is wrong with vintner's place in the firmware boot
// order, or "" when the vintner entry exists and BootOrder starts with it.
func BootOrderReport(dir string) (string, error) {
	entries, err := BootEntries(dir)
	if err != nil {
		return "", err
	}
	e, ok := FindEntry(entries, VintnerDescription, VintnerLoaderPath)
	if !ok {
		return fmt.Sprintf("NVRAM boot entry missing: no Boot#### entry %q starts %s; vintner boot-entry --disk <disk> creates it", VintnerDescription, VintnerLoaderPath), nil
	}
	order, err := BootOrder(dir)
	if err != nil {
		return "", err
	}
	if len(order) > 0 && order[0] == e.Number {
		return "", nil
	}
	var parts []string
	for _, n := range order {
		parts = append(parts, fmt.Sprintf("%04X", n))
	}
	return fmt.Sprintf("BootOrder does not start with the vintner entry Boot%04X (BootOrder: %s); vintner boot-entry --disk <disk> puts it first", e.Number, strings.Join(parts, ",")), nil
}
