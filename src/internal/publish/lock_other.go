// SPDX-License-Identifier: GPL-2.0-or-later
//go:build !linux

package publish

import (
	"os"
	"path/filepath"
)

// FileLock holds an open file on non-Linux platforms.
type FileLock struct {
	file *os.File
}

// AcquireLock creates parent directories and opens lockPath.
func AcquireLock(lockPath string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return &FileLock{file: f}, nil
}

// Close closes the lock file.
func (l *FileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
