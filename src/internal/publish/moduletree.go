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
