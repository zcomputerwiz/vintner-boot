// SPDX-License-Identifier: GPL-2.0-or-later

// Package status builds the machine-readable status of an installation (WO-0010
// 4.5, D-F): the generations, the image that booted, how many boots of it failed
// before this one, health, tier and firmware. It only reads. Products build
// their screens on it ("Windows did not start successfully", Last Known Good
// Configuration), so the JSON field names are a contract (docs/status.md).
package status

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/nextboot"
	"github.com/zcomputerwiz/vintner-boot/src/internal/state"
	"github.com/zcomputerwiz/vintner-boot/src/internal/variants"
)

// Format is the version of the JSON layout. Fields are only ever added within a
// format.
const Format = 1

// ImageToken is the command-line token the BIOS loader adds to every image it
// boots: "os-<gen>", "recovery-a" or "recovery-b". The kernel then tells which
// image booted even after the bless service has cleared VINTNER_BOOTED.
const ImageToken = "vintner.image="

// Kinds of boot.
const (
	KindGeneration = "generation"
	KindVariant    = "variant"
	KindRecovery   = "recovery"
	KindUnknown    = "unknown"
)

// Report is the status.
type Report struct {
	Format         int     `json:"format"`
	Firmware       string  `json:"firmware"`        // what the installation supports: uefi, bios or both
	BootedFirmware string  `json:"booted_firmware"` // what this boot came through: uefi or bios
	SecureBootTier string  `json:"secure_boot_tier"`
	SecureBoot     *bool   `json:"secure_boot"` // enabled now; null where there is no such variable (BIOS)
	Modules        Modules `json:"modules"`
	Tries          int     `json:"tries"`

	Generations  []Generation `json:"generations"`
	NewestGood   *int         `json:"newest_good"`   // newest GOOD generation
	PreviousGood *int         `json:"previous_good"` // what "next-boot previous-good" would boot
	Recovery     []Recovery   `json:"recovery"`

	Booted Booted `json:"booted"`

	Health  []string `json:"health"`
	Healthy bool     `json:"healthy"`
}

// Modules says whether out-of-tree kernel modules (DKMS) can load.
type Modules struct {
	SigEnforce *bool  `json:"sig_enforce"` // the kernel refuses unsigned modules; null if unreadable
	OutOfTree  string `json:"out_of_tree"` // "allowed", "blocked" or "unknown"
	Reason     string `json:"reason,omitempty"`
}

// Generation is one recorded image.
type Generation struct {
	Generation int       `json:"generation"`
	State      string    `json:"state"`
	Stale      bool      `json:"stale"`
	Date       string    `json:"date"`    // publish day, UTC, YYYY-MM-DD
	Created    string    `json:"created"` // RFC 3339
	ABI        string    `json:"abi"`
	Package    string    `json:"package,omitempty"`
	Version    string    `json:"version,omitempty"`
	TriesLeft  *int      `json:"tries_left"` // from the boot counter; null if there is none or it cannot be read
	FirstGood  string    `json:"first_good,omitempty"`
	LastGood   string    `json:"last_good,omitempty"`
	Variants   []Variant `json:"variants"`
}

// Variant is a declared extra entry of a generation.
type Variant struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Recovery is a recovery image.
type Recovery struct {
	Slot    string `json:"slot"`
	Version string `json:"version"`
}

// Booted describes the image this boot started.
type Booted struct {
	// Kind is generation, variant, recovery, or unknown when the boot did not
	// come from a vintner entry (or the firmware does not say which).
	Kind       string `json:"kind"`
	Generation *int   `json:"generation"`
	Variant    string `json:"variant,omitempty"`
	Slot       string `json:"slot,omitempty"` // a or b, for recovery
	// FailedAttempts is how many boots of this image failed since it last booted
	// good, not counting this boot. The loader has already counted this boot
	// when it started it, and "vintner bless" re-arms the counter only after
	// boot-complete.target, so the number is right early in boot, before the
	// display manager starts. For a variant it is the count of its generation's
	// ordinary image (a variant counts nothing itself). Null where there is no
	// counter to read: recovery B, or an image the loader did not count.
	FailedAttempts *int `json:"failed_attempts"`
	// Entry is the loader's name for the entry (UEFI: LoaderEntrySelected).
	Entry string `json:"entry,omitempty"`
	// IsNewestGood tells whether this boot is the newest GOOD generation: false
	// after a fallback to an older one. Null unless Kind is generation.
	IsNewestGood *bool `json:"is_newest_good"`
}

// Inputs is what the status reads besides the manifest and boot storage.
type Inputs struct {
	Manifest *manifest.Manifest
	View     *state.View // boot storage as scanned
	// Cmdline is the running kernel's command line.
	Cmdline string
	// IsUEFI says whether this boot came through UEFI (the efivars directory exists).
	IsUEFI        bool
	EfivarsDir    string
	SecureBootDir string
	// SigEnforcePath is the kernel's module.sig_enforce parameter; empty means the default.
	SigEnforcePath string
	// Reports are findings of reconciliation, added to the health report.
	Reports []string
}

// DefaultSigEnforce is where the kernel says whether it enforces module signatures.
const DefaultSigEnforce = "/sys/module/module/parameters/sig_enforce"

// Build makes the report.
func Build(in Inputs) *Report {
	m := in.Manifest
	r := &Report{
		Format:         Format,
		Firmware:       m.Firmware,
		BootedFirmware: "bios",
		SecureBootTier: m.SecureBootTier,
		Tries:          m.Tries,
		Generations:    []Generation{},
		Recovery:       []Recovery{},
	}
	efivars := in.EfivarsDir
	if efivars == "" {
		efivars = efivar.DefaultDir
	}
	if in.IsUEFI {
		r.BootedFirmware = "uefi"
		sb := efivar.SecureBootEnabled(efivars)
		r.SecureBoot = &sb
	}
	r.Modules = modules(r.SecureBoot, in.SigEnforcePath, m.SecureBootTier)

	for _, img := range m.Images {
		g := Generation{
			Generation: img.Generation,
			State:      img.State,
			Stale:      img.Stale,
			Created:    img.Created,
			Date:       day(img.Created),
			ABI:        img.Kernel.ABI,
			Variants:   []Variant{},
			TriesLeft:  triesLeft(in, img.Generation),
		}
		if img.Kernel.Package != nil {
			g.Package = *img.Kernel.Package
		}
		if img.Kernel.Version != nil {
			g.Version = *img.Kernel.Version
		}
		if img.FirstGood != nil {
			g.FirstGood = *img.FirstGood
		}
		if img.LastGood != nil {
			g.LastGood = *img.LastGood
		}
		for _, v := range img.Variants {
			g.Variants = append(g.Variants, Variant{ID: v.ID, Title: v.Title})
		}
		r.Generations = append(r.Generations, g)
		if img.State == "GOOD" && (r.NewestGood == nil || img.Generation > *r.NewestGood) {
			g := img.Generation
			r.NewestGood = &g
		}
	}
	// Newest first, as a product lists them.
	sort.Slice(r.Generations, func(i, j int) bool { return r.Generations[i].Generation > r.Generations[j].Generation })
	if g, err := nextboot.PreviousGood(m); err == nil {
		r.PreviousGood = &g
	}
	for _, rec := range m.Recovery {
		r.Recovery = append(r.Recovery, Recovery{Slot: rec.Slot, Version: rec.Version})
	}

	r.Booted = booted(in)
	if r.Booted.Kind == KindGeneration && r.Booted.Generation != nil {
		is := r.NewestGood != nil && *r.NewestGood == *r.Booted.Generation
		r.Booted.IsNewestGood = &is
	}

	r.Health = append([]string{}, in.Reports...)
	r.Health = append(r.Health, state.CheckHealth(m, in.View, in.EfivarsDir, in.SecureBootDir, in.IsUEFI)...)
	r.Healthy = len(r.Health) == 0
	return r
}

func day(created string) string {
	if len(created) >= 10 {
		return created[:10]
	}
	return created
}

// modules answers "can a DKMS module load": not if the kernel enforces module
// signatures or Secure Boot is on, because the machine key is not in any list the
// kernel trusts (no shim and so no MOK list in tier A; WO-0010 5.3 verifies the
// kernel's side). Only a readable, off answer for both is "allowed".
func modules(secureBoot *bool, sigEnforcePath, tier string) Modules {
	if sigEnforcePath == "" {
		sigEnforcePath = DefaultSigEnforce
	}
	var m Modules
	if data, err := os.ReadFile(sigEnforcePath); err == nil {
		v := strings.TrimSpace(string(data))
		if v == "Y" || v == "1" || v == "N" || v == "0" {
			b := v == "Y" || v == "1"
			m.SigEnforce = &b
		}
	}
	switch {
	case secureBoot != nil && *secureBoot:
		m.OutOfTree = "blocked"
		m.Reason = "Secure Boot is on; the machine key is not in a list the kernel trusts for modules (tier " + tier + ")"
	case m.SigEnforce != nil && *m.SigEnforce:
		m.OutOfTree = "blocked"
		m.Reason = "the kernel enforces module signatures"
	case m.SigEnforce != nil:
		m.OutOfTree = "allowed"
	default:
		m.OutOfTree = "unknown"
		m.Reason = "the kernel does not say whether it enforces module signatures"
	}
	return m
}

// triesLeft reads the boot counter of generation g from boot storage.
func triesLeft(in Inputs, g int) *int {
	if in.View == nil {
		return nil
	}
	uefi := in.Manifest.Firmware == "uefi" || in.Manifest.Firmware == "both"
	if in.IsUEFI && uefi {
		for name := range in.View.UEFIFiles {
			if e, ok := bls.Parse(name); ok && e.Stem == "vintner-os-"+strconv.Itoa(g) && e.Counted {
				left := e.Left
				return &left
			}
		}
		return nil
	}
	if left, ok := biosLeft(in.View, "VINTNER_T_"+strconv.Itoa(g)); ok {
		return &left
	}
	return nil
}

// biosLeft is the number of tries a grubenv counter has left: one for each "x",
// 0 for the bad mark. ok is false for an unset or malformed counter.
func biosLeft(v *state.View, name string) (int, bool) {
	if v.GrubEnv == nil {
		return 0, false
	}
	c, ok := v.GrubEnv.Get(name)
	if !ok {
		return 0, false
	}
	if c == "0" {
		return 0, true
	}
	if c == "" || strings.Trim(c, "x") != "" {
		return 0, false
	}
	return len(c), true
}

func tokenValue(cmdline, prefix string) string {
	for _, f := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(f, prefix); ok {
			return v
		}
	}
	return ""
}

func failed(n int) *int {
	if n < 0 {
		n = 0
	}
	return &n
}

// booted finds the image this boot started and its failed attempts.
func booted(in Inputs) Booted {
	if in.IsUEFI {
		return bootedUEFI(in)
	}
	return bootedBIOS(in)
}

func bootedUEFI(in Inputs) Booted {
	dir := in.EfivarsDir
	if dir == "" {
		dir = efivar.DefaultDir
	}
	b := Booted{Kind: KindUnknown}
	_, sel, _ := efivar.GetString(dir, "LoaderEntrySelected", efivar.LoaderInfo)
	_, countPath, cerr := efivar.GetString(dir, "LoaderBootCountPath", efivar.LoaderInfo)
	b.Entry = sel

	name := sel
	if name == "" && cerr == nil {
		name = countPath[strings.LastIndexAny(countPath, `\/`)+1:]
	}
	if !strings.HasSuffix(strings.ToLower(name), ".efi") && name != "" {
		name += ".efi"
	}
	parsed, ok := bls.Parse(name)
	if !ok {
		return b
	}
	var counted *bls.Entry
	if cerr == nil {
		cp := countPath[strings.LastIndexAny(countPath, `\/`)+1:]
		if e, ok := bls.Parse(cp); ok && e.Counted && e.Stem == parsed.Stem {
			counted = &e
		}
	}
	// The loader counted this boot, so Done includes it.
	boots := func() *int {
		if counted == nil {
			return nil
		}
		return failed(counted.Done - 1)
	}

	if gen, id, ok := bls.IsVintnerVariant(parsed.Stem); ok {
		b.Kind, b.Generation, b.Variant = KindVariant, &gen, id
		b.FailedAttempts = ordinaryFailedUEFI(in, gen)
		return b
	}
	if gen, ok := bls.IsVintnerOrdinary(parsed.Stem); ok {
		b.Kind, b.Generation = KindGeneration, &gen
		b.FailedAttempts = boots()
		return b
	}
	if slot, ok := bls.IsVintnerRecovery(parsed.Stem); ok {
		b.Kind, b.Slot = KindRecovery, slot
		b.FailedAttempts = boots()
	}
	return b
}

// ordinaryFailedUEFI is the failed attempts of generation g's ordinary image as
// its file name records them, when a variant of it booted: the variant counted
// nothing, so every try recorded is a boot that did not finish.
func ordinaryFailedUEFI(in Inputs, g int) *int {
	if in.View == nil {
		return nil
	}
	for name := range in.View.UEFIFiles {
		if e, ok := bls.Parse(name); ok && e.Counted && e.Stem == "vintner-os-"+strconv.Itoa(g) {
			return failed(e.Done)
		}
	}
	return nil
}

func bootedBIOS(in Inputs) Booted {
	b := Booted{Kind: KindUnknown}
	image := tokenValue(in.Cmdline, ImageToken)
	variant := tokenValue(in.Cmdline, variants.Token)
	var pending string // VINTNER_BOOTED: the image the loader counted and bless has not blessed
	if in.View != nil && in.View.GrubEnv != nil {
		pending, _ = in.View.GrubEnv.Get("VINTNER_BOOTED")
	}
	if image == "" {
		// A loader payload from before the token: the pending mark still names the
		// image until bless clears it.
		switch {
		case pending == "RA":
			image = "recovery-a"
		case pending != "":
			image = "os-" + pending
		}
	}

	switch {
	case strings.HasPrefix(image, "os-"):
		gen, err := strconv.Atoi(strings.TrimPrefix(image, "os-"))
		if err != nil || strconv.Itoa(gen) != strings.TrimPrefix(image, "os-") {
			return b
		}
		b.Generation = &gen
		left, ok := leftOf(in, "VINTNER_T_"+strconv.Itoa(gen))
		if variant != "" {
			b.Kind, b.Variant = KindVariant, variant
			if ok {
				b.FailedAttempts = failed(in.Manifest.Tries - left)
			}
			return b
		}
		b.Kind = KindGeneration
		if ok {
			n := in.Manifest.Tries - left
			if pending == strconv.Itoa(gen) {
				n-- // this boot is counted and not yet blessed
			}
			b.FailedAttempts = failed(n)
		}
	case image == "recovery-a" || image == "recovery-b":
		b.Kind, b.Slot = KindRecovery, strings.TrimPrefix(image, "recovery-")
		if b.Slot == "a" {
			if left, ok := leftOf(in, "VINTNER_T_RA"); ok {
				n := in.Manifest.Tries - left
				if pending == "RA" {
					n--
				}
				b.FailedAttempts = failed(n)
			}
		}
	}
	return b
}

func leftOf(in Inputs, name string) (int, bool) {
	if in.View == nil {
		return 0, false
	}
	return biosLeft(in.View, name)
}
