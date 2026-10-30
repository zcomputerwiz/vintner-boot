// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

type recordingUKIBuilder struct {
	lastReq UKIRequest
	called  bool
	fail    bool
}

func (r *recordingUKIBuilder) Build(ctx context.Context, req UKIRequest) error {
	r.called = true
	r.lastReq = req
	if r.fail {
		return fmt.Errorf("mock ukify failure")
	}
	osrelData, _ := os.ReadFile(req.OSReleasePath)
	initrdData, _ := os.ReadFile(req.InitrdPath)
	return writeTestPE(req.OutputPath, osrelData, []byte(req.Cmdline), []byte(req.Uname), initrdData)
}

type recordingVerifier struct {
	called           bool
	lastVerifiedFile string
	lastCertPEMPath  string
	errToReturn      error
}

func (r *recordingVerifier) Verify(ctx context.Context, file, certPEMPath string) error {
	r.called = true
	r.lastVerifiedFile = file
	r.lastCertPEMPath = certPEMPath
	return r.errToReturn
}

func TestPublisherTierA(t *testing.T) {
	rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
	newABI := "6.12.112-test"
	setupKernelFiles(t, rootDir, newABI)

	keyDir := t.TempDir()
	fp, err := secureboot.GenerateKey(keyDir, m.InstallationID)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	m.SecureBootTier = "A"
	m.Signing = &manifest.Signing{CertSHA256: fp}
	for i := range m.Images {
		if m.Images[i].UEFI != nil {
			m.Images[i].UEFI.Signed = true
		}
	}
	stateDir := filepath.Join(bootStorageDir, "vintner", "state")
	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	builder := &recordingUKIBuilder{}
	verifier := &recordingVerifier{}

	opts := Options{
		KernelABI:      newABI,
		BootStorageDir: bootStorageDir,
		RootDir:        rootDir,
		CmdlineContent: m.Images[0].Cmdline,
		LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
		SecureBootDir:  keyDir,
		UKIBuilder:     builder,
		Verifier:       verifier,
		PackageQuery:   mockPackageQuery{pkg: "linux-image-" + newABI, ver: "1.0"},
	}

	res, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if res.Unchanged {
		t.Fatal("expected publish to succeed with changes")
	}

	// Assert builder was passed key and cert
	if !builder.called {
		t.Fatal("expected UKIBuilder to be called")
	}
	wantKey := filepath.Join(keyDir, "key.pem")
	wantCert := filepath.Join(keyDir, "cert.pem")
	if builder.lastReq.SecureBootKey != wantKey {
		t.Errorf("builder key: got %q, want %q", builder.lastReq.SecureBootKey, wantKey)
	}
	if builder.lastReq.SecureBootCert != wantCert {
		t.Errorf("builder cert: got %q, want %q", builder.lastReq.SecureBootCert, wantCert)
	}

	// Assert verifier was called on staged file
	if !verifier.called {
		t.Fatal("expected Verifier to be called")
	}
	if verifier.lastCertPEMPath != wantCert {
		t.Errorf("verifier cert: got %q, want %q", verifier.lastCertPEMPath, wantCert)
	}
	if !strings.Contains(verifier.lastVerifiedFile, "vintner-os-2.efi") {
		t.Errorf("verifier file: got %q, want path with vintner-os-2.efi", verifier.lastVerifiedFile)
	}

	// Assert new image record has signed = true
	loaded, err := manifest.Load(stateDir)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	var newImg *manifest.Image
	for i := range loaded.Images {
		if loaded.Images[i].Generation == 2 {
			newImg = &loaded.Images[i]
			break
		}
	}
	if newImg == nil {
		t.Fatal("generation 2 not found in manifest")
	}
	if newImg.UEFI == nil || !newImg.UEFI.Signed {
		t.Errorf("expected generation 2 uefi.signed == true, got %+v", newImg.UEFI)
	}
}

func TestPublisherVerifierFailureLeavesNothingPublished(t *testing.T) {
	rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
	newABI := "6.12.112-test"
	setupKernelFiles(t, rootDir, newABI)

	keyDir := t.TempDir()
	fp, err := secureboot.GenerateKey(keyDir, m.InstallationID)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	m.SecureBootTier = "A"
	m.Signing = &manifest.Signing{CertSHA256: fp}
	for i := range m.Images {
		if m.Images[i].UEFI != nil {
			m.Images[i].UEFI.Signed = true
		}
	}
	stateDir := filepath.Join(bootStorageDir, "vintner", "state")
	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	builder := &recordingUKIBuilder{}
	verifier := &recordingVerifier{
		errToReturn: errors.New("sbverify: signature verification failed: signature does not verify"),
	}

	opts := Options{
		KernelABI:      newABI,
		BootStorageDir: bootStorageDir,
		RootDir:        rootDir,
		CmdlineContent: m.Images[0].Cmdline,
		LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
		SecureBootDir:  keyDir,
		UKIBuilder:     builder,
		Verifier:       verifier,
		PackageQuery:   mockPackageQuery{pkg: "linux-image-" + newABI, ver: "1.0"},
	}

	_, err = Publish(context.Background(), opts)
	if err == nil {
		t.Fatal("expected Publish to fail on verifier error")
	}
	if !strings.Contains(err.Error(), "signature check failed") || !strings.Contains(err.Error(), "signature does not verify") {
		t.Errorf("error message missing expected text: %v", err)
	}

	// Assert 1: /EFI/Linux/ has no new file
	efiDir := filepath.Join(bootStorageDir, "EFI", "Linux")
	if found, err := bls.Find(efiDir, "vintner-os-2"); err == nil {
		t.Errorf("file for generation 2 unexpectedly found in %s: %s", efiDir, found)
	}

	// Assert 2: staging is empty
	stagingDir := filepath.Join(bootStorageDir, "vintner", "staging")
	entries, err := os.ReadDir(stagingDir)
	if err == nil && len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("staging not empty after verifier failure: %v", names)
	}

	// Assert 3: no record of generation 2 in manifest
	loaded, err := manifest.Load(stateDir)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	for _, img := range loaded.Images {
		if img.Generation == 2 {
			t.Errorf("generation 2 record unexpectedly present in manifest: %+v", img)
		}
	}
}

func TestPublisherKeyCertValidationBeforeBuild(t *testing.T) {
	newABI := "6.12.112-test"

	t.Run("CertificateFingerprintMismatch", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, newABI)

		keyDir := t.TempDir()
		_, err := secureboot.GenerateKey(keyDir, m.InstallationID)
		if err != nil {
			t.Fatal(err)
		}

		m.SecureBootTier = "A"
		m.Signing = &manifest.Signing{CertSHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
		for i := range m.Images {
			if m.Images[i].UEFI != nil {
				m.Images[i].UEFI.Signed = true
			}
		}
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		_ = manifest.Save(stateDir, m)

		builder := &recordingUKIBuilder{}
		opts := Options{
			KernelABI:      newABI,
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			CmdlineContent: m.Images[0].Cmdline,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
			SecureBootDir:  keyDir,
			UKIBuilder:     builder,
			PackageQuery:   mockPackageQuery{pkg: "linux-image-" + newABI, ver: "1.0"},
		}

		_, err = Publish(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "certificate fingerprint") {
			t.Fatalf("expected certificate fingerprint mismatch error, got: %v", err)
		}
		if builder.called {
			t.Error("builder was called despite certificate fingerprint mismatch")
		}
	})

	t.Run("KeyDoesNotMatchCertificate", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, newABI)

		keyDir1 := t.TempDir()
		fp1, err := secureboot.GenerateKey(keyDir1, m.InstallationID)
		if err != nil {
			t.Fatal(err)
		}

		keyDir2 := t.TempDir()
		_, err = secureboot.GenerateKey(keyDir2, m.InstallationID)
		if err != nil {
			t.Fatal(err)
		}

		// Overwrite keyDir1's key.pem with keyDir2's key.pem
		key2Bytes, _ := os.ReadFile(filepath.Join(keyDir2, "key.pem"))
		_ = os.WriteFile(filepath.Join(keyDir1, "key.pem"), key2Bytes, 0600)

		m.SecureBootTier = "A"
		m.Signing = &manifest.Signing{CertSHA256: fp1}
		for i := range m.Images {
			if m.Images[i].UEFI != nil {
				m.Images[i].UEFI.Signed = true
			}
		}
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		_ = manifest.Save(stateDir, m)

		builder := &recordingUKIBuilder{}
		opts := Options{
			KernelABI:      newABI,
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			CmdlineContent: m.Images[0].Cmdline,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
			SecureBootDir:  keyDir1,
			UKIBuilder:     builder,
			PackageQuery:   mockPackageQuery{pkg: "linux-image-" + newABI, ver: "1.0"},
		}

		_, err = Publish(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "does not match certificate") {
			t.Fatalf("expected key mismatch error, got: %v", err)
		}
		if builder.called {
			t.Error("builder was called despite key mismatch")
		}
	})

	t.Run("MissingKey", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, newABI)

		keyDir := t.TempDir()
		fp, err := secureboot.GenerateKey(keyDir, m.InstallationID)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(filepath.Join(keyDir, "key.pem"))

		m.SecureBootTier = "A"
		m.Signing = &manifest.Signing{CertSHA256: fp}
		for i := range m.Images {
			if m.Images[i].UEFI != nil {
				m.Images[i].UEFI.Signed = true
			}
		}
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		_ = manifest.Save(stateDir, m)

		builder := &recordingUKIBuilder{}
		opts := Options{
			KernelABI:      newABI,
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			CmdlineContent: m.Images[0].Cmdline,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
			SecureBootDir:  keyDir,
			UKIBuilder:     builder,
			PackageQuery:   mockPackageQuery{pkg: "linux-image-" + newABI, ver: "1.0"},
		}

		_, err = Publish(context.Background(), opts)
		if err == nil {
			t.Fatal("expected missing key to cause publish failure")
		}
		if builder.called {
			t.Error("builder was called despite missing key")
		}
	})
}

func TestPublisherTierNoneNeverCallsVerifier(t *testing.T) {
	rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
	newABI := "6.12.112-test"
	setupKernelFiles(t, rootDir, newABI)

	builder := &recordingUKIBuilder{}
	verifier := &recordingVerifier{}

	opts := Options{
		KernelABI:      newABI,
		BootStorageDir: bootStorageDir,
		RootDir:        rootDir,
		CmdlineContent: m.Images[0].Cmdline,
		LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
		UKIBuilder:     builder,
		Verifier:       verifier,
		PackageQuery:   mockPackageQuery{pkg: "linux-image-" + newABI, ver: "1.0"},
	}

	_, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	if verifier.called {
		t.Error("verifier was called for tier none")
	}
	if builder.lastReq.SecureBootKey != "" || builder.lastReq.SecureBootCert != "" {
		t.Errorf("builder was passed signing options in tier none: key=%q, cert=%q",
			builder.lastReq.SecureBootKey, builder.lastReq.SecureBootCert)
	}
}

func TestPublisherTiersBAndCRefused(t *testing.T) {
	for _, tier := range []string{"B", "C"} {
		t.Run("Tier_"+tier, func(t *testing.T) {
			rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
			setupKernelFiles(t, rootDir, "test")
			m.SecureBootTier = tier
			stateDir := filepath.Join(bootStorageDir, "vintner", "state")
			_ = manifest.Save(stateDir, m)

			opts := Options{
				KernelABI:      "test",
				BootStorageDir: bootStorageDir,
				RootDir:        rootDir,
				CmdlineContent: m.Images[0].Cmdline,
				LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
				PackageQuery:   mockPackageQuery{pkg: "p", ver: "1"},
			}
			_, err := Publish(context.Background(), opts)
			wantErr := fmt.Sprintf("unsupported secure boot tier %q (only \"none\" and \"A\" supported)", tier)
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("got %v, want %s", err, wantErr)
			}
		})
	}
}

func TestRealSigningRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not found: skipping real signing test")
	}
	if _, err := exec.LookPath("sbverify"); err != nil {
		t.Skip("sbverify not found: skipping real signing test")
	}
	if _, err := exec.LookPath("ukify"); err != nil {
		t.Skip("ukify not found: skipping real signing test")
	}
	stubPath := "/usr/lib/systemd/boot/efi/linuxx64.efi.stub"
	if _, err := os.Stat(stubPath); err != nil {
		t.Skip("systemd stub not found: skipping real signing test")
	}

	tmpDir := t.TempDir()
	keyDir1 := filepath.Join(tmpDir, "keys1")
	keyDir2 := filepath.Join(tmpDir, "keys2")

	_, err := secureboot.GenerateKey(keyDir1, "00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("GenerateKey 1: %v", err)
	}
	_, err = secureboot.GenerateKey(keyDir2, "00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatalf("GenerateKey 2: %v", err)
	}

	// Use stubPath as kernelPath because ukify with sbsign verifies whether linux has a signature table with sbverify, requiring a valid PE file.
	kernelPath := stubPath
	initrdPath := filepath.Join(tmpDir, "initrd.img")
	if err := os.WriteFile(initrdPath, []byte("fake-initrd-content-for-test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	osRelPath := filepath.Join(tmpDir, "os-release")
	if err := os.WriteFile(osRelPath, []byte("NAME=Test\nID=vintner-os\nIMAGE_ID=vintner-os\nIMAGE_VERSION=1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	signedUKI := filepath.Join(tmpDir, "signed.efi")
	keyPath1 := filepath.Join(keyDir1, "key.pem")
	certPath1 := filepath.Join(keyDir1, "cert.pem")
	certPath2 := filepath.Join(keyDir2, "cert.pem")

	ctx := context.Background()
	builder := RealUKIBuilder{}

	// 1. Build signed UKI
	err = builder.Build(ctx, UKIRequest{
		LinuxPath:      kernelPath,
		InitrdPath:     initrdPath,
		StubPath:       stubPath,
		OSReleasePath:  osRelPath,
		Cmdline:        "console=ttyS0 quiet",
		Uname:          "6.12.0-test",
		OutputPath:     signedUKI,
		SecureBootKey:  keyPath1,
		SecureBootCert: certPath1,
	})
	if err != nil {
		t.Fatalf("RealUKIBuilder.Build (signed) failed: %v", err)
	}

	verifier := secureboot.SbverifyVerifier{}

	// 2. SbverifyVerifier accepts it with the generated certificate
	if err := verifier.Verify(ctx, signedUKI, certPath1); err != nil {
		t.Errorf("expected sbverify to accept signed UKI with cert1: %v", err)
	}

	// 3. SbverifyVerifier rejects it against a certificate from a second GenerateKey run
	if err := verifier.Verify(ctx, signedUKI, certPath2); err == nil {
		t.Error("expected sbverify to reject signed UKI with cert2, but it accepted")
	}

	// 4. SbverifyVerifier rejects it after one byte inside the initrd section is flipped
	peFile, err := pe.Open(signedUKI)
	if err != nil {
		t.Fatalf("open signed PE: %v", err)
	}
	var initrdSec *pe.Section
	for _, sec := range peFile.Sections {
		if strings.HasPrefix(sec.Name, ".initrd") {
			initrdSec = sec
			break
		}
	}
	peFile.Close()
	if initrdSec == nil {
		t.Fatal("no .initrd section found in signed UKI")
	}

	origBytes, err := os.ReadFile(signedUKI)
	if err != nil {
		t.Fatal(err)
	}
	flippedBytes := make([]byte, len(origBytes))
	copy(flippedBytes, origBytes)
	flipOffset := int(initrdSec.Offset)
	flippedBytes[flipOffset] ^= 0xFF

	// Assert the flipped file differs from the original at that offset
	if flippedBytes[flipOffset] == origBytes[flipOffset] {
		t.Fatal("flipped byte equals original byte")
	}
	flippedUKI := filepath.Join(tmpDir, "flipped.efi")
	if err := os.WriteFile(flippedUKI, flippedBytes, 0755); err != nil {
		t.Fatal(err)
	}

	if err := verifier.Verify(ctx, flippedUKI, certPath1); err == nil {
		t.Error("expected sbverify to reject UKI with flipped byte in .initrd, but it accepted")
	}

	// 5. SbverifyVerifier rejects an unsigned UKI built without signing options
	unsignedUKI := filepath.Join(tmpDir, "unsigned.efi")
	err = builder.Build(ctx, UKIRequest{
		LinuxPath:     kernelPath,
		InitrdPath:    initrdPath,
		StubPath:      stubPath,
		OSReleasePath: osRelPath,
		Cmdline:       "console=ttyS0 quiet",
		Uname:         "6.12.0-test",
		OutputPath:    unsignedUKI,
	})
	if err != nil {
		t.Fatalf("RealUKIBuilder.Build (unsigned) failed: %v", err)
	}
	if err := verifier.Verify(ctx, unsignedUKI, certPath1); err == nil {
		t.Error("expected sbverify to reject unsigned UKI, but it accepted")
	}
}
