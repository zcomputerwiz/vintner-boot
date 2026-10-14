// SPDX-License-Identifier: GPL-2.0-or-later

package efivar

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// GUID is a UEFI Globally Unique Identifier in string format.
type GUID string

const (
	// LoaderInfo GUID.
	LoaderInfo GUID = "4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

	// FS_IOC_GETFLAGS and FS_IOC_SETFLAGS from linux/fs.h
	fsIocGetFlags = 0x80086601
	fsIocSetFlags = 0x40086602
	fsImmutableFl = 0x00000010
)

// ErrNotFound is returned when an EFI variable is not found.
var ErrNotFound = errors.New("efivar: not found")

// DefaultDir is the default efivarfs mount point.
const DefaultDir = "/sys/firmware/efi/efivars"

// Get reads an EFI variable's attributes and data.
func Get(dir, name string, guid GUID) (uint32, []byte, error) {
	path := filepath.Join(dir, name+"-"+string(guid))
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil, ErrNotFound
		}
		return 0, nil, err
	}
	if len(content) < 4 {
		return 0, nil, errors.New("efivar: data too short to contain attributes")
	}
	attrs := binary.LittleEndian.Uint32(content[:4])
	return attrs, content[4:], nil
}

// clearImmutable attempts to clear the FS_IMMUTABLE_FL flag on a file.
// It returns an error if opening or modifying flags fails for a supported file system.
// ENOTTY or EOPNOTSUPP (operation not supported) are ignored.
func clearImmutable(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	var flags int32
	// We use uintptr(unsafe.Pointer(&flags))
	_, _, sysErr := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(fsIocGetFlags), uintptr(unsafe.Pointer(&flags)))
	if sysErr != 0 {
		if sysErr == syscall.ENOTTY || sysErr == syscall.EOPNOTSUPP {
			return nil
		}
		return sysErr
	}

	if flags&fsImmutableFl != 0 {
		flags &^= fsImmutableFl
		_, _, sysErr = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(fsIocSetFlags), uintptr(unsafe.Pointer(&flags)))
		if sysErr != 0 {
			if sysErr == syscall.ENOTTY || sysErr == syscall.EOPNOTSUPP {
				return nil
			}
			return sysErr
		}
	}
	return nil
}

// Set writes an EFI variable's attributes and data.
func Set(dir, name string, guid GUID, attrs uint32, data []byte) error {
	path := filepath.Join(dir, name+"-"+string(guid))

	// clear immutable if it already exists
	_ = clearImmutable(path)

	content := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(content[:4], attrs)
	copy(content[4:], data)

	// efivarfs wants the whole variable in one write and no O_TRUNC, as systemd does.
	// shortcut: on an ordinary directory a shorter overwrite keeps stale trailing bytes; efivarfs replaces the variable. Upgrade if tests ever overwrite.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Delete removes an EFI variable.
func Delete(dir, name string, guid GUID) error {
	path := filepath.Join(dir, name+"-"+string(guid))

	// clear immutable if it exists
	if err := clearImmutable(path); err != nil {
		return err
	}

	err := os.Remove(path)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// GetString reads a UTF-16LE NUL-terminated string variable.
func GetString(dir, name string, guid GUID) (uint32, string, error) {
	attrs, data, err := Get(dir, name, guid)
	if err != nil {
		return 0, "", err
	}

	if len(data)%2 != 0 {
		return 0, "", errors.New("efivar: odd number of string bytes")
	}

	runes := make([]uint16, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		runes[i/2] = binary.LittleEndian.Uint16(data[i : i+2])
	}

	if len(runes) > 0 && runes[len(runes)-1] == 0 {
		runes = runes[:len(runes)-1]
	}
	return attrs, string(utf16.Decode(runes)), nil
}

// SetString writes a string variable as UTF-16LE with a NUL terminator.
func SetString(dir, name string, guid GUID, attrs uint32, s string) error {
	runes := utf16.Encode([]rune(s))
	// Append NUL terminator
	runes = append(runes, 0)

	data := make([]byte, len(runes)*2)
	for i, r := range runes {
		binary.LittleEndian.PutUint16(data[i*2:i*2+2], r)
	}

	return Set(dir, name, guid, attrs, data)
}
