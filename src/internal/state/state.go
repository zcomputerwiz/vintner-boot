// SPDX-License-Identifier: GPL-2.0-or-later

// Package state reconciles the manifest with boot storage and selects images
// for garbage collection (schema/state-machine.md).
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

// View describes what is on boot storage.
type View struct {
	// UEFIFiles maps a file name in /EFI/Linux/ to its SHA-256.
	UEFIFiles map[string]string
	// BIOSDirs maps a slot directory name in /vintner/bios/ (os-<gen>) to its
	// files' SHA-256 by file name.
	BIOSDirs map[string]map[string]string
	// GrubEnv holds the grubenv variables; nil if /grub/grubenv is missing or
	// unreadable, with the reason in GrubEnvErr.
	GrubEnv    *grubenv.Env
	GrubEnvErr error
	// Staging lists the names in /vintner/staging/, files and directories.
	Staging []string
}

// Scan builds a View from a mounted boot storage directory.
// A missing directory is an empty set, not an error.
func Scan(bootStorage string) (*View, error) {
	v := &View{UEFIFiles: map[string]string{}, BIOSDirs: map[string]map[string]string{}}

	efiDir := filepath.Join(bootStorage, "EFI", "Linux")
	entries, err := readDir(efiDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			if v.UEFIFiles[e.Name()], err = fileSHA256(filepath.Join(efiDir, e.Name())); err != nil {
				return nil, err
			}
		}
	}

	biosDir := filepath.Join(bootStorage, "vintner", "bios")
	if entries, err = readDir(biosDir); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slot := filepath.Join(biosDir, e.Name())
		files, err := readDir(slot)
		if err != nil {
			return nil, err
		}
		v.BIOSDirs[e.Name()] = map[string]string{}
		for _, f := range files {
			if !f.IsDir() {
				if v.BIOSDirs[e.Name()][f.Name()], err = fileSHA256(filepath.Join(slot, f.Name())); err != nil {
					return nil, err
				}
			}
		}
	}

	v.GrubEnv, v.GrubEnvErr = grubenv.Read(filepath.Join(bootStorage, "grub", "grubenv"))

	if entries, err = readDir(filepath.Join(bootStorage, "vintner", "staging")); err != nil {
		return nil, err
	}
	for _, e := range entries {
		v.Staging = append(v.Staging, e.Name())
	}
	return v, nil
}

func readDir(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return entries, err
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

// StagingNames are the staging names of generation g: the UKI and the BIOS
// slot directory, as named in schema/state-machine.md.
func StagingNames(g int) []string {
	return []string{fmt.Sprintf("vintner-os-%d.efi", g), fmt.Sprintf("os-%d", g)}
}

// Action is a boot-storage change Reconcile asks for: removing a staging file
// or directory (rule 2), or finishing the BIOS arming of an image whose files
// are in place (rule 1b).
type Action struct {
	Type       string // "remove_staging" or "arm_bios"
	Name       string // remove_staging: name in /vintner/staging/
	Generation int    // arm_bios: the generation to arm
	Tries      int    // arm_bios: counter length, the manifest's tries
}

// String renders a as "<type> <target>", the form the ACTION: lines use.
func (a Action) String() string {
	if a.Type == "arm_bios" {
		return fmt.Sprintf("%s generation %d", a.Type, a.Generation)
	}
	return a.Type + " " + a.Name
}

// Apply carries out a. Arming writes VINTNER_T_<g> and prepends g to
// VINTNER_ORDER in one grubenv write, as the publisher's step 11 does.
func Apply(bootStorage string, a Action) error {
	switch a.Type {
	case "remove_staging":
		return os.RemoveAll(filepath.Join(bootStorage, "vintner", "staging", a.Name))
	case "arm_bios":
		path := filepath.Join(bootStorage, "grub", "grubenv")
		env, err := grubenv.Read(path)
		if err != nil {
			return fmt.Errorf("arm generation %d: %w", a.Generation, err)
		}
		g := strconv.Itoa(a.Generation)
		env.Set("VINTNER_T_"+g, strings.Repeat("x", a.Tries))
		order, _ := env.Get("VINTNER_ORDER")
		env.Set("VINTNER_ORDER", strings.TrimSpace(g+" "+order))
		return grubenv.Write(path, env)
	}
	return fmt.Errorf("unknown action %q", a.Type)
}

// uefiFile returns the file in /EFI/Linux/ for stem, with any counter.
func (v *View) uefiFile(stem string) (bls.Entry, string, bool) {
	for f := range v.UEFIFiles {
		if e, ok := bls.Parse(f); ok && e.Stem == stem {
			return e, f, true
		}
	}
	return bls.Entry{}, "", false
}

// inPlace reports whether any and all of img's final files are present, and
// whether all of them verify. For BIOS, verifying also needs the generation in
// VINTNER_ORDER and a counter set, as the STAGED to TRYING transition writes.
func (v *View) inPlace(img manifest.Image, uefi, bios bool) (anyPresent, allPresent, verifies, filesVerify bool) {
	allPresent, verifies = true, true
	if uefi {
		_, f, ok := v.uefiFile("vintner-os-" + strconv.Itoa(img.Generation))
		anyPresent = anyPresent || ok
		allPresent = allPresent && ok
		verifies = verifies && ok && img.UEFI != nil && v.UEFIFiles[f] == img.UEFI.SHA256
	}
	if bios {
		files, ok := v.BIOSDirs["os-"+strconv.Itoa(img.Generation)]
		anyPresent = anyPresent || ok
		allPresent = allPresent && ok
		biosFiles := ok && img.BIOS != nil &&
			files["vmlinuz"] == img.BIOS.Files.Vmlinuz &&
			files["initrd.img"] == img.BIOS.Files.InitrdImg &&
			files["slot.cfg"] == img.BIOS.Files.SlotCfg
		filesVerify = biosFiles
		verifies = verifies && biosFiles &&
			v.inOrder(img.Generation) && v.counter(img.Generation) != ""
	}
	return anyPresent, allPresent, verifies, filesVerify
}

// uefiVerifies reports whether img's UKI is in place and matches its record;
// true when the manifest has no UEFI side.
func (v *View) uefiVerifies(img manifest.Image, uefi bool) bool {
	if !uefi {
		return true
	}
	_, f, ok := v.uefiFile("vintner-os-" + strconv.Itoa(img.Generation))
	return ok && img.UEFI != nil && v.UEFIFiles[f] == img.UEFI.SHA256
}

func (v *View) counter(g int) string {
	if v.GrubEnv == nil {
		return ""
	}
	c, _ := v.GrubEnv.Get("VINTNER_T_" + strconv.Itoa(g))
	return c
}

func (v *View) inOrder(g int) bool {
	if v.GrubEnv == nil {
		return false
	}
	order, _ := v.GrubEnv.Get("VINTNER_ORDER")
	for _, s := range strings.Fields(order) {
		if s == strconv.Itoa(g) {
			return true
		}
	}
	return false
}

// Reconcile applies the rules in schema/state-machine.md "Reconciliation"
// to m in memory. Where the rules are ambiguous it takes the safer reading and
// reports instead of changing anything:
//   - a STAGED record with some or all files in place that do not verify;
//   - a BIOS counter that is unset or malformed, or an unreadable grubenv
//     (only "0" is a counter at zero).
func Reconcile(m *manifest.Manifest, v *View) (actions []Action, reports []string) {
	uefi := m.Firmware == "uefi" || m.Firmware == "both"
	bios := m.Firmware == "bios" || m.Firmware == "both"
	known := map[int]bool{}
	for _, img := range m.Images {
		known[img.Generation] = true
	}

	// Rule 3: vintner-os-* files without a record.
	if uefi {
		for _, f := range sortedKeys(v.UEFIFiles) {
			if e, ok := bls.Parse(f); ok {
				if g, ok := bls.IsVintnerOrdinary(e.Stem); ok && !known[g] {
					reports = append(reports, "untracked uefi file "+f)
				}
			}
		}
	}
	if bios {
		for _, d := range sortedKeys(v.BIOSDirs) {
			if g, err := strconv.Atoi(strings.TrimPrefix(d, "os-")); err == nil && strings.HasPrefix(d, "os-") && !known[g] {
				reports = append(reports, "untracked bios directory "+d)
			}
		}
		if v.GrubEnv == nil {
			reports = append(reports, fmt.Sprintf("grubenv unreadable, BIOS counters not checked: %v", v.GrubEnvErr))
		}
	}

	staging := map[string]bool{}
	for _, s := range v.Staging {
		staging[s] = true
	}
	images := []manifest.Image{}
	for _, img := range m.Images {
		g := img.Generation
		armed := false
		anyPresent, allPresent, verifies, biosFilesVerify := v.inPlace(img, uefi, bios)
		switch {
		case img.State == "STAGED" && verifies: // rule 1
			img.State = "TRYING"
		case img.State == "STAGED" && bios && biosFilesVerify && allPresent && v.GrubEnv != nil &&
			!v.inOrder(g) && v.counter(g) == "" && v.uefiVerifies(img, uefi): // rule 1b
			actions = append(actions, Action{Type: "arm_bios", Generation: g, Tries: m.Tries})
			img.State, armed = "TRYING", true
		case img.State == "STAGED" && !anyPresent: // rule 2
			for _, s := range StagingNames(g) {
				if staging[s] {
					actions = append(actions, Action{Type: "remove_staging", Name: s})
				}
			}
			continue
		case img.State == "STAGED":
			reports = append(reports, fmt.Sprintf("generation %d is STAGED and in place but does not verify", g))
		case !allPresent: // rule 4
			reports = append(reports, fmt.Sprintf("missing files for generation %d", g))
		}
		if !armed && (img.State == "TRYING" || img.State == "GOOD") { // rule 5
			if e, _, ok := v.uefiFile("vintner-os-" + strconv.Itoa(g)); uefi && ok && e.Bad() {
				img.State = "FAILED"
			}
			if c := v.counter(g); bios && v.GrubEnv != nil {
				switch {
				case c == "0":
					img.State = "FAILED"
				case strings.Trim(c, "x") != "" || c == "":
					reports = append(reports, fmt.Sprintf("generation %d has BIOS counter %q", g, c))
				}
			}
		}
		images = append(images, img)
	}
	m.Images = images
	return actions, reports
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Keep applies the keep rules in schema/state-machine.md "Garbage collection"
// and returns the generations to keep (ascending) and to remove, in removal
// order: FAILED first, then the oldest. Recovery records are not in m.Images.
func Keep(m *manifest.Manifest) (keep, remove []int, overBudget bool) {
	newest := append([]manifest.Image(nil), m.Images...)
	sort.Slice(newest, func(i, j int) bool { return newest[i].Generation > newest[j].Generation })

	kept := map[int]bool{}
	baseABI := ""
	for _, img := range newest { // rule 1
		if img.State == "GOOD" && !img.Stale {
			kept[img.Generation], baseABI = true, img.Kernel.ABI
			break
		}
	}
	for _, img := range newest { // rule 2
		if baseABI != "" && img.State == "GOOD" && img.Kernel.ABI != baseABI {
			kept[img.Generation] = true
			break
		}
	}
	for _, img := range newest { // rule 3
		if img.State == "TRYING" {
			kept[img.Generation] = true
		}
	}
	overBudget = len(kept) > m.Retain
	// Rule 4, newest first; FAILED images last, since they go first.
	for _, failed := range []bool{false, true} {
		for _, img := range newest {
			if len(kept) < m.Retain && (img.State == "FAILED") == failed {
				kept[img.Generation] = true
			}
		}
	}

	for i := len(newest) - 1; i >= 0; i-- {
		if g := newest[i].Generation; kept[g] {
			keep = append(keep, g)
		}
	}
	for _, failed := range []bool{true, false} {
		for i := len(newest) - 1; i >= 0; i-- {
			if img := newest[i]; !kept[img.Generation] && (img.State == "FAILED") == failed {
				remove = append(remove, img.Generation)
			}
		}
	}
	return keep, remove, overBudget
}
