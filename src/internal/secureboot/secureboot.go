// SPDX-License-Identifier: GPL-2.0-or-later

package secureboot

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Verifier verifies that a PE/COFF binary is signed by the certificate at certPEMPath.
type Verifier interface {
	Verify(ctx context.Context, file, certPEMPath string) error
}

// SbverifyVerifier implements Verifier using the system sbsigntool (sbverify).
type SbverifyVerifier struct{}

// Verify runs sbverify --cert <certPEMPath> <file>.
func (SbverifyVerifier) Verify(ctx context.Context, file, certPEMPath string) error {
	bin, err := exec.LookPath("sbverify")
	if err != nil {
		return fmt.Errorf("sbverify not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, "--cert", certPEMPath, file)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("sbverify failed: %s", msg)
	}
	return nil
}

// GenerateKey generates an RSA 2048 private key and self-signed X.509 v3 certificate
// for the given installationID and writes them into dir:
// - <dir>/key.pem (mode 0600, PKCS#8)
// - <dir>/cert.pem (mode 0644, PEM)
// - <dir>/cert.der (mode 0644, DER)
// It creates dir with mode 0700 if missing.
// It refuses to overwrite if any of the three files exists, writing nothing.
// Returns the lowercase hex SHA-256 fingerprint of the DER certificate.
func GenerateKey(dir, installationID string) (string, error) {
	keyPath := filepath.Join(dir, "key.pem")
	certPEMPath := filepath.Join(dir, "cert.pem")
	certDERPath := filepath.Join(dir, "cert.der")

	// Refuse to overwrite: if any of the three files exists, return an error and write nothing.
	for _, p := range []string{keyPath, certPEMPath, certDERPath} {
		if _, err := os.Stat(p); err == nil {
			return "", fmt.Errorf("file already exists: %s", p)
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", fmt.Errorf("generate RSA key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return "", fmt.Errorf("generate serial: %w", err)
	}

	notBefore := time.Now().UTC()
	notAfter := notBefore.Add(3650 * 24 * time.Hour)

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: fmt.Sprintf("vintner machine %s", installationID),
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		return "", fmt.Errorf("create certificate: %w", err)
	}

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return "", fmt.Errorf("marshal PKCS#8 key: %w", err)
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create directory %s: %w", dir, err)
	}

	// Write key.pem with O_EXCL mode 0600
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})
	fKey, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("create key.pem: %w", err)
	}
	if _, err := fKey.Write(keyPEM); err != nil {
		fKey.Close()
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("write key.pem: %w", err)
	}
	if err := fKey.Close(); err != nil {
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("close key.pem: %w", err)
	}

	// Write cert.pem with O_EXCL mode 0644
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	fCert, err := os.OpenFile(certPEMPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("create cert.pem: %w", err)
	}
	if _, err := fCert.Write(certPEM); err != nil {
		fCert.Close()
		_ = os.Remove(keyPath)
		_ = os.Remove(certPEMPath)
		return "", fmt.Errorf("write cert.pem: %w", err)
	}
	if err := fCert.Close(); err != nil {
		_ = os.Remove(keyPath)
		_ = os.Remove(certPEMPath)
		return "", fmt.Errorf("close cert.pem: %w", err)
	}

	// Write cert.der with O_EXCL mode 0644
	fDER, err := os.OpenFile(certDERPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		_ = os.Remove(keyPath)
		_ = os.Remove(certPEMPath)
		return "", fmt.Errorf("create cert.der: %w", err)
	}
	if _, err := fDER.Write(derBytes); err != nil {
		fDER.Close()
		_ = os.Remove(keyPath)
		_ = os.Remove(certPEMPath)
		_ = os.Remove(certDERPath)
		return "", fmt.Errorf("write cert.der: %w", err)
	}
	if err := fDER.Close(); err != nil {
		_ = os.Remove(keyPath)
		_ = os.Remove(certPEMPath)
		_ = os.Remove(certDERPath)
		return "", fmt.Errorf("close cert.der: %w", err)
	}

	sum := sha256.Sum256(derBytes)
	return hex.EncodeToString(sum[:]), nil
}

// CertSHA256 returns the lowercase hex SHA-256 fingerprint of the certificate at certPEMPath.
func CertSHA256(certPEMPath string) (string, error) {
	data, err := os.ReadFile(certPEMPath)
	if err != nil {
		return "", fmt.Errorf("read certificate: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("failed to decode certificate PEM block")
	}
	if block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("unexpected PEM block type: %s", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse certificate: %w", err)
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

// KeyMatchesCert parses both keyPEMPath and certPEMPath and returns nil if the
// private key's public half equals the certificate's public key. It fails if
// either file is missing or unparsable, or if the key is not RSA.
func KeyMatchesCert(keyPEMPath, certPEMPath string) error {
	certData, err := os.ReadFile(certPEMPath)
	if err != nil {
		return fmt.Errorf("read certificate %s: %w", certPEMPath, err)
	}
	certBlock, _ := pem.Decode(certData)
	if certBlock == nil {
		return fmt.Errorf("decode certificate PEM %s: no PEM block found", certPEMPath)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse certificate %s: %w", certPEMPath, err)
	}

	rsaCertPub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("certificate %s public key is not RSA", certPEMPath)
	}

	keyData, err := os.ReadFile(keyPEMPath)
	if err != nil {
		return fmt.Errorf("read private key %s: %w", keyPEMPath, err)
	}
	keyBlock, _ := pem.Decode(keyData)
	if keyBlock == nil {
		return fmt.Errorf("decode private key PEM %s: no PEM block found", keyPEMPath)
	}

	var privRSA *rsa.PrivateKey
	if parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err == nil {
		rsaKey, ok := parsedKey.(*rsa.PrivateKey)
		if !ok {
			return fmt.Errorf("private key in %s is not RSA", keyPEMPath)
		}
		privRSA = rsaKey
	} else if parsedKey, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err == nil {
		privRSA = parsedKey
	} else {
		return fmt.Errorf("parse private key %s: neither PKCS#8 nor PKCS#1 RSA key", keyPEMPath)
	}

	if privRSA.PublicKey.N.Cmp(rsaCertPub.N) != 0 || privRSA.PublicKey.E != rsaCertPub.E {
		return fmt.Errorf("private key %s does not match certificate %s", keyPEMPath, certPEMPath)
	}

	return nil
}
