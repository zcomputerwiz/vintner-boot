// SPDX-License-Identifier: GPL-2.0-or-later

package kcmdline

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse("# comment\n\nquiet   splash\nconsole=ttyS0,115200 foo=\"a\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"quiet", "splash", "console=ttyS0,115200", `foo="a"`}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, bad := range []string{"root=/dev/sda1", "root=UUID=x", "vintner.bootstorage=PARTUUID=x", "vintner.image=os-1", "vintner.variant=x", "ro", "rw quiet", "café", "a\x01b"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// Names that only look like them are fine.
	for _, ok := range []string{"rootflags=noatime", "rootwait", "vintnerx=1", "roo", "rodata=off"} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, found, err := Load(filepath.Join(dir, "missing")); found || err != nil {
		t.Errorf("a missing file: found %v, err %v", found, err)
	}
	p := filepath.Join(dir, "cmdline")
	os.WriteFile(p, []byte("quiet nomodeset\n"), 0o644)
	opts, found, err := Load(p)
	if err != nil || !found || !reflect.DeepEqual(opts, []string{"quiet", "nomodeset"}) {
		t.Errorf("got %q, %v, %v", opts, found, err)
	}
	os.WriteFile(p, []byte("quiet root=/dev/x\n"), 0o644)
	if _, found, err := Load(p); err == nil || !found || !strings.Contains(err.Error(), p) {
		t.Errorf("a bad file: found %v, err %v (it must name the file)", found, err)
	}
	os.WriteFile(p, nil, 0o644)
	if opts, found, err := Load(p); err != nil || !found || len(opts) != 0 {
		t.Errorf("an empty file: %q, %v, %v", opts, found, err)
	}
}

func TestComposeAndRebuild(t *testing.T) {
	got, err := Compose("root=UUID=U", "vintner.bootstorage=PARTUUID=B", []string{"quiet", "nomodeset"})
	if err != nil || got != "root=UUID=U ro quiet nomodeset vintner.bootstorage=PARTUUID=B" {
		t.Errorf("Compose: %q, %v", got, err)
	}
	// The earlier line's own options are replaced, its root and boot storage kept.
	got, err = Rebuild("root=PARTUUID=P ro quiet splash vintner.bootstorage=PARTUUID=B", []string{"nomodeset"})
	if err != nil || got != "root=PARTUUID=P ro nomodeset vintner.bootstorage=PARTUUID=B" {
		t.Errorf("Rebuild: %q, %v", got, err)
	}
	got, _ = Rebuild("root=PARTUUID=P ro quiet vintner.bootstorage=PARTUUID=B", nil)
	if got != "root=PARTUUID=P ro vintner.bootstorage=PARTUUID=B" {
		t.Errorf("no options: %q", got)
	}
	// Without a root or a boot storage token the line is left for the publisher to refuse.
	if got, _ := Rebuild("quiet", []string{"x"}); got != "quiet" {
		t.Errorf("a line without root: %q", got)
	}
	if _, err := Compose("root=UUID=U", "vintner.bootstorage=PARTUUID=B", []string{strings.Repeat("a", MaxLen)}); err == nil {
		t.Error("an over-long line was accepted")
	}
}
