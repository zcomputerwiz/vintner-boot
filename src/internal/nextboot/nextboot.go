// SPDX-License-Identifier: GPL-2.0-or-later

// Package nextboot sets what the next boot does, once (WO-0010 4.4): boot the
// recovery image, a chosen generation, a variant, or show the menu. The setting
// is consumed by the boot loader, so it never outlives that boot.
//
// UEFI uses systemd-boot's one-shot variables, which it reads and unsets:
// LoaderEntryOneShot (matched as a glob against the entry IDs, ADR-0003
// finding 1) and LoaderConfigTimeoutOneShot (shows the menu). BIOS uses the
// grubenv variable VINTNER_NEXT, which grub.cfg reads and clears in the same
// save_env as its counter write (firmware/bios/INTERFACE.md).
package nextboot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

// Kind is what the next boot does.
type Kind string

const (
	Recovery   Kind = "recovery"
	Generation Kind = "generation"
	Variant    Kind = "variant"
	Menu       Kind = "menu"
	Clear      Kind = "clear"
)

// DefaultMenuTimeout is how long the menu waits, in seconds, when the caller
// names no timeout. When it runs out, the default entry boots.
const DefaultMenuTimeout = 30

// MaxMenuTimeout is the longest menu timeout accepted, in seconds.
const MaxMenuTimeout = 3600

// Variable names.
const (
	// GrubVar is the grubenv variable that carries the BIOS one-shot.
	GrubVar = "VINTNER_NEXT"

	entryVar   = "LoaderEntryOneShot"
	timeoutVar = "LoaderConfigTimeoutOneShot"
)

// Target is one next-boot request, with any generation already resolved.
type Target struct {
	Kind       Kind
	Generation int    // Generation, Variant
	Variant    string // Variant
	Timeout    int    // Menu, in seconds
}

// Error is a request that cannot be honoured. Code is stable and meant for
// products (the JSON output carries it).
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Inventory is what the boot storage holds, as far as next-boot looks.
type Inventory struct {
	// UEFIFiles are the file names in /EFI/Linux/.
	UEFIFiles []string
	// BIOSSlots are the slot directories in /vintner/bios/ (os-<gen>, recovery-a).
	BIOSSlots map[string]bool
	// Env is the grubenv (BIOS); nil if there is none.
	Env *grubenv.Env
}

// PreviousGood returns the newest GOOD generation older than the newest
// generation: Windows' Last Known Good Configuration.
func PreviousGood(m *manifest.Manifest) (int, error) {
	newest, best := -1, -1
	for _, img := range m.Images {
		if img.Generation > newest {
			newest = img.Generation
		}
	}
	for _, img := range m.Images {
		if img.State == "GOOD" && img.Generation < newest && img.Generation > best {
			best = img.Generation
		}
	}
	if best < 0 {
		return 0, errf("no-previous-good", "no GOOD generation older than the newest one (generation %d)", newest)
	}
	return best, nil
}

// BootsNext returns the ordinary generation that the next automatic boot would
// start: the newest one that is not bad.
func BootsNext(uefi bool, inv *Inventory) (int, error) {
	best := -1
	if uefi {
		for _, name := range inv.UEFIFiles {
			e, ok := bls.Parse(name)
			if !ok || e.Bad() {
				continue
			}
			if g, ok := bls.IsVintnerOrdinary(e.Stem); ok && g > best {
				best = g
			}
		}
	} else if inv.Env != nil {
		order, _ := inv.Env.Get("VINTNER_ORDER")
		for _, s := range strings.Fields(order) {
			g, err := strconv.Atoi(s)
			if err != nil || strconv.Itoa(g) != s {
				continue
			}
			// The same test as grub.cfg: a counter of x characters has tries left.
			if c, _ := inv.Env.Get("VINTNER_T_" + s); c != "" && strings.Trim(c, "x") == "" {
				best = g
				break
			}
		}
	}
	if best < 0 {
		return 0, errf("nothing-bootable", "no ordinary generation has tries left")
	}
	return best, nil
}

// Check says whether t can be honoured: the generation and variant are recorded
// and their files are on boot storage.
func Check(m *manifest.Manifest, uefi bool, inv *Inventory, t Target) error {
	switch t.Kind {
	case Menu, Clear:
		return nil
	case Recovery:
		if uefi {
			for _, name := range inv.UEFIFiles {
				if e, ok := bls.Parse(name); ok {
					if _, ok := bls.IsVintnerRecovery(e.Stem); ok {
						return nil
					}
				}
			}
		} else if inv.BIOSSlots["recovery-a"] || inv.BIOSSlots["recovery-b"] {
			return nil
		}
		return errf("no-recovery", "no recovery image on boot storage")
	}
	var img *manifest.Image
	for i := range m.Images {
		if m.Images[i].Generation == t.Generation {
			img = &m.Images[i]
		}
	}
	if img == nil {
		return errf("no-such-generation", "generation %d is not in the manifest", t.Generation)
	}
	if t.Kind == Variant {
		recorded := false
		for _, v := range img.Variants {
			recorded = recorded || v.ID == t.Variant
		}
		if !recorded {
			return errf("no-such-variant", "generation %d has no variant %q", t.Generation, t.Variant)
		}
	}
	present := false
	if uefi {
		stem := fmt.Sprintf("vintner-os-%d", t.Generation)
		if t.Kind == Variant {
			stem = bls.VariantStem(t.Generation, t.Variant)
		}
		for _, name := range inv.UEFIFiles {
			if e, ok := bls.Parse(name); ok && e.Stem == stem {
				present = true
			}
		}
	} else {
		present = inv.BIOSSlots[fmt.Sprintf("os-%d", t.Generation)]
	}
	if !present && t.Kind == Variant {
		return errf("no-such-variant", "variant %q of generation %d is not on boot storage", t.Variant, t.Generation)
	}
	if !present {
		return errf("no-such-generation", "generation %d is not on boot storage", t.Generation)
	}
	return nil
}

// Entry is the value of LoaderEntryOneShot for t, a glob over entry IDs.
// Recovery takes recovery A while it is not bad and recovery B after that,
// because a bad entry sorts last. The ID of a counted entry has no counter in it,
// so "vintner-os-<n>.efi" names the generation whatever its state (ci/selection-policy S19).
func Entry(t Target) string {
	switch t.Kind {
	case Recovery:
		return "vintner-recovery-*"
	case Generation:
		return fmt.Sprintf("vintner-os-%d.efi", t.Generation)
	case Variant:
		return bls.VariantStem(t.Generation, t.Variant) + ".efi"
	}
	return ""
}

// BIOSValue is the value of VINTNER_NEXT for t. grub.cfg matches it against
// these exact forms and ignores anything else.
func BIOSValue(t Target) string {
	switch t.Kind {
	case Recovery:
		return "recovery"
	case Generation:
		return strconv.Itoa(t.Generation)
	case Variant:
		return fmt.Sprintf("%d:%s", t.Generation, t.Variant)
	case Menu:
		return fmt.Sprintf("menu:%d", t.Timeout)
	}
	return ""
}

// Ours reports whether a LoaderEntryOneShot value is one that Entry makes. The
// health check does not report those: vintner set them.
func Ours(value string) bool {
	return strings.HasPrefix(value, "vintner-os-") || strings.HasPrefix(value, "vintner-recovery-") ||
		strings.HasPrefix(value, bls.VariantPrefix)
}

// SetUEFI replaces any earlier next-boot setting with t. Clear removes
// vintner's own (a foreign LoaderEntryOneShot stays).
func SetUEFI(efivarsDir string, t Target) error {
	if t.Kind != Clear || ourEntry(efivarsDir) {
		if err := efivar.Delete(efivarsDir, entryVar, efivar.LoaderInfo); err != nil && !errors.Is(err, efivar.ErrNotFound) {
			return err
		}
	}
	if err := efivar.Delete(efivarsDir, timeoutVar, efivar.LoaderInfo); err != nil && !errors.Is(err, efivar.ErrNotFound) {
		return err
	}
	switch t.Kind {
	case Clear:
		return nil
	case Menu:
		return efivar.SetString(efivarsDir, timeoutVar, efivar.LoaderInfo, efivar.BootAttrs, strconv.Itoa(t.Timeout))
	}
	return efivar.SetString(efivarsDir, entryVar, efivar.LoaderInfo, efivar.BootAttrs, Entry(t))
}

func ourEntry(dir string) bool {
	_, v, err := efivar.GetString(dir, entryVar, efivar.LoaderInfo)
	return err == nil && Ours(v)
}

// SetBIOS replaces any earlier next-boot setting with t in the grubenv at path.
func SetBIOS(path string, t Target) error {
	env, err := grubenv.Read(path)
	if err != nil {
		return err
	}
	if t.Kind == Clear {
		env.Unset(GrubVar)
	} else {
		env.Set(GrubVar, BIOSValue(t))
	}
	return grubenv.Write(path, env)
}

// Scan lists what boot storage holds. A missing directory is an empty set.
func Scan(bootStorage string) (*Inventory, error) {
	inv := &Inventory{BIOSSlots: map[string]bool{}}
	entries, err := os.ReadDir(filepath.Join(bootStorage, "EFI", "Linux"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			inv.UEFIFiles = append(inv.UEFIFiles, e.Name())
		}
	}
	entries, err = os.ReadDir(filepath.Join(bootStorage, "vintner", "bios"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			inv.BIOSSlots[e.Name()] = true
		}
	}
	// A BIOS-only machine needs the grubenv; a UEFI one has none to read.
	if env, err := grubenv.Read(filepath.Join(bootStorage, "grub", "grubenv")); err == nil {
		inv.Env = env
	}
	return inv, nil
}
