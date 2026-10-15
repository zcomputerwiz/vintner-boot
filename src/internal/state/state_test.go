// SPDX-License-Identifier: GPL-2.0-or-later

package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func env(kv ...string) *grubenv.Env {
	e := &grubenv.Env{}
	for i := 0; i < len(kv); i += 2 {
		e.Set(kv[i], kv[i+1])
	}
	return e
}

var biosFiles = &manifest.BIOSFiles{Files: manifest.BIOSFilesMap{Vmlinuz: "h1", InitrdImg: "h2", SlotCfg: "h3"}}
var biosSlot = map[string]string{"vmlinuz": "h1", "initrd.img": "h2", "slot.cfg": "h3"}

func uefiImg(g int, state string) manifest.Image {
	return manifest.Image{Generation: g, State: state, UEFI: &manifest.UEFIFile{Entry: "vintner-os-" + strconv.Itoa(g), SHA256: "h"}}
}

func biosImg(g int, state string) manifest.Image {
	return manifest.Image{Generation: g, State: state, BIOS: biosFiles}
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name        string
		firmware    string
		images      []manifest.Image
		v           *View
		wantActions []Action
		wantReports []string
		wantStates  []string // states of the records left, in order
	}{
		{
			name:       "rule 1: STAGED that verifies becomes TRYING (UEFI)",
			firmware:   "uefi",
			images:     []manifest.Image{uefiImg(42, "STAGED")},
			v:          &View{UEFIFiles: map[string]string{"vintner-os-42+3-0.efi": "h"}},
			wantStates: []string{"TRYING"},
		},
		{
			name:     "rule 1: STAGED that verifies becomes TRYING (BIOS)",
			firmware: "bios",
			images:   []manifest.Image{biosImg(43, "STAGED")},
			v: &View{BIOSDirs: map[string]map[string]string{"os-43": biosSlot},
				GrubEnv: env("VINTNER_ORDER", "43", "VINTNER_T_43", "xxx")},
			wantStates: []string{"TRYING"},
		},
		{
			name:     "rule 2: STAGED not in place loses its staging entries and record, and only its own",
			firmware: "bios",
			images:   []manifest.Image{biosImg(43, "STAGED")},
			v: &View{BIOSDirs: map[string]map[string]string{}, GrubEnv: env(),
				Staging: []string{"os-43", "vintner-os-43.efi", "os-430", "vintner-os-430.efi"}},
			wantActions: []Action{{"remove_staging", "vintner-os-43.efi"}, {"remove_staging", "os-43"}},
			wantStates:  []string{},
		},
		{
			name:        "STAGED in place but not verifying is reported and kept",
			firmware:    "uefi",
			images:      []manifest.Image{uefiImg(44, "STAGED")},
			v:           &View{UEFIFiles: map[string]string{"vintner-os-44+3-0.efi": "other"}, Staging: []string{"vintner-os-44.efi"}},
			wantReports: []string{"generation 44 is STAGED and in place but does not verify"},
			wantStates:  []string{"STAGED"},
		},
		{
			name:        "BIOS slot in place but not in VINTNER_ORDER does not verify",
			firmware:    "bios",
			images:      []manifest.Image{biosImg(45, "STAGED")},
			v:           &View{BIOSDirs: map[string]map[string]string{"os-45": biosSlot}, GrubEnv: env("VINTNER_T_45", "xxx")},
			wantReports: []string{"generation 45 is STAGED and in place but does not verify"},
			wantStates:  []string{"STAGED"},
		},
		{
			name:     "rule 3: untracked files reported",
			firmware: "both",
			v: &View{UEFIFiles: map[string]string{"vintner-os-99.efi": "h", "vintner-recovery-a+3-0.efi": "h"},
				BIOSDirs: map[string]map[string]string{"os-100": {}, "recovery-a": {}}, GrubEnv: env()},
			wantReports: []string{"untracked uefi file vintner-os-99.efi", "untracked bios directory os-100"},
			wantStates:  []string{},
		},
		{
			name:        "rule 4: missing file reported",
			firmware:    "uefi",
			images:      []manifest.Image{uefiImg(10, "GOOD")},
			v:           &View{UEFIFiles: map[string]string{}},
			wantReports: []string{"missing files for generation 10"},
			wantStates:  []string{"GOOD"},
		},
		{
			name:     "rule 5: counter at zero sets FAILED (UEFI and BIOS)",
			firmware: "both",
			images: []manifest.Image{
				{Generation: 11, State: "TRYING", UEFI: &manifest.UEFIFile{Entry: "vintner-os-11"}, BIOS: biosFiles},
				{Generation: 12, State: "GOOD", UEFI: &manifest.UEFIFile{Entry: "vintner-os-12"}, BIOS: biosFiles},
				{Generation: 13, State: "GOOD", UEFI: &manifest.UEFIFile{Entry: "vintner-os-13"}, BIOS: biosFiles},
			},
			v: &View{
				UEFIFiles: map[string]string{"vintner-os-11+0-3.efi": "h", "vintner-os-12+1-0.efi": "h", "vintner-os-13+3-0.efi": "h"},
				BIOSDirs:  map[string]map[string]string{"os-11": biosSlot, "os-12": biosSlot, "os-13": biosSlot},
				GrubEnv:   env("VINTNER_T_11", "xx", "VINTNER_T_12", "0", "VINTNER_T_13", "x"),
			},
			wantStates: []string{"FAILED", "FAILED", "GOOD"},
		},
		{
			name:     "unset or malformed BIOS counter is reported, not FAILED",
			firmware: "bios",
			images:   []manifest.Image{biosImg(14, "GOOD"), biosImg(15, "TRYING")},
			v: &View{BIOSDirs: map[string]map[string]string{"os-14": biosSlot, "os-15": biosSlot},
				GrubEnv: env("VINTNER_T_15", "x0")},
			wantReports: []string{`generation 14 has BIOS counter ""`, `generation 15 has BIOS counter "x0"`},
			wantStates:  []string{"GOOD", "TRYING"},
		},
		{
			name:        "unreadable grubenv is reported once and changes nothing",
			firmware:    "bios",
			images:      []manifest.Image{biosImg(16, "GOOD")},
			v:           &View{BIOSDirs: map[string]map[string]string{"os-16": biosSlot}, GrubEnvErr: errors.New("grubenv: invalid header")},
			wantReports: []string{"grubenv unreadable, BIOS counters not checked: grubenv: invalid header"},
			wantStates:  []string{"GOOD"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manifest.Manifest{Firmware: tt.firmware, Images: tt.images}
			gotActions, gotReports := Reconcile(m, tt.v)
			if len(gotActions)+len(tt.wantActions) > 0 && !reflect.DeepEqual(gotActions, tt.wantActions) {
				t.Errorf("actions = %v, want %v", gotActions, tt.wantActions)
			}
			if len(gotReports)+len(tt.wantReports) > 0 && !reflect.DeepEqual(gotReports, tt.wantReports) {
				t.Errorf("reports = %q, want %q", gotReports, tt.wantReports)
			}
			states := []string{}
			for _, img := range m.Images {
				states = append(states, img.State)
			}
			if !reflect.DeepEqual(states, tt.wantStates) {
				t.Errorf("states = %v, want %v", states, tt.wantStates)
			}
		})
	}
}

func TestScan(t *testing.T) {
	bs := t.TempDir()
	for p, data := range map[string]string{
		"EFI/Linux/vintner-os-1+3-0.efi": "uki",
		"vintner/bios/os-1/vmlinuz":      "k",
		"vintner/staging/os-2/vmlinuz":   "k",
		"grub/grubenv":                   "not a grubenv",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(bs, p)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bs, p), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	v, err := Scan(bs)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(v.UEFIFiles) != 1 || len(v.BIOSDirs["os-1"]) != 1 || !reflect.DeepEqual(v.Staging, []string{"os-2"}) {
		t.Errorf("view = %+v", v)
	}
	if v.GrubEnv != nil || v.GrubEnvErr == nil {
		t.Errorf("corrupt grubenv: GrubEnv %v, GrubEnvErr %v", v.GrubEnv, v.GrubEnvErr)
	}
	if v, err := Scan(filepath.Join(bs, "missing")); err != nil || len(v.UEFIFiles) != 0 {
		t.Errorf("missing boot storage: %v, %v", v, err)
	}
}

func TestKeep(t *testing.T) {
	img := func(g int, state, abi string, stale bool) manifest.Image {
		return manifest.Image{Generation: g, State: state, Kernel: manifest.Kernel{ABI: abi}, Stale: stale}
	}
	tests := []struct {
		name           string
		retain         int
		images         []manifest.Image
		wantKeep       []int
		wantRemove     []int
		wantOverBudget bool
	}{
		{
			name:   "ABI rule, stale GOOD, TRYING over budget, removal order",
			retain: 3,
			images: []manifest.Image{
				img(1, "FAILED", "A", false),
				img(2, "GOOD", "B", false),
				img(3, "GOOD", "B", true),
				img(4, "GOOD", "A", false), // rule 2
				img(5, "GOOD", "B", false), // rule 1
				img(6, "TRYING", "B", false),
				img(7, "TRYING", "B", false),
			},
			wantKeep:       []int{4, 5, 6, 7},
			wantRemove:     []int{1, 2, 3},
			wantOverBudget: true,
		},
		{
			name:     "under budget",
			retain:   3,
			images:   []manifest.Image{img(1, "GOOD", "A", false), img(2, "GOOD", "A", false)},
			wantKeep: []int{1, 2},
		},
		{
			name:     "unsorted records; FAILED kept while under budget",
			retain:   3,
			images:   []manifest.Image{img(3, "GOOD", "A", false), img(1, "FAILED", "A", false), img(2, "GOOD", "A", false)},
			wantKeep: []int{1, 2, 3},
		},
		{
			name:       "FAILED removed before older good images",
			retain:     2,
			images:     []manifest.Image{img(4, "FAILED", "A", false), img(1, "GOOD", "A", false), img(3, "GOOD", "A", false), img(2, "FAILED", "A", false)},
			wantKeep:   []int{1, 3},
			wantRemove: []int{2, 4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keep, remove, over := Keep(&manifest.Manifest{Retain: tt.retain, Images: tt.images})
			if !reflect.DeepEqual(keep, tt.wantKeep) {
				t.Errorf("keep = %v, want %v", keep, tt.wantKeep)
			}
			if len(remove)+len(tt.wantRemove) > 0 && !reflect.DeepEqual(remove, tt.wantRemove) {
				t.Errorf("remove = %v, want %v", remove, tt.wantRemove)
			}
			if over != tt.wantOverBudget {
				t.Errorf("overBudget = %v, want %v", over, tt.wantOverBudget)
			}
		})
	}
}
