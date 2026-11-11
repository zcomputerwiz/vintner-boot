// SPDX-License-Identifier: GPL-2.0-or-later

package bootstorage

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestDevice(t *testing.T) {
	for cmdline, want := range map[string]string{
		"root=UUID=x ro vintner.bootstorage=PARTUUID=9D8C7B6A-5F4E-4D3C-2B1A-0E9F8D7C6B5A quiet": "/dev/disk/by-partuuid/9d8c7b6a-5f4e-4d3c-2b1a-0e9f8d7c6b5a",
		"root=UUID=x ro quiet":                  "",
		"vintner.bootstorage=PARTUUID= quiet":   "",
		"vintner.bootstorage=UUID=1234-ABCD ro": "",
	} {
		got, err := Device(cmdline)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("Device(%q) = %q, %v; want %q", cmdline, got, err, want)
		}
	}
}

func TestESPDevice(t *testing.T) {
	if got, err := ESPDevice("ABCD-1234"); err != nil || got != "/dev/disk/by-partuuid/abcd-1234" {
		t.Errorf("ESPDevice = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "a b"} {
		if _, err := ESPDevice(bad); err == nil {
			t.Errorf("ESPDevice(%q) succeeded", bad)
		}
	}
}

// A mount that meets another holder of the same file system waits for it.
func TestMountRetriesWhileBusy(t *testing.T) {
	defer func(m func(string, string, string, uintptr, string) error, s func(time.Duration)) {
		mountFn, sleepFn = m, s
	}(mountFn, sleepFn)
	var slept time.Duration
	sleepFn = func(d time.Duration) { slept += d }

	calls := 0
	mountFn = func(string, string, string, uintptr, string) error {
		calls++
		if calls < 4 {
			return syscall.EBUSY
		}
		return nil
	}
	if err := mountRetry("/dev/x", "/mnt", "vfat", 0, ""); err != nil || calls != 4 || slept != 3*mountWait {
		t.Errorf("err %v after %d calls, slept %v; want success on the 4th", err, calls, slept)
	}

	calls = 0
	mountFn = func(string, string, string, uintptr, string) error { calls++; return syscall.EBUSY }
	if err := mountRetry("/dev/x", "/mnt", "vfat", 0, ""); !errors.Is(err, syscall.EBUSY) || calls != mountTries {
		t.Errorf("a mount that stays busy: err %v after %d calls, want EBUSY after %d", err, calls, mountTries)
	}

	// Any other error is final.
	calls = 0
	mountFn = func(string, string, string, uintptr, string) error { calls++; return syscall.EPERM }
	if err := mountRetry("/dev/x", "/mnt", "vfat", 0, ""); !errors.Is(err, syscall.EPERM) || calls != 1 {
		t.Errorf("EPERM: err %v after %d calls, want one call", err, calls)
	}
}
