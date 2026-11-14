// SPDX-License-Identifier: GPL-2.0-or-later

package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

func boolp(b bool) *bool { return &b }

// machineKey makes a key set in a directory and the drop-in that names it.
func machineKey(t *testing.T, content func(key, cert string) string) (dropIn string, m *manifest.Manifest) {
	t.Helper()
	dir := t.TempDir()
	fp, err := secureboot.GenerateKey(dir, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	dropIn = filepath.Join(t.TempDir(), "vintner-secureboot.conf")
	if err := os.WriteFile(dropIn, []byte(content(filepath.Join(dir, "key.pem"), filepath.Join(dir, "cert.der"))), 0o644); err != nil {
		t.Fatal(err)
	}
	return dropIn, &manifest.Manifest{SecureBootTier: "B", Signing: &manifest.Signing{CertSHA256: fp}}
}

func TestModulesSignedTiersAllowSignedModulesWhenDKMSSigns(t *testing.T) {
	dropIn, m := machineKey(t, secureboot.DKMSDropInContent)
	for _, tier := range []string{"A", "B"} {
		m.SecureBootTier = tier
		got := modules(boolp(true), "/nonexistent", m, dropIn)
		if got.OutOfTree != "allowed" || got.RequiresSignature == nil || !*got.RequiresSignature || !strings.Contains(got.Reason, "signed with the machine key") {
			t.Errorf("tier %s: %+v, want allowed, requires a signature, reason naming the machine key", tier, got)
		}
	}
}

func TestModulesBlockedWhenDKMSDoesNotSignWithTheMachineKey(t *testing.T) {
	good, m := machineKey(t, secureboot.DKMSDropInContent)
	other, _ := machineKey(t, secureboot.DKMSDropInContent) // a drop-in for another key set
	cases := map[string]struct {
		dropIn string
		want   string
	}{
		"no drop-in":          {filepath.Join(t.TempDir(), "none.conf"), "is missing"},
		"another certificate": {other, "is not the machine certificate"},
		"names nothing":       {writeFile(t, "# empty\n"), "names no mok_signing_key"},
		"key file gone":       {writeFile(t, "mok_signing_key=/nonexistent/key.pem\nmok_certificate=/nonexistent/cert.der\n"), "is missing: DKMS would make a key of its own"},
		"certificate gone":    {writeFile(t, "mok_signing_key="+os.Args[0]+"\nmok_certificate=/nonexistent/cert.der\n"), "is missing: DKMS would make a key of its own"},
	}
	_ = good
	for name, c := range cases {
		got := modules(boolp(true), "/nonexistent", m, c.dropIn)
		if got.OutOfTree != "blocked" || !strings.Contains(got.Reason, c.want) {
			t.Errorf("%s: %+v, want blocked with %q", name, got, c.want)
		}
		if got.RequiresSignature == nil || !*got.RequiresSignature {
			t.Errorf("%s: Secure Boot is on, so a signature is required: %+v", name, got)
		}
	}
}

// A key that does not belong to the certificate would make DKMS sign modules the
// kernel cannot verify.
func TestModulesBlockedWhenTheKeyDoesNotBelongToTheCertificate(t *testing.T) {
	_, m := machineKey(t, secureboot.DKMSDropInContent)
	a, b := t.TempDir(), t.TempDir()
	if _, err := secureboot.GenerateKey(a, "11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatal(err)
	}
	if _, err := secureboot.GenerateKey(b, "11111111-2222-4333-8444-666666666666"); err != nil {
		t.Fatal(err)
	}
	// The manifest holds a's certificate; the drop-in names a's certificate and b's key.
	fp, _ := secureboot.CertFileSHA256(filepath.Join(a, "cert.der"))
	m.Signing = &manifest.Signing{CertSHA256: fp}
	dropIn := writeFile(t, secureboot.DKMSDropInContent(filepath.Join(b, "key.pem"), filepath.Join(a, "cert.der")))
	got := modules(boolp(true), "/nonexistent", m, dropIn)
	if got.OutOfTree != "blocked" || !strings.Contains(got.Reason, "does not belong to its certificate") {
		t.Errorf("%+v", got)
	}
}

// Values with quotes and spaces are read back as the installer wrote them.
func TestModulesReadsQuotedDropInValues(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fp, err := secureboot.GenerateKey(dir, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{SecureBootTier: "A", Signing: &manifest.Signing{CertSHA256: fp}}
	dropIn := writeFile(t, "export "+secureboot.DKMSDropInContent(filepath.Join(dir, "key.pem"), filepath.Join(dir, "cert.der")))
	if got := modules(boolp(true), "/nonexistent", m, dropIn); got.OutOfTree != "allowed" {
		t.Errorf("%+v", got)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.conf")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Tier none has no machine key to sign with.
func TestModulesTierNoneWithSecureBootOnIsBlocked(t *testing.T) {
	m := &manifest.Manifest{SecureBootTier: "none"}
	got := modules(boolp(true), "/nonexistent", m, "")
	if got.OutOfTree != "blocked" || !strings.Contains(got.Reason, "no machine key") {
		t.Errorf("%+v", got)
	}
}

// With Secure Boot off the kernel's own answer decides, as before.
func TestModulesSecureBootOffFollowsSigEnforce(t *testing.T) {
	m := &manifest.Manifest{SecureBootTier: "B"}
	for sig, want := range map[string]string{"N": "allowed", "Y": "blocked"} {
		got := modules(boolp(false), writeFile(t, sig+"\n"), m, "")
		if got.OutOfTree != want || got.RequiresSignature != nil {
			t.Errorf("sig_enforce %s: %+v, want %s", sig, got, want)
		}
	}
	if got := modules(nil, "/nonexistent", m, ""); got.OutOfTree != "unknown" {
		t.Errorf("unreadable: %+v", got)
	}
}
