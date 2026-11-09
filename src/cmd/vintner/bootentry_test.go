// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
)

func TestBootEntryCreatesAndChecks(t *testing.T) {
	disk := installDisk(t, true)
	vars := t.TempDir()
	esp := t.TempDir()
	args := []string{"--disk", disk, "--efivars", vars, "--esp", esp}

	var out, errb bytes.Buffer
	// the loader is not on the ESP: refused, nothing written
	if rc := runBootEntry(args, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "not on the ESP") {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	if ents, _ := efivar.BootEntries(vars); len(ents) != 0 {
		t.Fatal("an entry was written for a loader that is not there")
	}

	os.MkdirAll(filepath.Join(esp, "EFI", "vintner"), 0o755)
	os.WriteFile(filepath.Join(esp, "EFI/vintner/systemd-bootx64.efi"), []byte("loader"), 0o644)

	out.Reset()
	if rc := runBootEntry([]string{"--disk", disk, "--efivars", vars, "--check"}, &out, &errb); rc != 1 || !strings.Contains(out.String(), "entry missing") {
		t.Fatalf("check before: rc %d %s", rc, out.String())
	}
	out.Reset()
	if rc := runBootEntry(args, &out, &errb); rc != 0 || !strings.Contains(out.String(), "created Boot0000") {
		t.Fatalf("create: rc %d %s %s", rc, out.String(), errb.String())
	}
	out.Reset()
	if rc := runBootEntry([]string{"--disk", disk, "--efivars", vars, "--check"}, &out, &errb); rc != 0 {
		t.Fatalf("check after: rc %d %s", rc, out.String())
	}
	// firmware puts something in front: --check says so, the command repairs it
	efivar.Set(vars, "BootOrder", efivar.GlobalVariable, efivar.BootAttrs, []byte{5, 0, 0, 0})
	out.Reset()
	if rc := runBootEntry([]string{"--disk", disk, "--efivars", vars, "--check"}, &out, &errb); rc != 1 || !strings.Contains(out.String(), "does not start") {
		t.Fatalf("check reordered: rc %d %s", rc, out.String())
	}
	out.Reset()
	if rc := runBootEntry(args, &out, &errb); rc != 0 || !strings.Contains(out.String(), "is first in BootOrder") {
		t.Fatalf("repair: rc %d %s", rc, out.String())
	}
	if o, _ := efivar.BootOrder(vars); len(o) != 2 || o[0] != 0 || o[1] != 5 {
		t.Fatalf("BootOrder %v", o)
	}
}
