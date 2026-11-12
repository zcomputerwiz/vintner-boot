// SPDX-License-Identifier: GPL-2.0-or-later

package bootstorage

import (
	"errors"
	"os"
	"path/filepath"
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

const mountinfoRW = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
30 22 0:28 / /efi rw,relatime shared:9 - autofs systemd-1 rw,fd=33
31 30 8:2 / /efi rw,nosuid,nodev,noexec,relatime shared:10 - vfat /dev/vda2 rw,fmask=0077
32 22 8:3 /sub /other rw,relatime - vfat /dev/sda3 rw
33 22 8:5 / /mnt/with\040space rw,relatime - vfat /dev/sda5 rw
`

func TestExistingMount(t *testing.T) {
	for _, c := range []struct{ num, target, want string }{
		{"8:2", "/run/vintner/esp", "/efi"},            // the vfat mount, not the autofs on top of the same path
		{"8:2", "/efi", ""},                            // never the target itself
		{"8:3", "/run/vintner/esp", ""},                // a subdirectory of the file system, not the whole of it
		{"8:5", "/run/vintner/esp", "/mnt/with space"}, // octal escapes
		{"8:9", "/run/vintner/esp", ""},                // not mounted
		{"8:1", "/run/vintner/esp", ""},                // not vfat
	} {
		if got := existingMount(mountinfoRW, c.num, c.target); got != c.want {
			t.Errorf("existingMount(%s, %s) = %q, want %q", c.num, c.target, got, c.want)
		}
	}
}

// A read-only mount of a file system that something else holds read-write is
// refused with EBUSY for good. vintner binds that mount and makes the bind
// read-only, and does not touch the other mount.
func TestMountReadOnlyBindsAnExistingReadWriteMount(t *testing.T) {
	defer func(m func(string, string, string, uintptr, string) error, s func(time.Duration), p string, d func(string) (string, error)) {
		mountFn, sleepFn, mountInfoPath, devNumberFn = m, s, p, d
	}(mountFn, sleepFn, mountInfoPath, devNumberFn)
	sleepFn = func(time.Duration) {}
	devNumberFn = func(string) (string, error) { return "8:2", nil }
	mountInfoPath = filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(mountInfoPath, []byte(mountinfoRW), 0o600); err != nil {
		t.Fatal(err)
	}
	type call struct {
		src, target, fstype string
		flags               uintptr
	}
	var calls []call
	mountFn = func(src, target, fstype string, flags uintptr, _ string) error {
		calls = append(calls, call{src, target, fstype, flags})
		if fstype == "vfat" {
			return syscall.EBUSY
		}
		return nil
	}
	if err := mountReadOnly("/dev/disk/by-partuuid/x", "/run/vintner/esp", syscall.MS_NOSUID, "umask=0077"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("%d mount calls, want 3 (try, bind, remount): %+v", len(calls), calls)
	}
	if b := calls[1]; b.src != "/efi" || b.target != "/run/vintner/esp" || b.flags != syscall.MS_BIND {
		t.Errorf("bind call %+v, want /efi bound at the target", b)
	}
	if r := calls[2]; r.target != "/run/vintner/esp" || r.src != "" ||
		r.flags&(syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY) != syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY {
		t.Errorf("remount call %+v, want a read-only bind remount of the target only", r)
	}

	// Nothing to bind: it waits like any busy mount, then reports EBUSY.
	if err := os.WriteFile(mountInfoPath, []byte("22 1 8:1 / / rw - ext4 /dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls = nil
	if err := mountReadOnly("/dev/x", "/run/vintner/esp", 0, ""); !errors.Is(err, syscall.EBUSY) || len(calls) != mountTries {
		t.Errorf("no mount to bind: err %v after %d calls, want EBUSY after %d", err, len(calls), mountTries)
	}
}
