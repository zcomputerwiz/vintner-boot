// SPDX-License-Identifier: GPL-2.0-or-later

package variants

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	v, err := Parse("safe", []byte("# Safe Mode\ntitle = Safe Mode \n\ntokens=wexmode=safe  nomodeset\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "safe" || v.Title != "Safe Mode" || v.Tokens != "wexmode=safe  nomodeset" {
		t.Fatalf("got %+v", v)
	}
	if got := v.Cmdline("root=UUID=x ro "); got != "root=UUID=x ro wexmode=safe nomodeset vintner.variant=safe" {
		t.Fatalf("cmdline %q", got)
	}
}

func TestParseRefuses(t *testing.T) {
	cases := []struct{ id, body, want string }{
		{"Safe", "title=a\ntokens=b", "invalid id"},
		{"1safe", "title=a\ntokens=b", "invalid id"},
		{strings.Repeat("a", 25), "title=a\ntokens=b", "invalid id"},
		{"a-b", "title=a\ntokens=b", "invalid id"},
		{"s", "tokens=b", "no title"},
		{"s", "title=a", "no tokens"},
		{"s", "title=a\ntokens=root=/dev/sda1", "root="},
		{"s", "title=a\ntokens=ro root=UUID=x", "root="},
		{"s", "title=a\ntokens=vintner.variant=x", "vintner.*"},
		{"s", "title=a\ntokens=vintner.bootstorage=PARTUUID=x", "vintner.*"},
		{"s", "title=a\ntokens=x\"y", "printable"},
		{"s", "title=a\ntokens=x$y", "printable"},
		{"s", "title=a\ntokens=x\\y", "printable"},
		{"s", "title=a\ntitle=b\ntokens=x", "twice"},
		{"s", "title=a\ntokens=x\ncolor=red", "unknown key"},
		{"s", "title=a\ntokens", "not key=value"},
		{"s", "title=" + strings.Repeat("t", 65) + "\ntokens=x", "64 bytes"},
		{"s", "title=a\x01b\ntokens=x", "not printable"},
	}
	for _, c := range cases {
		_, err := Parse(c.id, []byte(c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%q, %q) = %v, want an error with %q", c.id, c.body, err, c.want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if vs, err := Load(filepath.Join(dir, "missing")); err != nil || vs != nil {
		t.Fatalf("missing dir: %v %v", vs, err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("zed.conf", "title=Z\ntokens=z")
	write("alpha.conf", "title=A\ntokens=a")
	write("README", "not a declaration")
	vs, err := Load(dir)
	if err != nil || len(vs) != 2 || vs[0].ID != "alpha" || vs[1].ID != "zed" {
		t.Fatalf("got %+v, %v", vs, err)
	}
	write("bad.conf", "title=B")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "bad.conf") {
		t.Fatalf("a bad declaration must be an error naming the file, got %v", err)
	}
}
