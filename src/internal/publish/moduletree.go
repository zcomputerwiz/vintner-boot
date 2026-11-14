// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

// fileSHA256 returns the lowercase hex SHA-256 checksum of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeDKMSSHA256 calculates the SHA-256 of the sorted list of "<path> <sha256>\n" lines
// for every regular file under dkmsDir, or the SHA-256 of the empty string if dkmsDir does not exist or has no files.
func ComputeDKMSSHA256(dkmsDir string) (string, error) {
	info, err := os.Stat(dkmsDir)
	if err != nil {
		if os.IsNotExist(err) {
			h := sha256.Sum256([]byte(""))
			return hex.EncodeToString(h[:]), nil
		}
		return "", err
	}
	if !info.IsDir() {
		h := sha256.Sum256([]byte(""))
		return hex.EncodeToString(h[:]), nil
	}

	var lines []string
	err = filepath.Walk(dkmsDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			rel, err := filepath.Rel(dkmsDir, path)
			if err != nil {
				return err
			}
			fHash, err := fileSHA256(path)
			if err != nil {
				return err
			}
			lines = append(lines, fmt.Sprintf("%s %s\n", filepath.ToSlash(rel), fHash))
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(lines) == 0 {
		h := sha256.Sum256([]byte(""))
		return hex.EncodeToString(h[:]), nil
	}

	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeModuleTree computes the module tree identity for kernel ABI under rootDir.
func ComputeModuleTree(rootDir, abi string) (*manifest.ModuleTree, error) {
	vmlinuzPath := filepath.Join(rootDir, "boot", "vmlinuz-"+abi)
	kSHA, err := fileSHA256(vmlinuzPath)
	if err != nil {
		return nil, fmt.Errorf("kernel %s: %w", vmlinuzPath, err)
	}

	depPath := filepath.Join(rootDir, "lib", "modules", abi, "modules.dep")
	depSHA, err := fileSHA256(depPath)
	if err != nil {
		return nil, fmt.Errorf("modules.dep %s: %w", depPath, err)
	}

	dkmsDir := filepath.Join(rootDir, "lib", "modules", abi, "updates", "dkms")
	dkmsSHA, err := ComputeDKMSSHA256(dkmsDir)
	if err != nil {
		return nil, fmt.Errorf("dkms %s: %w", dkmsDir, err)
	}

	return &manifest.ModuleTree{
		KernelSHA256:     kSHA,
		ModulesDepSHA256: depSHA,
		DKMSSHA256:       dkmsSHA,
	}, nil
}

// emptyDKMSSHA256 is what ComputeDKMSSHA256 gives for no DKMS module.
var emptyDKMSSHA256 = func() string {
	h := sha256.Sum256(nil)
	return hex.EncodeToString(h[:])
}()

// treeDifference says what differs between the module tree now (cur) and the one
// an image recorded (rec): "dkms", "modules.dep", both joined by ", ", or "" when
// they agree. The kernel is compared elsewhere. An image with no record
// (module_tree null, as the installer writes generation 1) differs only when
// DKMS modules exist, so that installing does not make a pointless successor.
func treeDifference(cur, rec *manifest.ModuleTree) string {
	if rec == nil {
		if cur.DKMSSHA256 != emptyDKMSSHA256 {
			return "dkms"
		}
		return ""
	}
	var parts []string
	if cur.DKMSSHA256 != rec.DKMSSHA256 {
		parts = append(parts, "dkms")
	}
	if cur.ModulesDepSHA256 != rec.ModulesDepSHA256 {
		parts = append(parts, "modules.dep")
	}
	return strings.Join(parts, ", ")
}

// StaleReports names the images whose recorded module tree no longer matches
// /lib/modules under rootDir, which the health service shows (WO-0010 5.3). Only
// a publish recomputes the manifest's stale flag; this reads the tree now.
func StaleReports(m *manifest.Manifest, rootDir string) []string {
	// Only the newest image of a kernel matters: an older one is out of date for
	// good, and the newer one says what to do about it.
	newest := map[string]int{}
	for _, img := range m.Images {
		if img.State != "FAILED" && img.Generation > newest[img.Kernel.ABI] {
			newest[img.Kernel.ABI] = img.Generation
		}
	}
	var reports []string
	for _, img := range m.Images {
		if img.Generation != newest[img.Kernel.ABI] {
			continue
		}
		cur, err := ComputeModuleTree(rootDir, img.Kernel.ABI)
		if err != nil {
			continue // the kernel or its modules are gone: publish flags that as stale
		}
		if diff := treeDifference(cur, img.ModuleTree); diff != "" {
			reports = append(reports, fmt.Sprintf("generation %d is out of date: the module tree of %s changed since it was published (%s); vintner publish --kernel %s publishes an image that records the current one", img.Generation, img.Kernel.ABI, diff, img.Kernel.ABI))
		}
	}
	return reports
}

// ModuleTreeMatches checks if the installed module tree for abi under rootDir matches expected.
// A missing kernel, modules.dep or module directory counts as not matching.
func ModuleTreeMatches(rootDir, abi string, expected *manifest.ModuleTree) bool {
	if expected == nil {
		return false
	}

	vmlinuzPath := filepath.Join(rootDir, "boot", "vmlinuz-"+abi)
	if _, err := os.Stat(vmlinuzPath); err != nil {
		return false
	}

	modDir := filepath.Join(rootDir, "lib", "modules", abi)
	fi, err := os.Stat(modDir)
	if err != nil || !fi.IsDir() {
		return false
	}

	depPath := filepath.Join(modDir, "modules.dep")
	if _, err := os.Stat(depPath); err != nil {
		return false
	}

	curr, err := ComputeModuleTree(rootDir, abi)
	if err != nil {
		return false
	}

	return curr.KernelSHA256 == expected.KernelSHA256 &&
		curr.ModulesDepSHA256 == expected.ModulesDepSHA256 &&
		curr.DKMSSHA256 == expected.DKMSSHA256
}
