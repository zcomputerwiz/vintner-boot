// SPDX-License-Identifier: GPL-2.0-or-later

package product

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	good := map[string]string{
		"name=WineExplorer\n":                         "WineExplorer",
		"# the product\n\n  name =  Wine Explorer \n": "Wine Explorer",
		"":            "",
		"name=Café\n": "Café",
	}
	for in, want := range good {
		p, err := Parse([]byte(in))
		if err != nil || p.Name != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, p.Name, err, want)
		}
	}
	bad := map[string]string{
		"name\n":                          "not key=value",
		"name=A\nname=B\n":                "given twice",
		"nme=A\n":                         "unknown key",
		"name=\n":                         "no title",
		"name=" + strings.Repeat("x", 65): "longer than 64",
		"name=a\tb\n":                     "not printable",
	}
	for in, want := range bad {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error %v, want one containing %q", in, err, want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if p, err := Load(filepath.Join(dir, "none.conf")); err != nil || p.Name != "" {
		t.Errorf("a missing file declares nothing: %+v %v", p, err)
	}
	f := filepath.Join(dir, "product.conf")
	if err := os.WriteFile(f, []byte("name=WineExplorer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err := Load(f); err != nil || p.Name != "WineExplorer" {
		t.Errorf("Load: %+v %v", p, err)
	}
	if err := os.WriteFile(f, []byte("name=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err == nil || !strings.Contains(err.Error(), f) {
		t.Errorf("a bad file is an error that names it: %v", err)
	}
}
