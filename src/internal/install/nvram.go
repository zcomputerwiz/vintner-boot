// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
)

// BootEntryDevicePath is the device path of a file on the ESP p: the partition
// by number, start, size and PARTUUID, then the file (efivar.VintnerEntryPath).
func BootEntryDevicePath(p gpt.Partition, file string) ([]byte, error) {
	return efivar.DevicePath(efivar.PartitionRef{
		Number: uint32(p.Number), FirstLBA: p.FirstLBA, Sectors: p.LastLBA - p.FirstLBA + 1, Signature: p.UUID,
	}, file)
}

// EntryPathOnESP is the file vintner's boot entry must start on the mounted
// ESP: shim when vintner installed it there (Secure Boot tier B), else
// systemd-boot.
func EntryPathOnESP(espDir string) string {
	if _, err := os.Stat(filepath.Join(espDir, "EFI", "vintner", "shimx64.efi")); err == nil {
		return efivar.VintnerShimPath
	}
	return efivar.VintnerLoaderPath
}

// SetBootEntry creates (or reuses) vintner's Boot#### entry for file on the ESP
// p and puts it first in BootOrder. espDir, when set, is the mounted ESP: the
// file must exist on it, because an entry for a missing file only makes the
// firmware fall through to the next one. It is the explicit command the health
// report names (vintner boot-entry).
func SetBootEntry(efivarDir, espDir string, p gpt.Partition, file string) (num uint16, created bool, err error) {
	if espDir != "" {
		rel := filepath.FromSlash(strings.ReplaceAll(strings.TrimPrefix(file, `\`), `\`, "/"))
		if _, err := os.Stat(filepath.Join(espDir, rel)); err != nil {
			return 0, false, fmt.Errorf("vintner's loader is not on the ESP: %w", err)
		}
	}
	dp, err := BootEntryDevicePath(p, file)
	if err != nil {
		return 0, false, err
	}
	return efivar.SetFirstBootEntry(efivarDir, efivar.VintnerDescription, dp)
}
