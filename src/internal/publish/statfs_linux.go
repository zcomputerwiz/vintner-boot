// SPDX-License-Identifier: GPL-2.0-or-later
//go:build linux

package publish

import "syscall"

func statfsFreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
