// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

// The signed tiers make DKMS sign out-of-tree modules with the machine key
// (WO-0010 5.3): the drop-in names the key where it lies and copies nothing.

func dropInPath(e *env) string {
	return filepath.Join(e.target, "etc", "dkms", "framework.conf.d", "vintner-secureboot.conf")
}

func TestInstallSignedTiersWriteTheDKMSDropIn(t *testing.T) {
	setups := map[string]func(t *testing.T) (*env, FullOptions){
		"A": func(t *testing.T) (*env, FullOptions) {
			e, _, f, _, _ := tierAEnv(t)
			f.SecureBootEnrolled = true
			return e, f
		},
		"B": func(t *testing.T) (*env, FullOptions) {
			e, _, f, _ := tierBEnv(t)
			return e, f
		},
	}
	for tier, setup := range setups {
		t.Run("tier "+tier, func(t *testing.T) {
			e, f := setup(t)
			if _, err := Install(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dropInPath(e))
			if err != nil {
				t.Fatal(err)
			}
			want := secureboot.DKMSDropInContent("/etc/vintner/secureboot/key.pem", "/etc/vintner/secureboot/cert.der")
			if string(got) != want {
				t.Errorf("drop-in:\n%s\nwant:\n%s", got, want)
			}
			for _, line := range []string{"mok_signing_key='/etc/vintner/secureboot/key.pem'\n", "mok_certificate='/etc/vintner/secureboot/cert.der'\n"} {
				if !strings.Contains(string(got), line) {
					t.Errorf("drop-in lacks %q", line)
				}
			}

			// R11: the key stays where vintner keeps it, and the drop-in copies nothing.
			key := read(t, filepath.Join(sbDir(e), "key.pem"))
			for _, root := range []string{e.esp, e.bs} {
				filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						if b, _ := os.ReadFile(p); bytes.Contains(b, key) || bytes.Contains(b, []byte("PRIVATE KEY")) {
							t.Errorf("the machine key is in %s", p)
						}
					}
					return nil
				})
			}
			if strings.Contains(string(got), "PRIVATE KEY") {
				t.Error("the drop-in holds key material")
			}
		})
	}
}

func TestInstallTierNoneWritesNoDKMSDropIn(t *testing.T) {
	e, _, f, _ := fullEnv(t, "uefi")
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if exists(dropInPath(e)) {
		t.Error("a drop-in was written although vintner holds no machine key")
	}
}

func TestInstallDKMSDropInNamesTheKeyWhereItLies(t *testing.T) {
	e, _, f, _, _ := tierAEnv(t)
	f.SecureBootEnrolled = true
	// A key directory outside the target is named as given.
	outside := filepath.Join(t.TempDir(), "keys")
	f.SecureBootDir = outside
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	got := read(t, dropInPath(e))
	want := secureboot.DKMSDropInContent(filepath.ToSlash(filepath.Join(outside, "key.pem")), filepath.ToSlash(filepath.Join(outside, "cert.der")))
	if string(got) != want {
		t.Errorf("drop-in:\n%s\nwant:\n%s", got, want)
	}
}

func TestInTarget(t *testing.T) {
	cases := []struct{ target, path, want string }{
		{"/", "/etc/vintner/secureboot/key.pem", "/etc/vintner/secureboot/key.pem"},
		{"", "/etc/vintner/secureboot/key.pem", "/etc/vintner/secureboot/key.pem"},
		{"", "etc/vintner/secureboot/key.pem", "/etc/vintner/secureboot/key.pem"}, // no target: relative to the root
		{".", "etc/vintner/secureboot/cert.der", "/etc/vintner/secureboot/cert.der"},
		{"/mnt/target", "/mnt/target/etc/vintner/secureboot/key.pem", "/etc/vintner/secureboot/key.pem"},
		{"/mnt/target/", "/mnt/target/etc/k", "/etc/k"},
		{"/mnt/target", "/mnt/targetx/etc/k", "/mnt/targetx/etc/k"}, // another directory that starts with the same letters
		{"/mnt/target", "/elsewhere/key.pem", "/elsewhere/key.pem"},
	}
	for _, c := range cases {
		d := &deployer{Options: Options{TargetDir: c.target}}
		if got := d.inTarget(c.path); got != c.want {
			t.Errorf("target %q, path %q: %q, want %q", c.target, c.path, got, c.want)
		}
	}
}
