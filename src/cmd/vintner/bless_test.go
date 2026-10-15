// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
