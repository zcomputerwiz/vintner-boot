// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt/gpttest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish/publishtest"
)

const rootFS = "4e9f54f3-865b-43fa-8f1c-9e644d3a2b11"

// stockRunner answers dpkg-query and apt-get for RemoveStock and grub-mkimage
// for the core image.
type stockRunner struct {
	*fakeRunner
	installed []string // packages dpkg-query reports as installed
	apt       [][]string
	aptErr    error
}

func (s *stockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "chroot" { // RemoveStock runs apt in the target: chroot <target> <command> ...
		return s.Run(ctx, args[1], args[2:]...)
	}
	switch name {
	case "dpkg-query":
		var b strings.Builder
		for _, p := range s.installed {
			b.WriteString("ii  " + p + "\n")
		}
		return []byte(b.String()), nil
	case "env":
		s.apt = append(s.apt, args)
		if s.aptErr != nil {
			return []byte("E: could not purge"), s.aptErr
		}
		return nil, nil
	}
	return s.fakeRunner.Run(ctx, name, args...)
}

func kernelFiles(t *testing.T, target, abi string, initrd bool) {
	t.Helper()
	os.MkdirAll(filepath.Join(target, "boot"), 0o755)
	os.WriteFile(filepath.Join(target, "boot", "vmlinuz-"+abi), []byte("vmlinuz "+abi), 0o644)
	if initrd {
		os.WriteFile(filepath.Join(target, "boot", "initrd.img-"+abi), []byte("initrd "+abi), 0o644)
	}
	os.MkdirAll(filepath.Join(target, "lib", "modules", abi), 0o755)
	os.WriteFile(filepath.Join(target, "lib", "modules", abi, "modules.dep"), []byte("# dep"), 0o644)
}

func targetForPublish(t *testing.T, target string) {
	t.Helper()
	os.MkdirAll(filepath.Join(target, "etc", "apt", "apt.conf.d"), 0o755)
	os.MkdirAll(filepath.Join(target, "usr", "lib", "systemd", "boot", "efi"), 0o755)
	os.WriteFile(filepath.Join(target, "usr/lib/systemd/boot/efi/linuxx64.efi.stub"), []byte("stub"), 0o644)
	os.WriteFile(filepath.Join(target, "etc", "os-release"), []byte("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\n"), 0o644)
}

func fullEnv(t *testing.T, fw string) (*env, *stockRunner, FullOptions, *bytes.Buffer) {
	t.Helper()
	e := newEnv(t, fw)
	kernelFiles(t, e.target, "6.12.9-amd64", true)
	targetForPublish(t, e.target)
	sr := &stockRunner{fakeRunner: e.run}
	e.opts.Runner = sr
	var log bytes.Buffer
	e.opts.Log = &log
	f := FullOptions{Options: e.opts}
	f.Publish = publish.Options{
		LockFile:         filepath.Join(e.bs, "publish.lock"),
		PackageQuery:     publishtest.PackageQuery{Pkg: "linux-image-6.12.9-amd64", Ver: "6.12.9-1"},
		UKIBuilder:       publishtest.UKIBuilder{},
		DiskSpaceChecker: publishtest.DiskChecker{Free: 100 << 20},
		Clock:            publishtest.Clock{T: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)},
	}
	f.UKI = publishtest.UKIBuilder{} // the recovery images go through the same fake
	return e, sr, f, &log
}

func TestInstallPublishesGenerationOneBeforeTakingOver(t *testing.T) {
	for _, fw := range []string{"uefi", "bios", "both"} {
		t.Run(fw, func(t *testing.T) {
			e, _, f, log := fullEnv(t, fw)
			os.MkdirAll(filepath.Join(e.target, "etc", "kernel"), 0o755)
			os.WriteFile(filepath.Join(e.target, "etc/kernel/cmdline"), []byte("# the product's options\nquiet splash\n"), 0o644)
			sector0 := make([]byte, 512)
			e.disk.ReadAt(sector0, 0)

			res, err := Install(context.Background(), f)
			if err != nil {
				t.Fatalf("Install: %v\nlog:\n%s", err, log.String())
			}
			m, err := manifest.Load(filepath.Join(e.bs, "vintner/state"))
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Images) != 1 || m.Images[0].Generation != 1 || m.Images[0].State != "TRYING" || m.Images[0].Kernel.ABI != "6.12.9-amd64" || m.NextGeneration != 2 {
				t.Fatalf("images %+v next %d", m.Images, m.NextGeneration)
			}
			want := "root=UUID=" + rootFS + " ro quiet splash vintner.bootstorage=PARTUUID=" + bsUUID
			if m.Images[0].Cmdline != want {
				t.Errorf("command line %q, want %q", m.Images[0].Cmdline, want)
			}
			if res.Manifest.Images[0].Generation != 1 {
				t.Errorf("result manifest %+v", res.Manifest.Images)
			}
			if fw != "bios" {
				if !exists(filepath.Join(e.bs, "EFI/Linux/vintner-os-1+3-0.efi")) {
					t.Error("the UKI of generation 1 is not on boot storage")
				}
			}
			if fw != "uefi" {
				env, err := grubenv.Read(filepath.Join(e.bs, "grub/grubenv"))
				if err != nil {
					t.Fatal(err)
				}
				if v, _ := env.Get("VINTNER_ORDER"); v != "1" {
					t.Errorf("VINTNER_ORDER=%q", v)
				}
				if v, _ := env.Get("VINTNER_T_RA"); v != "xxx" {
					t.Errorf("recovery A tries %q", v)
				}
			}

			// The order of architecture §5: publish first, take over after.
			out := log.String()
			pub := strings.Index(out, "publishing generation 1")
			if pub < 0 {
				t.Fatalf("no publish line:\n%s", out)
			}
			for _, takeover := range []string{"wrote EFI/BOOT/BOOTX64.EFI", "writing core.img"} {
				if i := strings.Index(out, takeover); i >= 0 && i < pub {
					t.Errorf("%q comes before the publish:\n%s", takeover, out)
				}
			}
			if fw != "uefi" {
				after := make([]byte, 512)
				e.disk.ReadAt(after, 0)
				if bytes.Equal(sector0, after) {
					t.Error("the boot sector was not written")
				}
			}
		})
	}
}

func TestAFailureBeforePublishingLeavesBootingAsItWas(t *testing.T) {
	cases := []struct {
		name string
		mod  func(e *env, f *FullOptions)
		step string
	}{
		{"no kernel", func(e *env, f *FullOptions) { os.RemoveAll(filepath.Join(e.target, "boot")) }, "kernel"},
		{"the product names the root", func(e *env, f *FullOptions) {
			os.MkdirAll(filepath.Join(e.target, "etc", "kernel"), 0o755)
			os.WriteFile(filepath.Join(e.target, "etc/kernel/cmdline"), []byte("root=/dev/sda1\n"), 0o644)
		}, "command line"},
		{"publish fails", func(e *env, f *FullOptions) {
			f.Publish.UKIBuilder = failingUKI{}
		}, "publish"},
	}
	for _, c := range cases {
		e, _, f, _ := fullEnv(t, "both")
		c.mod(e, &f)
		sector0 := make([]byte, 512)
		e.disk.ReadAt(sector0, 0)
		_, err := Install(context.Background(), f)
		var se *Error
		if !errors.As(err, &se) || se.Step != c.step {
			t.Errorf("%s: error %v, want step %q", c.name, err, c.step)
			continue
		}
		if len(se.Changed) == 0 {
			t.Errorf("%s: the report lists nothing although boot storage and the ESP were staged", c.name)
		}
		after := make([]byte, 512)
		e.disk.ReadAt(after, 0)
		if !bytes.Equal(sector0, after) {
			t.Errorf("%s: the boot sector changed", c.name)
		}
		if exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
			t.Errorf("%s: BOOTX64.EFI was written before the first generation was published", c.name)
		}
	}
}

func TestInstallRemovesTheStockLoader(t *testing.T) {
	e, sr, f, _ := fullEnv(t, "both")
	sr.installed = []string{"grub-pc", "shim-signed"}
	os.MkdirAll(filepath.Join(e.esp, "EFI/debian"), 0o755)
	os.WriteFile(filepath.Join(e.esp, "EFI/debian/shimx64.efi"), []byte("shim"), 0o644)
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(e.esp, "EFI/debian")) {
		t.Error("EFI/debian is still on the ESP")
	}
	if len(sr.apt) != 1 {
		t.Fatalf("apt calls: %v", sr.apt)
	}
	got := strings.Join(sr.apt[0], " ")
	if got != "DEBIAN_FRONTEND=noninteractive apt-get -y --allow-remove-essential purge grub-pc shim-signed" {
		t.Errorf("apt command %q", got)
	}
}

func TestNoStockLoaderNoPurge(t *testing.T) {
	_, sr, f, _ := fullEnv(t, "uefi")
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(sr.apt) != 0 {
		t.Errorf("apt was run: %v", sr.apt)
	}
}

func TestAFailedPurgeLeavesVintnerInstalled(t *testing.T) {
	e, sr, f, _ := fullEnv(t, "uefi")
	sr.installed = []string{"grub-efi-amd64-signed"}
	sr.aptErr = errors.New("exit status 100")
	_, err := Install(context.Background(), f)
	var se *Error
	if !errors.As(err, &se) || se.Step != "stock loaders" {
		t.Fatalf("error %v", err)
	}
	m, lerr := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if lerr != nil || len(m.Images) != 1 {
		t.Errorf("vintner is not installed after the failed purge: %v", lerr)
	}
	if !exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
		t.Error("the takeover is missing")
	}
}

func TestRemoveStockInAChroot(t *testing.T) {
	sr := &stockRunner{fakeRunner: &fakeRunner{}, installed: []string{"grub-pc"}}
	var calls [][]string
	rr := runnerFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "chroot" {
			return sr.Run(ctx, args[1], args[2:]...)
		}
		return sr.Run(ctx, name, args...)
	})
	if _, err := RemoveStock(context.Background(), rr, "/mnt/target", ""); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0][0] != "chroot" || calls[0][1] != "/mnt/target" || calls[0][2] != "dpkg-query" || calls[1][0] != "chroot" || calls[1][3] != "DEBIAN_FRONTEND=noninteractive" {
		t.Errorf("calls %v", calls)
	}
}

type runnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func TestNewestKernel(t *testing.T) {
	target := t.TempDir()
	kernelFiles(t, target, "6.12.9-amd64", true)
	kernelFiles(t, target, "6.12.10-amd64", true)
	kernelFiles(t, target, "6.13.1-amd64", false) // newer, but no initramfs
	kernelFiles(t, target, "6.12.10-cloud-amd64", true)
	got, err := NewestKernel(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != "6.12.10-cloud-amd64" {
		t.Errorf("newest kernel %q, want 6.12.10-cloud-amd64 (6.12.10 > 6.12.9, and 6.13.1 has no initramfs)", got)
	}
	if _, err := NewestKernel(t.TempDir()); err == nil {
		t.Error("an empty target has a newest kernel")
	}
}

func TestCmdline(t *testing.T) {
	got, err := Cmdline("U", "B", "# comment\n\nquiet   splash\nconsole=ttyS0\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "root=UUID=U ro quiet splash console=ttyS0 vintner.bootstorage=PARTUUID=B"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, bad := range []string{"root=/dev/sda1", "vintner.bootstorage=PARTUUID=x", "ro", "rw quiet"} {
		if _, err := Cmdline("U", "B", bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if got, _ := Cmdline("U", "B", ""); got != "root=UUID=U ro vintner.bootstorage=PARTUUID=B" {
		t.Errorf("empty product options: %q", got)
	}
}

var _ = gpttest.New
