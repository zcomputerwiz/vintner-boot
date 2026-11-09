// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload/payloadtest"
)

// fakeSigner appends a marker naming the certificate, which fakeVerifier checks.
type fakeSigner struct{ signed []string }

func marker(cert string) []byte {
	b, _ := os.ReadFile(cert)
	h := sha256.Sum256(b)
	return []byte("\nSIGNED-BY " + hex.EncodeToString(h[:]))
}

func (s *fakeSigner) Sign(_ context.Context, in, out, key, cert string) error {
	if _, err := os.Stat(key); err != nil {
		return err
	}
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	s.signed = append(s.signed, filepath.Base(in))
	return os.WriteFile(out, append(b, marker(cert)...), 0o644)
}

// fakeVerifier accepts what carries the marker for the certificate. The UKI
// builder of the tests does not sign, so only loaders are held to it.
type fakeVerifier struct{ checked []string }

func (v *fakeVerifier) Verify(_ context.Context, file, cert string) error {
	v.checked = append(v.checked, filepath.Base(file))
	if base := filepath.Base(file); base != "loader.efi" && base != "systemd-bootx64.efi" {
		return nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if !bytes.HasSuffix(b, marker(cert)) {
		return errors.New("signature does not match the certificate")
	}
	return nil
}

type verifierFunc func(file, cert string) error

func (f verifierFunc) Verify(_ context.Context, file, cert string) error { return f(file, cert) }

func tierAEnv(t *testing.T) (*env, *stockRunner, FullOptions, *fakeSigner, *fakeVerifier) {
	t.Helper()
	e, sr, f, _ := fullEnv(t, "uefi")
	s, v := &fakeSigner{}, &fakeVerifier{}
	f.SecureBootTier = "A"
	f.Signer, f.Verifier = s, v
	return e, sr, f, s, v
}

func sbDir(e *env) string { return filepath.Join(e.target, "etc", "vintner", "secureboot") }

// enroll puts the machine certificate into a fake efivarfs's db.
func enroll(t *testing.T, e *env, efivars string) {
	t.Helper()
	der := read(t, filepath.Join(sbDir(e), "cert.der"))
	data := binary.LittleEndian.AppendUint32(nil, 7)
	data = append(data, []byte("EFI_SIGNATURE_LIST header")...)
	data = append(data, der...)
	if err := os.WriteFile(filepath.Join(efivars, "db-"+string(efivar.ImageSecurityDatabase)), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileFingerprint(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func TestInstallTierASignsLoaderAndRecoveryAndRecordsTheTier(t *testing.T) {
	e, _, f, s, v := tierAEnv(t)
	f.SecureBootEnrolled = true
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(sbDir(e), "cert.pem")
	if m.SecureBootTier != "A" || m.Signing == nil {
		t.Fatalf("tier %q signing %v", m.SecureBootTier, m.Signing)
	}
	if fp, err := fileFingerprint(filepath.Join(sbDir(e), "cert.der")); err != nil || m.Signing.CertSHA256 != fp {
		t.Errorf("manifest cert_sha256 %s, certificate on disk %s (%v)", m.Signing.CertSHA256, fp, err)
	}
	for _, p := range []string{"key.pem", "cert.pem", "cert.der"} {
		if !exists(filepath.Join(sbDir(e), p)) {
			t.Errorf("%s was not created", p)
		}
	}
	// The loader at both places the firmware may start is the signed copy.
	want := append(payloadtest.Content(payload.SystemdBoot), marker(cert)...)
	for _, p := range []string{"EFI/vintner/systemd-bootx64.efi", "EFI/BOOT/BOOTX64.EFI"} {
		if !bytes.Equal(read(t, filepath.Join(e.esp, p)), want) {
			t.Errorf("%s is not the loader signed with the machine key", p)
		}
	}
	if len(s.signed) != 1 {
		t.Errorf("signed %v, want the loader once", s.signed)
	}
	if len(v.checked) == 0 {
		t.Error("nothing was verified")
	}
}

func TestInstallTierNoneCreatesNoKey(t *testing.T) {
	e, _, f, _ := fullEnv(t, "uefi")
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if m.SecureBootTier != "none" || m.Signing != nil {
		t.Errorf("tier %q signing %v", m.SecureBootTier, m.Signing)
	}
	if exists(sbDir(e)) {
		t.Error("a Secure Boot key was created without --secure-boot")
	}
}

func TestInstallTierARefusesToTakeOverBeforeTheCertificateIsEnrolled(t *testing.T) {
	e, sr, f, _, _ := tierAEnv(t)
	sr.installed = []string{"grub-efi-amd64"}
	efivars := t.TempDir()
	f.EFIVarDir = efivars

	_, err := Install(context.Background(), f)
	var se *Error
	if !errors.As(err, &se) || !se.Resumable || se.Step != "secure boot" || !strings.Contains(err.Error(), "not in the firmware's db") {
		t.Fatalf("Install: %v", err)
	}
	if exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
		t.Error("the removable path was taken over before the certificate was enrolled")
	}
	if entries, _ := efivar.BootEntries(efivars); len(entries) != 0 {
		t.Errorf("a boot entry was written: %v", entries)
	}
	if len(sr.apt) != 0 {
		t.Errorf("the stock loader was purged: %v", sr.apt)
	}

	// Enrolling the certificate, then --resume, finishes it, with the loader on
	// the ESP checked against the machine certificate.
	enroll(t, e, efivars)
	if _, err := Resume(context.Background(), f); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
		t.Error("Resume did not take over")
	}
	if len(sr.apt) != 1 {
		t.Errorf("Resume did not purge the stock loader: %v", sr.apt)
	}
}

func TestInstallTierANeedsEnrollmentEvidenceWithoutEfivars(t *testing.T) {
	e, _, f, _, _ := tierAEnv(t)
	f.EFIVarDir = filepath.Join(t.TempDir(), "none")
	_, err := Install(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "--secure-boot-enrolled") {
		t.Fatalf("Install: %v", err)
	}
	if exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
		t.Error("took over without evidence that the certificate is enrolled")
	}
}

func TestInstallTierARefusesBeforeWritingAnything(t *testing.T) {
	cases := map[string]struct {
		setup func(*env, *FullOptions)
		want  string
	}{
		"tier B": {func(_ *env, f *FullOptions) { f.SecureBootTier = "B" }, "not implemented"},
		"no sbsign": {func(_ *env, f *FullOptions) {
			f.LookPath = func(n string) (string, error) {
				if n == "sbsign" {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + n, nil
			}
		}, "sbsigntool"},
		"a key exists": {func(e *env, _ *FullOptions) {
			os.MkdirAll(sbDir(e), 0o700)
			os.WriteFile(filepath.Join(sbDir(e), "key.pem"), []byte("old"), 0o600)
		}, "not replacing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e, _, f, _, _ := tierAEnv(t)
			c.setup(e, &f)
			_, err := Install(context.Background(), f)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Install: %v, want %q", err, c.want)
			}
			if exists(filepath.Join(e.bs, "vintner")) || exists(filepath.Join(e.esp, "EFI")) {
				t.Error("something was written before the refusal")
			}
		})
	}
	t.Run("bios", func(t *testing.T) {
		e, _, f, _ := fullEnv(t, "bios")
		f.SecureBootTier = "A"
		if _, err := Install(context.Background(), f); err == nil || !strings.Contains(err.Error(), "needs UEFI") {
			t.Fatalf("Install: %v", err)
		}
		if exists(filepath.Join(e.bs, "vintner")) {
			t.Error("something was written before the refusal")
		}
	})
}

func TestInstallTierAStopsWhenTheLoaderSignatureDoesNotVerify(t *testing.T) {
	e, _, f, _, _ := tierAEnv(t)
	f.Verifier = verifierFunc(func(file, cert string) error { return fmt.Errorf("bad signature on %s", filepath.Base(file)) })
	_, err := Install(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "bad signature") {
		t.Fatalf("Install: %v", err)
	}
	if exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
		t.Error("an unverified loader reached the removable path")
	}
}
