// SPDX-License-Identifier: GPL-2.0-or-later

package menutimeout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
)

func TestValid(t *testing.T) {
	for s, want := range map[string]int{"0": 0, "5": 5, "3600": 3600} {
		if n, err := Valid(s); err != nil || n != want {
			t.Errorf("%q: %d, %v", s, n, err)
		}
	}
	for _, s := range []string{"", "-1", "3601", "05", "1.5", "x", "+3", " 3"} {
		if _, err := Valid(s); err == nil {
			t.Errorf("%q was accepted", s)
		}
	}
}

// The shipped loader.conf with timeout 0 round-trips, and another value changes
// only the timeout line.
func TestLoaderConfShipped(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "firmware", "uefi", "loader.conf"))
	if err != nil {
		t.Fatal(err)
	}
	shipped := string(data)
	if !strings.Contains(shipped, "\ntimeout 0\n") {
		t.Fatalf("the shipped loader.conf has no \"timeout 0\" line:\n%s", shipped)
	}
	if got := LoaderConf(shipped, 0); got != shipped {
		t.Errorf("timeout 0 changed the shipped file:\n%s", got)
	}
	want := strings.Replace(shipped, "\ntimeout 0\n", "\ntimeout 7\n", 1)
	if got := LoaderConf(shipped, 7); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if n, ok := ReadLoaderConf(want); !ok || n != 7 {
		t.Errorf("read back %d, %v", n, ok)
	}
}

func TestLoaderConf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"default vintner-*\ntimeout 0\neditor no\n", "default vintner-*\ntimeout 4\neditor no\n"},
		{"timeout\t10   \n", "timeout 4\n"},
		{"default x\n", "default x\ntimeout 4\n"},
		{"", "timeout 4\n"},
		{"default x", "default x\ntimeout 4\n"},
		// Later duplicates are dropped: systemd-boot uses the last.
		{"timeout 1\n# keep\ntimeout 9\n", "timeout 4\n# keep\n"},
		// A comment that mentions timeout is not the directive.
		{"# timeout 8\ntimeout 1\n", "# timeout 8\ntimeout 4\n"},
	}
	for _, c := range cases {
		if got := LoaderConf(c.in, 4); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	for text, want := range map[string]int{"timeout 3\n": 3, "timeout 1\ntimeout 6\n": 6} {
		if n, ok := ReadLoaderConf(text); !ok || n != want {
			t.Errorf("%q: %d, %v", text, n, ok)
		}
	}
	for _, text := range []string{"", "default x\n", "timeout menu-force\n", "timeout 99999\n"} {
		if _, ok := ReadLoaderConf(text); ok {
			t.Errorf("%q reads as a timeout", text)
		}
	}
}

func TestBIOS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grubenv")
	env := &grubenv.Env{}
	env.Set("VINTNER_ORDER", "2 1")
	if err := grubenv.Create(path, env); err != nil {
		t.Fatal(err)
	}
	if n, err := ReadBIOS(path); err != nil || n != 0 {
		t.Fatalf("a fresh grubenv: %d, %v", n, err)
	}
	if err := SetBIOS(path, 8); err != nil {
		t.Fatal(err)
	}
	if n, err := ReadBIOS(path); err != nil || n != 8 {
		t.Errorf("after 8: %d, %v", n, err)
	}
	e, _ := grubenv.Read(path)
	if v, _ := e.Get("VINTNER_ORDER"); v != "2 1" {
		t.Errorf("the other variables changed: VINTNER_ORDER=%q", v)
	}
	if err := SetBIOS(path, 0); err != nil {
		t.Fatal(err)
	}
	e, _ = grubenv.Read(path)
	if _, ok := e.Get(GrubVar); ok {
		t.Error("0 left the variable in the grubenv")
	}
}

func TestClearOverride(t *testing.T) {
	dir := t.TempDir()
	if had, err := ClearOverride(dir); err != nil || had {
		t.Errorf("nothing set: %v, %v", had, err)
	}
	if err := efivar.SetString(dir, "LoaderConfigTimeout", efivar.LoaderInfo, efivar.BootAttrs, "10"); err != nil {
		t.Fatal(err)
	}
	if had, err := ClearOverride(dir); err != nil || !had {
		t.Errorf("one set: %v, %v", had, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "LoaderConfigTimeout-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f")); err == nil {
		t.Error("the variable is still there")
	}
}
