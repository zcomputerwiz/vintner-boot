// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// syncDir fsyncs the directory at dir.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// UpdateRetainedKernels rewrites /etc/apt/apt.conf.d/50vintner-retained-kernels under rootDir
// so that its APT::NeverAutoRemove list holds exactly the unique packages provided,
// each as an anchored, escaped regular expression (^...$).
// It writes a temporary file in the same directory, fsyncs it, renames it into place, and fsyncs the directory.
func UpdateRetainedKernels(rootDir string, packages []string) error {
	aptConfDir := filepath.Join(rootDir, "etc", "apt", "apt.conf.d")
	if err := os.MkdirAll(aptConfDir, 0755); err != nil {
		return fmt.Errorf("create apt.conf.d: %w", err)
	}

	unique := make(map[string]bool)
	for _, p := range packages {
		if p != "" {
			unique[p] = true
		}
	}
	sortedPkgs := make([]string, 0, len(unique))
	for p := range unique {
		sortedPkgs = append(sortedPkgs, p)
	}
	sort.Strings(sortedPkgs)

	var buf bytes.Buffer
	buf.WriteString("APT::NeverAutoRemove {\n")
	for _, p := range sortedPkgs {
		escaped := regexp.QuoteMeta(p)
		buf.WriteString(fmt.Sprintf("\t\"^%s$\";\n", escaped))
	}
	buf.WriteString("};\n")

	tmpFile, err := os.CreateTemp(aptConfDir, ".50vintner-retained-kernels.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp apt config: %w", err)
	}
	_ = tmpFile.Chmod(0644)
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write(buf.Bytes()); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write apt config: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temp apt config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp apt config: %w", err)
	}

	targetPath := filepath.Join(aptConfDir, "50vintner-retained-kernels")
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("rename apt config into place: %w", err)
	}

	if err := syncDir(aptConfDir); err != nil {
		return fmt.Errorf("sync apt.conf.d directory: %w", err)
	}

	return nil
}
