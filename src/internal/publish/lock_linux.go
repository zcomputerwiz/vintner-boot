// SPDX-License-Identifier: GPL-2.0-or-later
//go:build linux

package publish

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// FileLock holds an open file with an exclusive flock.
type FileLock struct {
	file *os.File
}

// AcquireLock creates parent directories and acquires an exclusive flock on lockPath.
func AcquireLock(lockPath string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &FileLock{file: f}, nil
}

// Close unlocks and closes the lock file.
func (l *FileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}
