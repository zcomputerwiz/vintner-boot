// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"os"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bootstorage"
)

// mountBootStorage returns dir when it is set (tests, the recovery image).
// Otherwise it mounts the partition the kernel command line names at
// bootstorage.MountPoint and makes osExit unmount it first, because os.Exit
// skips deferred calls.
func mountBootStorage(dir string, rw bool) (string, error) {
	if dir != "" {
		return dir, nil
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return "", err
	}
	dev, err := bootstorage.Device(string(cmdline))
	if err != nil {
		return "", err
	}
	unmount, err := bootstorage.Mount(dev, rw)
	if err != nil {
		return "", err
	}
	exit := osExit
	osExit = func(code int) {
		if err := unmount(); err != nil {
			fmt.Fprintf(os.Stderr, "vintner: unmount %s: %v\n", bootstorage.MountPoint, err)
		}
		exit(code)
	}
	return bootstorage.MountPoint, nil
}
