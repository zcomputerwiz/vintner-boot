// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
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

const zeroHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// nextBootEnv is an installation with generations 3 (TRYING), 2 (GOOD) and 1
// (FAILED), a variant "safe" on 3 and 2, and recovery A and B.
func nextBootEnv(t *testing.T, firmware string) (bootStorage, efivarsDir string) {
	t.Helper()
	data, err := os.ReadFile("../../../schema/examples/manifest-uefi.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	uefi := firmware == "uefi"
	if !uefi {
		m.Firmware = "bios"
		for i := range m.Images {
			m.Images[i].UEFI = nil
			m.Images[i].BIOS = &manifest.BIOSFiles{Dir: fmt.Sprintf("/vintner/bios/os-%d", m.Images[i].Generation),
				Files: manifest.BIOSFilesMap{Vmlinuz: zeroHash, InitrdImg: zeroHash, SlotCfg: zeroHash}}
		}
		for i := range m.Recovery {
			m.Recovery[i].UEFI = nil
			m.Recovery[i].BIOS = &manifest.BIOSFiles{Dir: fmt.Sprintf("/vintner/bios/recovery-%s", m.Recovery[i].Slot),
				Files: manifest.BIOSFilesMap{Vmlinuz: zeroHash, InitrdImg: zeroHash, SlotCfg: zeroHash}}
		}
	}
	for i := range m.Images {
		g := m.Images[i].Generation
		if g == 1 {
			continue
		}
		v := manifest.Variant{ID: "safe", Title: "Safe Mode", Cmdline: "root=UUID=x ro vintner.variant=safe"}
		if uefi {
			v.UEFI = &manifest.UEFIFile{Entry: fmt.Sprintf("vintner_variant-os-%d-safe", g), SHA256: zeroHash, Size: 1}
		}
		m.Images[i].Variants = []manifest.Variant{v}
	}
	bootStorage, efivarsDir = setupTestEnv(t, uefi, &m)

	if uefi {
		efi := filepath.Join(bootStorage, "EFI", "Linux")
		for _, n := range []string{"vintner-os-3+2-1.efi", "vintner-os-2+3.efi", "vintner-os-1+0-3.efi",
			"vintner-recovery-a+3.efi", "vintner-recovery-b.efi",
			"vintner_variant-os-3-safe.efi", "vintner_variant-os-2-safe.efi"} {
			if err := os.WriteFile(filepath.Join(efi, n), []byte("uki"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for _, d := range []string{"os-3", "os-2", "os-1", "recovery-a", "recovery-b"} {
		if err := os.MkdirAll(filepath.Join(bootStorage, "vintner", "bios", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	envPath := filepath.Join(bootStorage, "grub", "grubenv")
	env, _ := grubenv.Read(envPath)
	env.Set("VINTNER_ORDER", "3 2 1")
	env.Set("VINTNER_T_3", "xx")
	env.Set("VINTNER_T_2", "xxx")
	env.Set("VINTNER_T_1", "0")
	if err := grubenv.Write(envPath, env); err != nil {
		t.Fatal(err)
	}
	return
}

func runNB(t *testing.T, bootStorage, efivarsDir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	args = append([]string{"--boot-storage", bootStorage, "--efivars", efivarsDir}, args...)
	code = runNextBoot(args, &out, &errb)
	return code, out.String(), errb.String()
}

func oneShot(t *testing.T, dir, name string) string {
	t.Helper()
	_, v, err := efivar.GetString(dir, name, efivar.LoaderInfo)
	if err != nil {
		return ""
	}
	return v
}

func biosNext(t *testing.T, bootStorage string) string {
	t.Helper()
	env, err := grubenv.Read(filepath.Join(bootStorage, "grub", "grubenv"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := env.Get("VINTNER_NEXT")
	return v
}

func TestNextBootTargets(t *testing.T) {
	cases := []struct {
		args       []string
		uefi, bios string // the one-shot value on each firmware
	}{
		{[]string{"recovery"}, "vintner-recovery-*", "recovery"},
		{[]string{"generation", "2"}, "vintner-os-2.efi", "2"},
		{[]string{"previous-good"}, "vintner-os-2.efi", "2"},
		// Generation 3 is the newest one with tries left, so it boots next.
		{[]string{"variant", "safe"}, "vintner_variant-os-3-safe.efi", "3:safe"},
	}
	for _, firmware := range []string{"uefi", "bios"} {
		for _, c := range cases {
			t.Run(firmware+"_"+strings.Join(c.args, "_"), func(t *testing.T) {
				bs, ev := nextBootEnv(t, firmware)
				code, out, errs := runNB(t, bs, ev, c.args...)
				if code != 0 {
					t.Fatalf("exit %d: %s%s", code, out, errs)
				}
				if firmware == "uefi" {
					if got := oneShot(t, ev, "LoaderEntryOneShot"); got != c.uefi {
						t.Errorf("LoaderEntryOneShot = %q, want %q", got, c.uefi)
					}
				} else if got := biosNext(t, bs); got != c.bios {
					t.Errorf("VINTNER_NEXT = %q, want %q", got, c.bios)
				}
			})
		}
	}
}

func TestNextBootMenu(t *testing.T) {
	bs, ev := nextBootEnv(t, "uefi")
	if code, out, errs := runNB(t, bs, ev, "menu", "--timeout", "10"); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	if got := oneShot(t, ev, "LoaderConfigTimeoutOneShot"); got != "10" {
		t.Errorf("LoaderConfigTimeoutOneShot = %q, want 10", got)
	}
	if got := oneShot(t, ev, "LoaderEntryOneShot"); got != "" {
		t.Errorf("the menu left an entry one-shot: %q", got)
	}

	bs, ev = nextBootEnv(t, "bios")
	if code, out, errs := runNB(t, bs, ev, "menu"); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	if got := biosNext(t, bs); got != "menu:30" {
		t.Errorf("VINTNER_NEXT = %q, want menu:30 (the default timeout)", got)
	}
}

// A later request replaces an earlier one, and clear removes vintner's own
// setting but never a foreign LoaderEntryOneShot.
func TestNextBootReplacesAndClears(t *testing.T) {
	bs, ev := nextBootEnv(t, "uefi")
	runNB(t, bs, ev, "recovery")
	runNB(t, bs, ev, "menu", "--timeout", "5")
	if got := oneShot(t, ev, "LoaderEntryOneShot"); got != "" {
		t.Errorf("menu after recovery kept the recovery one-shot: %q", got)
	}
	runNB(t, bs, ev, "generation", "2")
	if got := oneShot(t, ev, "LoaderConfigTimeoutOneShot"); got != "" {
		t.Errorf("generation after menu kept the menu timeout: %q", got)
	}
	if code, _, errs := runNB(t, bs, ev, "clear"); code != 0 {
		t.Fatalf("clear: exit %d: %s", code, errs)
	}
	if got := oneShot(t, ev, "LoaderEntryOneShot"); got != "" {
		t.Errorf("clear left %q", got)
	}
	if err := efivar.SetString(ev, "LoaderEntryOneShot", efivar.LoaderInfo, efivar.BootAttrs, "debian-6.12.efi"); err != nil {
		t.Fatal(err)
	}
	runNB(t, bs, ev, "clear")
	if got := oneShot(t, ev, "LoaderEntryOneShot"); got != "debian-6.12.efi" {
		t.Errorf("clear removed a foreign one-shot: %q", got)
	}

	bs, ev = nextBootEnv(t, "bios")
	runNB(t, bs, ev, "recovery")
	runNB(t, bs, ev, "clear")
	if got := biosNext(t, bs); got != "" {
		t.Errorf("clear left VINTNER_NEXT=%q", got)
	}
}

// The variant is one of the generation that would boot next: a bad newest
// generation is skipped.
func TestNextBootVariantSkipsBadGeneration(t *testing.T) {
	bs, ev := nextBootEnv(t, "uefi")
	efi := filepath.Join(bs, "EFI", "Linux")
	if err := os.Rename(filepath.Join(efi, "vintner-os-3+2-1.efi"), filepath.Join(efi, "vintner-os-3+0-3.efi")); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runNB(t, bs, ev, "variant", "safe"); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	if got := oneShot(t, ev, "LoaderEntryOneShot"); got != "vintner_variant-os-2-safe.efi" {
		t.Errorf("LoaderEntryOneShot = %q, want the variant of generation 2", got)
	}

	bs, ev = nextBootEnv(t, "bios")
	envPath := filepath.Join(bs, "grub", "grubenv")
	env, _ := grubenv.Read(envPath)
	env.Set("VINTNER_T_3", "0")
	if err := grubenv.Write(envPath, env); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runNB(t, bs, ev, "variant", "safe"); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	if got := biosNext(t, bs); got != "2:safe" {
		t.Errorf("VINTNER_NEXT = %q, want 2:safe", got)
	}
}

func TestNextBootRefusals(t *testing.T) {
	for _, firmware := range []string{"uefi", "bios"} {
		for _, c := range []struct {
			args []string
			exit int
			code string
		}{
			{[]string{"generation", "9"}, 3, "no-such-generation"},
			{[]string{"variant", "nosuch"}, 3, "no-such-variant"},
			{[]string{"generation", "x"}, 2, "usage"},
			{[]string{"generation", "02"}, 2, "usage"},
			{[]string{"generation"}, 2, "usage"},
			{[]string{"variant", "Bad-ID"}, 2, "usage"},
			{[]string{"recovery", "extra"}, 2, "usage"},
			{[]string{"menu", "--timeout", "0"}, 2, "usage"},
			{[]string{"menu", "--timeout", "99999"}, 2, "usage"},
			{[]string{"reboot"}, 2, "usage"},
			{nil, 2, "usage"},
		} {
			t.Run(firmware+"_"+strings.Join(c.args, "_"), func(t *testing.T) {
				bs, ev := nextBootEnv(t, firmware)
				code, out, _ := runNB(t, bs, ev, append([]string{"--json"}, c.args...)...)
				if code != c.exit {
					t.Errorf("exit %d, want %d", code, c.exit)
				}
				var r nextBootResult
				if err := json.Unmarshal([]byte(out), &r); err != nil {
					t.Fatalf("--json printed %q: %v", out, err)
				}
				if r.OK || r.Code != c.code || r.Error == "" {
					t.Errorf("result %+v, want code %q and a message", r, c.code)
				}
				// A refused request sets nothing.
				if oneShot(t, ev, "LoaderEntryOneShot") != "" || (firmware == "bios" && biosNext(t, bs) != "") {
					t.Error("a refused request still set a one-shot")
				}
			})
		}
	}
}

// With nothing older than the newest generation that is GOOD, there is no
// Last Known Good Configuration.
func TestNextBootNoPreviousGood(t *testing.T) {
	bs, ev := nextBootEnv(t, "uefi")
	sDir := filepath.Join(bs, "vintner", "state")
	m, err := manifest.Load(sDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Images {
		if m.Images[i].Generation == 2 {
			m.Images[i].State = "FAILED"
		}
	}
	if err := manifest.Save(sDir, m); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runNB(t, bs, ev, "--json", "previous-good")
	var r nextBootResult
	_ = json.Unmarshal([]byte(out), &r)
	if code != 3 || r.Code != "no-previous-good" {
		t.Errorf("exit %d, %+v; want exit 3 no-previous-good", code, r)
	}
}

func TestNextBootJSON(t *testing.T) {
	bs, ev := nextBootEnv(t, "uefi")
	code, out, errs := runNB(t, bs, ev, "--json", "variant", "safe")
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	var r nextBootResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if !r.OK || r.Target != "variant" || r.Firmware != "uefi" || r.Variant != "safe" || r.Generation == nil || *r.Generation != 3 {
		t.Errorf("result %+v", r)
	}
	// Flags may follow the target.
	if code, out, errs := runNB(t, bs, ev, "menu", "--json", "--timeout", "7"); code != 0 || !strings.Contains(out, `"timeout":7`) {
		t.Errorf("menu --json --timeout 7: exit %d: %s%s", code, out, errs)
	}
}
