// SPDX-License-Identifier: GPL-2.0-or-later

// Package payloadtest writes payload directories for tests. It is not used by
// the vintner binary.
package payloadtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
)

// Content is what Write gives a file unless the caller overrides it. boot.img
// is one 512-byte sector with recognisable bytes in the places GRUB's boot
// sector uses.
func Content(name string) []byte {
	switch name {
	case "bios/i386-pc/boot.img":
		b := bytes.Repeat([]byte{0xB0}, 512)
		copy(b[0:3], []byte{0xEB, 0x63, 0x90})
		return b
	case "bios/core-modules.txt":
		return []byte("biosdisk part_gpt fat search_fs_uuid normal\n")
	case "recovery/version":
		return []byte("20261108-abcdef1\n")
	}
	return []byte("content of " + name + "\n")
}

// Write makes a verified-intact payload directory and returns it. Files in
// override replace the default content; names not in Required are added.
func Write(t testing.TB, override map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	names := append([]string{"VERSIONS"}, payload.Required...)
	for n := range override {
		found := false
		for _, m := range names {
			found = found || m == n
		}
		if !found {
			names = append(names, n)
		}
	}
	var lines []string
	for _, n := range names {
		b, ok := override[n]
		if !ok {
			b = Content(n)
		}
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(b)
		lines = append(lines, hex.EncodeToString(s[:])+"  ./"+n)
	}
	sort.Strings(lines)
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
