// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func TestCmdReconcile(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
	}

	tmpDir := t.TempDir()

	stateDir := filepath.Join(tmpDir, "vintner", "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	stagingDir := filepath.Join(tmpDir, "vintner", "staging")
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f",
		MachineID:      "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f",
		Firmware:       "uefi",
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
		NextGeneration: 2,
		Images: []manifest.Image{
			{
				Generation: 1,
				State:      "STAGED",
				Kernel: manifest.Kernel{
					ABI: "a",
				},
				Created: "2024-01-01T00:00:00Z",
				UEFI: &manifest.UEFIFile{
					Entry:  "vintner-os-1",
					SHA256: strings.Repeat("0", 64),
					Size:   100,
				},
			},
		},
		Recovery: []manifest.Recovery{
			{
				Slot:    "a",
				Version: "1",
				UEFI: &manifest.UEFIFile{
					Entry:  "vintner-recovery-a",
					SHA256: "6a1f3c2e0d5b4f7e9a8c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f",
					Size:   100,
				},
			},
		},
	}

	if err := m.Validate(); err != nil {
		t.Fatalf("setup failed, invalid manifest: %v", err)
	}

	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"vintner-os-1.efi", "vintner-os-10.efi"} {
		if err := os.WriteFile(filepath.Join(stagingDir, f), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	exitCode = 0
	cmdReconcile([]string{"--boot-storage", tmpDir, "--state-dir", stateDir})

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if _, err := os.Stat(filepath.Join(stagingDir, "vintner-os-1.efi")); !os.IsNotExist(err) {
		t.Errorf("expected staging file to be removed, got err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "vintner-os-10.efi")); err != nil {
		t.Errorf("another generation's staging file was touched: %v", err)
	}

	m2, err := manifest.Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(m2.Images) != 0 {
		t.Errorf("expected images to be empty, got %v", m2.Images)
	}
}

func TestCmdReconcileMissingManifest(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
		panic("exit")
	}

	tmpDir := t.TempDir()

	func() {
		defer func() { recover() }()
		cmdReconcile([]string{"--boot-storage", tmpDir, "--state-dir", "/does-not-exist"})
	}()

	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
}

func TestCmdReconcileDryRun(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
	}

	tmpDir := t.TempDir()

	stateDir := filepath.Join(tmpDir, "vintner", "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	stagingDir := filepath.Join(tmpDir, "vintner", "staging")
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f",
		MachineID:      "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f",
		Firmware:       "uefi",
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
		NextGeneration: 2,
		Images: []manifest.Image{
			{
				Generation: 1,
				State:      "STAGED",
				Kernel: manifest.Kernel{
					ABI: "a",
				},
				Created: "2024-01-01T00:00:00Z",
				UEFI: &manifest.UEFIFile{
					Entry:  "vintner-os-1",
					SHA256: strings.Repeat("0", 64),
					Size:   100,
				},
			},
		},
		Recovery: []manifest.Recovery{
			{
				Slot:    "a",
				Version: "1",
				UEFI: &manifest.UEFIFile{
					Entry:  "vintner-recovery-a",
					SHA256: "6a1f3c2e0d5b4f7e9a8c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f",
					Size:   100,
				},
			},
		},
	}

	if err := m.Validate(); err != nil {
		t.Fatalf("setup failed, invalid manifest: %v", err)
	}

	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"vintner-os-1.efi", "vintner-os-10.efi"} {
		if err := os.WriteFile(filepath.Join(stagingDir, f), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	exitCode = 0
	cmdReconcile([]string{"--boot-storage", tmpDir, "--state-dir", stateDir, "--dry-run"})

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Should not be removed in dry run
	if _, err := os.Stat(filepath.Join(stagingDir, "vintner-os-1.efi")); os.IsNotExist(err) {
		t.Errorf("expected staging file to exist in dry-run, got err: %v", err)
	}

	m2, err := manifest.Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(m2.Images) != 1 {
		t.Errorf("expected manifest to be unchanged in dry-run, got %v", m2.Images)
	}
}

func TestCmdReconcileDefaultStateDir(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
	}

	tmpDir := t.TempDir()

	stateDir := filepath.Join(tmpDir, "vintner", "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	m := &manifest.Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f",
		MachineID:      "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f",
		Firmware:       "uefi",
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
		NextGeneration: 2,
		Images:         []manifest.Image{},
		Recovery:       []manifest.Recovery{},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("setup failed, invalid manifest: %v", err)
	}

	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatal(err)
	}

	exitCode = 0
	cmdReconcile([]string{"--boot-storage", tmpDir})

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
}

func TestCmdReconcileNoBootStorage(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	var exitCode int
	osExit = func(code int) {
		exitCode = code
		panic("exit")
	}

	func() {
		defer func() { recover() }()
		cmdReconcile([]string{}) // Should attempt to mount /proc/cmdline and fail
	}()

	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
}
