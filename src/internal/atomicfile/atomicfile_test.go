// SPDX-License-Identifier: GPL-2.0-or-later

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.conf")
	if err := Write(p, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	info, _ := os.Stat(p)
	if string(got) != "two\n" || info.Mode().Perm() != 0o600 {
		t.Errorf("%q, %v", got, info.Mode())
	}
	// No temporary file stays behind.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("the directory holds %d files", len(ents))
	}
	if err := Write(filepath.Join(dir, "missing", "f"), nil, 0o644); err == nil {
		t.Error("a missing directory was accepted")
	}
}
