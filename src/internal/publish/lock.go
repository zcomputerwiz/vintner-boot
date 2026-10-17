// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"os"
	"path/filepath"
)

// DefaultLockPath returns the path to the publish lock file.
// On live systems, this is /run/vintner/publish.lock.
// In test environments where bootStorageDir is provided and /run/vintner is not writable,
// it falls back to a lock file in bootStorageDir.
func DefaultLockPath(bootStorageDir string) string {
	const defaultPath = "/run/vintner/publish.lock"
	if bootStorageDir == "" {
		return defaultPath
	}
	dir := filepath.Dir(defaultPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return filepath.Join(bootStorageDir, "publish.lock")
	}
	f, err := os.OpenFile(defaultPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return filepath.Join(bootStorageDir, "publish.lock")
	}
	_ = f.Close()
	return defaultPath
}
