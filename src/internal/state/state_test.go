// SPDX-License-Identifier: GPL-2.0-or-later

package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
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
			wantActions: []Action{{Type: "remove_staging", Name: "vintner-os-43.efi"}, {Type: "remove_staging", Name: "os-43"}},
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
			name:     "rule 1b: STAGED BIOS slot in place, never armed, is armed and becomes TRYING",
			firmware: "bios",
			images:   []manifest.Image{biosImg(46, "STAGED")},
			v: &View{BIOSDirs: map[string]map[string]string{"os-46": biosSlot}, GrubEnv: env("VINTNER_ORDER", "1"),
				Staging: []string{}},
			wantActions: []Action{{Type: "arm_bios", Generation: 46, Tries: 3}},
			wantStates:  []string{"TRYING"},
		},
		{
			name:     "rule 1b: BIOS slot files that do not match the record are not armed",
			firmware: "bios",
			images:   []manifest.Image{biosImg(47, "STAGED")},
			v: &View{BIOSDirs: map[string]map[string]string{"os-47": {"vmlinuz": "x", "initrd.img": "h2", "slot.cfg": "h3"}},
				GrubEnv: env("VINTNER_ORDER", "1")},
			wantReports: []string{"generation 47 is STAGED and in place but does not verify"},
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
			m := &manifest.Manifest{Firmware: tt.firmware, Tries: 3, Images: tt.images}
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

func TestApplyArm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grub", "grubenv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	blank := "# GRUB Environment Block\n" + strings.Repeat("#", 1024-len("# GRUB Environment Block\n"))
	if err := os.WriteFile(path, []byte(blank), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := grubenv.Write(path, env("VINTNER_ORDER", "1", "VINTNER_T_1", "xxx")); err != nil {
		t.Fatal(err)
	}
	if err := Apply(dir, Action{Type: "arm_bios", Generation: 2, Tries: 3}); err != nil {
		t.Fatal(err)
	}
	e, err := grubenv.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := e.Get("VINTNER_ORDER"); o != "2 1" {
		t.Errorf("VINTNER_ORDER = %q, want %q", o, "2 1")
	}
	if c, _ := e.Get("VINTNER_T_2"); c != "xxx" {
		t.Errorf("VINTNER_T_2 = %q, want xxx", c)
	}
}

func TestCheckHealth_Recovery(t *testing.T) {
	healthyVars := t.TempDir() // never the real efivarfs of the machine running the tests
	vintnerEntry(t, healthyVars, 0)
	recA := manifest.Recovery{
		Slot: "a",
		UEFI: &manifest.UEFIFile{Entry: "vintner-recovery-a", SHA256: "sha_a"},
		BIOS: &manifest.BIOSFiles{Files: manifest.BIOSFilesMap{Vmlinuz: "k_a", InitrdImg: "i_a", SlotCfg: "c_a"}},
	}
	recB := manifest.Recovery{
		Slot: "b",
		UEFI: &manifest.UEFIFile{Entry: "vintner-recovery-b", SHA256: "sha_b"},
		BIOS: &manifest.BIOSFiles{Files: manifest.BIOSFilesMap{Vmlinuz: "k_b", InitrdImg: "i_b", SlotCfg: "c_b"}},
	}

	validUEFI := map[string]string{
		"vintner-recovery-a+3-0.efi": "sha_a",
		"vintner-recovery-b.efi":     "sha_b",
	}
	validBIOS := map[string]map[string]string{
		"recovery-a": {"vmlinuz": "k_a", "initrd.img": "i_a", "slot.cfg": "c_a"},
		"recovery-b": {"vmlinuz": "k_b", "initrd.img": "i_b", "slot.cfg": "c_b"},
	}

	// 1. Valid recovery on both firmwares -> no reports
	m := &manifest.Manifest{Firmware: "both", Recovery: []manifest.Recovery{recA, recB}}
	v := &View{UEFIFiles: validUEFI, BIOSDirs: validBIOS, GrubEnv: env()}
	reports := CheckHealth(m, v, "", "", false)
	if len(reports) != 0 {
		t.Errorf("expected 0 reports for valid recovery, got %q", reports)
	}

	// 2. Missing UEFI file for slot a
	vMissingUEFI := &View{
		UEFIFiles: map[string]string{"vintner-recovery-b.efi": "sha_b"},
		BIOSDirs:  validBIOS,
		GrubEnv:   env(),
	}
	reports = CheckHealth(m, vMissingUEFI, healthyVars, "", true)
	want := []string{"invalid recovery image: missing uefi file for slot a"}
	if !reflect.DeepEqual(reports, want) {
		t.Errorf("missing UEFI recovery: reports = %q, want %q", reports, want)
	}

	// 3. Hash mismatch for slot a (like S4)
	vMismatchUEFI := &View{
		UEFIFiles: map[string]string{"vintner-recovery-a+3-0.efi": "sha_corrupt", "vintner-recovery-b.efi": "sha_b"},
		BIOSDirs:  validBIOS,
		GrubEnv:   env(),
	}
	reports = CheckHealth(m, vMismatchUEFI, healthyVars, "", true)
	want = []string{"invalid recovery image: hash mismatch for slot a"}
	if !reflect.DeepEqual(reports, want) {
		t.Errorf("mismatch UEFI recovery: reports = %q, want %q", reports, want)
	}

	// 4. Missing BIOS directory for slot b
	vMissingBIOS := &View{
		UEFIFiles: validUEFI,
		BIOSDirs:  map[string]map[string]string{"recovery-a": validBIOS["recovery-a"]},
		GrubEnv:   env(),
	}
	reports = CheckHealth(m, vMissingBIOS, "", "", false)
	want = []string{"invalid recovery image: missing bios directory for slot b"}
	if !reflect.DeepEqual(reports, want) {
		t.Errorf("missing BIOS recovery: reports = %q, want %q", reports, want)
	}

	// 5. File mismatch inside BIOS directory for slot a
	vMismatchBIOS := &View{
		UEFIFiles: validUEFI,
		BIOSDirs: map[string]map[string]string{
			"recovery-a": {"vmlinuz": "bad_k", "initrd.img": "i_a", "slot.cfg": "c_a"},
			"recovery-b": validBIOS["recovery-b"],
		},
		GrubEnv: env(),
	}
	reports = CheckHealth(m, vMismatchBIOS, "", "", false)
	want = []string{"invalid recovery image: file mismatch for slot a"}
	if !reflect.DeepEqual(reports, want) {
		t.Errorf("mismatch BIOS recovery: reports = %q, want %q", reports, want)
	}
}

func TestCheckHealth(t *testing.T) {
	efivarsDir := t.TempDir()
	sbDir := t.TempDir()

	// Generate a real key/cert for testing certificate check
	fp, err := secureboot.GenerateKey(sbDir, "test-install-uuid")
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	// Tier none -> no reports
	mNone := &manifest.Manifest{SecureBootTier: "none"}
	if r := CheckHealth(mNone, nil, efivarsDir, sbDir, true); len(r) != 0 {
		t.Errorf("tier none: expected 0 reports, got %q", r)
	}

	// Tier A + UEFI, SecureBoot disabled -> secure_boot=off
	mA := &manifest.Manifest{
		SecureBootTier: "A",
		Signing:        &manifest.Signing{CertSHA256: fp},
	}
	r := CheckHealth(mA, nil, efivarsDir, sbDir, true)
	if !reflect.DeepEqual(r, []string{"secure_boot=off"}) {
		t.Errorf("tier A without SecureBoot: reports = %q, want [\"secure_boot=off\"]", r)
	}

	// Enable SecureBoot in efivars
	if err := efivar.Set(efivarsDir, "SecureBoot", efivar.GlobalVariable, 0x7, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := efivar.Set(efivarsDir, "SetupMode", efivar.GlobalVariable, 0x7, []byte{0}); err != nil {
		t.Fatal(err)
	}

	// Now with SecureBoot enabled and matching cert -> 0 reports
	if r := CheckHealth(mA, nil, efivarsDir, sbDir, true); len(r) != 0 {
		t.Errorf("tier A healthy: expected 0 reports, got %q", r)
	}

	// SetupMode=1 -> secure_boot=off
	if err := efivar.Set(efivarsDir, "SetupMode", efivar.GlobalVariable, 0x7, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if r := CheckHealth(mA, nil, efivarsDir, sbDir, true); !reflect.DeepEqual(r, []string{"secure_boot=off"}) {
		t.Errorf("tier A with SetupMode=1: reports = %q, want [\"secure_boot=off\"]", r)
	}
	_ = efivar.Set(efivarsDir, "SetupMode", efivar.GlobalVariable, 0x7, []byte{0})

	// Certificate mismatch
	mAMismatch := &manifest.Manifest{
		SecureBootTier: "A",
		Signing:        &manifest.Signing{CertSHA256: strings.Repeat("0", 64)},
	}
	r = CheckHealth(mAMismatch, nil, efivarsDir, sbDir, true)
	if len(r) != 1 || !strings.Contains(r[0], "secureboot certificate mismatch") {
		t.Errorf("certificate mismatch: reports = %q, want mismatch report", r)
	}

	// Missing certificate file
	missingSBDir := t.TempDir()
	r = CheckHealth(mA, nil, efivarsDir, missingSBDir, true)
	if len(r) != 1 || !strings.Contains(r[0], "secureboot certificate unreadable") {
		t.Errorf("missing cert: reports = %q, want unreadable report", r)
	}

	// Tier A on BIOS (isUEFI = false) -> SecureBoot efivar not checked
	if r := CheckHealth(mA, nil, efivarsDir, sbDir, false); len(r) != 0 {
		t.Errorf("tier A on BIOS: expected 0 reports, got %q", r)
	}

	// Recovery image integrity checks via CheckHealth
	recManifest := &manifest.Manifest{
		Firmware: "both",
		Recovery: []manifest.Recovery{
			{Slot: "a", UEFI: &manifest.UEFIFile{SHA256: "good_sha"}, BIOS: &manifest.BIOSFiles{Files: manifest.BIOSFilesMap{Vmlinuz: "k", InitrdImg: "i", SlotCfg: "c"}}},
		},
	}
	vGoodRec := &View{
		UEFIFiles: map[string]string{"vintner-recovery-a.efi": "good_sha"},
		BIOSDirs:  map[string]map[string]string{"recovery-a": {"vmlinuz": "k", "initrd.img": "i", "slot.cfg": "c"}},
	}
	if r := CheckHealth(recManifest, vGoodRec, efivarsDir, sbDir, false); len(r) != 0 {
		t.Errorf("healthy recovery: expected 0 reports, got %q", r)
	}

	vBadRec := &View{
		UEFIFiles: map[string]string{"vintner-recovery-a.efi": "bad_sha"},
		BIOSDirs:  map[string]map[string]string{"recovery-a": {"vmlinuz": "bad_k", "initrd.img": "i", "slot.cfg": "c"}},
	}
	r = CheckHealth(recManifest, vBadRec, efivarsDir, sbDir, false)
	wantRecReports := []string{
		"invalid recovery image: hash mismatch for slot a",
		"invalid recovery image: file mismatch for slot a",
	}
	if !reflect.DeepEqual(r, wantRecReports) {
		t.Errorf("bad recovery: reports = %q, want %q", r, wantRecReports)
	}
}

func TestCheckHealth_SelectionPolicy(t *testing.T) {
	efivarsDir := t.TempDir()
	sbDir := t.TempDir()
	vintnerEntry(t, efivarsDir, 0)

	m := &manifest.Manifest{
		Firmware: "both",
		Images: []manifest.Image{
			{Generation: 1},
		},
		Recovery: []manifest.Recovery{
			{Slot: "a", UEFI: &manifest.UEFIFile{SHA256: "rec_sha"}, BIOS: &manifest.BIOSFiles{Files: manifest.BIOSFilesMap{Vmlinuz: "k", InitrdImg: "i", SlotCfg: "c"}}},
		},
	}

	// 1. Test EFI variables reporting (LoaderEntryDefault, LoaderConfigTimeout, LoaderEntryOneShot)
	if err := efivar.SetString(efivarsDir, "LoaderEntryDefault", efivar.LoaderInfo, 0x7, "debian-6.12.efi"); err != nil {
		t.Fatal(err)
	}
	if err := efivar.SetString(efivarsDir, "LoaderConfigTimeout", efivar.LoaderInfo, 0x7, "5"); err != nil {
		t.Fatal(err)
	}
	if err := efivar.SetString(efivarsDir, "LoaderEntryOneShot", efivar.LoaderInfo, 0x7, "debian-6.12.efi"); err != nil {
		t.Fatal(err)
	}

	r := CheckHealth(m, nil, efivarsDir, sbDir, true)
	wantEFIVars := []string{
		"LoaderEntryDefault is set: debian-6.12.efi",
		"LoaderConfigTimeout is set: 5",
		"LoaderEntryOneShot is set: debian-6.12.efi",
	}
	if !reflect.DeepEqual(r, wantEFIVars) {
		t.Errorf("EFI vars reports = %q, want %q", r, wantEFIVars)
	}

	// A one-shot that vintner next-boot made (WO-0010 4.4) is not a finding; the
	// same variable with a foreign value still is.
	for _, ours := range []string{"vintner-recovery-*", "vintner-os-3.efi", "vintner_variant-os-3-safe.efi"} {
		if err := efivar.SetString(efivarsDir, "LoaderEntryOneShot", efivar.LoaderInfo, 0x7, ours); err != nil {
			t.Fatal(err)
		}
		if r := CheckHealth(m, nil, efivarsDir, sbDir, true); len(r) != 2 {
			t.Errorf("LoaderEntryOneShot %q made by vintner is reported: %q", ours, r)
		}
	}
	if err := efivar.SetString(efivarsDir, "LoaderEntryOneShot", efivar.LoaderInfo, 0x7, "debian-6.12.efi"); err != nil {
		t.Fatal(err)
	}

	// Under non-UEFI (isUEFI = false), efivars are not checked
	if rBIOS := CheckHealth(m, nil, efivarsDir, sbDir, false); len(rBIOS) != 0 {
		t.Errorf("non-UEFI: expected 0 reports, got %q", rBIOS)
	}

	// 2. Test foreign entries and unlisted files on boot storage
	emptyEFIVars := t.TempDir()
	vintnerEntry(t, emptyEFIVars, 0)
	v := &View{
		UEFIFiles: map[string]string{
			"vintner-os-1+3-0.efi":   "h1",      // tracked ordinary image
			"vintner-recovery-a.efi": "rec_sha", // tracked recovery image
			"debian-6.12.efi":        "h2",      // foreign entry in /EFI/Linux
			"vintner-recovery-b.efi": "h3",      // unlisted recovery slot
			"vintner-unknown.efi":    "h4",      // unlisted vintner file
		},
		LoaderEntries: []string{"aaa.conf", "other.conf"},
		SideLoaded: []string{
			"EFI/Linux/vintner-os-1.efi.extra.d/sysext.raw",
			"loader/addons/my.addon.efi",
			"loader/credentials/my.cred",
		},
		BIOSDirs: map[string]map[string]string{
			"os-1":       {"vmlinuz": "k", "initrd.img": "i", "slot.cfg": "c"}, // tracked ordinary image
			"recovery-a": {"vmlinuz": "k", "initrd.img": "i", "slot.cfg": "c"}, // tracked recovery slot
			"recovery-b": {"vmlinuz": "k", "initrd.img": "i", "slot.cfg": "c"}, // unlisted recovery slot
			"foreign-os": {"vmlinuz": "k"},                                     // foreign entry in /vintner/bios
		},
	}

	r = CheckHealth(m, v, emptyEFIVars, sbDir, true)
	wantReports := []string{
		"side-loaded file: EFI/Linux/vintner-os-1.efi.extra.d/sysext.raw",
		"side-loaded file: loader/addons/my.addon.efi",
		"side-loaded file: loader/credentials/my.cred",
		"foreign entry in /loader/entries: aaa.conf",
		"foreign entry in /loader/entries: other.conf",
		"foreign entry in /EFI/Linux: debian-6.12.efi",
		"unlisted vintner file: vintner-recovery-b.efi",
		"unlisted vintner file: vintner-unknown.efi",
		"foreign entry in /vintner/bios: foreign-os",
		"unlisted bios directory: recovery-b",
	}
	if !reflect.DeepEqual(r, wantReports) {
		t.Errorf("health reports = %q, want %q", r, wantReports)
	}
}

func TestScan_SelectionPolicyAndSideLoaded(t *testing.T) {
	dir := t.TempDir()

	// Setup directories
	efiDir := filepath.Join(dir, "EFI", "Linux")
	os.MkdirAll(filepath.Join(efiDir, "vintner-os-1.efi.extra.d"), 0755)
	os.MkdirAll(filepath.Join(dir, "loader", "entries", "test.conf.extra.d"), 0755)
	os.MkdirAll(filepath.Join(dir, "loader", "addons"), 0755)
	os.MkdirAll(filepath.Join(dir, "loader", "credentials"), 0755)
	os.MkdirAll(filepath.Join(dir, "vintner", "bios", "recovery-a"), 0755)

	// Create files
	os.WriteFile(filepath.Join(efiDir, "vintner-os-1.efi"), []byte("uki1"), 0644)
	os.WriteFile(filepath.Join(efiDir, "debian-6.12.efi"), []byte("uki2"), 0644)
	os.WriteFile(filepath.Join(efiDir, "vintner-os-1.efi.extra.d", "ext1.raw"), []byte("ext"), 0644)
	os.WriteFile(filepath.Join(dir, "loader", "entries", "test.conf"), []byte("conf"), 0644)
	os.WriteFile(filepath.Join(dir, "loader", "entries", "test.conf.extra.d", "ext2.raw"), []byte("ext2"), 0644)
	os.WriteFile(filepath.Join(dir, "loader", "addons", "foo.addon.efi"), []byte("addon"), 0644)
	os.WriteFile(filepath.Join(dir, "loader", "credentials", "foo.cred"), []byte("cred"), 0644)
	os.WriteFile(filepath.Join(dir, "vintner", "bios", "recovery-a", "vmlinuz"), []byte("k"), 0644)

	v, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(v.LoaderEntries) != 1 || v.LoaderEntries[0] != "test.conf" {
		t.Errorf("v.LoaderEntries = %v, want [test.conf]", v.LoaderEntries)
	}

	wantSideLoaded := []string{
		"EFI/Linux/vintner-os-1.efi.extra.d/ext1.raw",
		"loader/addons/foo.addon.efi",
		"loader/credentials/foo.cred",
		"loader/entries/test.conf.extra.d/ext2.raw",
	}
	if !reflect.DeepEqual(v.SideLoaded, wantSideLoaded) {
		t.Errorf("v.SideLoaded = %v, want %v", v.SideLoaded, wantSideLoaded)
	}

	if _, ok := v.UEFIFiles["debian-6.12.efi"]; !ok {
		t.Errorf("v.UEFIFiles missing debian-6.12.efi")
	}
	if _, ok := v.UEFIFiles["vintner-os-1.efi"]; !ok {
		t.Errorf("v.UEFIFiles missing vintner-os-1.efi")
	}
}

func TestAddESPReportsSideLoadedFilesWithPartitionPrefix(t *testing.T) {
	esp := t.TempDir()
	os.MkdirAll(filepath.Join(esp, "loader", "addons"), 0755)
	os.MkdirAll(filepath.Join(esp, "EFI", "Linux", "x.efi.extra.d"), 0755)
	os.WriteFile(filepath.Join(esp, "loader", "addons", "a.addon.efi"), nil, 0644)
	os.WriteFile(filepath.Join(esp, "EFI", "Linux", "x.efi.extra.d", "b.raw"), nil, 0644)
	v := &View{SideLoaded: []string{"loader/credentials/c"}}
	if err := v.AddESP(esp); err != nil {
		t.Fatal(err)
	}
	want := []string{"ESP:EFI/Linux/x.efi.extra.d/b.raw", "ESP:loader/addons/a.addon.efi", "loader/credentials/c"}
	if !reflect.DeepEqual(v.SideLoaded, want) {
		t.Errorf("SideLoaded = %v, want %v", v.SideLoaded, want)
	}
}

// vintnerEntry writes vintner's Boot#### entry (number n) and a BootOrder that starts with it.
func vintnerEntry(t *testing.T, dir string, _ uint16) {
	t.Helper()
	dp, err := efivar.DevicePath(efivar.PartitionRef{Number: 1, FirstLBA: 2048, Sectors: 4096, Signature: "11223344-5566-7788-99aa-bbccddeeff00"}, efivar.VintnerLoaderPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := efivar.SetFirstBootEntry(dir, efivar.VintnerDescription, dp); err != nil || !created {
		t.Fatalf("vintner entry: created=%v err=%v", created, err)
	}

}

func TestCheckHealth_BootOrder(t *testing.T) {
	m := &manifest.Manifest{Firmware: "uefi"}
	only := func(r []string) string {
		for _, s := range r {
			if strings.Contains(s, "NVRAM") || strings.Contains(s, "BootOrder") {
				return s
			}
		}
		return ""
	}

	// no efivarfs at all: nothing to report
	if got := only(CheckHealth(m, nil, filepath.Join(t.TempDir(), "none"), "", true)); got != "" {
		t.Errorf("without efivarfs: %q", got)
	}
	// BIOS installation: no NVRAM entry is expected
	bios := &manifest.Manifest{Firmware: "bios"}
	if got := only(CheckHealth(bios, nil, t.TempDir(), "", true)); got != "" {
		t.Errorf("bios manifest: %q", got)
	}

	// the entry is missing
	dir := t.TempDir()
	if got := only(CheckHealth(m, nil, dir, "", true)); !strings.Contains(got, "NVRAM boot entry missing") {
		t.Errorf("missing entry: %q", got)
	}

	// present and first: silent
	vintnerEntry(t, dir, 0)
	if got := only(CheckHealth(m, nil, dir, "", true)); got != "" {
		t.Errorf("healthy: %q", got)
	}

	// the firmware (or another system) moved something in front of it
	if err := efivar.Set(dir, "BootOrder", efivar.GlobalVariable, efivar.BootAttrs, []byte{7, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := only(CheckHealth(m, nil, dir, "", true)); !strings.Contains(got, "BootOrder does not start with the vintner entry Boot0000") || !strings.Contains(got, "0007,0000") {
		t.Errorf("reordered: %q", got)
	}
}
