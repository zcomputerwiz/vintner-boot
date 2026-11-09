// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
)

func TestRecoveryExtraCmdlineReachesBothFirmwares(t *testing.T) {
	e := newEnv(t, "both")
	e.opts.RecoveryExtraA = " console=ttyS0 vintner.recovery_test=1 "
	e.opts.RecoveryExtraB = "vintner.test_slot=b"
	if _, err := Deploy(context.Background(), e.opts); err != nil {
		t.Fatal(err)
	}
	byslot := map[string]string{}
	for _, r := range e.uki.reqs {
		for _, s := range []string{"a", "b"} {
			if strings.Contains(r.Cmdline, "vintner.recovery="+s+" ") {
				byslot[s] = r.Cmdline
			}
		}
	}
	if !strings.HasSuffix(byslot["a"], " console=ttyS0 vintner.recovery_test=1") || strings.Contains(byslot["a"], "test_slot") {
		t.Errorf("recovery A UKI command line %q", byslot["a"])
	}
	if !strings.HasSuffix(byslot["b"], " vintner.test_slot=b") || strings.Contains(byslot["b"], "recovery_test") {
		t.Errorf("recovery B UKI command line %q", byslot["b"])
	}
	cfgA := string(read(t, filepath.Join(e.bs, "vintner/bios/recovery-a/slot.cfg")))
	cfgB := string(read(t, filepath.Join(e.bs, "vintner/bios/recovery-b/slot.cfg")))
	if !strings.Contains(cfgA, "vintner.recovery_test=1") || strings.Contains(cfgA, "test_slot") {
		t.Errorf("recovery A slot.cfg %q", cfgA)
	}
	if !strings.Contains(cfgB, "vintner.test_slot=b") || strings.Contains(cfgB, "recovery_test") {
		t.Errorf("recovery B slot.cfg %q", cfgB)
	}
}

func TestNoEFIVarsWritesNoUEFIVariable(t *testing.T) {
	e := newEnv(t, "uefi")
	vars := t.TempDir() // a directory that exists, as a real efivarfs would
	e.opts.EFIVarDir = vars
	e.opts.NoEFIVars = true
	if _, err := Deploy(context.Background(), e.opts); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(vars); len(ents) != 0 {
		t.Errorf("variables were written: %v", ents)
	}
	if o, _ := efivar.BootOrder(vars); len(o) != 0 {
		t.Errorf("BootOrder %v", o)
	}
}
