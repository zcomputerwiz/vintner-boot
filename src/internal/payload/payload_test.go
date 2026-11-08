// SPDX-License-Identifier: GPL-2.0-or-later

package payload

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// write makes a payload directory holding the required files plus extra, and a
// MANIFEST in the format of debian/stage-payloads.sh ("<sha256>  ./<path>").
func write(t *testing.T, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	names := append([]string{"VERSIONS"}, Required...)
	names = append(names, extra...)
	var lines []string
	for _, n := range names {
		content := []byte("content of " + n)
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(content)
		lines = append(lines, hex.EncodeToString(s[:])+"  ./"+n)
	}
	sort.Strings(lines)
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenAcceptsAnIntactDirectory(t *testing.T) {
	dir := write(t, "bios/i386-pc/biosdisk.mod")
	p, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p.Path(SystemdBoot)); err != nil || string(got) != "content of "+SystemdBoot {
		t.Errorf("Path(%s): %q, %v", SystemdBoot, got, err)
	}
	if p.Versions != "content of VERSIONS" {
		t.Errorf("versions %q", p.Versions)
	}
}

func TestOpenRefusesWhatIsNotIntact(t *testing.T) {
	cases := []struct {
		name string
		mod  func(t *testing.T, dir string)
		want string
	}{
		{"a changed file", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "uefi/loader.conf"), []byte("edited"), 0o644)
		}, "uefi/loader.conf differs"},
		{"a missing file", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "recovery/initrd.img")) }, "recovery/initrd.img is listed but missing"},
		{"an unlisted file", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "bios/i386-pc/evil.mod"), []byte("x"), 0o644)
		}, "bios/i386-pc/evil.mod is not listed"},
		{"a required file left out of the manifest", func(t *testing.T, dir string) {
			b, _ := os.ReadFile(filepath.Join(dir, "MANIFEST"))
			var keep []string
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if !strings.HasSuffix(l, "  ./"+Stub) {
					keep = append(keep, l)
				}
			}
			os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(strings.Join(keep, "\n")+"\n"), 0o644)
		}, Stub + " is required"},
		{"a malformed line", func(t *testing.T, dir string) {
			f, _ := os.OpenFile(filepath.Join(dir, "MANIFEST"), os.O_APPEND|os.O_WRONLY, 0)
			fmt.Fprintln(f, "not a manifest line")
			f.Close()
		}, "is not \"<sha256>  <path>\""},
		{"a path that leaves the directory", func(t *testing.T, dir string) {
			f, _ := os.OpenFile(filepath.Join(dir, "MANIFEST"), os.O_APPEND|os.O_WRONLY, 0)
			fmt.Fprintln(f, strings.Repeat("0", 64)+"  ./../etc/passwd")
			f.Close()
		}, "names the path"},
	}
	for _, c := range cases {
		dir := write(t)
		c.mod(t, dir)
		_, err := Open(dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to contain %q", c.name, err, c.want)
		}
	}
	if _, err := Open(t.TempDir()); err == nil || !strings.Contains(err.Error(), "vintner-platform") {
		t.Errorf("no MANIFEST: %v", err)
	}
}
