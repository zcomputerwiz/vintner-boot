// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/uki"
)

// writeKernelCmdline sets the product's options in the test root's /etc/kernel/cmdline.
func writeKernelCmdline(t *testing.T, rootDir, body string) {
	t.Helper()
	dir := filepath.Join(rootDir, "etc", "kernel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newestCmdline is the command line the manifest records for the newest image.
func newestCmdline(t *testing.T, bs string) (gen int, cmdline string) {
	t.Helper()
	m, err := manifest.Load(filepath.Join(bs, "vintner", "state"))
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range m.Images {
		if img.Generation > gen {
			gen, cmdline = img.Generation, img.Cmdline
		}
	}
	return gen, cmdline
}

// /etc/kernel/cmdline composes the new image's line: the earlier line's root and boot
// storage, the file's options, and nothing carried over from the earlier line. A
// change to the file is a new generation; the same file is "unchanged" (WO-0010 4.2).
func TestKernelCmdlineFile(t *testing.T) {
	for _, fw := range []string{"uefi", "bios"} {
		t.Run(fw, func(t *testing.T) {
			rootDir, bs, m := setupTestEnv(t, fw)
			const head = "root=UUID=00000000-0000-0000-0000-000000000001 ro "
			const tail = " vintner.bootstorage=PARTUUID=00000000-0000-0000-0000-000000000003"
			publishABI := func(abi string) *Result {
				t.Helper()
				setupKernelFiles(t, rootDir, abi)
				r, err := Publish(context.Background(), variantOpts(rootDir, bs, m, abi, 100<<20, &bytes.Buffer{}))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}

			writeKernelCmdline(t, rootDir, "# the product's options\nquiet splash\n")
			if r := publishABI("abi-2"); r.Unchanged || r.Generation != 2 {
				t.Fatalf("first publish: %+v", r)
			}
			if gen, got := newestCmdline(t, bs); gen != 2 || got != head+"quiet splash"+tail {
				t.Fatalf("generation %d has %q", gen, got)
			}
			if fw == "uefi" {
				info, err := uki.Read(filepath.Join(bs, "EFI", "Linux", "vintner-os-2+3-0.efi"))
				if err != nil {
					t.Fatal(err)
				}
				if string(info.Cmdline) != head+"quiet splash"+tail {
					t.Errorf("the UKI's .cmdline is %q", info.Cmdline)
				}
			} else {
				cfg, err := os.ReadFile(filepath.Join(bs, "vintner", "bios", "os-2", "slot.cfg"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(cfg), `set vintner_cmdline="`+head+"quiet splash"+tail+`"`) {
					t.Errorf("slot.cfg lacks the command line:\n%s", cfg)
				}
			}

			// Same file, same kernel: nothing to do.
			if r := publishABI("abi-2"); !r.Unchanged {
				t.Errorf("the same options published again: %+v", r)
			}
			// A new option on the same kernel is a new generation, and the options that
			// were removed from the file are gone from the line.
			writeKernelCmdline(t, rootDir, "nomodeset\n")
			if r := publishABI("abi-2"); r.Unchanged || r.Generation != 3 {
				t.Fatalf("a changed file did not publish a new generation: %+v", r)
			}
			if gen, got := newestCmdline(t, bs); gen != 3 || got != head+"nomodeset"+tail {
				t.Errorf("generation %d has %q", gen, got)
			}
			// An empty file is "no options", not "carry the old ones forward".
			writeKernelCmdline(t, rootDir, "")
			if r := publishABI("abi-2"); r.Unchanged || r.Generation != 4 {
				t.Fatalf("an emptied file: %+v", r)
			}
			if _, got := newestCmdline(t, bs); got != strings.TrimSuffix(head, " ")+tail {
				t.Errorf("an empty file left %q", got)
			}
		})
	}
}

// Without the file the earlier image's line is carried forward as before.
func TestNoKernelCmdlineFileCarriesForward(t *testing.T) {
	rootDir, bs, m := setupTestEnv(t, "uefi")
	setupKernelFiles(t, rootDir, "abi-2")
	if _, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if _, got := newestCmdline(t, bs); got != m.Images[0].Cmdline {
		t.Errorf("got %q, want the earlier line %q", got, m.Images[0].Cmdline)
	}
}

// A file that names the root or a vintner. option refuses the publish, names the
// file and leaves boot storage as it was.
func TestKernelCmdlineFileRefused(t *testing.T) {
	for _, bad := range []string{"quiet root=/dev/sda1\n", "vintner.bootstorage=PARTUUID=x\n", "ro\n"} {
		rootDir, bs, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, "abi-2")
		writeKernelCmdline(t, rootDir, bad)
		_, err := Publish(context.Background(), variantOpts(rootDir, bs, m, "abi-2", 100<<20, &bytes.Buffer{}))
		if err == nil || !strings.Contains(err.Error(), filepath.Join("etc", "kernel", "cmdline")) {
			t.Errorf("%q: want a refusal naming the file, got %v", bad, err)
		}
		if gen, _ := newestCmdline(t, bs); gen != 1 {
			t.Errorf("%q: a generation %d was published", bad, gen)
		}
	}
}
