// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/uki"
)

// Variants that did not fit are not a change: the same inputs publish nothing,
// or every kernel hook would make a new generation and a new set of files.
func TestSkippedVariantsAreNotAChange(t *testing.T) {
	// Measure the image on a scratch copy.
	probeRoot, probeBS, probeM := setupTestEnv(t, "uefi")
	setupKernelFiles(t, probeRoot, "abi-2")
	if _, err := Publish(context.Background(), variantOpts(probeRoot, probeBS, probeM, "abi-2", 100<<20, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	pm, err := manifest.Load(filepath.Join(probeBS, "vintner", "state"))
	if err != nil {
		t.Fatal(err)
	}
	size := uint64(pm.Images[1].UEFI.Size)

	rootDir, bs, m := setupTestEnv(t, "uefi")
	setupKernelFiles(t, rootDir, "abi-2")
	writeVariant(t, rootDir, "safe", "title=Safe Mode\ntokens=wexmode=safe\n")

	var stderr bytes.Buffer
	free := size + size/2 // room for the image, not for a variant
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", free, &stderr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "variants skipped") {
		t.Fatalf("the variants were not skipped: %q", stderr.String())
	}
	var out bytes.Buffer
	o := variantOpts(rootDir, bs, m, "abi-2", free-size/2, &bytes.Buffer{}) // what is left after the image
	o.Stdout = &out
	res, err := Publish(context.Background(), o)
	if err != nil || !res.Unchanged {
		t.Fatalf("a second publish of the same inputs must be unchanged: %+v %v\n%s", res, err, out.String())
	}
}

// The title is the product's text: it reaches the .osrel as written, UTF-8
// included, with only the characters os-release(5) escapes escaped.
func TestVariantTitleInOSRelease(t *testing.T) {
	rootDir, bs, m := setupTestEnv(t, "uefi")
	setupKernelFiles(t, rootDir, "abi-2")
	writeVariant(t, rootDir, "safe", "title=Mode sécurisé \"$x\"\ntokens=wexmode=safe\n")
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	info, err := uki.Read(filepath.Join(bs, "EFI", "Linux", "vintner_variant-os-2-safe.efi"))
	if err != nil {
		t.Fatal(err)
	}
	// uki.Read returns the value as written, quotes and escapes included.
	want := "\"Debian GNU/Linux 13 (trixie), abi-2 (Mode sécurisé \\\"\\$x\\\")\""
	if got := info.OSRelease["PRETTY_NAME"]; got != want {
		t.Errorf("PRETTY_NAME %q, want %q", got, want)
	}
}
