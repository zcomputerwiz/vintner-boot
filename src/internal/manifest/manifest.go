// SPDX-License-Identifier: GPL-2.0-or-later

package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNoValidCopy = errors.New("no valid copy")
)

type Manifest struct {
	Format         int         `json:"format"`
	Seq            int         `json:"seq"`
	InstallationID string      `json:"installation_id"`
	MachineID      string      `json:"machine_id"`
	Firmware       string      `json:"firmware"`
	SecureBootTier string      `json:"secure_boot_tier"`
	Root           Root        `json:"root"`
	BootStorage    BootStorage `json:"boot_storage"`
	Tries          int         `json:"tries"`
	Retain         int         `json:"retain"`
	NextGeneration int         `json:"next_generation"`
	Images         []Image     `json:"images"`
	Recovery       []Recovery  `json:"recovery"`

	LoadedCopy string `json:"-"`
}

type Root struct {
	PartUUID string `json:"partuuid"`
	UUID     string `json:"uuid"`
}

type BootStorage struct {
	PartUUID string `json:"partuuid"`
}

type Image struct {
	Generation int         `json:"generation"`
	State      string      `json:"state"`
	Stale      bool        `json:"stale"`
	Kernel     Kernel      `json:"kernel"`
	ModuleTree *ModuleTree `json:"module_tree"`
	Cmdline    string      `json:"cmdline"`
	UEFI       *UEFIFile   `json:"uefi,omitempty"`
	BIOS       *BIOSFiles  `json:"bios,omitempty"`
	Created    string      `json:"created"`
	FirstGood  *string     `json:"first_good"`
	LastGood   *string     `json:"last_good"`
}

type Kernel struct {
	ABI     string  `json:"abi"`
	Package *string `json:"package"`
	Version *string `json:"version"`
}

type ModuleTree struct {
	KernelSHA256     string `json:"kernel_sha256"`
	ModulesDepSHA256 string `json:"modules_dep_sha256"`
	DKMSSHA256       string `json:"dkms_sha256"`
}

type UEFIFile struct {
	Entry  string `json:"entry"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

type BIOSFiles struct {
	Dir   string       `json:"dir"`
	Files BIOSFilesMap `json:"files"`
}

type BIOSFilesMap struct {
	Vmlinuz   string `json:"vmlinuz"`
	InitrdImg string `json:"initrd.img"`
	SlotCfg   string `json:"slot.cfg"`
}

type Recovery struct {
	Slot    string     `json:"slot"`
	Version string     `json:"version"`
	UEFI    *UEFIFile  `json:"uefi,omitempty"`
	BIOS    *BIOSFiles `json:"bios,omitempty"`
}

func readCopy(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.SplitN(string(data), "\n", 3)
	if len(lines) < 2 {
		return nil, errors.New("invalid format: less than 2 lines")
	}

	jsonLine := lines[0]
	hashLine := strings.TrimSpace(lines[1]) // In case there's a trailing newline in the file

	hash := sha256.Sum256([]byte(jsonLine))
	expectedHash := hex.EncodeToString(hash[:])
	if hashLine != expectedHash {
		return nil, fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, hashLine)
	}

	dec := json.NewDecoder(strings.NewReader(jsonLine))
	dec.DisallowUnknownFields()

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}

	// ensure nothing else follows in JSON
	if dec.More() {
		return nil, errors.New("unexpected trailing data")
	}

	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	return &m, nil
}

// Load reads manifest-a and manifest-b, returning the valid one with highest seq.
func Load(stateDir string) (*Manifest, error) {
	pathA := filepath.Join(stateDir, "manifest-a")
	pathB := filepath.Join(stateDir, "manifest-b")

	mA, errA := readCopy(pathA)
	mB, errB := readCopy(pathB)

	// If neither is valid, see if it was an I/O error or just validation/format errors
	if mA == nil && mB == nil {
		if errA != nil && !os.IsNotExist(errA) && !isFormatError(errA) {
			return nil, errA // Return real I/O errors immediately
		}
		if errB != nil && !os.IsNotExist(errB) && !isFormatError(errB) {
			return nil, errB
		}

		return nil, ErrNoValidCopy
	}

	if mA != nil && mB != nil {
		if mA.Seq >= mB.Seq {
			mA.LoadedCopy = "a"
			return mA, nil
		}
		mB.LoadedCopy = "b"
		return mB, nil
	}

	if mA != nil {
		mA.LoadedCopy = "a"
		return mA, nil
	}
	mB.LoadedCopy = "b"
	return mB, nil
}

func isFormatError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "invalid format:") || strings.Contains(msg, "hash mismatch:") || strings.Contains(msg, "json decode:") || strings.Contains(msg, "unexpected trailing data") || strings.Contains(msg, "validate:")
}

func fsyncFile(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func Save(stateDir string, m *Manifest) error {
	pathA := filepath.Join(stateDir, "manifest-a")
	pathB := filepath.Join(stateDir, "manifest-b")

	var currentCopy string
	mA, _ := readCopy(pathA)
	mB, _ := readCopy(pathB)

	if mA != nil && mB != nil {
		if mA.Seq >= mB.Seq {
			currentCopy = "a"
		} else {
			currentCopy = "b"
		}
	} else if mA != nil {
		currentCopy = "a"
	} else if mB != nil {
		currentCopy = "b"
	} else {
		// Neither valid, write to a, then it will become current next time
		currentCopy = "b"
	}

	var targetPath string
	var targetCopy string
	if currentCopy == "a" {
		targetPath = pathB
		targetCopy = "b"
	} else {
		targetPath = pathA
		targetCopy = "a"
	}

	// Never write a seq at or below a valid copy's, or Load would keep the older one.
	newSeq := m.Seq
	for _, c := range []*Manifest{mA, mB} {
		if c != nil && c.Seq > newSeq {
			newSeq = c.Seq
		}
	}
	newSeq++
	// Create a shallow copy just to correctly marshal it with the new Seq
	mForSave := *m
	mForSave.Seq = newSeq

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // just to match compact if needed
	if err := enc.Encode(mForSave); err != nil {
		return err
	}

	// Encode puts a newline at the end
	jsonLine := strings.TrimSpace(buf.String())
	hash := sha256.Sum256([]byte(jsonLine))

	outData := fmt.Sprintf("%s\n%x\n", jsonLine, hash)

	f, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(outData)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// fsync dir
	if err := fsyncFile(stateDir); err != nil {
		return err
	}

	m.Seq = newSeq
	m.LoadedCopy = targetCopy
	return nil
}

var (
	uuidRe      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	machineIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	sha256Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (m *Manifest) Validate() error {
	if m.Format != 1 {
		return fmt.Errorf("invalid format: %d", m.Format)
	}
	if m.Seq < 1 {
		return fmt.Errorf("invalid seq: %d", m.Seq)
	}
	if !uuidRe.MatchString(m.InstallationID) {
		return fmt.Errorf("invalid installation_id")
	}
	if !machineIDRe.MatchString(m.MachineID) {
		return fmt.Errorf("invalid machine_id")
	}
	switch m.Firmware {
	case "uefi", "bios", "both":
	default:
		return fmt.Errorf("invalid firmware: %s", m.Firmware)
	}
	switch m.SecureBootTier {
	case "A", "B", "C", "none":
	default:
		return fmt.Errorf("invalid secure_boot_tier: %s", m.SecureBootTier)
	}
	if !uuidRe.MatchString(m.Root.PartUUID) {
		return fmt.Errorf("invalid root partuuid")
	}
	if len(m.Root.UUID) < 1 {
		return fmt.Errorf("invalid root uuid")
	}
	if !uuidRe.MatchString(m.BootStorage.PartUUID) {
		return fmt.Errorf("invalid boot_storage partuuid")
	}
	if m.Tries < 1 || m.Tries > 9 {
		return fmt.Errorf("invalid tries")
	}
	if m.Retain < 2 {
		return fmt.Errorf("invalid retain")
	}
	if m.NextGeneration < 1 {
		return fmt.Errorf("invalid next_generation")
	}

	if m.Images == nil {
		return fmt.Errorf("images is required")
	}
	if m.Recovery == nil {
		return fmt.Errorf("recovery is required")
	}

	genSeen := make(map[int]bool)
	for i, img := range m.Images {
		if img.Generation < 1 {
			return fmt.Errorf("invalid generation in image %d", i)
		}
		if genSeen[img.Generation] {
			return fmt.Errorf("duplicate generation %d", img.Generation)
		}
		genSeen[img.Generation] = true
		if img.Generation >= m.NextGeneration {
			return fmt.Errorf("generation %d >= next_generation %d", img.Generation, m.NextGeneration)
		}

		switch img.State {
		case "STAGED", "TRYING", "GOOD", "FAILED":
		default:
			return fmt.Errorf("invalid state %s in image %d", img.State, i)
		}
		if len(img.Kernel.ABI) < 1 {
			return fmt.Errorf("empty kernel abi in image %d", i)
		}

		if img.ModuleTree != nil {
			if !sha256Re.MatchString(img.ModuleTree.KernelSHA256) ||
				!sha256Re.MatchString(img.ModuleTree.ModulesDepSHA256) ||
				!sha256Re.MatchString(img.ModuleTree.DKMSSHA256) {
				return fmt.Errorf("invalid module_tree hashes in image %d", i)
			}
		}

		if _, err := time.Parse(time.RFC3339, img.Created); err != nil {
			return fmt.Errorf("invalid created time in image %d", i)
		}
		if img.FirstGood != nil {
			if _, err := time.Parse(time.RFC3339, *img.FirstGood); err != nil {
				return fmt.Errorf("invalid first_good time in image %d", i)
			}
		}
		if img.LastGood != nil {
			if _, err := time.Parse(time.RFC3339, *img.LastGood); err != nil {
				return fmt.Errorf("invalid last_good time in image %d", i)
			}
		}

		if (m.Firmware == "uefi" || m.Firmware == "both") && img.UEFI == nil {
			return fmt.Errorf("uefi missing for image %d", i)
		}
		if m.Firmware == "bios" && img.UEFI != nil {
			return fmt.Errorf("uefi present for bios firmware in image %d", i)
		}

		if (m.Firmware == "bios" || m.Firmware == "both") && img.BIOS == nil {
			return fmt.Errorf("bios missing for image %d", i)
		}
		if m.Firmware == "uefi" && img.BIOS != nil {
			return fmt.Errorf("bios present for uefi firmware in image %d", i)
		}

		expectedStem := fmt.Sprintf("vintner-os-%d", img.Generation)
		if img.UEFI != nil {
			if img.UEFI.Entry != expectedStem {
				return fmt.Errorf("invalid uefi entry in image %d: expected %s", i, expectedStem)
			}
			if !sha256Re.MatchString(img.UEFI.SHA256) {
				return fmt.Errorf("invalid uefi sha256 in image %d", i)
			}
			if img.UEFI.Size < 1 {
				return fmt.Errorf("invalid uefi size in image %d", i)
			}
		}
		if img.BIOS != nil {
			expectedDir := fmt.Sprintf("/vintner/bios/os-%d", img.Generation)
			if img.BIOS.Dir != expectedDir {
				return fmt.Errorf("invalid bios dir in image %d: expected %s", i, expectedDir)
			}
			if !sha256Re.MatchString(img.BIOS.Files.Vmlinuz) ||
				!sha256Re.MatchString(img.BIOS.Files.InitrdImg) ||
				!sha256Re.MatchString(img.BIOS.Files.SlotCfg) {
				return fmt.Errorf("invalid bios hashes in image %d", i)
			}
		}
	}

	if len(m.Recovery) > 2 {
		return fmt.Errorf("too many recovery records")
	}

	slotSeen := make(map[string]bool)
	for i, rec := range m.Recovery {
		switch rec.Slot {
		case "a", "b":
		default:
			return fmt.Errorf("invalid recovery slot %s in record %d", rec.Slot, i)
		}
		if slotSeen[rec.Slot] {
			return fmt.Errorf("duplicate recovery slot %s", rec.Slot)
		}
		slotSeen[rec.Slot] = true

		if len(rec.Version) < 1 {
			return fmt.Errorf("empty recovery version in record %d", i)
		}

		if (m.Firmware == "uefi" || m.Firmware == "both") && rec.UEFI == nil {
			return fmt.Errorf("uefi missing for recovery %d", i)
		}
		if m.Firmware == "bios" && rec.UEFI != nil {
			return fmt.Errorf("uefi present for bios firmware in recovery %d", i)
		}

		if (m.Firmware == "bios" || m.Firmware == "both") && rec.BIOS == nil {
			return fmt.Errorf("bios missing for recovery %d", i)
		}
		if m.Firmware == "uefi" && rec.BIOS != nil {
			return fmt.Errorf("bios present for uefi firmware in recovery %d", i)
		}

		expectedStem := fmt.Sprintf("vintner-recovery-%s", rec.Slot)
		if rec.UEFI != nil {
			if rec.UEFI.Entry != expectedStem {
				return fmt.Errorf("invalid uefi entry in recovery %d: expected %s", i, expectedStem)
			}
			if !sha256Re.MatchString(rec.UEFI.SHA256) {
				return fmt.Errorf("invalid uefi sha256 in recovery %d", i)
			}
			if rec.UEFI.Size < 1 {
				return fmt.Errorf("invalid uefi size in recovery %d", i)
			}
		}
		if rec.BIOS != nil {
			expectedDir := fmt.Sprintf("/vintner/bios/recovery-%s", rec.Slot)
			if rec.BIOS.Dir != expectedDir {
				return fmt.Errorf("invalid bios dir in recovery %d: expected %s", i, expectedDir)
			}
			if !sha256Re.MatchString(rec.BIOS.Files.Vmlinuz) ||
				!sha256Re.MatchString(rec.BIOS.Files.InitrdImg) ||
				!sha256Re.MatchString(rec.BIOS.Files.SlotCfg) {
				return fmt.Errorf("invalid bios hashes in recovery %d", i)
			}
		}
	}

	return nil
}
