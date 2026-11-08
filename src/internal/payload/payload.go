// SPDX-License-Identifier: GPL-2.0-or-later

// Package payload opens the inert payloads that the vintner-platform package
// installs under /usr/lib/vintner/payloads (docs/packaging.md) and verifies
// them against their MANIFEST before anything is deployed from them.
package payload

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultDir is where vintner-platform puts the payloads.
const DefaultDir = "/usr/lib/vintner/payloads"

// Paths inside the payload directory that a deployment reads.
const (
	SystemdBoot  = "uefi/systemd-bootx64.efi"
	Stub         = "uefi/linuxx64.efi.stub"
	LoaderConf   = "uefi/loader.conf"
	GrubDir      = "bios/i386-pc"
	GrubCfg      = "bios/grub.cfg"
	CoreModules  = "bios/core-modules.txt"
	RecoveryKern = "recovery/vmlinuz"
	RecoveryInit = "recovery/initrd.img"
)

// Required lists the files a payload directory must hold (the MANIFEST and
// VERSIONS are checked separately).
var Required = []string{
	SystemdBoot, Stub, LoaderConf,
	GrubDir + "/boot.img", GrubDir + "/diskboot.img", GrubDir + "/kernel.img", GrubDir + "/lzma_decompress.img", GrubDir + "/moddep.lst",
	GrubCfg, CoreModules, RecoveryKern, RecoveryInit,
}

// Payload is a verified payload directory.
type Payload struct {
	Dir      string
	Files    map[string]string // path (slash form, no "./") to its SHA-256
	Versions string            // the contents of VERSIONS
}

// Path returns the absolute path of a file in the payload.
func (p *Payload) Path(rel string) string { return filepath.Join(p.Dir, filepath.FromSlash(rel)) }

// Open reads dir/MANIFEST, hashes every file it lists, and refuses a directory
// where a listed file is missing or differs, a file is not listed, or a file a
// deployment needs is absent.
func Open(dir string) (*Payload, error) {
	mf, err := os.Open(filepath.Join(dir, "MANIFEST"))
	if err != nil {
		return nil, fmt.Errorf("payload: %w (is vintner-platform installed?)", err)
	}
	defer mf.Close()

	p := &Payload{Dir: dir, Files: map[string]string{}}
	sc := bufio.NewScanner(mf)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			return nil, fmt.Errorf("payload: MANIFEST line %d is not \"<sha256>  <path>\"", n)
		}
		name = strings.TrimPrefix(name, "./")
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return nil, fmt.Errorf("payload: MANIFEST line %d names the path %q", n, name)
		}
		if _, dup := p.Files[name]; dup {
			return nil, fmt.Errorf("payload: MANIFEST lists %s twice", name)
		}
		p.Files[name] = strings.ToLower(sum)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	var bad []string
	for name, want := range p.Files {
		got, err := hashFile(p.Path(name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			bad = append(bad, name+" is listed but missing")
		case err != nil:
			return nil, err
		case got != want:
			bad = append(bad, name+" differs from its MANIFEST hash")
		}
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if rel == "MANIFEST" {
			return nil
		}
		if _, ok := p.Files[rel]; !ok {
			bad = append(bad, rel+" is not listed in the MANIFEST")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, r := range Required {
		if _, ok := p.Files[r]; !ok {
			bad = append(bad, r+" is required and not in the MANIFEST")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf("payload: %s is not intact: %s", dir, strings.Join(bad, "; "))
	}
	if v, err := os.ReadFile(filepath.Join(dir, "VERSIONS")); err == nil {
		p.Versions = string(v)
	}
	return p, nil
}

func hashFile(path string) (string, error) {
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
