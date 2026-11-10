// SPDX-License-Identifier: GPL-2.0-or-later

package bls

import (
	"path"
	"testing"
)

func TestVariantStem(t *testing.T) {
	for _, tc := range []struct {
		gen int
		id  string
	}{{1, "safe"}, {42, "safe_net"}, {2147483647, "a"}} {
		stem := VariantStem(tc.gen, tc.id)
		gen, id, ok := IsVintnerVariant(stem)
		if !ok || gen != tc.gen || id != tc.id {
			t.Errorf("%q parsed as %d %q %v", stem, gen, id, ok)
		}
		// The file name parses as an uncounted entry with this stem.
		e, ok := Parse(stem + ".efi")
		if !ok || e.Counted || e.Stem != stem {
			t.Errorf("Parse(%s.efi) = %+v %v", stem, e, ok)
		}
		// Neither an ordinary image nor a recovery image.
		if _, ok := IsVintnerOrdinary(stem); ok {
			t.Errorf("%q is taken for an ordinary image", stem)
		}
		if _, ok := IsVintnerRecovery(stem); ok {
			t.Errorf("%q is taken for a recovery image", stem)
		}
	}
	for _, stem := range []string{"vintner-os-1", "vintner_variant-os-", "vintner_variant-os-1", "vintner_variant-os-1-",
		"vintner_variant-os-01-safe", "vintner_variant-os-x-safe", "vintner_variant-os--1-safe", "vintner_variant-os-99999999999-safe"} {
		if _, _, ok := IsVintnerVariant(stem); ok {
			t.Errorf("%q must not parse as a variant", stem)
		}
	}
}

// loader.conf selects with "default vintner-*", matched as a glob against the
// whole entry ID (ADR-0003 finding 1). No variant may match it (D-C): with every
// ordinary image bad an uncounted variant would otherwise boot for ever.
func TestVariantNeverMatchesTheDefaultPattern(t *testing.T) {
	for _, name := range []string{VariantStem(1, "safe") + ".efi", VariantStem(30, "a_b") + ".efi"} {
		if ok, _ := path.Match("vintner-*", name); ok {
			t.Errorf("%s matches the default pattern vintner-*", name)
		}
	}
	// Control: the ordinary and recovery names do match it.
	for _, name := range []string{"vintner-os-1+3-0.efi", "vintner-recovery-a+3-0.efi", "vintner-recovery-b.efi"} {
		if ok, _ := path.Match("vintner-*", name); !ok {
			t.Errorf("%s should match the default pattern", name)
		}
	}
}
