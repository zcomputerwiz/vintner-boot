// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload/payloadtest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

// tierBEnv is a UEFI installation with --secure-boot B, an efivarfs with
// another system's entry, and Debian's stock loader installed.
func tierBEnv(t *testing.T) (*env, *stockRunner, FullOptions, string) {
	t.Helper()
	e, sr, f, _ := fullEnv(t, "uefi")
	sr.installed = []string{"shim-signed", "grub-efi-amd64"}
	f.SecureBootTier = "B"
	f.Signer, f.Verifier = &fakeSigner{}, &fakeVerifier{}
	dir := efivarEnv(t, e)
	f.EFIVarDir = dir
	return e, sr, f, dir
}

func mokVar(t *testing.T, dir, name string) []byte {
	t.Helper()
	_, b, err := efivar.Get(dir, name, efivar.ShimLock)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b
}

func TestInstallTierBInstallsShimAndWritesTheMOKRequest(t *testing.T) {
	e, sr, f, dir := tierBEnv(t)
	res, err := Install(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if err != nil {
		t.Fatal(err)
	}
	if m.SecureBootTier != "B" || m.Signing == nil {
		t.Fatalf("tier %q signing %v", m.SecureBootTier, m.Signing)
	}

	// shim at both places the firmware starts, systemd-boot signed with the
	// machine key beside it as grubx64.efi, MokManager, and the certificate.
	cert := filepath.Join(sbDir(e), "cert.pem")
	der := read(t, filepath.Join(sbDir(e), "cert.der"))
	signed := append(payloadtest.Content(payload.SystemdBoot), marker(cert)...)
	want := map[string][]byte{
		"EFI/vintner/shimx64.efi":      payloadtest.Content(payload.Shim),
		"EFI/vintner/grubx64.efi":      signed,
		"EFI/vintner/mmx64.efi":        payloadtest.Content(payload.MokManager),
		"EFI/vintner/machine-cert.cer": der,
		"EFI/BOOT/BOOTX64.EFI":         payloadtest.Content(payload.Shim),
		"EFI/BOOT/grubx64.efi":         signed,
		"EFI/BOOT/mmx64.efi":           payloadtest.Content(payload.MokManager),
	}
	for p, w := range want {
		if got, err := os.ReadFile(filepath.Join(e.esp, p)); err != nil || !bytes.Equal(got, w) {
			t.Errorf("%s: %q (%v), want %q", p, got, err, w)
		}
	}
	if exists(filepath.Join(e.esp, "EFI/vintner/systemd-bootx64.efi")) {
		t.Error("tier B wrote systemd-bootx64.efi, which nothing starts")
	}

	// The boot entry starts shim.
	entries, _ := efivar.BootEntries(dir)
	en, ok := efivar.FindEntry(entries, efivar.VintnerDescription, efivar.VintnerShimPath)
	if !ok {
		t.Fatalf("no boot entry starts shim: %v", entries)
	}
	if order, _ := efivar.BootOrder(dir); len(order) == 0 || order[0] != en.Number {
		t.Errorf("BootOrder %v does not start with Boot%04X", order, en.Number)
	}

	// The request: the certificate, the password's hash, and no time limit.
	if res.MOKPassword == "" || secureboot.ValidatePassword(res.MOKPassword) != nil {
		t.Fatalf("password %q", res.MOKPassword)
	}
	req := mokVar(t, dir, "MokNew")
	if !bytes.Equal(req, secureboot.MOKSignatureList(der)) {
		t.Error("MokNew is not the machine certificate")
	}
	if !bytes.Equal(mokVar(t, dir, "MokAuth"), secureboot.MOKAuth(req, res.MOKPassword)) {
		t.Error("MokAuth does not match the password the result carries")
	}
	if !bytes.Equal(mokVar(t, dir, "MokTimeout"), []byte{0xff, 0xff, 0xff, 0xff}) {
		t.Error("MokTimeout is not -1")
	}
	if len(sr.apt) != 1 {
		t.Errorf("the stock loader was not purged: %v", sr.apt)
	}
}

func TestInstallTierBUsesTheGivenPassword(t *testing.T) {
	_, _, f, dir := tierBEnv(t)
	f.MOKPassword = "owner-chose-this"
	res, err := Install(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if res.MOKPassword != "owner-chose-this" {
		t.Errorf("password %q", res.MOKPassword)
	}
	if !bytes.Equal(mokVar(t, dir, "MokAuth"), secureboot.MOKAuth(mokVar(t, dir, "MokNew"), "owner-chose-this")) {
		t.Error("MokAuth is not the given password's")
	}
}

// A machine whose certificate shim already trusts (a reinstall that kept the
// key, or vintner secureboot request first) keeps the key and asks for nothing.
func TestInstallTierBKeepsAnEnrolledKeyAndWritesNoRequest(t *testing.T) {
	e, _, f, dir := tierBEnv(t)
	fp, err := secureboot.GenerateKey(sbDir(e), "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	der := read(t, filepath.Join(sbDir(e), "cert.der"))
	if err := efivar.Set(dir, "MokListRT", efivar.ShimLock, 6, secureboot.MOKSignatureList(der)); err != nil {
		t.Fatal(err)
	}
	res, err := Install(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if res.MOKPassword != "" {
		t.Error("a password was made although no request was needed")
	}
	if _, _, err := efivar.Get(dir, "MokNew", efivar.ShimLock); !errors.Is(err, efivar.ErrNotFound) {
		t.Errorf("MokNew was written: %v", err)
	}
	m, _ := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if m.Signing == nil || m.Signing.CertSHA256 != fp {
		t.Errorf("signing %v, want the kept certificate %s", m.Signing, fp)
	}
	if !bytes.Equal(read(t, filepath.Join(e.esp, "EFI/vintner/machine-cert.cer")), der) {
		t.Error("the ESP does not carry the kept certificate")
	}
}

func TestInstallTierBRefusesAKeySetItCannotUse(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, dir string)
		want  string
	}{
		"partial": {func(t *testing.T, dir string) {
			os.MkdirAll(dir, 0o700)
			os.WriteFile(filepath.Join(dir, "key.pem"), []byte("old"), 0o600)
		}, "part of a Secure Boot key set"},
		"key of another certificate": {func(t *testing.T, dir string) {
			other := filepath.Join(t.TempDir(), "other")
			if _, err := secureboot.GenerateKey(dir, "11111111-2222-4333-8444-555555555555"); err != nil {
				t.Fatal(err)
			}
			if _, err := secureboot.GenerateKey(other, "11111111-2222-4333-8444-666666666666"); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "key.pem"), read(t, filepath.Join(other, "key.pem")), 0o600)
		}, "cannot be used"},
		"DER copy of another certificate": {func(t *testing.T, dir string) {
			other := filepath.Join(t.TempDir(), "other")
			secureboot.GenerateKey(dir, "11111111-2222-4333-8444-555555555555")
			secureboot.GenerateKey(other, "11111111-2222-4333-8444-666666666666")
			os.WriteFile(filepath.Join(dir, "cert.der"), read(t, filepath.Join(other, "cert.der")), 0o644)
		}, "is not the certificate"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e, _, f, _ := tierBEnv(t)
			c.setup(t, sbDir(e))
			_, err := Install(context.Background(), f)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Install: %v, want %q", err, c.want)
			}
			if exists(filepath.Join(e.bs, "vintner")) || exists(filepath.Join(e.esp, "EFI/vintner")) {
				t.Error("something was written before the refusal")
			}
		})
	}
	t.Run("password", func(t *testing.T) {
		e, _, f, _ := tierBEnv(t)
		f.MOKPassword = "has a space"
		if _, err := Install(context.Background(), f); err == nil || !strings.Contains(err.Error(), "printable ASCII") {
			t.Fatalf("Install: %v", err)
		}
		if exists(filepath.Join(e.bs, "vintner")) {
			t.Error("something was written before the refusal")
		}
	})
}

// Without UEFI variables no request can be written: nothing is taken over, and
// --resume --secure-boot-enrolled finishes once the certificate is enrolled.
func TestInstallTierBWithoutEfivarsStopsBeforeTheTakeover(t *testing.T) {
	e, sr, f, _ := tierBEnv(t)
	f.EFIVarDir = filepath.Join(t.TempDir(), "none")
	_, err := Install(context.Background(), f)
	var se *Error
	if !errors.As(err, &se) || !se.Resumable || !strings.Contains(err.Error(), "--secure-boot-enrolled") {
		t.Fatalf("Install: %v", err)
	}
	if exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) || len(sr.apt) != 0 {
		t.Fatal("took over without a request or an enrolled certificate")
	}

	f.SecureBootTier = "" // Resume takes the tier from the manifest
	f.SecureBootEnrolled = true
	res, err := Resume(context.Background(), f)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.MOKPassword != "" {
		t.Error("Resume made a password although no request was written")
	}
	if !bytes.Equal(read(t, filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")), payloadtest.Content(payload.Shim)) {
		t.Error("Resume did not put shim in the removable path")
	}
	if len(sr.apt) != 1 {
		t.Errorf("Resume did not purge the stock loader: %v", sr.apt)
	}
}

// Resume writes the request too, and checks the loader on the ESP against the
// machine certificate first.
func TestResumeTierBWritesTheRequestAndChecksTheLoader(t *testing.T) {
	e, _, f, dir := tierBEnv(t)
	f.EFIVarDir = filepath.Join(t.TempDir(), "none")
	if _, err := Install(context.Background(), f); err == nil {
		t.Fatal("Install took over without UEFI variables")
	}
	f.EFIVarDir = dir

	good := read(t, filepath.Join(e.esp, "EFI/vintner/grubx64.efi"))
	os.WriteFile(filepath.Join(e.esp, "EFI/vintner/grubx64.efi"), payloadtest.Content(payload.SystemdBoot), 0o644)
	if _, err := Resume(context.Background(), f); err == nil || !strings.Contains(err.Error(), "not signed with this machine's key") {
		t.Fatalf("Resume with an unsigned loader: %v", err)
	}
	os.WriteFile(filepath.Join(e.esp, "EFI/vintner/grubx64.efi"), good, 0o644)
	res, err := Resume(context.Background(), f)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.MOKPassword == "" || !bytes.Equal(mokVar(t, dir, "MokAuth"), secureboot.MOKAuth(mokVar(t, dir, "MokNew"), res.MOKPassword)) {
		t.Error("Resume did not write a request that matches its password")
	}
}

// shim started from the removable path runs fbx64.efi when it lies beside it.
func TestInstallTierBRemovesShimFallbackFromTheRemovablePath(t *testing.T) {
	e, _, f, _ := tierBEnv(t)
	os.MkdirAll(filepath.Join(e.esp, "EFI/BOOT"), 0o755)
	os.MkdirAll(filepath.Join(e.esp, "EFI/debian"), 0o755)
	os.WriteFile(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI"), []byte("debian shim"), 0o644)
	os.WriteFile(filepath.Join(e.esp, "EFI/BOOT/fbx64.efi"), []byte("fallback"), 0o644)
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(e.esp, "EFI/BOOT/fbx64.efi")) {
		t.Error("fbx64.efi is still beside shim in the removable path")
	}
}

func TestEntryPathOnESP(t *testing.T) {
	esp := t.TempDir()
	if got := EntryPathOnESP(esp); got != efivar.VintnerLoaderPath {
		t.Errorf("without shim: %s", got)
	}
	os.MkdirAll(filepath.Join(esp, "EFI/vintner"), 0o755)
	os.WriteFile(filepath.Join(esp, "EFI/vintner/shimx64.efi"), []byte("shim"), 0o644)
	if got := EntryPathOnESP(esp); got != efivar.VintnerShimPath {
		t.Errorf("with shim: %s", got)
	}
}
