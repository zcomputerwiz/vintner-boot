// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

func TestSecureBootRequestMakesTheKeyAndWritesTheRequest(t *testing.T) {
	efivars, dir := t.TempDir(), filepath.Join(t.TempDir(), "sb")
	var out, errb bytes.Buffer
	if rc := runSecureBootRequest([]string{"--efivars", efivars, "--dir", dir}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	m := regexp.MustCompile(`one-time password: (\S+)`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no password printed:\n%s", out.String())
	}
	der, err := os.ReadFile(filepath.Join(dir, "cert.der"))
	if err != nil {
		t.Fatal(err)
	}
	_, req, err := efivar.Get(efivars, "MokNew", efivar.ShimLock)
	if err != nil || !bytes.Equal(req, secureboot.MOKSignatureList(der)) {
		t.Fatalf("MokNew: %v", err)
	}
	_, auth, _ := efivar.Get(efivars, "MokAuth", efivar.ShimLock)
	if !bytes.Equal(auth, secureboot.MOKAuth(req, m[1])) {
		t.Error("MokAuth does not match the printed password")
	}

	// Once shim lists the certificate there is nothing to ask, and the key stays.
	key, _ := os.ReadFile(filepath.Join(dir, "key.pem"))
	efivar.Delete(efivars, "MokNew", efivar.ShimLock)
	efivar.Set(efivars, "MokListRT", efivar.ShimLock, 6, secureboot.MOKSignatureList(der))
	out.Reset()
	if rc := runSecureBootRequest([]string{"--efivars", efivars, "--dir", dir}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	if !strings.Contains(out.String(), "in shim's MOK list already") {
		t.Errorf("output: %s", out.String())
	}
	if _, _, err := efivar.Get(efivars, "MokNew", efivar.ShimLock); err == nil {
		t.Error("a request was written for an enrolled certificate")
	}
	if k, _ := os.ReadFile(filepath.Join(dir, "key.pem")); !bytes.Equal(k, key) {
		t.Error("the key was replaced")
	}
}

func TestSecureBootRequestPasswordFile(t *testing.T) {
	efivars, dir := t.TempDir(), filepath.Join(t.TempDir(), "sb")
	pw := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pw, []byte("from-the-product\n"), 0o600)
	var out, errb bytes.Buffer
	if rc := runSecureBootRequest([]string{"--efivars", efivars, "--dir", dir, "--password-file", pw}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	if strings.Contains(out.String(), "from-the-product") {
		t.Error("a password from a file was printed")
	}
	_, req, _ := efivar.Get(efivars, "MokNew", efivar.ShimLock)
	_, auth, _ := efivar.Get(efivars, "MokAuth", efivar.ShimLock)
	if !bytes.Equal(auth, secureboot.MOKAuth(req, "from-the-product")) {
		t.Error("MokAuth is not the file's password without its newline")
	}
}

func TestSecureBootRequestRefusals(t *testing.T) {
	cases := map[string]struct {
		args  func(efivars, dir string) []string
		setup func(dir string)
		want  string
	}{
		"no efivarfs": {func(_, dir string) []string { return []string{"--efivars", filepath.Join(dir, "none"), "--dir", dir} }, nil, "no UEFI variables"},
		"partial key set": {func(efivars, dir string) []string { return []string{"--efivars", efivars, "--dir", dir} }, func(dir string) {
			os.MkdirAll(dir, 0o700)
			os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("x"), 0o644)
		}, "part of a key set"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			efivars, dir := t.TempDir(), filepath.Join(t.TempDir(), "sb")
			if c.setup != nil {
				c.setup(dir)
			}
			var out, errb bytes.Buffer
			if rc := runSecureBootRequest(c.args(efivars, dir), &out, &errb); rc != 1 || !strings.Contains(errb.String(), c.want) {
				t.Fatalf("rc %d: %s", rc, errb.String())
			}
			if _, _, err := efivar.Get(efivars, "MokNew", efivar.ShimLock); err == nil {
				t.Error("a request was written")
			}
		})
	}
}
