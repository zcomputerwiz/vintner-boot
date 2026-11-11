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
	"github.com/zcomputerwiz/vintner-boot/src/internal/menutimeout"
)

const shippedLoaderConf = "# systemd-boot configuration\ndefault vintner-*\ntimeout 0\neditor no\nauto-entries no\n"

// menuTimeoutEnv is an installation with the ESP and the grubenv a command needs.
// firmware is the manifest's: uefi, bios or both.
func menuTimeoutEnv(t *testing.T, firmware string) (args []string, esp, grubenvPath, efivars string) {
	t.Helper()
	fixture := "uefi"
	if firmware == "bios" {
		fixture = "bios"
	}
	bootStorage, efivars := nextBootEnv(t, fixture)
	stateDir := filepath.Join(bootStorage, "vintner", "state")
	if firmware == "both" {
		m, err := manifest.Load(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		m.Firmware = "both"
		// Every image and recovery slot needs the BIOS files too.
		bios := func(dir string) *manifest.BIOSFiles {
			return &manifest.BIOSFiles{Dir: dir, Files: manifest.BIOSFilesMap{Vmlinuz: zeroHash, InitrdImg: zeroHash, SlotCfg: zeroHash}}
		}
		for i := range m.Images {
			m.Images[i].BIOS = bios(fmt.Sprintf("/vintner/bios/os-%d", m.Images[i].Generation))
		}
		for i := range m.Recovery {
			m.Recovery[i].BIOS = bios(fmt.Sprintf("/vintner/bios/recovery-%s", m.Recovery[i].Slot))
		}
		m.Seq++
		if err := manifest.Save(stateDir, m); err != nil {
			t.Fatal(err)
		}
	}
	esp = filepath.Join(t.TempDir(), "esp")
	if err := os.MkdirAll(filepath.Join(esp, "loader"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(esp, "loader", "loader.conf"), []byte(shippedLoaderConf), 0o644); err != nil {
		t.Fatal(err)
	}
	grubenvPath = filepath.Join(bootStorage, "grub", "grubenv")
	if _, err := os.Stat(grubenvPath); err != nil {
		if err := os.MkdirAll(filepath.Dir(grubenvPath), 0o755); err != nil {
			t.Fatal(err)
		}
		e := &grubenv.Env{}
		e.Set("VINTNER_ORDER", "3 2 1")
		if err := grubenv.Create(grubenvPath, e); err != nil {
			t.Fatal(err)
		}
	}
	args = []string{"--boot-storage", bootStorage, "--state-dir", stateDir, "--esp", esp, "--efivars", efivars}
	return args, esp, grubenvPath, efivars
}

func runMenuTimeoutJSON(t *testing.T, args ...string) (int, menuTimeoutResult) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runMenuTimeout(append([]string{"--json"}, args...), &out, &errb)
	var r menuTimeoutResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("not one JSON object (%v): %q (stderr %q)", err, out.String(), errb.String())
	}
	return code, r
}

func TestMenuTimeoutSetAndShow(t *testing.T) {
	base, esp, genv, _ := menuTimeoutEnv(t, "both")
	loader := filepath.Join(esp, "loader", "loader.conf")

	code, r := runMenuTimeoutJSON(t, base...)
	if code != menuTimeoutOK || r.UEFI == nil || *r.UEFI != 0 || r.BIOS == nil || *r.BIOS != 0 || r.Timeout == nil || *r.Timeout != 0 || r.Firmware != "both" {
		t.Fatalf("show: %d %+v", code, r)
	}

	code, r = runMenuTimeoutJSON(t, append([]string{"5"}, base...)...)
	if code != menuTimeoutOK || !r.OK || *r.UEFI != 5 || *r.BIOS != 5 || *r.Timeout != 5 {
		t.Fatalf("set 5: %d %+v", code, r)
	}
	// loader.conf changed in its timeout line only.
	want := strings.Replace(shippedLoaderConf, "timeout 0\n", "timeout 5\n", 1)
	if got, _ := os.ReadFile(loader); string(got) != want {
		t.Errorf("loader.conf:\n%s\nwant\n%s", got, want)
	}
	if n, err := menutimeout.ReadBIOS(genv); err != nil || n != 5 {
		t.Errorf("grubenv: %d, %v", n, err)
	}
	// Other grubenv variables are as they were.
	e, _ := grubenv.Read(genv)
	if v, _ := e.Get("VINTNER_ORDER"); v == "" {
		t.Error("VINTNER_ORDER is gone from the grubenv")
	}

	// Show, with no lock and no writing.
	code, r = runMenuTimeoutJSON(t, base...)
	if code != menuTimeoutOK || *r.Timeout != 5 {
		t.Errorf("show after set: %d %+v", code, r)
	}

	// 0 hides the menu again: the line is back, the variable is gone.
	code, r = runMenuTimeoutJSON(t, append(base, "0")...) // the number may come after the flags
	if code != menuTimeoutOK || *r.Timeout != 0 {
		t.Fatalf("set 0: %d %+v", code, r)
	}
	if got, _ := os.ReadFile(loader); string(got) != shippedLoaderConf {
		t.Errorf("loader.conf after 0:\n%s", got)
	}
	if e, _ := grubenv.Read(genv); true {
		if _, ok := e.Get(menutimeout.GrubVar); ok {
			t.Error("0 left VINTNER_TIMEOUT in the grubenv")
		}
	}
}

// Each firmware writes its own store only.
func TestMenuTimeoutByFirmware(t *testing.T) {
	base, esp, genv, _ := menuTimeoutEnv(t, "uefi")
	code, r := runMenuTimeoutJSON(t, append([]string{"9"}, base...)...)
	if code != menuTimeoutOK || r.UEFI == nil || *r.UEFI != 9 || r.BIOS != nil || r.Firmware != "uefi" {
		t.Fatalf("uefi: %d %+v", code, r)
	}
	if n, _ := menutimeout.ReadBIOS(genv); n != 0 {
		t.Errorf("a UEFI installation wrote the grubenv: %d", n)
	}
	_ = esp

	base, esp, genv, _ = menuTimeoutEnv(t, "bios")
	code, r = runMenuTimeoutJSON(t, append([]string{"9"}, base...)...)
	if code != menuTimeoutOK || r.BIOS == nil || *r.BIOS != 9 || r.UEFI != nil || r.Firmware != "bios" {
		t.Fatalf("bios: %d %+v", code, r)
	}
	if got, _ := os.ReadFile(filepath.Join(esp, "loader", "loader.conf")); string(got) != shippedLoaderConf {
		t.Errorf("a BIOS installation wrote loader.conf:\n%s", got)
	}
	if n, _ := menutimeout.ReadBIOS(genv); n != 9 {
		t.Errorf("grubenv %d", n)
	}
}

// A LoaderConfigTimeout from the menu keys beats loader.conf, so setting the
// timeout removes it, and says so.
func TestMenuTimeoutClearsOverride(t *testing.T) {
	base, _, _, efivars := menuTimeoutEnv(t, "uefi")
	if err := efivar.SetString(efivars, "LoaderConfigTimeout", efivar.LoaderInfo, efivar.BootAttrs, "30"); err != nil {
		t.Fatal(err)
	}
	// Showing leaves it alone.
	runMenuTimeoutJSON(t, base...)
	if _, _, err := efivar.GetString(efivars, "LoaderConfigTimeout", efivar.LoaderInfo); err != nil {
		t.Fatalf("show removed the variable: %v", err)
	}
	code, r := runMenuTimeoutJSON(t, append([]string{"4"}, base...)...)
	if code != menuTimeoutOK || !r.ClearedOverride {
		t.Errorf("set: %d %+v", code, r)
	}
	if _, _, err := efivar.GetString(efivars, "LoaderConfigTimeout", efivar.LoaderInfo); err == nil {
		t.Error("the stale LoaderConfigTimeout is still set")
	}
}

func TestMenuTimeoutRefusals(t *testing.T) {
	base, esp, _, _ := menuTimeoutEnv(t, "both")
	for _, bad := range []string{"-1", "3601", "x", "1.5", ""} {
		code, r := runMenuTimeoutJSON(t, append(append([]string{}, base...), "--", bad)...)
		if code != menuTimeoutUsage || r.OK || r.Code != "usage" {
			t.Errorf("%q: %d %+v", bad, code, r)
		}
	}
	if code, r := runMenuTimeoutJSON(t, append([]string{"1", "2"}, base...)...); code != menuTimeoutUsage || r.OK {
		t.Errorf("two numbers: %d %+v", code, r)
	}
	// Nothing was written.
	if got, _ := os.ReadFile(filepath.Join(esp, "loader", "loader.conf")); string(got) != shippedLoaderConf {
		t.Errorf("a refused value changed loader.conf:\n%s", got)
	}

	// No loader.conf: it is not created (systemd-boot would lose "default vintner-*").
	os.Remove(filepath.Join(esp, "loader", "loader.conf"))
	code, r := runMenuTimeoutJSON(t, append([]string{"3"}, base...)...)
	if code != menuTimeoutFailed || r.OK || r.Code != "failed" {
		t.Errorf("no loader.conf: %d %+v", code, r)
	}
	if _, err := os.Stat(filepath.Join(esp, "loader", "loader.conf")); err == nil {
		t.Error("loader.conf was created")
	}

	// Not a vintner installation.
	var out, errb bytes.Buffer
	if code := runMenuTimeout([]string{"--boot-storage", t.TempDir(), "--esp", esp, "--json"}, &out, &errb); code != menuTimeoutNone {
		t.Errorf("no manifest: %d %s", code, out.String())
	}
}
