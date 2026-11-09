// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
)

// BootEntryDevicePath is the device path of vintner's loader on the ESP p: the
// partition by number, start, size and PARTUUID, then \EFI\vintner\systemd-bootx64.efi.
func BootEntryDevicePath(p gpt.Partition) ([]byte, error) {
	return efivar.DevicePath(efivar.PartitionRef{
		Number: uint32(p.Number), FirstLBA: p.FirstLBA, Sectors: p.LastLBA - p.FirstLBA + 1, Signature: p.UUID,
	}, efivar.VintnerLoaderPath)
}

// SetBootEntry creates (or reuses) vintner's Boot#### entry for the ESP p and
// puts it first in BootOrder. espDir, when set, is the mounted ESP: the loader
// must exist on it, because an entry for a missing file only makes the firmware
// fall through to the next one. It is the explicit command the health report
// names (vintner boot-entry), and what the installer's takeover does too.
func SetBootEntry(efivarDir, espDir string, p gpt.Partition) (num uint16, created bool, err error) {
	if espDir != "" {
		if _, err := os.Stat(filepath.Join(espDir, "EFI", "vintner", "systemd-bootx64.efi")); err != nil {
			return 0, false, fmt.Errorf("vintner's loader is not on the ESP: %w", err)
		}
	}
	dp, err := BootEntryDevicePath(p)
	if err != nil {
		return 0, false, err
	}
	return efivar.SetFirstBootEntry(efivarDir, efivar.VintnerDescription, dp)
}
