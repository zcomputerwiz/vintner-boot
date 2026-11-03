// SPDX-License-Identifier: GPL-2.0-or-later

package secureboot

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeyProperties(t *testing.T) {
	dir := t.TempDir()
	instID := "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f"

	fingerprint, err := GenerateKey(dir, instID)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	keyPath := filepath.Join(dir, "key.pem")
	certPEMPath := filepath.Join(dir, "cert.pem")
	certDERPath := filepath.Join(dir, "cert.der")

	// Verify file modes
	fiKey, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key.pem: %v", err)
	}
	if fiKey.Mode().Perm() != 0600 {
		t.Errorf("key.pem mode: got %o, want 0600", fiKey.Mode().Perm())
	}

	fiCert, err := os.Stat(certPEMPath)
	if err != nil {
		t.Fatalf("stat cert.pem: %v", err)
	}
	if fiCert.Mode().Perm() != 0644 {
		t.Errorf("cert.pem mode: got %o, want 0644", fiCert.Mode().Perm())
	}

	fiDER, err := os.Stat(certDERPath)
	if err != nil {
		t.Fatalf("stat cert.der: %v", err)
	}
	if fiDER.Mode().Perm() != 0644 {
		t.Errorf("cert.der mode: got %o, want 0644", fiDER.Mode().Perm())
	}

	// Read and parse DER
	derBytes, err := os.ReadFile(certDERPath)
	if err != nil {
		t.Fatalf("read cert.der: %v", err)
	}
	expectedFP := hex.EncodeToString(func() []byte {
		s := sha256.Sum256(derBytes)
		return s[:]
	}())
	if fingerprint != expectedFP {
		t.Errorf("fingerprint mismatch: got %s, want %s", fingerprint, expectedFP)
	}

	// Read and parse PEM
	pemBytes, err := os.ReadFile(certPEMPath)
	if err != nil {
		t.Fatalf("read cert.pem: %v", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("failed to decode cert.pem")
	}
	if string(block.Bytes) != string(derBytes) {
		t.Error("cert.pem DER content does not equal cert.der bytes")
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	// Verify certificate properties
	if cert.SignatureAlgorithm != x509.SHA256WithRSA {
		t.Errorf("signature algorithm: got %v, want SHA256WithRSA", cert.SignatureAlgorithm)
	}

	rsaPub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key is not RSA: %T", cert.PublicKey)
	}
	if rsaPub.N.BitLen() != 2048 {
		t.Errorf("RSA bit length: got %d, want 2048", rsaPub.N.BitLen())
	}

	expectedSubject := "vintner machine " + instID
	if cert.Subject.CommonName != expectedSubject {
		t.Errorf("subject CN: got %q, want %q", cert.Subject.CommonName, expectedSubject)
	}

	if cert.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("key usage: got %v, want KeyUsageDigitalSignature", cert.KeyUsage)
	}

	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageCodeSigning {
		t.Errorf("ext key usage: got %v, want [ExtKeyUsageCodeSigning]", cert.ExtKeyUsage)
	}

	if cert.IsCA {
		t.Error("IsCA is true, want false")
	}

	validity := cert.NotAfter.Sub(cert.NotBefore)
	days := int(validity.Hours() / 24)
	if days < 3649 || days > 3651 {
		t.Errorf("validity days: got %d, want ~3650", days)
	}

	// CertSHA256 test
	fp2, err := CertSHA256(certPEMPath)
	if err != nil {
		t.Fatalf("CertSHA256 failed: %v", err)
	}
	if fp2 != fingerprint {
		t.Errorf("CertSHA256 mismatch: got %s, want %s", fp2, fingerprint)
	}

	// KeyMatchesCert test
	if err := KeyMatchesCert(keyPath, certPEMPath); err != nil {
		t.Errorf("KeyMatchesCert failed for valid pair: %v", err)
	}
}

func TestRefusalToOverwrite(t *testing.T) {
	dir := t.TempDir()
	instID := "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f"

	// Pre-create cert.pem
	certPEMPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPEMPath, []byte("existing cert"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := GenerateKey(dir, instID)
	if err == nil {
		t.Fatal("expected GenerateKey to fail when cert.pem already exists")
	}

	// Verify key.pem and cert.der were not created
	if _, err := os.Stat(filepath.Join(dir, "key.pem")); !os.IsNotExist(err) {
		t.Error("key.pem was created despite refusal to overwrite")
	}
	if _, err := os.Stat(filepath.Join(dir, "cert.der")); !os.IsNotExist(err) {
		t.Error("cert.der was created despite refusal to overwrite")
	}

	// Verify existing cert.pem content remains intact
	content, err := os.ReadFile(certPEMPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing cert" {
		t.Errorf("cert.pem content modified: got %q, want 'existing cert'", string(content))
	}
}

func TestKeyMatchesCertMismatches(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	instID := "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f"

	_, err := GenerateKey(dir1, instID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = GenerateKey(dir2, instID)
	if err != nil {
		t.Fatal(err)
	}

	key1 := filepath.Join(dir1, "key.pem")
	cert1 := filepath.Join(dir1, "cert.pem")
	key2 := filepath.Join(dir2, "key.pem")
	cert2 := filepath.Join(dir2, "cert.pem")

	// Matching pairs pass
	if err := KeyMatchesCert(key1, cert1); err != nil {
		t.Errorf("expected key1/cert1 to match: %v", err)
	}
	if err := KeyMatchesCert(key2, cert2); err != nil {
		t.Errorf("expected key2/cert2 to match: %v", err)
	}

	// Mismatched pairs fail
	if err := KeyMatchesCert(key1, cert2); err == nil {
		t.Error("expected key1/cert2 to fail mismatch check")
	}
	if err := KeyMatchesCert(key2, cert1); err == nil {
		t.Error("expected key2/cert1 to fail mismatch check")
	}

	// Missing files fail
	if err := KeyMatchesCert(filepath.Join(dir1, "nonexistent.pem"), cert1); err == nil {
		t.Error("expected missing key to fail")
	}
	if err := KeyMatchesCert(key1, filepath.Join(dir1, "nonexistent.pem")); err == nil {
		t.Error("expected missing cert to fail")
	}

	// ECDSA key fails (must be RSA)
	ecdsaDir := t.TempDir()
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaBytes, err := x509.MarshalPKCS8PrivateKey(ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaKeyPath := filepath.Join(ecdsaDir, "key.pem")
	if err := os.WriteFile(ecdsaKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecdsaBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := KeyMatchesCert(ecdsaKeyPath, cert1); err == nil {
		t.Error("expected ECDSA private key to fail RSA check")
	}
}
