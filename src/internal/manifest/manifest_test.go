// SPDX-License-Identifier: GPL-2.0-or-later

package manifest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSave(t *testing.T) {
	tmpDir := t.TempDir()

	m := &Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f",
		MachineID:      "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f",
		Firmware:       "uefi",
		SecureBootTier: "B",
		Root: Root{
			PartUUID: "6a1f3c2e-0d5b-4f7e-9a8c-1b2d3e4f5a6b",
			UUID:     "b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e",
		},
		BootStorage: BootStorage{
			PartUUID: "9d8c7b6a-5f4e-4d3c-2b1a-0e9f8d7c6b5a",
		},
		Tries:          3,
		Retain:         3,
		NextGeneration: 2,
		Images: []Image{
			{
				Generation: 1,
				State:      "TRYING",
				Stale:      false,
				Kernel: Kernel{
					ABI: "6.12.41+deb13-amd64",
				},
				Cmdline: "root=PARTUUID=6a1f3c2e-0d5b-4f7e-9a8c-1b2d3e4f5a6b ro quiet",
				UEFI: &UEFIFile{
					Entry:  "vintner-os-1",
					SHA256: "bb82030dbc2bcaba32a90bf2e207a84a856fc5f033b77c480836ab6f77f40f19",
					Size:   98765432,
				},
				Created: "2026-10-14T20:00:00Z",
			},
		},
		Recovery: []Recovery{},
	}

	err := Save(tmpDir, m)
	if err != nil {
		t.Fatalf("initial save: %v", err)
	}

	loaded, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	if loaded.LoadedCopy != "a" {
		t.Errorf("expected to write to and load a, got %s", loaded.LoadedCopy)
	}
	if loaded.Seq != 2 {
		t.Errorf("expected seq 2, got %d", loaded.Seq)
	}

	err = Save(tmpDir, loaded)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}

	loaded2, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if loaded2.LoadedCopy != "b" {
		t.Errorf("expected to write to and load b, got %s", loaded2.LoadedCopy)
	}
	if loaded2.Seq != 3 {
		t.Errorf("expected seq 3, got %d", loaded2.Seq)
	}

	// Corrupt file b (make it empty)
	pathB := filepath.Join(tmpDir, "manifest-b")
	os.WriteFile(pathB, []byte{}, 0644)

	loaded3, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("load after corrupting b: %v", err)
	}
	if loaded3.LoadedCopy != "a" {
		t.Errorf("expected fallback to a, got %s", loaded3.LoadedCopy)
	}
	if loaded3.Seq != 2 {
		t.Errorf("expected seq 2 from a, got %d", loaded3.Seq)
	}

	// Corrupt file a (invalid json)
	pathA := filepath.Join(tmpDir, "manifest-a")
	os.WriteFile(pathA, []byte("invalid\nbb82030dbc2bcaba32a90bf2e207a84a856fc5f033b77c480836ab6f77f40f19\n"), 0644)

	_, err = Load(tmpDir)
	if err != ErrNoValidCopy {
		t.Errorf("expected ErrNoValidCopy, got %v", err)
	}
}

func TestIOError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions, so a mode-0000 file is still readable")
	}
	tmpDir := t.TempDir()

	// create file with unreadable permissions
	pathA := filepath.Join(tmpDir, "manifest-a")
	os.WriteFile(pathA, []byte("test"), 0000)

	_, err := Load(tmpDir)
	if err == nil {
		t.Errorf("expected an error, got nil")
	} else if err == ErrNoValidCopy {
		t.Errorf("expected IO error, got ErrNoValidCopy")
	} else if !os.IsPermission(err) {
		t.Errorf("expected permission error, got %v", err)
	}

	// Ensure permission is restored so temp dir can be cleaned up
	os.Chmod(pathA, 0644)
}

func TestValidation(t *testing.T) {
	// Read example manifest
	examplePath := "../../../schema/examples/manifest-uefi.json"
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("failed to read example manifest: %v", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal example: %v", err)
	}

	if err := m.Validate(); err != nil {
		t.Errorf("example manifest failed validation: %v", err)
	}

	// Read tier A example manifest
	exampleTierAPath := "../../../schema/examples/manifest-uefi-tier-a.json"
	dataTierA, err := os.ReadFile(exampleTierAPath)
	if err != nil {
		t.Fatalf("failed to read tier A example manifest: %v", err)
	}
	var mTierA Manifest
	if err := json.Unmarshal(dataTierA, &mTierA); err != nil {
		t.Fatalf("unmarshal tier A example: %v", err)
	}
	if err := mTierA.Validate(); err != nil {
		t.Errorf("tier A example manifest failed validation: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string
	}{
		{
			name: "missing images",
			mutate: func(m *Manifest) {
				m.Images = nil
			},
			wantErr: "images is required",
		},
		{
			name: "missing recovery",
			mutate: func(m *Manifest) {
				m.Recovery = nil
			},
			wantErr: "recovery is required",
		},
		{
			name: "duplicate generation",
			mutate: func(m *Manifest) {
				m.Images = append(m.Images, m.Images[0])
			},
			wantErr: "duplicate generation",
		},
		{
			name: "generation >= next_generation",
			mutate: func(m *Manifest) {
				m.NextGeneration = m.Images[0].Generation
			},
			wantErr: ">= next_generation",
		},
		{
			name: "invalid state",
			mutate: func(m *Manifest) {
				m.Images[0].State = "INVALID"
			},
			wantErr: "invalid state",
		},
		{
			name: "uefi missing for uefi firmware",
			mutate: func(m *Manifest) {
				m.Firmware = "uefi"
				m.Images[0].UEFI = nil
			},
			wantErr: "uefi missing",
		},
		{
			name: "bios present for uefi firmware",
			mutate: func(m *Manifest) {
				m.Firmware = "uefi"
				m.Images[0].BIOS = &BIOSFiles{}
			},
			wantErr: "bios present for uefi firmware",
		},
		{
			name: "bios missing for bios firmware",
			mutate: func(m *Manifest) {
				m.Firmware = "bios"
				// Ensure uefi is cleared to bypass uefi present check
				for i := range m.Images {
					m.Images[i].UEFI = nil
				}
				for i := range m.Recovery {
					m.Recovery[i].UEFI = nil
				}
				m.Images[0].BIOS = nil
			},
			wantErr: "bios missing",
		},
		{
			name: "invalid uefi entry stem",
			mutate: func(m *Manifest) {
				m.Images[0].UEFI.Entry = "wrong-stem"
			},
			wantErr: "invalid uefi entry",
		},
		{
			name: "duplicate recovery slot",
			mutate: func(m *Manifest) {
				m.Recovery = append(m.Recovery, m.Recovery[0])
			},
			wantErr: "too many recovery records",
		},
		{
			name: "duplicate recovery slot 2",
			mutate: func(m *Manifest) {
				if len(m.Recovery) >= 2 {
					m.Recovery[1].Slot = m.Recovery[0].Slot
				}
			},
			wantErr: "duplicate recovery slot",
		},
		{
			name: "tier A without signing",
			mutate: func(m *Manifest) {
				m.SecureBootTier = "A"
				m.Signing = nil
				for i := range m.Images {
					if m.Images[i].UEFI != nil {
						m.Images[i].UEFI.Signed = true
					}
				}
			},
			wantErr: "tier A requires signing",
		},
		{
			name: "tier A with bios firmware",
			mutate: func(m *Manifest) {
				m.SecureBootTier = "A"
				m.Firmware = "bios"
				m.Signing = &Signing{CertSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
			},
			wantErr: "tier A requires firmware uefi or both",
		},
		{
			name: "tier none with signing",
			mutate: func(m *Manifest) {
				m.SecureBootTier = "none"
				m.Signing = &Signing{CertSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
				for i := range m.Images {
					if m.Images[i].UEFI != nil {
						m.Images[i].UEFI.Signed = false
					}
				}
			},
			wantErr: "tier none must not have signing",
		},
		{
			name: "tier A image with signed false",
			mutate: func(m *Manifest) {
				m.SecureBootTier = "A"
				m.Signing = &Signing{CertSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
				for i := range m.Images {
					if m.Images[i].UEFI != nil {
						m.Images[i].UEFI.Signed = true
					}
				}
				m.Images[0].UEFI.Signed = false
			},
			wantErr: "tier A requires uefi.signed to be true",
		},
		{
			name: "invalid uefi sha256",
			mutate: func(m *Manifest) {
				m.Images[0].UEFI.SHA256 = "invalid"
			},
			wantErr: "invalid uefi sha256",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Deep copy
			b, _ := json.Marshal(m)
			var mCopy Manifest
			json.Unmarshal(b, &mCopy)

			tt.mutate(&mCopy)
			err := mCopy.Validate()
			if err == nil {
				t.Errorf("expected error, got nil")
			} else if tt.wantErr != "" && !contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s != "" && substr != "" && stringContains(s, substr)
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestUnknownFields(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "manifest-a")

	jsonStr := `{"format":1,"seq":1,"unknown":true,"installation_id":"0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f","machine_id":"3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f","firmware":"uefi","secure_boot_tier":"B","root":{"partuuid":"6a1f3c2e-0d5b-4f7e-9a8c-1b2d3e4f5a6b","uuid":"b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e"},"boot_storage":{"partuuid":"9d8c7b6a-5f4e-4d3c-2b1a-0e9f8d7c6b5a"},"tries":3,"retain":3,"next_generation":2,"images":[],"recovery":[]}`
	h := sha256.Sum256([]byte(jsonStr))
	content := fmt.Sprintf("%s\n%x\n", jsonStr, h)

	os.WriteFile(path, []byte(content), 0644)

	_, err := readCopy(path)
	if err == nil {
		t.Errorf("expected error due to unknown field")
	} else if !stringContains(err.Error(), "unknown field") {
		t.Errorf("expected unknown field error, got %v", err)
	}
}

func TestSaveStaleSeqStillWins(t *testing.T) {
	dir := t.TempDir()
	m := loadExample(t)
	if err := Save(dir, m); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, m); err != nil {
		t.Fatal(err)
	}
	stale := loadExample(t) // seq from the example file, below what is on disk
	stale.Retain = 5
	if err := Save(dir, stale); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Retain != 5 {
		t.Fatalf("stale-seq save was lost: retain %d, seq %d", got.Retain, got.Seq)
	}
}

func loadExample(t *testing.T) *Manifest {
	t.Helper()
	data, err := os.ReadFile("../../../schema/examples/manifest-uefi.json")
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}
