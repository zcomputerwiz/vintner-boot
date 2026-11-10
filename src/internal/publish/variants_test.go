// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/state"
	"github.com/zcomputerwiz/vintner-boot/src/internal/uki"
)

// writeVariant declares a variant in the test root's variants.d.
func writeVariant(t *testing.T, rootDir, id, body string) {
	t.Helper()
	dir := filepath.Join(rootDir, "etc", "vintner", "variants.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".conf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func variantOpts(rootDir, bootStorageDir string, m *manifest.Manifest, abi string, free uint64, stderr *bytes.Buffer) Options {
	return Options{
		KernelABI:        abi,
		BootStorageDir:   bootStorageDir,
		RootDir:          rootDir,
		LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
		CmdlineContent:   m.Images[0].Cmdline,
		PackageQuery:     mockPackageQuery{pkg: "linux-image-" + abi, ver: "1"},
		UKIBuilder:       mockUKIBuilder{},
		DiskSpaceChecker: mockDiskChecker{free: free},
		Stdout:           &bytes.Buffer{},
		Stderr:           stderr,
	}
}

func TestPublishVariants(t *testing.T) {
	for _, fw := range []string{"uefi", "bios", "both"} {
		t.Run(fw, func(t *testing.T) {
			rootDir, bs, m := setupTestEnv(t, fw)
			setupKernelFiles(t, rootDir, "abi-2")
			writeVariant(t, rootDir, "safe", "title=Safe Mode\ntokens=wexmode=safe nomodeset\n")
			writeVariant(t, rootDir, "netw", "# with networking\ntitle=Safe Mode with Networking\ntokens=wexmode=safenet\n")

			if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})); err != nil {
				t.Fatal(err)
			}
			mf, err := manifest.Load(filepath.Join(bs, "vintner", "state"))
			if err != nil {
				t.Fatal(err)
			}
			img := mf.Images[len(mf.Images)-1]
			if img.Generation != 2 || len(img.Variants) != 2 || img.Variants[0].ID != "netw" || img.Variants[1].ID != "safe" {
				t.Fatalf("variants not recorded in id order: %+v", img.Variants)
			}
			wantCmdline := m.Images[0].Cmdline + " wexmode=safe nomodeset vintner.variant=safe"
			if img.Variants[1].Cmdline != wantCmdline || img.Variants[1].Title != "Safe Mode" {
				t.Errorf("safe variant: %+v, want cmdline %q", img.Variants[1], wantCmdline)
			}
			if img.Cmdline != m.Images[0].Cmdline {
				t.Errorf("the ordinary command line changed: %q", img.Cmdline)
			}

			efiDir := filepath.Join(bs, "EFI", "Linux")
			if fw != "bios" {
				for _, v := range img.Variants {
					// No boot counter (D-C), and not named vintner-*: loader.conf's
					// "default vintner-*" must not be able to select it.
					name := "vintner_variant-os-2-" + v.ID + ".efi"
					if _, err := os.Stat(filepath.Join(efiDir, name)); err != nil {
						t.Fatalf("variant file: %v", err)
					}
					if strings.HasPrefix(name, "vintner-") {
						t.Errorf("%s matches the default pattern vintner-*", name)
					}
					if v.UEFI == nil || v.UEFI.Entry != "vintner_variant-os-2-"+v.ID {
						t.Errorf("variant %s has no UEFI record: %+v", v.ID, v.UEFI)
					}
					info, err := uki.Read(filepath.Join(efiDir, name))
					if err != nil {
						t.Fatal(err)
					}
					if string(info.Cmdline) != v.Cmdline {
						t.Errorf("%s .cmdline %q, want %q", name, info.Cmdline, v.Cmdline)
					}
					if got := info.OSRelease["IMAGE_VERSION"]; got != "2~"+v.ID {
						t.Errorf("%s IMAGE_VERSION %q, want 2~%s", name, got, v.ID)
					}
					if info.OSRelease["IMAGE_ID"] != "vintner-os" {
						t.Errorf("%s IMAGE_ID %q", name, info.OSRelease["IMAGE_ID"])
					}
					if !strings.Contains(info.OSRelease["PRETTY_NAME"], "("+v.Title+")") {
						t.Errorf("%s PRETTY_NAME %q lacks the title", name, info.OSRelease["PRETTY_NAME"])
					}
				}
				// The ordinary UKI keeps its counter and its own command line.
				if _, err := bls.Find(efiDir, "vintner-os-2"); err != nil {
					t.Error(err)
				}
			}
			if fw != "uefi" {
				cfg, err := os.ReadFile(filepath.Join(bs, "vintner", "bios", "os-2", "slot.cfg"))
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{
					`set vintner_variants="netw safe"`,
					`set vintner_vtitle_safe="Safe Mode"`,
					`set vintner_vcmdline_safe="` + wantCmdline + `"`,
				} {
					if !strings.Contains(string(cfg), want) {
						t.Errorf("slot.cfg lacks %s:\n%s", want, cfg)
					}
				}
				if !strings.HasPrefix(string(cfg), `set vintner_cmdline="`+m.Images[0].Cmdline+"\"\n") {
					t.Errorf("slot.cfg's ordinary line changed:\n%s", cfg)
				}
			}

			// Every file verifies and nothing is reported.
			v, err := state.Scan(bs)
			if err != nil {
				t.Fatal(err)
			}
			isUEFI, isBIOS := fw != "bios", fw != "uefi"
			if !v.FilesVerify(img, isUEFI, isBIOS) {
				t.Error("FilesVerify is false for the published image")
			}
			if _, reports := state.Reconcile(mf, v); len(reports) != 0 {
				t.Errorf("reconcile reports: %v", reports)
			}
			for _, r := range state.CheckHealth(mf, v, filepath.Join(rootDir, "no-efivars"), "", false) {
				if strings.Contains(r, "variant") || strings.Contains(r, "foreign") || strings.Contains(r, "unlisted") {
					t.Errorf("health reports a variant file: %s", r)
				}
			}

			// The same inputs publish nothing; a changed declaration publishes a new image.
			var out bytes.Buffer
			o := variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})
			o.Stdout = &out
			res, err := Publish(context.Background(), o)
			if err != nil || !res.Unchanged {
				t.Fatalf("second publish: unchanged=%v err=%v\n%s", res != nil && res.Unchanged, err, out.String())
			}
			writeVariant(t, rootDir, "safe", "title=Safe Mode\ntokens=wexmode=safe\n")
			res, err = Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{}))
			if err != nil || res.Unchanged || res.Generation != 3 {
				t.Fatalf("a changed variant must publish generation 3: %+v %v", res, err)
			}
		})
	}
}

func TestPublishVariantsRefusedDeclaration(t *testing.T) {
	cases := map[string]string{
		"root":     "title=Bad\ntokens=root=/dev/sda1\n",
		"vintner":  "title=Bad\ntokens=vintner.bootstorage=PARTUUID=x\n",
		"notitle":  "tokens=x\n",
		"notokens": "title=x\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rootDir, bs, m := setupTestEnv(t, "both")
			setupKernelFiles(t, rootDir, "abi-2")
			writeVariant(t, rootDir, "bad", body)
			before := snapshotDir(t, bs)
			_, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{}))
			if err == nil || !strings.Contains(err.Error(), "variants") || !strings.Contains(err.Error(), "bad.conf") {
				t.Fatalf("want an error naming bad.conf, got %v", err)
			}
			after := snapshotDir(t, bs)
			delete(after, "publish.lock")
			delete(before, "publish.lock")
			if len(after) != len(before) {
				t.Errorf("boot storage changed by a refused publish: before %v, after %v", before, after)
			}
			for k, v := range before {
				if after[k] != v {
					t.Errorf("%s changed by a refused publish", k)
				}
			}
		})
	}
}

// The budget counts the variants: when they do not fit the image is published
// without them (WO-0010 4.3), and no GOOD image is dropped to make room.
func TestPublishVariantsThatDoNotFitAreSkipped(t *testing.T) {
	rootDir, bs, m := setupTestEnv(t, "uefi")
	setupKernelFiles(t, rootDir, "abi-2")
	writeVariant(t, rootDir, "safe", "title=Safe Mode\ntokens=wexmode=safe\n")

	// Measure the image alone.
	probe := &bytes.Buffer{}
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, probe)); err != nil {
		t.Fatal(err)
	}
	mf, _ := manifest.Load(filepath.Join(bs, "vintner", "state"))
	img := mf.Images[len(mf.Images)-1]
	need := uint64(img.UEFI.Size)
	vsize := uint64(img.Variants[0].UEFI.Size)

	rootDir2, bs2, m2 := setupTestEnv(t, "uefi")
	setupKernelFiles(t, rootDir2, "abi-2")
	writeVariant(t, rootDir2, "safe", "title=Safe Mode\ntokens=wexmode=safe\n")
	var stderr bytes.Buffer
	if _, err := Publish(context.Background(), variantOpts(rootDir2, bs2, m2, "abi-2", need+vsize-1, &stderr)); err != nil {
		t.Fatalf("a publish whose variants do not fit must still succeed: %v", err)
	}
	if !strings.Contains(stderr.String(), "variants skipped") {
		t.Errorf("no report that the variants were skipped: %q", stderr.String())
	}
	mf2, _ := manifest.Load(filepath.Join(bs2, "vintner", "state"))
	if len(mf2.Images) != 2 || mf2.Images[1].State != "TRYING" || len(mf2.Images[1].Variants) != 0 {
		t.Errorf("want generation 1 kept and generation 2 published without variants: %+v", mf2.Images)
	}
	entries, _ := os.ReadDir(filepath.Join(bs2, "EFI", "Linux"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), bls.VariantPrefix) {
			t.Errorf("variant file %s published although it did not fit", e.Name())
		}
	}
}

// Garbage collection removes a generation's variant files with it, and finds
// them by name: a record that lost its variants must not leave files behind.
func TestVariantsGoWithTheirGeneration(t *testing.T) {
	rootDir, bs, m := setupTestEnv(t, "both")
	m.Retain = 2
	stateDir := filepath.Join(bs, "vintner", "state")
	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatal(err)
	}
	writeVariant(t, rootDir, "safe", "title=Safe Mode\ntokens=wexmode=safe\n")
	setupKernelFiles(t, rootDir, "abi-2")
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	efiDir := filepath.Join(bs, "EFI", "Linux")
	if _, err := os.Stat(filepath.Join(efiDir, "vintner_variant-os-2-safe.efi")); err != nil {
		t.Fatal(err)
	}
	// Generation 2 fails; generation 3 pushes it out (generation 1 is the GOOD one).
	mf, _ := manifest.Load(stateDir)
	mf.Images[1].State = "FAILED"
	if err := manifest.Save(stateDir, mf); err != nil {
		t.Fatal(err)
	}
	setupKernelFiles(t, rootDir, "abi-3")
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-3", 100<<20, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	mf, _ = manifest.Load(stateDir)
	for _, img := range mf.Images {
		if img.Generation == 2 {
			t.Fatalf("generation 2 should have been collected: %+v", mf.Images)
		}
	}
	entries, _ := os.ReadDir(efiDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "-os-2") {
			t.Errorf("%s outlived generation 2", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(efiDir, "vintner_variant-os-3-safe.efi")); err != nil {
		t.Errorf("the newest generation's variant is gone: %v", err)
	}
}

// A power cut after the variants are in place and before the ordinary UKI is:
// the image is still STAGED, and reconciliation removes the variant files it
// would otherwise leave behind.
func TestVariantsOfAnImageThatNeverGotThere(t *testing.T) {
	rootDir, bs, m := setupTestEnv(t, "uefi")
	setupKernelFiles(t, rootDir, "abi-2")
	writeVariant(t, rootDir, "safe", "title=Safe Mode\ntokens=wexmode=safe\n")
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(bs, "vintner", "state")
	mf, _ := manifest.Load(stateDir)
	mf.Images[1].State = "STAGED"
	if err := manifest.Save(stateDir, mf); err != nil {
		t.Fatal(err)
	}
	ordinary, err := bls.Find(filepath.Join(bs, "EFI", "Linux"), "vintner-os-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(bs, "EFI", "Linux", ordinary)); err != nil {
		t.Fatal(err)
	}

	v, err := state.Scan(bs)
	if err != nil {
		t.Fatal(err)
	}
	mf, _ = manifest.Load(stateDir)
	actions, _ := state.Reconcile(mf, v)
	var removed []string
	for _, a := range actions {
		if a.Type == "remove_efi" {
			removed = append(removed, a.Name)
			if err := state.Apply(bs, a); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(removed) != 1 || removed[0] != "vintner_variant-os-2-safe.efi" {
		t.Fatalf("reconcile asked to remove %v, want the orphaned variant file", removed)
	}
	if len(mf.Images) != 1 {
		t.Errorf("the STAGED record should be gone: %+v", mf.Images)
	}
	if _, err := os.Stat(filepath.Join(bs, "EFI", "Linux", "vintner_variant-os-2-safe.efi")); !os.IsNotExist(err) {
		t.Errorf("variant file still there: %v", err)
	}
}

// A variant file that no record lists is reported, and an image whose variant
// file was damaged does not verify, so it is not called unchanged.
func TestVariantFilesAreChecked(t *testing.T) {
	rootDir, bs, m := setupTestEnv(t, "uefi")
	setupKernelFiles(t, rootDir, "abi-2")
	writeVariant(t, rootDir, "safe", "title=Safe Mode\ntokens=wexmode=safe\n")
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	efiDir := filepath.Join(bs, "EFI", "Linux")
	stateDir := filepath.Join(bs, "vintner", "state")

	if err := os.WriteFile(filepath.Join(efiDir, "vintner_variant-os-2-other.efi"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf, _ := manifest.Load(stateDir)
	v, _ := state.Scan(bs)
	_, reports := state.Reconcile(mf, v)
	if len(reports) != 1 || !strings.Contains(reports[0], "untracked uefi file vintner_variant-os-2-other.efi") {
		t.Errorf("reports: %v", reports)
	}
	health := strings.Join(state.CheckHealth(mf, v, filepath.Join(rootDir, "no-efivars"), "", false), "\n")
	if !strings.Contains(health, "unlisted vintner file: vintner_variant-os-2-other.efi") {
		t.Errorf("health: %s", health)
	}
	_ = os.Remove(filepath.Join(efiDir, "vintner_variant-os-2-other.efi"))

	if err := os.WriteFile(filepath.Join(efiDir, "vintner_variant-os-2-safe.efi"), []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})
	o.Stdout = &out
	res, err := Publish(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unchanged {
		t.Error("an image with a damaged variant file must not be called unchanged")
	}
}
