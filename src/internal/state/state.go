// SPDX-License-Identifier: GPL-2.0-or-later

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

// View describes what is on boot storage.
type View struct {
	// UEFIFiles maps a filename in /EFI/Linux/ to its SHA-256.
	UEFIFiles map[string]string
	// BIOSDirs maps a slot directory name (e.g. os-<gen>) to a map of filename to SHA-256.
	BIOSDirs map[string]map[string]string
	// GrubEnv holds the grubenv variables.
	GrubEnv *grubenv.Env
	// StagingFiles lists the filenames in /vintner/staging/.
	StagingFiles []string
}

// Scan builds a View from a mounted boot storage directory.
// A missing directory is an empty set, not an error.
func Scan(bootStorage string) (*View, error) {
	v := &View{
		UEFIFiles: make(map[string]string),
		BIOSDirs:  make(map[string]map[string]string),
		GrubEnv:   &grubenv.Env{},
	}

	// Read /EFI/Linux/
	efiDir := filepath.Join(bootStorage, "EFI", "Linux")
	entries, err := os.ReadDir(efiDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				h, hErr := fileSHA256(filepath.Join(efiDir, e.Name()))
				if hErr == nil {
					v.UEFIFiles[e.Name()] = h
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Read /vintner/bios/
	biosBase := filepath.Join(bootStorage, "vintner", "bios")
	bEntries, err := os.ReadDir(biosBase)
	if err == nil {
		for _, e := range bEntries {
			if e.IsDir() {
				slotDir := filepath.Join(biosBase, e.Name())
				sEntries, sErr := os.ReadDir(slotDir)
				if sErr == nil {
					files := make(map[string]string)
					for _, se := range sEntries {
						if !se.IsDir() {
							h, hErr := fileSHA256(filepath.Join(slotDir, se.Name()))
							if hErr == nil {
								files[se.Name()] = h
							}
						}
					}
					v.BIOSDirs[e.Name()] = files
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Read /grub/grubenv
	grubPath := filepath.Join(bootStorage, "grub", "grubenv")
	env, err := grubenv.Read(grubPath)
	if err == nil {
		v.GrubEnv = env
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Read /vintner/staging/
	stagingDir := filepath.Join(bootStorage, "vintner", "staging")
	stEntries, err := os.ReadDir(stagingDir)
	if err == nil {
		for _, e := range stEntries {
			if !e.IsDir() {
				v.StagingFiles = append(v.StagingFiles, e.Name())
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return v, nil
}

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

// Action represents a boot-storage action to perform.
type Action struct {
	Type string // only "remove_staging" currently
	Name string
}

// Reconcile applies the reconciliation rules to m in memory.
func Reconcile(m *manifest.Manifest, v *View) (actions []Action, reports []string) {
	// Rule 3: A file in /EFI/Linux/ or /vintner/bios/ that matches vintner-os-* but has no record is reported.
	if m.Firmware == "uefi" || m.Firmware == "both" {
		for f := range v.UEFIFiles {
			parsed, ok := bls.Parse(f)
			if !ok {
				continue
			}
			gen, ok := bls.IsVintnerOrdinary(parsed.Stem)
			if ok {
				found := false
				for _, img := range m.Images {
					if img.Generation == gen {
						found = true
						break
					}
				}
				if !found {
					reports = append(reports, fmt.Sprintf("untracked uefi file %s", f))
				}
			}
		}
	}

	if m.Firmware == "bios" || m.Firmware == "both" {
		for d := range v.BIOSDirs {
			if strings.HasPrefix(d, "os-") {
				var gen int
				_, err := fmt.Sscanf(d, "os-%d", &gen)
				if err == nil {
					found := false
					for _, img := range m.Images {
						if img.Generation == gen {
							found = true
							break
						}
					}
					if !found {
						reports = append(reports, fmt.Sprintf("untracked bios directory %s", d))
					}
				}
			}
		}
	}

	var newImages []manifest.Image

	for _, img := range m.Images {
		if img.State == "STAGED" {
			// Rule 1: A STAGED record whose file is in its final place and verifies becomes TRYING.
			// Rule 2: A STAGED record whose file is not in its final place has its staging file and record removed.
			verifies := true

			if m.Firmware == "uefi" || m.Firmware == "both" {
				// verify UEFI
				expectedStem := img.UEFI.Entry
				foundName := ""
				for f := range v.UEFIFiles {
					parsed, ok := bls.Parse(f)
					if ok && parsed.Stem == expectedStem {
						foundName = f
						break
					}
				}
				if foundName != "" && v.UEFIFiles[foundName] == img.UEFI.SHA256 {
					// ok
				} else {
					verifies = false
				}
			}

			if m.Firmware == "bios" || m.Firmware == "both" {
				// verify BIOS
				dirName := fmt.Sprintf("os-%d", img.Generation)
				if files, ok := v.BIOSDirs[dirName]; ok {
					if files["vmlinuz"] != img.BIOS.Files.Vmlinuz ||
						files["initrd.img"] != img.BIOS.Files.InitrdImg ||
						files["slot.cfg"] != img.BIOS.Files.SlotCfg {
						verifies = false
					}
				} else {
					verifies = false
				}
			}

			if verifies {
				img.State = "TRYING"
				newImages = append(newImages, img)
			} else {
				// Rule 2
				for _, f := range v.StagingFiles {
					if strings.HasPrefix(f, fmt.Sprintf("vintner-os-%d", img.Generation)) || strings.HasPrefix(f, fmt.Sprintf("os-%d", img.Generation)) {
						actions = append(actions, Action{Type: "remove_staging", Name: f})
					}
				}
			}
			continue
		}

		// Rule 4: A record whose file is missing is reported.
		missing := false
		if m.Firmware == "uefi" || m.Firmware == "both" {
			expectedStem := img.UEFI.Entry
			found := false
			for f := range v.UEFIFiles {
				parsed, ok := bls.Parse(f)
				if ok && parsed.Stem == expectedStem {
					found = true
					break
				}
			}
			if !found {
				missing = true
			}
		}

		if m.Firmware == "bios" || m.Firmware == "both" {
			dirName := fmt.Sprintf("os-%d", img.Generation)
			if _, ok := v.BIOSDirs[dirName]; !ok {
				missing = true
			}
		}

		if missing {
			reports = append(reports, fmt.Sprintf("missing files for generation %d", img.Generation))
		}

		// Rule 5: A counter at zero on a TRYING or GOOD record sets the record to FAILED.
		if img.State == "TRYING" || img.State == "GOOD" {
			zero := false
			if m.Firmware == "uefi" || m.Firmware == "both" {
				expectedStem := img.UEFI.Entry
				for f := range v.UEFIFiles {
					parsed, ok := bls.Parse(f)
					if ok && parsed.Stem == expectedStem {
						if parsed.Bad() {
							zero = true
						}
						break
					}
				}
			}

			if m.Firmware == "bios" || m.Firmware == "both" {
				varName := fmt.Sprintf("VINTNER_T_%d", img.Generation)
				if val, ok := v.GrubEnv.Get(varName); ok {
					if val == "0" || !strings.Contains(val, "x") {
						zero = true
					}
				} else {
					// Unset counts as bad according to INTERFACE.md
					zero = true
				}
			}

			if zero {
				img.State = "FAILED"
			}
		}

		newImages = append(newImages, img)
	}

	m.Images = newImages

	if m.Images == nil {
		m.Images = []manifest.Image{}
	}

	return actions, reports
}

// Keep selects the images to keep and returns the generations to remove in order.
func Keep(m *manifest.Manifest) (keep, remove []int, overBudget bool) {
	// 1. the newest GOOD image that is not stale
	// 2. the newest GOOD image whose ABI differs from 1
	// 3. every TRYING image
	// 4. further images, newest first, until retain images remain.

	// FAILED images go first, then the oldest.

	// Helper to reverse list if needed, but we can just process backwards if sorted by generation.
	// Since Generation is strictly increasing (ADR-0003), higher generation == newer.

	var goodNewestFirst []manifest.Image
	for i := len(m.Images) - 1; i >= 0; i-- {
		if m.Images[i].State == "GOOD" {
			goodNewestFirst = append(goodNewestFirst, m.Images[i])
		}
	}

	keptSet := make(map[int]bool)

	// Rule 1: Newest GOOD not stale
	var baseABI string
	for _, img := range goodNewestFirst {
		if !img.Stale {
			keptSet[img.Generation] = true
			baseABI = img.Kernel.ABI
			break
		}
	}

	// Rule 2: Newest GOOD whose ABI differs from 1
	if baseABI != "" {
		for _, img := range goodNewestFirst {
			if img.Kernel.ABI != baseABI {
				keptSet[img.Generation] = true
				break
			}
		}
	}

	// Rule 3: every TRYING image
	for _, img := range m.Images {
		if img.State == "TRYING" {
			keptSet[img.Generation] = true
		}
	}

	// Rule 4: further images, newest first, until retain images remain
	for i := len(m.Images) - 1; i >= 0; i-- {
		img := m.Images[i]
		if len(keptSet) >= m.Retain {
			break
		}
		if img.State != "FAILED" && !keptSet[img.Generation] {
			keptSet[img.Generation] = true
		}
	}

	if len(keptSet) > m.Retain {
		overBudget = true
	}

	for _, img := range m.Images {
		if keptSet[img.Generation] {
			keep = append(keep, img.Generation)
		}
	}

	// Removal list: FAILED first, then oldest.
	var failedRemovals []int
	var otherRemovals []int

	for _, img := range m.Images {
		if !keptSet[img.Generation] {
			if img.State == "FAILED" {
				failedRemovals = append(failedRemovals, img.Generation)
			} else {
				otherRemovals = append(otherRemovals, img.Generation)
			}
		}
	}

	remove = append(remove, failedRemovals...)
	remove = append(remove, otherRemovals...)

	return keep, remove, overBudget
}
