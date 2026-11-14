// SPDX-License-Identifier: GPL-2.0-or-later

package secureboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
)

// The bytes ci/mok-chain writes for a request that MokManager accepted (E1):
// EFI_CERT_X509_GUID, the list size, a zero header size, the signature size,
// SHIM_LOCK_GUID as the owner, then the certificate.
func TestMOKSignatureListLayout(t *testing.T) {
	der := []byte("abc")
	want, _ := hex.DecodeString(
		"a159c0a5e494a74a87b5ab155c2bf072" + // EFI_CERT_X509_GUID
			"2f000000" + "00000000" + "13000000" + // 28+19, 0, 16+3
			"50ab5d6046e00043abb63dd810dd8b23" + // SHIM_LOCK_GUID
			"616263")
	if got := MOKSignatureList(der); !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
}

func TestMOKAuthIsSHA256OfTheRequestAndTheUTF16Password(t *testing.T) {
	req := []byte("request")
	in := append([]byte(nil), req...)
	for _, c := range "pw1" {
		in = append(in, byte(c), 0)
	}
	want := sha256.Sum256(in)
	if got := MOKAuth(req, "pw1"); !bytes.Equal(got, want[:]) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestWriteMOKRequest(t *testing.T) {
	dir := t.TempDir()
	// An older, longer request is replaced, not partly overwritten.
	if err := WriteMOKRequest(dir, bytes.Repeat([]byte("old certificate "), 10), "first"); err != nil {
		t.Fatal(err)
	}
	der := []byte("the machine certificate")
	if err := WriteMOKRequest(dir, der, "vintner1"); err != nil {
		t.Fatal(err)
	}
	get := func(name string) (uint32, []byte) {
		attrs, data, err := efivar.Get(dir, name, efivar.ShimLock)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return attrs, data
	}
	a, req := get("MokNew")
	if a != 7 || !bytes.Equal(req, MOKSignatureList(der)) {
		t.Errorf("MokNew attrs %d data %x", a, req)
	}
	if a, auth := get("MokAuth"); a != 7 || !bytes.Equal(auth, MOKAuth(req, "vintner1")) {
		t.Errorf("MokAuth attrs %d data %x", a, auth)
	}
	if a, to := get("MokTimeout"); a != 7 || int32(binary.LittleEndian.Uint32(to)) != -1 || len(to) != 4 {
		t.Errorf("MokTimeout attrs %d data %x, want 4 bytes of -1", a, to)
	}
	if ok, err := MOKRequested(dir, der); !ok || err != nil {
		t.Errorf("MOKRequested: %v %v", ok, err)
	}
	if ok, _ := MOKRequested(dir, []byte("another certificate")); ok {
		t.Error("MOKRequested for a certificate that was not requested")
	}
}

func TestWriteMOKRequestRefusesWhatCannotBeTyped(t *testing.T) {
	dir := t.TempDir()
	for _, pw := range []string{"", "with space", "naïve", strings.Repeat("x", 257)} {
		if err := WriteMOKRequest(dir, []byte("cert"), pw); err == nil {
			t.Errorf("password %q was accepted", pw)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("a refused request wrote %v", ents)
	}
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		p, err := GeneratePassword(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 10 || ValidatePassword(p) != nil || strings.Trim(p, passwordAlphabet) != "" {
			t.Fatalf("password %q", p)
		}
		seen[p] = true
	}
	if len(seen) < 19 {
		t.Errorf("only %d different passwords in 20", len(seen))
	}
}

func TestCertFileSHA256TakesPEMAndDER(t *testing.T) {
	dir := t.TempDir()
	fp, err := GenerateKey(dir, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cert.pem", "cert.der"} {
		got, err := CertFileSHA256(filepath.Join(dir, name))
		if err != nil || got != fp {
			t.Errorf("%s: %q, %v, want %s", name, got, err, fp)
		}
	}
	// A key is not a certificate.
	if _, err := CertFileSHA256(filepath.Join(dir, "key.pem")); err == nil {
		t.Error("key.pem was taken for a certificate")
	}
	if _, err := CertFileSHA256(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestDKMSDropInContent(t *testing.T) {
	got := DKMSDropInContent("/k/key.pem", "/k/cert.der")
	for _, want := range []string{"mok_signing_key='/k/key.pem'\n", "mok_certificate='/k/cert.der'\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	// DKMS sources the file as shell: a path with a space, a quote or a dollar sign
	// must stay one word with its own value.
	got = DKMSDropInContent("/k k/it's $HOME/key.pem", "/k/cert.der")
	if !strings.Contains(got, `mok_signing_key='/k k/it'\''s $HOME/key.pem'`+"\n") {
		t.Errorf("unquoted special characters in\n%s", got)
	}
	if !strings.HasPrefix(got, "#") {
		t.Error("the drop-in does not say who wrote it")
	}
}

func TestKeyMatchesCertTakesADERCertificate(t *testing.T) {
	dir := t.TempDir()
	if _, err := GenerateKey(dir, "11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatal(err)
	}
	if err := KeyMatchesCert(filepath.Join(dir, "key.pem"), filepath.Join(dir, "cert.der")); err != nil {
		t.Errorf("DER certificate: %v", err)
	}
	other := t.TempDir()
	if _, err := GenerateKey(other, "11111111-2222-4333-8444-666666666666"); err != nil {
		t.Fatal(err)
	}
	if err := KeyMatchesCert(filepath.Join(dir, "key.pem"), filepath.Join(other, "cert.der")); err == nil {
		t.Error("another certificate's DER form was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.der"), []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := KeyMatchesCert(filepath.Join(dir, "key.pem"), filepath.Join(dir, "junk.der")); err == nil {
		t.Error("junk was accepted as a certificate")
	}
}

func TestMOKEnrolled(t *testing.T) {
	der := []byte("the machine certificate")
	other := MOKSignatureList([]byte("someone else"))
	set := func(dir, name string, data []byte) {
		if err := efivar.Set(dir, name, efivar.ShimLock, 6, data); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("no list", func(t *testing.T) {
		if ok, err := MOKEnrolled(t.TempDir(), "", der); ok || err != nil {
			t.Errorf("%v %v", ok, err)
		}
	})
	t.Run("in MokListRT", func(t *testing.T) {
		dir := t.TempDir()
		set(dir, "MokListRT", append(other, MOKSignatureList(der)...))
		if ok, err := MOKEnrolled(dir, "", der); !ok || err != nil {
			t.Errorf("%v %v", ok, err)
		}
	})
	t.Run("another certificate only", func(t *testing.T) {
		dir := t.TempDir()
		set(dir, "MokListRT", other)
		if ok, _ := MOKEnrolled(dir, "", der); ok {
			t.Error("enrolled")
		}
	})
	t.Run("split list", func(t *testing.T) {
		dir := t.TempDir()
		set(dir, "MokListRT", other)
		set(dir, "MokListRT1", MOKSignatureList(der))
		if ok, err := MOKEnrolled(dir, "", der); !ok || err != nil {
			t.Errorf("%v %v", ok, err)
		}
	})
	t.Run("configuration table", func(t *testing.T) {
		mok := t.TempDir()
		os.WriteFile(filepath.Join(mok, "MokListRT"), MOKSignatureList(der), 0o444)
		if ok, err := MOKEnrolled(t.TempDir(), mok, der); !ok || err != nil {
			t.Errorf("%v %v", ok, err)
		}
	})
}
