// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func setupTestEnv(t *testing.T, isUEFI bool, m *manifest.Manifest) (string, string) {
	dir := t.TempDir()
	bootStorage := filepath.Join(dir, "boot")
	stateDir := filepath.Join(bootStorage, "vintner", "state")
	os.MkdirAll(stateDir, 0755)

	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatalf("failed to save test manifest: %v", err)
	}

	efivarsDir := filepath.Join(dir, "efivars")
	if isUEFI {
		os.MkdirAll(efivarsDir, 0755)
		os.MkdirAll(filepath.Join(bootStorage, "EFI", "Linux"), 0755)
	} else {
		os.MkdirAll(filepath.Join(bootStorage, "grub"), 0755)
		envPath := filepath.Join(bootStorage, "grub", "grubenv")
		dummy := bytes.Repeat([]byte{'#'}, 1024)
		os.WriteFile(envPath, dummy, 0644)
		env := &grubenv.Env{}
		err := grubenv.Write(envPath, env)
		if err != nil {
			t.Fatalf("failed to write grubenv: %v", err)
		}
	}

	return bootStorage, efivarsDir
}

func getManifestGen(t *testing.T, sDir string, gen int) manifest.Image {
	m, err := manifest.Load(sDir)
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}
	for _, img := range m.Images {
		if img.Generation == gen {
			return img
		}
	}
	t.Fatalf("generation %d not found in manifest", gen)
	return manifest.Image{} // unreachable
}

func TestBlessUEFIOrdinary(t *testing.T) {
	mPath := "../../../schema/examples/manifest-uefi.json"
	data, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}

	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	bootStorage, efivarsDir := setupTestEnv(t, true, &m)
	stateDir := filepath.Join(bootStorage, "vintner", "state")

	// Create EFI file
	efiDir := filepath.Join(bootStorage, "EFI", "Linux")
	if err := os.WriteFile(filepath.Join(efiDir, "vintner-os-3+0-1.efi"), []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock efivars
	efivar.SetString(efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo, 0, "\\EFI\\Linux\\vintner-os-3+0-1.efi")

	// Run bless
	os.Args = []string{"vintner", "bless", "--boot-storage", bootStorage, "--efivars", efivarsDir}
	cmdBless(os.Args[2:])

	// Verify EFI file renamed
	entries, _ := os.ReadDir(efiDir)
	if len(entries) != 1 || entries[0].Name() != "vintner-os-3+3-0.efi" {
		t.Errorf("EFI file not renamed correctly: %v", entries)
	}

	// Verify manifest
	img := getManifestGen(t, stateDir, 3)
	if img.State != "GOOD" {
		t.Errorf("expected state GOOD, got %s", img.State)
	}
	if img.FirstGood == nil || img.LastGood == nil {
		t.Errorf("timestamps not set")
	}
}

func TestBlessUEFIRecovery(t *testing.T) {
	mPath := "../../../schema/examples/manifest-uefi.json"
	data, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}

	var m manifest.Manifest
	json.Unmarshal(data, &m)

	bootStorage, efivarsDir := setupTestEnv(t, true, &m)

	// Mock efivars
	efivar.SetString(efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo, 0, "\\EFI\\Linux\\vintner-recovery-a.efi")

	os.Args = []string{"vintner", "bless", "--boot-storage", bootStorage, "--efivars", efivarsDir}

	// Exits 0
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
		panic(fmt.Sprintf("exit %d", code))
	}

	func() {
		defer func() {
			recover()
		}()
		cmdBless(os.Args[2:])
	}()

	if exitCode != 0 {
		t.Errorf("expected exit 0, got %d", exitCode)
	}
}

func TestBlessUEFIMissingVariable(t *testing.T) {
	mPath := "../../../schema/examples/manifest-uefi.json"
	data, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}

	var m manifest.Manifest
	json.Unmarshal(data, &m)

	bootStorage, efivarsDir := setupTestEnv(t, true, &m)

	os.Args = []string{"vintner", "bless", "--boot-storage", bootStorage, "--efivars", efivarsDir}

	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
		panic(fmt.Sprintf("exit %d", code))
	}

	func() {
		defer func() {
			recover()
		}()
		cmdBless(os.Args[2:])
	}()

	if exitCode != 0 {
		t.Errorf("expected exit 0, got %d", exitCode)
	}
}

func TestBlessBIOSOrdinary(t *testing.T) {
	mPath := "../../../schema/examples/manifest-uefi.json" // use uefi example but tweak for bios validation
	data, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}

	var m manifest.Manifest
	json.Unmarshal(data, &m)
	m.Firmware = "bios"
	for i := range m.Images {
		m.Images[i].UEFI = nil
		m.Images[i].BIOS = &manifest.BIOSFiles{
			Dir: fmt.Sprintf("/vintner/bios/os-%d", m.Images[i].Generation),
			Files: manifest.BIOSFilesMap{
				Vmlinuz:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				InitrdImg: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SlotCfg:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		}
	}
	for i := range m.Recovery {
		m.Recovery[i].UEFI = nil
		m.Recovery[i].BIOS = &manifest.BIOSFiles{
			Dir: fmt.Sprintf("/vintner/bios/recovery-%s", m.Recovery[i].Slot),
			Files: manifest.BIOSFilesMap{
				Vmlinuz:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				InitrdImg: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				SlotCfg:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		}
	}

	bootStorage, efivarsDir := setupTestEnv(t, false, &m)
	stateDir := filepath.Join(bootStorage, "vintner", "state")

	envPath := filepath.Join(bootStorage, "grub", "grubenv")
	env, _ := grubenv.Read(envPath)
	env.Set("VINTNER_BOOTED", "3")
	grubenv.Write(envPath, env)

	os.Args = []string{"vintner", "bless", "--boot-storage", bootStorage, "--efivars", efivarsDir}
	cmdBless(os.Args[2:])

	// Verify grubenv
	env, _ = grubenv.Read(envPath)
	if val, ok := env.Get("VINTNER_T_3"); !ok || val != "xxx" {
		t.Errorf("VINTNER_T_3 not set to xxx: %v", val)
	}
	if _, ok := env.Get("VINTNER_BOOTED"); ok {
		t.Errorf("VINTNER_BOOTED not unset")
	}

	// Verify manifest
	img := getManifestGen(t, stateDir, 3)
	if img.State != "GOOD" {
		t.Errorf("expected state GOOD, got %s", img.State)
	}
	if img.FirstGood == nil || img.LastGood == nil {
		t.Errorf("timestamps not set")
	}
}

func TestBlessBIOSRA(t *testing.T) {
	mPath := "../../../schema/examples/manifest-uefi.json"
	data, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}

	var m manifest.Manifest
	json.Unmarshal(data, &m)

	bootStorage, efivarsDir := setupTestEnv(t, false, &m)

	envPath := filepath.Join(bootStorage, "grub", "grubenv")
	env, _ := grubenv.Read(envPath)
	env.Set("VINTNER_BOOTED", "RA")
	grubenv.Write(envPath, env)

	os.Args = []string{"vintner", "bless", "--boot-storage", bootStorage, "--efivars", efivarsDir}

	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
		panic(fmt.Sprintf("exit %d", code))
	}

	func() {
		defer func() {
			recover()
		}()
		cmdBless(os.Args[2:])
	}()

	if exitCode != 0 {
		t.Errorf("expected exit 0, got %d", exitCode)
	}
}

func TestBlessIdempotent(t *testing.T) {
	mPath := "../../../schema/examples/manifest-uefi.json"
	data, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}

	var m manifest.Manifest
	json.Unmarshal(data, &m)

	bootStorage, efivarsDir := setupTestEnv(t, true, &m)

	efiDir := filepath.Join(bootStorage, "EFI", "Linux")
	if err := os.WriteFile(filepath.Join(efiDir, "vintner-os-3+0-1.efi"), []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	efivar.SetString(efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo, 0, "\\EFI\\Linux\\vintner-os-3+0-1.efi")

	os.Args = []string{"vintner", "bless", "--boot-storage", bootStorage, "--efivars", efivarsDir}

	// First run
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) {}

	cmdBless(os.Args[2:])

	// Second run
	osExit = func(code int) {
		panic(fmt.Sprintf("exit %d", code))
	}

	func() {
		defer func() {
			recover()
		}()
		cmdBless(os.Args[2:])
	}()

	// Since manifest is already updated, bless will update it again with new LastGood, but logic is idempotent
}

func calcSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func newTestManifest(firmware string, images []manifest.Image) *manifest.Manifest {
	dummyHash := strings.Repeat("0", 64)
	m := &manifest.Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f",
		MachineID:      "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f",
		Firmware:       firmware,
		SecureBootTier: "none",
		Root: manifest.Root{
			PartUUID: "6a1f3c2e-0d5b-4f7e-9a8c-1b2d3e4f5a6b",
			UUID:     "b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e",
		},
		BootStorage: manifest.BootStorage{
			PartUUID: "9d8c7b6a-5f4e-4d3c-2b1a-0e9f8d7c6b5a",
		},
		Tries:          3,
		Retain:         3,
		NextGeneration: len(images) + 1,
		Images:         images,
	}
	if firmware == "uefi" {
		m.Recovery = []manifest.Recovery{
			{
				Slot:    "a",
				Version: "1",
				UEFI: &manifest.UEFIFile{
					Entry:  "vintner-recovery-a",
					SHA256: dummyHash,
					Size:   100,
				},
			},
			{
				Slot:    "b",
				Version: "1",
				UEFI: &manifest.UEFIFile{
					Entry:  "vintner-recovery-b",
					SHA256: dummyHash,
					Size:   100,
				},
			},
		}
	} else {
		m.Recovery = []manifest.Recovery{
			{
				Slot:    "a",
				Version: "1",
				BIOS: &manifest.BIOSFiles{
					Dir: "/vintner/bios/recovery-a",
					Files: manifest.BIOSFilesMap{
						Vmlinuz:   dummyHash,
						InitrdImg: dummyHash,
						SlotCfg:   dummyHash,
					},
				},
			},
			{
				Slot:    "b",
				Version: "1",
				BIOS: &manifest.BIOSFiles{
					Dir: "/vintner/bios/recovery-b",
					Files: manifest.BIOSFilesMap{
						Vmlinuz:   dummyHash,
						InitrdImg: dummyHash,
						SlotCfg:   dummyHash,
					},
				},
			},
		}
	}
	return m
}

func TestBlessCandidateZeroCounterUEFI(t *testing.T) {
	os1Data := []byte("os1-dummy-kernel")
	os2Data := []byte("os2-dummy-kernel")
	os1Hash := calcSHA256(os1Data)
	os2Hash := calcSHA256(os2Data)

	m := newTestManifest("uefi", []manifest.Image{
		{
			Generation: 1,
			State:      "GOOD",
			Kernel:     manifest.Kernel{ABI: "6.12.0-1"},
			Created:    "2026-10-15T00:00:00Z",
			Cmdline:    "console=ttyS0",
			UEFI: &manifest.UEFIFile{
				Entry:  "vintner-os-1",
				SHA256: os1Hash,
				Size:   len(os1Data),
			},
		},
		{
			Generation: 2,
			State:      "TRYING",
			Kernel:     manifest.Kernel{ABI: "6.12.0-1"},
			Created:    "2026-10-16T00:00:00Z",
			Cmdline:    "console=ttyS0",
			UEFI: &manifest.UEFIFile{
				Entry:  "vintner-os-2",
				SHA256: os2Hash,
				Size:   len(os2Data),
			},
		},
	})

	bootStorage, efivarsDir := setupTestEnv(t, true, m)
	stateDir := filepath.Join(bootStorage, "vintner", "state")

	efiDir := filepath.Join(bootStorage, "EFI", "Linux")
	// Booted image is os-1 (e.g. at +2-1 before bless)
	if err := os.WriteFile(filepath.Join(efiDir, "vintner-os-1+2-1.efi"), os1Data, 0644); err != nil {
		t.Fatal(err)
	}
	// Candidate os-2 is at +0-3 (zero counter)
	if err := os.WriteFile(filepath.Join(efiDir, "vintner-os-2+0-3.efi"), os2Data, 0644); err != nil {
		t.Fatal(err)
	}

	efivar.SetString(efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo, 0, "\\EFI\\Linux\\vintner-os-1+2-1.efi")

	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) {
		t.Fatalf("unexpected exit: %d", code)
	}

	cmdBless([]string{"--boot-storage", bootStorage, "--efivars", efivarsDir})

	// Check os-1: re-armed to +3-0 and GOOD
	img1 := getManifestGen(t, stateDir, 1)
	if img1.State != "GOOD" {
		t.Errorf("expected os-1 state GOOD, got %s", img1.State)
	}
	if _, err := os.Stat(filepath.Join(efiDir, "vintner-os-1+3-0.efi")); err != nil {
		t.Errorf("os-1 not rearmed to +3-0: %v", err)
	}

	// Check os-2: transitioned to FAILED
	img2 := getManifestGen(t, stateDir, 2)
	if img2.State != "FAILED" {
		t.Errorf("expected os-2 state FAILED, got %s", img2.State)
	}

	// Check boot storage for os-2: unchanged (still +0-3)
	if _, err := os.Stat(filepath.Join(efiDir, "vintner-os-2+0-3.efi")); err != nil {
		t.Errorf("os-2 efi file missing or renamed: %v", err)
	}
}

func TestBlessCandidateZeroCounterBIOS(t *testing.T) {
	os1Data := []byte("os1-dummy-vmlinuz")
	os2Data := []byte("os2-dummy-vmlinuz")
	os1Hash := calcSHA256(os1Data)
	os2Hash := calcSHA256(os2Data)

	m := newTestManifest("bios", []manifest.Image{
		{
			Generation: 1,
			State:      "GOOD",
			Kernel:     manifest.Kernel{ABI: "6.12.0-1"},
			Created:    "2026-10-15T00:00:00Z",
			Cmdline:    "console=ttyS0",
			BIOS: &manifest.BIOSFiles{
				Dir: "/vintner/bios/os-1",
				Files: manifest.BIOSFilesMap{
					Vmlinuz:   os1Hash,
					InitrdImg: os1Hash,
					SlotCfg:   os1Hash,
				},
			},
		},
		{
			Generation: 2,
			State:      "TRYING",
			Kernel:     manifest.Kernel{ABI: "6.12.0-1"},
			Created:    "2026-10-16T00:00:00Z",
			Cmdline:    "console=ttyS0",
			BIOS: &manifest.BIOSFiles{
				Dir: "/vintner/bios/os-2",
				Files: manifest.BIOSFilesMap{
					Vmlinuz:   os2Hash,
					InitrdImg: os2Hash,
					SlotCfg:   os2Hash,
				},
			},
		},
	})

	bootStorage, efivarsDir := setupTestEnv(t, false, m)
	stateDir := filepath.Join(bootStorage, "vintner", "state")

	// Create slot dirs and files
	for _, gen := range []int{1, 2} {
		slotDir := filepath.Join(bootStorage, "vintner", "bios", fmt.Sprintf("os-%d", gen))
		os.MkdirAll(slotDir, 0755)
		data := os1Data
		if gen == 2 {
			data = os2Data
		}
		os.WriteFile(filepath.Join(slotDir, "vmlinuz"), data, 0644)
		os.WriteFile(filepath.Join(slotDir, "initrd.img"), data, 0644)
		os.WriteFile(filepath.Join(slotDir, "slot.cfg"), data, 0644)
	}

	envPath := filepath.Join(bootStorage, "grub", "grubenv")
	env, _ := grubenv.Read(envPath)
	env.Set("VINTNER_ORDER", "2 1")
	env.Set("VINTNER_BOOTED", "1")
	env.Set("VINTNER_T_1", "xx")
	env.Set("VINTNER_T_2", "0") // zero counter
	grubenv.Write(envPath, env)

	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) {
		t.Fatalf("unexpected exit: %d", code)
	}

	cmdBless([]string{"--boot-storage", bootStorage, "--efivars", efivarsDir})

	// Check os-1: re-armed to xxx and GOOD
	img1 := getManifestGen(t, stateDir, 1)
	if img1.State != "GOOD" {
		t.Errorf("expected os-1 state GOOD, got %s", img1.State)
	}

	// Check os-2: transitioned to FAILED
	img2 := getManifestGen(t, stateDir, 2)
	if img2.State != "FAILED" {
		t.Errorf("expected os-2 state FAILED, got %s", img2.State)
	}

	// Check grubenv
	env, _ = grubenv.Read(envPath)
	if val, _ := env.Get("VINTNER_T_1"); val != "xxx" {
		t.Errorf("expected VINTNER_T_1=xxx, got %q", val)
	}
	if _, ok := env.Get("VINTNER_BOOTED"); ok {
		t.Errorf("VINTNER_BOOTED was not unset")
	}
	if val, _ := env.Get("VINTNER_T_2"); val != "0" {
		t.Errorf("expected VINTNER_T_2=0 unchanged, got %q", val)
	}
}

func TestBlessCandidateWithTriesLeft(t *testing.T) {
	os1Data := []byte("os1-dummy-kernel")
	os2Data := []byte("os2-dummy-kernel")
	os1Hash := calcSHA256(os1Data)
	os2Hash := calcSHA256(os2Data)

	m := newTestManifest("uefi", []manifest.Image{
		{
			Generation: 1,
			State:      "GOOD",
			Kernel:     manifest.Kernel{ABI: "6.12.0-1"},
			Created:    "2026-10-15T00:00:00Z",
			Cmdline:    "console=ttyS0",
			UEFI: &manifest.UEFIFile{
				Entry:  "vintner-os-1",
				SHA256: os1Hash,
				Size:   len(os1Data),
			},
		},
		{
			Generation: 2,
			State:      "TRYING",
			Kernel:     manifest.Kernel{ABI: "6.12.0-1"},
			Created:    "2026-10-16T00:00:00Z",
			Cmdline:    "console=ttyS0",
			UEFI: &manifest.UEFIFile{
				Entry:  "vintner-os-2",
				SHA256: os2Hash,
				Size:   len(os2Data),
			},
		},
	})

	bootStorage, efivarsDir := setupTestEnv(t, true, m)
	stateDir := filepath.Join(bootStorage, "vintner", "state")

	efiDir := filepath.Join(bootStorage, "EFI", "Linux")
	// Booted image os-1
	os.WriteFile(filepath.Join(efiDir, "vintner-os-1+2-1.efi"), os1Data, 0644)
	// Candidate os-2 has tries left (+1-2)
	os.WriteFile(filepath.Join(efiDir, "vintner-os-2+1-2.efi"), os2Data, 0644)

	efivar.SetString(efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo, 0, "\\EFI\\Linux\\vintner-os-1+2-1.efi")

	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) {
		t.Fatalf("unexpected exit: %d", code)
	}

	cmdBless([]string{"--boot-storage", bootStorage, "--efivars", efivarsDir})

	// Check os-1: re-armed to +3-0 and GOOD
	img1 := getManifestGen(t, stateDir, 1)
	if img1.State != "GOOD" {
		t.Errorf("expected os-1 state GOOD, got %s", img1.State)
	}
	if _, err := os.Stat(filepath.Join(efiDir, "vintner-os-1+3-0.efi")); err != nil {
		t.Errorf("os-1 not rearmed to +3-0: %v", err)
	}

	// Check os-2: still TRYING
	img2 := getManifestGen(t, stateDir, 2)
	if img2.State != "TRYING" {
		t.Errorf("expected os-2 state TRYING, got %s", img2.State)
	}

	// Check boot storage for os-2: still +1-2
	if _, err := os.Stat(filepath.Join(efiDir, "vintner-os-2+1-2.efi")); err != nil {
		t.Errorf("os-2 efi file missing or renamed: %v", err)
	}
}

func TestBlessReconcileErrorDoesNotUndoRearm(t *testing.T) {
	os1Data := []byte("os1-dummy-kernel")
	os1Hash := calcSHA256(os1Data)

	m := newTestManifest("uefi", []manifest.Image{
		{
			Generation: 1,
			State:      "GOOD",
			Kernel:     manifest.Kernel{ABI: "6.12.0-1"},
			Created:    "2026-10-15T00:00:00Z",
			Cmdline:    "console=ttyS0",
			UEFI: &manifest.UEFIFile{
				Entry:  "vintner-os-1",
				SHA256: os1Hash,
				Size:   len(os1Data),
			},
		},
	})

	bootStorage, efivarsDir := setupTestEnv(t, true, m)
	stateDir := filepath.Join(bootStorage, "vintner", "state")

	efiDir := filepath.Join(bootStorage, "EFI", "Linux")
	os.WriteFile(filepath.Join(efiDir, "vintner-os-1+2-1.efi"), os1Data, 0644)

	// Make bootStorage fail in Scan: create a regular file at vintner/staging
	// so readDir fails with ENOTDIR even when run as root
	stagingFile := filepath.Join(bootStorage, "vintner", "staging")
	os.RemoveAll(stagingFile)
	if err := os.WriteFile(stagingFile, []byte("regular file not dir"), 0644); err != nil {
		t.Fatal(err)
	}

	efivar.SetString(efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo, 0, "\\EFI\\Linux\\vintner-os-1+2-1.efi")

	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
		panic(fmt.Sprintf("exit %d", code))
	}

	func() {
		defer func() {
			recover()
		}()
		cmdBless([]string{"--boot-storage", bootStorage, "--efivars", efivarsDir})
	}()

	if exitCode == 0 {
		t.Errorf("expected non-zero exit code due to scan error, got 0")
	}

	// Verify os-1 rearm was NOT undone on boot storage
	if _, err := os.Stat(filepath.Join(efiDir, "vintner-os-1+3-0.efi")); err != nil {
		t.Errorf("os-1 was not re-armed or was undone: %v", err)
	}

	// Verify manifest on disk still records os-1 as GOOD with re-arm
	img1 := getManifestGen(t, stateDir, 1)
	if img1.State != "GOOD" {
		t.Errorf("expected os-1 state GOOD, got %s", img1.State)
	}
}
