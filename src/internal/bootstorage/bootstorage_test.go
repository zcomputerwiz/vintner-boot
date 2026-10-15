// SPDX-License-Identifier: GPL-2.0-or-later

package bootstorage

import "testing"

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
