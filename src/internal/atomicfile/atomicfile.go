// SPDX-License-Identifier: GPL-2.0-or-later

// Package atomicfile replaces a file so that a reader, or a power cut, sees the
// old content or the new and never a mixture.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces path with data: a temporary file in the same directory (the
// same file system, so the rename is one), synced, then renamed over path. The
// directory must exist.
func Write(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Best effort: a FAT file system, which the ESP is, refuses a mode that its
	// mount options do not allow.
	_ = tmp.Chmod(perm)
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
