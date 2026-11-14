// SPDX-License-Identifier: GPL-2.0-or-later

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

// Tier B is healthy when Secure Boot is on and shim's MOK list holds the
// machine certificate (docs/secure-boot.md §8.5).
func TestCheckHealthTierB(t *testing.T) {
	efivarsDir := t.TempDir()
	sbDir := t.TempDir()
	fp, err := secureboot.GenerateKey(sbDir, "test-install-uuid")
	if err != nil {
		t.Fatal(err)
	}
	der, _ := os.ReadFile(filepath.Join(sbDir, "cert.der"))
	m := &manifest.Manifest{SecureBootTier: "B", Signing: &manifest.Signing{CertSHA256: fp}}
	set := func(name string, guid efivar.GUID, data []byte) {
		t.Helper()
		if err := efivar.Set(efivarsDir, name, guid, 7, data); err != nil {
			t.Fatal(err)
		}
	}

	r := CheckHealth(m, nil, efivarsDir, sbDir, true)
	if len(r) != 2 || r[0] != "secure_boot=off" || !strings.Contains(r[1], "not in shim's MOK list") {
		t.Fatalf("Secure Boot off, nothing enrolled: %q", r)
	}

	set("SecureBoot", efivar.GlobalVariable, []byte{1})
	set("SetupMode", efivar.GlobalVariable, []byte{0})
	if err := secureboot.WriteMOKRequest(efivarsDir, der, "vintner1"); err != nil {
		t.Fatal(err)
	}
	r = CheckHealth(m, nil, efivarsDir, sbDir, true)
	if len(r) != 1 || !strings.Contains(r[0], "a request is pending") {
		t.Fatalf("request pending: %q", r)
	}

	set("MokListRT", efivar.ShimLock, secureboot.MOKSignatureList(der))
	if r := CheckHealth(m, nil, efivarsDir, sbDir, true); len(r) != 0 {
		t.Fatalf("enrolled, Secure Boot on: %q", r)
	}

	// Tier A does not look at the MOK list.
	os.Remove(filepath.Join(efivarsDir, "MokListRT-"+string(efivar.ShimLock)))
	mA := &manifest.Manifest{SecureBootTier: "A", Signing: &manifest.Signing{CertSHA256: fp}}
	if r := CheckHealth(mA, nil, efivarsDir, sbDir, true); len(r) != 0 {
		t.Fatalf("tier A: %q", r)
	}

	// The certificate cannot be read.
	r = CheckHealth(m, nil, efivarsDir, t.TempDir(), true)
	if len(r) < 1 || !strings.HasPrefix(r[0], "secure boot tier B: the machine certificate cannot be read") {
		t.Fatalf("no certificate: %q", r)
	}
}

// The boot entry check looks for the file the tier's entry starts.
func TestCheckHealthBootEntryFollowsTheTier(t *testing.T) {
	dir := t.TempDir()
	dp, err := efivar.DevicePath(efivar.PartitionRef{Number: 1, FirstLBA: 2048, Sectors: 4096, Signature: "11223344-5566-7788-99aa-bbccddeeff00"}, efivar.VintnerShimPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := efivar.SetFirstBootEntry(dir, efivar.VintnerDescription, dp); err != nil {
		t.Fatal(err)
	}
	has := func(r []string) bool {
		for _, s := range r {
			if strings.Contains(s, "NVRAM boot entry missing") {
				return true
			}
		}
		return false
	}
	mB := &manifest.Manifest{Firmware: "uefi", SecureBootTier: "B"}
	if r := CheckHealth(mB, nil, dir, "", true); has(r) {
		t.Errorf("tier B with an entry for shim: %q", r)
	}
	mNone := &manifest.Manifest{Firmware: "uefi", SecureBootTier: "none"}
	if r := CheckHealth(mNone, nil, dir, "", true); !has(r) {
		t.Errorf("tier none with only an entry for shim: %q", r)
	}
}
