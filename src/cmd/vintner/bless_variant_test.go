// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

// treeHash hashes every file's relative name and bytes under dir.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s %v\n", rel, info.IsDir())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// A boot of a variant changes nothing (WO-0010 D-D, 4.3): not the file names,
// not the grubenv, not the manifest. On BIOS that includes the VINTNER_BOOTED
// that a failed ordinary boot left just before, which must stay so that the next
// ordinary boot counts the failure.
func TestBlessVariantBootChangesNothing(t *testing.T) {
	data, err := os.ReadFile("../../../schema/examples/manifest-uefi.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, firmware := range []string{"uefi", "bios"} {
		for _, imgState := range []string{"GOOD", "TRYING", "FAILED"} {
			t.Run(firmware+"_"+imgState, func(t *testing.T) {
				var m manifest.Manifest
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				const zero = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
				if firmware == "bios" {
					m.Firmware = "bios"
					for i := range m.Images {
						m.Images[i].UEFI = nil
						m.Images[i].BIOS = &manifest.BIOSFiles{Dir: fmt.Sprintf("/vintner/bios/os-%d", m.Images[i].Generation),
							Files: manifest.BIOSFilesMap{Vmlinuz: zero, InitrdImg: zero, SlotCfg: zero}}
					}
					for i := range m.Recovery {
						m.Recovery[i].UEFI = nil
						m.Recovery[i].BIOS = &manifest.BIOSFiles{Dir: fmt.Sprintf("/vintner/bios/recovery-%s", m.Recovery[i].Slot),
							Files: manifest.BIOSFilesMap{Vmlinuz: zero, InitrdImg: zero, SlotCfg: zero}}
					}
				}
				for i := range m.Images {
					if m.Images[i].Generation == 3 {
						m.Images[i].State = imgState
					}
				}
				bootStorage, efivarsDir := setupTestEnv(t, firmware == "uefi", &m)

				if firmware == "uefi" {
					// The variable of an earlier counted boot, as a stale one would be.
					efiDir := filepath.Join(bootStorage, "EFI", "Linux")
					name := "vintner-os-3+2-1.efi"
					if err := os.WriteFile(filepath.Join(efiDir, name), []byte("uki"), 0o644); err != nil {
						t.Fatal(err)
					}
					if err := efivar.SetString(efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo, 0, `\EFI\Linux\`+name); err != nil {
						t.Fatal(err)
					}
				} else {
					envPath := filepath.Join(bootStorage, "grub", "grubenv")
					env, _ := grubenv.Read(envPath)
					env.Set("VINTNER_BOOTED", "3")
					env.Set("VINTNER_T_3", "xx")
					env.Set("VINTNER_ORDER", "3 2 1")
					if err := grubenv.Write(envPath, env); err != nil {
						t.Fatal(err)
					}
				}

				before := treeHash(t, filepath.Dir(bootStorage))
				cmdline := filepath.Join(t.TempDir(), "cmdline")
				if err := os.WriteFile(cmdline, []byte("root=UUID=x ro wexmode=safe vintner.variant=safe\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				oldOsExit := osExit
				defer func() { osExit = oldOsExit }()
				exited := -1
				osExit = func(code int) { exited = code }
				cmdBless([]string{"--boot-storage", bootStorage, "--efivars", efivarsDir, "--cmdline", cmdline})
				if exited != 0 {
					t.Errorf("exit status %d, want 0", exited)
				}
				if after := treeHash(t, filepath.Dir(bootStorage)); after != before {
					t.Error("a variant boot changed boot storage, the manifest or the variables")
				}
			})
		}
	}
}
