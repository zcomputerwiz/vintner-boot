// SPDX-License-Identifier: GPL-2.0-or-later

package manifest

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func exampleWithVariant(t *testing.T) *Manifest {
	t.Helper()
	data, err := os.ReadFile("../../../schema/examples/manifest-uefi.json")
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	img := &m.Images[len(m.Images)-1]
	img.Variants = []Variant{{
		ID:      "safe",
		Title:   "Safe Mode",
		Cmdline: img.Cmdline + " wexmode=safe vintner.variant=safe",
		UEFI: &UEFIFile{
			Entry:  "vintner_variant-os-" + itoa(img.Generation) + "-safe",
			SHA256: strings.Repeat("a", 64),
			Size:   10,
			Signed: m.SecureBootTier == "A",
		},
	}}
	return &m
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestVariantValidation(t *testing.T) {
	m := exampleWithVariant(t)
	if err := m.Validate(); err != nil {
		t.Fatalf("a valid variant failed validation: %v", err)
	}
	// It survives a save and a load, and an image without variants writes no key.
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"variants":[{"id":"safe"`) {
		t.Errorf("variants not written: %s", out)
	}
	m.Images[len(m.Images)-1].Variants = nil
	out, _ = json.Marshal(m)
	if strings.Contains(string(out), `"variants"`) {
		t.Errorf("an image without variants must not write the key: %s", out)
	}

	tests := []struct {
		name    string
		mutate  func(*Image, *Manifest)
		wantErr string
	}{
		{"bad id", func(i *Image, _ *Manifest) { i.Variants[0].ID = "Safe" }, "invalid variant id"},
		{"duplicate", func(i *Image, _ *Manifest) {
			i.Variants = append(i.Variants, i.Variants[0])
		}, "duplicate variant"},
		{"no title", func(i *Image, _ *Manifest) { i.Variants[0].Title = "" }, "needs a title"},
		{"no variant token", func(i *Image, _ *Manifest) { i.Variants[0].Cmdline = i.Cmdline }, "vintner.variant=safe"},
		{"token of another id", func(i *Image, _ *Manifest) {
			i.Variants[0].Cmdline = i.Cmdline + " vintner.variant=other"
		}, "vintner.variant=safe"},
		{"no uefi file on uefi", func(i *Image, _ *Manifest) { i.Variants[0].UEFI = nil }, "uefi file must be present"},
		{"wrong entry", func(i *Image, _ *Manifest) { i.Variants[0].UEFI.Entry = "vintner-os-" + itoa(i.Generation) }, "invalid uefi entry"},
		{"counted entry", func(i *Image, _ *Manifest) {
			i.Variants[0].UEFI.Entry = "vintner_variant-os-" + itoa(i.Generation) + "-safe+3-0"
		}, "invalid uefi entry"},
		{"bad sha", func(i *Image, _ *Manifest) { i.Variants[0].UEFI.SHA256 = "zz" }, "sha256"},
		{"signed in tier none", func(i *Image, m *Manifest) {
			m.SecureBootTier, m.Signing = "none", nil
			for k := range m.Images {
				if m.Images[k].UEFI != nil {
					m.Images[k].UEFI.Signed = false
				}
			}
			i.Variants[0].UEFI.Signed = true
		}, "tier none must not have uefi.signed in variant"},
		{"unsigned in tier A", func(i *Image, m *Manifest) {
			m.SecureBootTier = "A"
			m.Signing = &Signing{CertSHA256: strings.Repeat("b", 64)}
			for k := range m.Images {
				if m.Images[k].UEFI != nil {
					m.Images[k].UEFI.Signed = true
				}
			}
			i.Variants[0].UEFI.Signed = false
		}, "tier A requires uefi.signed to be true in variant"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := exampleWithVariant(t)
			tc.mutate(&m.Images[len(m.Images)-1], m)
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want an error with %q", err, tc.wantErr)
			}
		})
	}

	t.Run("a BIOS-only image has no variant file", func(t *testing.T) {
		m := exampleWithVariant(t)
		m.Firmware = "bios"
		for i := range m.Images {
			m.Images[i].UEFI = nil
			m.Images[i].BIOS = &BIOSFiles{Dir: "/vintner/bios/os-" + itoa(m.Images[i].Generation),
				Files: BIOSFilesMap{Vmlinuz: strings.Repeat("a", 64), InitrdImg: strings.Repeat("a", 64), SlotCfg: strings.Repeat("a", 64)}}
		}
		for i := range m.Recovery {
			m.Recovery[i].UEFI = nil
			m.Recovery[i].BIOS = &BIOSFiles{Dir: "/vintner/bios/recovery-" + m.Recovery[i].Slot,
				Files: BIOSFilesMap{Vmlinuz: strings.Repeat("a", 64), InitrdImg: strings.Repeat("a", 64), SlotCfg: strings.Repeat("a", 64)}}
		}
		m.SecureBootTier = "none"
		m.Signing = nil
		last := &m.Images[len(m.Images)-1]
		if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "uefi file must be present") {
			t.Fatalf("a variant with a UEFI file on a BIOS manifest must not validate: %v", err)
		}
		last.Variants[0].UEFI = nil
		if err := m.Validate(); err != nil {
			t.Fatalf("a BIOS variant has no file of its own: %v", err)
		}
	})
}
