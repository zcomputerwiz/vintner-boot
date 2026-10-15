package state

import (
	"reflect"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func TestReconcile(t *testing.T) {
	tests := []struct {
		name         string
		m            *manifest.Manifest
		v            *View
		wantActions  []Action
		wantReports  []string
		wantManifest *manifest.Manifest
	}{
		{
			name: "rule 1: STAGED verifies becomes TRYING (UEFI)",
			m: &manifest.Manifest{
				Firmware: "uefi",
				Images: []manifest.Image{
					{
						Generation: 42,
						State:      "STAGED",
						UEFI: &manifest.UEFIFile{
							Entry:  "vintner-os-42",
							SHA256: "hash123",
						},
					},
				},
			},
			v: &View{
				UEFIFiles: map[string]string{
					"vintner-os-42+3-0.efi": "hash123",
				},
			},
			wantActions: nil,
			wantReports: nil,
			wantManifest: &manifest.Manifest{
				Firmware: "uefi",
				Images: []manifest.Image{
					{
						Generation: 42,
						State:      "TRYING",
						UEFI: &manifest.UEFIFile{
							Entry:  "vintner-os-42",
							SHA256: "hash123",
						},
					},
				},
			},
		},
		{
			name: "rule 2: STAGED not in final place removes staging (BIOS)",
			m: &manifest.Manifest{
				Firmware: "bios",
				Images: []manifest.Image{
					{
						Generation: 43,
						State:      "STAGED",
						BIOS: &manifest.BIOSFiles{
							Files: manifest.BIOSFilesMap{
								Vmlinuz:   "h1",
								InitrdImg: "h2",
								SlotCfg:   "h3",
							},
						},
					},
				},
			},
			v: &View{
				BIOSDirs:     map[string]map[string]string{},
				StagingFiles: []string{"os-43-tmp1", "vintner-os-43-tmp2"},
			},
			wantActions: []Action{
				{Type: "remove_staging", Name: "os-43-tmp1"},
				{Type: "remove_staging", Name: "vintner-os-43-tmp2"},
			},
			wantReports: nil,
			wantManifest: &manifest.Manifest{
				Firmware: "bios",
				Images:   nil, // Reconcile sets newImages but if empty it might be nil
			},
		},
		{
			name: "rule 3: untracked file reported",
			m: &manifest.Manifest{
				Firmware: "both",
			},
			v: &View{
				UEFIFiles: map[string]string{
					"vintner-os-99.efi": "h",
				},
				BIOSDirs: map[string]map[string]string{
					"os-100": {},
				},
			},
			wantActions: nil,
			wantReports: []string{"untracked uefi file vintner-os-99.efi", "untracked bios directory os-100"},
			wantManifest: &manifest.Manifest{
				Firmware: "both",
				Images:   nil,
			},
		},
		{
			name: "rule 4: missing file reported",
			m: &manifest.Manifest{
				Firmware: "uefi",
				Images: []manifest.Image{
					{
						Generation: 10,
						State:      "GOOD",
						UEFI: &manifest.UEFIFile{
							Entry: "vintner-os-10",
						},
					},
				},
			},
			v: &View{
				UEFIFiles: map[string]string{},
			},
			wantActions: nil,
			wantReports: []string{"missing files for generation 10"},
			wantManifest: &manifest.Manifest{
				Firmware: "uefi",
				Images: []manifest.Image{
					{
						Generation: 10,
						State:      "GOOD",
						UEFI: &manifest.UEFIFile{
							Entry: "vintner-os-10",
						},
					},
				},
			},
		},
		{
			name: "rule 5: counter at zero sets FAILED (UEFI & BIOS)",
			m: &manifest.Manifest{
				Firmware: "both",
				Images: []manifest.Image{
					{
						Generation: 11,
						State:      "TRYING",
						UEFI: &manifest.UEFIFile{
							Entry: "vintner-os-11",
						},
					},
					{
						Generation: 12,
						State:      "GOOD",
						UEFI: &manifest.UEFIFile{
							Entry: "vintner-os-12",
						},
					},
				},
			},
			v: &View{
				UEFIFiles: map[string]string{
					"vintner-os-11+0-3.efi": "h", // Bad
					"vintner-os-12+1-0.efi": "h", // Not bad
				},
				BIOSDirs: map[string]map[string]string{
					"os-11": {},
					"os-12": {},
				},
				GrubEnv: &grubenv.Env{}, // VINTNER_T_11 and VINTNER_T_12 are unset (which means bad)
			},
			wantActions: nil,
			wantReports: nil,
			wantManifest: &manifest.Manifest{
				Firmware: "both",
				Images: []manifest.Image{
					{
						Generation: 11,
						State:      "FAILED", // Because UEFI is bad and BIOS is bad
						UEFI: &manifest.UEFIFile{
							Entry: "vintner-os-11",
						},
					},
					{
						Generation: 12,
						State:      "FAILED", // Because BIOS is bad (unset)
						UEFI: &manifest.UEFIFile{
							Entry: "vintner-os-12",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotActions, gotReports := Reconcile(tt.m, tt.v)

			if len(gotActions) == 0 && len(tt.wantActions) == 0 {
				gotActions = nil
				tt.wantActions = nil
			}
			if len(gotReports) == 0 && len(tt.wantReports) == 0 {
				gotReports = nil
				tt.wantReports = nil
			}
			if len(tt.m.Images) == 0 && len(tt.wantManifest.Images) == 0 {
				tt.m.Images = nil
				tt.wantManifest.Images = nil
			}

			if !reflect.DeepEqual(gotActions, tt.wantActions) {
				t.Errorf("actions = %v, want %v", gotActions, tt.wantActions)
			}
			if !reflect.DeepEqual(gotReports, tt.wantReports) {
				t.Errorf("reports = %v, want %v", gotReports, tt.wantReports)
			}
			if !reflect.DeepEqual(tt.m, tt.wantManifest) {
				t.Errorf("manifest = %v, want %v", tt.m, tt.wantManifest)
			}
		})
	}
}

func TestKeep(t *testing.T) {
	tests := []struct {
		name           string
		m              *manifest.Manifest
		wantKeep       []int
		wantRemove     []int
		wantOverBudget bool
	}{
		{
			name: "ABI rule, stale rule, overBudget, removal order",
			m: &manifest.Manifest{
				Retain: 3,
				Images: []manifest.Image{
					{Generation: 1, State: "FAILED", Kernel: manifest.Kernel{ABI: "A"}},
					{Generation: 2, State: "GOOD", Kernel: manifest.Kernel{ABI: "B"}},              // Not kept, because rules 1, 2, 3 don't catch it and rule 4 fills up before reaching it
					{Generation: 3, State: "GOOD", Kernel: manifest.Kernel{ABI: "B"}, Stale: true}, // Stale GOOD
					{Generation: 4, State: "GOOD", Kernel: manifest.Kernel{ABI: "A"}},              // Rule 2
					{Generation: 5, State: "GOOD", Kernel: manifest.Kernel{ABI: "B"}},              // Rule 1
					{Generation: 6, State: "TRYING", Kernel: manifest.Kernel{ABI: "B"}},            // Rule 3
					{Generation: 7, State: "TRYING", Kernel: manifest.Kernel{ABI: "B"}},            // Rule 3
				},
			},
			wantKeep:       []int{4, 5, 6, 7}, // over Retain 3 -> overBudget
			wantRemove:     []int{1, 2, 3},    // 1 FAILED, 2, 3 other
			wantOverBudget: true,
		},
		{
			name: "Under budget",
			m: &manifest.Manifest{
				Retain: 3,
				Images: []manifest.Image{
					{Generation: 1, State: "GOOD", Kernel: manifest.Kernel{ABI: "A"}},
					{Generation: 2, State: "GOOD", Kernel: manifest.Kernel{ABI: "A"}},
				},
			},
			wantKeep:       []int{1, 2},
			wantRemove:     nil,
			wantOverBudget: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKeep, gotRemove, gotOverBudget := Keep(tt.m)

			if len(gotKeep) == 0 && len(tt.wantKeep) == 0 {
				gotKeep = nil
				tt.wantKeep = nil
			}
			if len(gotRemove) == 0 && len(tt.wantRemove) == 0 {
				gotRemove = nil
				tt.wantRemove = nil
			}

			if !reflect.DeepEqual(gotKeep, tt.wantKeep) {
				t.Errorf("keep = %v, want %v", gotKeep, tt.wantKeep)
			}
			if !reflect.DeepEqual(gotRemove, tt.wantRemove) {
				t.Errorf("remove = %v, want %v", gotRemove, tt.wantRemove)
			}
			if gotOverBudget != tt.wantOverBudget {
				t.Errorf("overBudget = %v, want %v", gotOverBudget, tt.wantOverBudget)
			}
		})
	}
}
