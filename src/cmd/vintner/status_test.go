// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/status"
)

// runStatusJSON runs vintner status --json against a prepared installation.
func runStatusJSON(t *testing.T, bs, ev, cmdline string) status.Report {
	t.Helper()
	cf := filepath.Join(t.TempDir(), "cmdline")
	if err := os.WriteFile(cf, []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runStatus([]string{"--json", "--boot-storage", bs, "--efivars", ev, "--cmdline", cf,
		"--sig-enforce", filepath.Join(t.TempDir(), "none")}, &out, &errb)
	if code != statusOK {
		t.Fatalf("exit %d: %s%s", code, out.String(), errb.String())
	}
	var r status.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	return r
}

func ip(n int) *int { return &n }

func eqp(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func setLoader(t *testing.T, ev, name, val string) {
	t.Helper()
	if err := efivar.SetString(ev, name, efivar.LoaderInfo, 0, val); err != nil {
		t.Fatal(err)
	}
}

// What the product shows early in boot: which image booted and how many boots
// of it failed before this one (WO-0010 4.5). The fixture's generations are 3
// (TRYING, two tries left), 2 (GOOD) and 1 (FAILED, no tries), 3 tries each.
func TestStatusBootedUEFI(t *testing.T) {
	cases := []struct {
		name      string
		selected  string // LoaderEntrySelected
		countPath string // LoaderBootCountPath, "" for none
		kind      string
		gen       *int
		variant   string
		slot      string
		failed    *int
	}{
		// The loader counted this boot: +2-1 is the first try, so none failed.
		{"first try", "vintner-os-3.efi", `\EFI\Linux\vintner-os-3+2-1.efi`, "generation", ip(3), "", "", ip(0)},
		{"third try", "vintner-os-1.efi", `\EFI\Linux\vintner-os-1+0-3.efi`, "generation", ip(1), "", "", ip(2)},
		{"recovery A", "vintner-recovery-a.efi", `\EFI\Linux\vintner-recovery-a+2-1.efi`, "recovery", nil, "", "a", ip(0)},
		// Recovery B has no counter.
		{"recovery B", "vintner-recovery-b.efi", "", "recovery", nil, "", "b", nil},
		// A variant counts nothing: the failures are the ordinary image's, as its name records them.
		{"variant", "vintner_variant-os-3-safe.efi", "", "variant", ip(3), "safe", "", ip(1)},
		{"foreign entry", "debian-6.12.efi", "", "unknown", nil, "", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bs, ev := nextBootEnv(t, "uefi")
			setLoader(t, ev, "LoaderEntrySelected", c.selected)
			if c.countPath != "" {
				setLoader(t, ev, "LoaderBootCountPath", c.countPath)
			}
			r := runStatusJSON(t, bs, ev, "root=UUID=x ro")
			b := r.Booted
			if b.Kind != c.kind || !eqp(b.Generation, c.gen) || b.Variant != c.variant || b.Slot != c.slot || !eqp(b.FailedAttempts, c.failed) {
				t.Errorf("booted %+v, want kind %s gen %v variant %q slot %q failed %v", b, c.kind, c.gen, c.variant, c.slot, c.failed)
			}
			if r.BootedFirmware != "uefi" {
				t.Errorf("booted_firmware %q", r.BootedFirmware)
			}
		})
	}
}

// The failed-attempts count must survive the bless service re-arming the file:
// LoaderBootCountPath is the name as the loader booted it.
func TestStatusFailedAttemptsAfterRearmUEFI(t *testing.T) {
	bs, ev := nextBootEnv(t, "uefi")
	setLoader(t, ev, "LoaderEntrySelected", "vintner-os-3.efi")
	setLoader(t, ev, "LoaderBootCountPath", `\EFI\Linux\vintner-os-3+1-2.efi`)
	efi := filepath.Join(bs, "EFI", "Linux")
	if err := os.Rename(filepath.Join(efi, "vintner-os-3+2-1.efi"), filepath.Join(efi, "vintner-os-3+3-0.efi")); err != nil {
		t.Fatal(err)
	}
	r := runStatusJSON(t, bs, ev, "")
	if !eqp(r.Booted.FailedAttempts, ip(1)) {
		t.Errorf("failed_attempts %v, want 1", r.Booted.FailedAttempts)
	}
}

func TestStatusBootedBIOS(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		booted  string // VINTNER_BOOTED
		kind    string
		gen     *int
		variant string
		slot    string
		failed  *int
	}{
		// Counter "xx" is two of three tries left: this boot is the first try, still unblessed.
		{"unblessed", "ro vintner.image=os-3", "3", "generation", ip(3), "", "", ip(0)},
		// Blessed: the counter is full again and VINTNER_BOOTED is gone. Fixture counter for 2 is xxx.
		{"blessed", "ro vintner.image=os-2", "", "generation", ip(2), "", "", ip(0)},
		// The last try of a generation that was bad before (counter 0).
		{"last try", "ro vintner.image=os-1", "1", "generation", ip(1), "", "", ip(2)},
		// A loader payload from before the token still names the image until bless.
		{"no token", "ro", "3", "generation", ip(3), "", "", ip(0)},
		{"no token, blessed", "ro", "", "unknown", nil, "", "", nil},
		{"variant", "ro vintner.image=os-3 vintner.variant=safe", "", "variant", ip(3), "safe", "", ip(1)},
		{"recovery A", "ro vintner.image=recovery-a", "RA", "recovery", nil, "", "a", ip(0)},
		{"recovery B", "ro vintner.image=recovery-b", "", "recovery", nil, "", "b", nil},
		{"garbage", "ro vintner.image=os-x", "", "unknown", nil, "", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bs, ev := nextBootEnv(t, "bios")
			envPath := filepath.Join(bs, "grub", "grubenv")
			env, err := grubenv.Read(envPath)
			if err != nil {
				t.Fatal(err)
			}
			if c.booted != "" {
				env.Set("VINTNER_BOOTED", c.booted)
			}
			if c.booted == "RA" {
				env.Set("VINTNER_T_RA", "xx")
			}
			if err := grubenv.Write(envPath, env); err != nil {
				t.Fatal(err)
			}
			r := runStatusJSON(t, bs, ev, c.cmdline)
			b := r.Booted
			if b.Kind != c.kind || !eqp(b.Generation, c.gen) || b.Variant != c.variant || b.Slot != c.slot || !eqp(b.FailedAttempts, c.failed) {
				t.Errorf("booted %+v, want kind %s gen %v variant %q slot %q failed %v", b, c.kind, c.gen, c.variant, c.slot, c.failed)
			}
			if r.BootedFirmware != "bios" || r.SecureBoot != nil {
				t.Errorf("firmware %q secure_boot %v", r.BootedFirmware, r.SecureBoot)
			}
		})
	}
}

func TestStatusGenerations(t *testing.T) {
	for _, fw := range []string{"uefi", "bios"} {
		t.Run(fw, func(t *testing.T) {
			bs, ev := nextBootEnv(t, fw)
			if fw == "uefi" {
				setLoader(t, ev, "LoaderEntrySelected", "vintner-os-3.efi")
				setLoader(t, ev, "LoaderBootCountPath", `\EFI\Linux\vintner-os-3+2-1.efi`)
			}
			r := runStatusJSON(t, bs, ev, "ro vintner.image=os-3")
			if r.Format != status.Format || r.Firmware != fw {
				t.Errorf("format %d firmware %q", r.Format, r.Firmware)
			}
			var gens []int
			for _, g := range r.Generations {
				gens = append(gens, g.Generation)
			}
			if len(gens) != 3 || gens[0] != 3 || gens[1] != 2 || gens[2] != 1 {
				t.Fatalf("generations %v, want newest first 3 2 1", gens)
			}
			g3, g2, g1 := r.Generations[0], r.Generations[1], r.Generations[2]
			if g3.State != "TRYING" || g2.State != "GOOD" || g1.State != "FAILED" {
				t.Errorf("states %s %s %s", g3.State, g2.State, g1.State)
			}
			if !eqp(g3.TriesLeft, ip(2)) || !eqp(g2.TriesLeft, ip(3)) || !eqp(g1.TriesLeft, ip(0)) {
				t.Errorf("tries_left %v %v %v, want 2 3 0", g3.TriesLeft, g2.TriesLeft, g1.TriesLeft)
			}
			if len(g3.Variants) != 1 || g3.Variants[0].ID != "safe" || g3.Variants[0].Title != "Safe Mode" || len(g1.Variants) != 0 {
				t.Errorf("variants %+v / %+v", g3.Variants, g1.Variants)
			}
			if len(g3.Date) != 10 || g3.ABI == "" {
				t.Errorf("date %q abi %q", g3.Date, g3.ABI)
			}
			// Last Known Good: the newest GOOD is 2, and 3 is booted, so Last Known
			// Good is not what booted.
			if !eqp(r.NewestGood, ip(2)) || !eqp(r.PreviousGood, ip(2)) {
				t.Errorf("newest_good %v previous_good %v, want 2 and 2", r.NewestGood, r.PreviousGood)
			}
			if r.Booted.IsNewestGood == nil || *r.Booted.IsNewestGood {
				t.Errorf("is_newest_good %v, want false", r.Booted.IsNewestGood)
			}
			if len(r.Recovery) != 2 {
				t.Errorf("recovery %+v", r.Recovery)
			}
		})
	}
}

func TestStatusModules(t *testing.T) {
	bs, ev := nextBootEnv(t, "bios")
	for _, c := range []struct{ value, want string }{{"N\n", "allowed"}, {"Y\n", "blocked"}, {"", "unknown"}} {
		p := filepath.Join(t.TempDir(), "sig_enforce")
		if c.value != "" {
			if err := os.WriteFile(p, []byte(c.value), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var out, errb bytes.Buffer
		if code := runStatus([]string{"--json", "--boot-storage", bs, "--efivars", ev, "--cmdline", "/nonexistent", "--sig-enforce", p}, &out, &errb); code != statusOK {
			t.Fatal(code, errb.String())
		}
		var r status.Report
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Modules.OutOfTree != c.want {
			t.Errorf("sig_enforce %q: out_of_tree %q, want %s", c.value, r.Modules.OutOfTree, c.want)
		}
	}
	// Secure Boot on blocks them whatever the kernel parameter says.
	bs, ev = nextBootEnv(t, "uefi")
	if err := efivar.Set(ev, "SecureBoot", efivar.GlobalVariable, efivar.BootAttrs, []byte{1}); err != nil {
		t.Fatal(err)
	}
	r := runStatusJSON(t, bs, ev, "")
	if r.SecureBoot == nil || !*r.SecureBoot || r.Modules.OutOfTree != "blocked" {
		t.Errorf("secure_boot %v out_of_tree %q", r.SecureBoot, r.Modules.OutOfTree)
	}
}

func TestStatusHealthAndText(t *testing.T) {
	bs, ev := nextBootEnv(t, "uefi")
	setLoader(t, ev, "LoaderEntrySelected", "vintner-os-3.efi")
	setLoader(t, ev, "LoaderBootCountPath", `\EFI\Linux\vintner-os-3+2-1.efi`)
	// A finding does not change the exit code: the report is still made.
	setLoader(t, ev, "LoaderEntryDefault", "debian-6.12.efi")
	r := runStatusJSON(t, bs, ev, "")
	if r.Healthy || len(r.Health) == 0 || !strings.Contains(strings.Join(r.Health, "\n"), "LoaderEntryDefault") {
		t.Errorf("health %v healthy %v", r.Health, r.Healthy)
	}
	var out, errb bytes.Buffer
	if code := runStatus([]string{"--boot-storage", bs, "--efivars", ev, "--cmdline", "/nonexistent"}, &out, &errb); code != statusOK {
		t.Fatal(code, errb.String())
	}
	for _, want := range []string{"generation 3: TRYING", "booted: generation 3, 0 earlier boots failed", "health: LoaderEntryDefault"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, out.String())
		}
	}
}

func TestStatusRefusals(t *testing.T) {
	var out, errb bytes.Buffer
	// No manifest: not a vintner installation.
	empty := t.TempDir()
	if code := runStatus([]string{"--json", "--boot-storage", empty, "--efivars", filepath.Join(empty, "none")}, &out, &errb); code != statusNone {
		t.Errorf("exit %d, want %d", code, statusNone)
	}
	var e statusError
	if err := json.Unmarshal(out.Bytes(), &e); err != nil || e.OK || e.Code != "not-vintner" {
		t.Errorf("error output %q: %+v %v", out.String(), e, err)
	}
	out.Reset()
	if code := runStatus([]string{"extra"}, &out, &errb); code != statusUsage {
		t.Errorf("exit %d, want %d", code, statusUsage)
	}
	if code := runStatus([]string{"--nonsense"}, &out, &errb); code != statusUsage {
		t.Errorf("exit %d, want %d", code, statusUsage)
	}
}
