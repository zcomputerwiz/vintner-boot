// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt/gpttest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/install"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish/publishtest"
)

func installDisk(t *testing.T, withBootStorage bool) string {
	t.Helper()
	parts := []gpttest.Part{
		{Type: gpt.TypeBIOSBoot, UUID: "2b0f98d3-e62a-48b4-b260-dfdfda5b8b3f", FirstLBA: 2048, LastLBA: 8191},
		{Type: gpt.TypeESP, UUID: "b558256e-e822-4787-9bd2-c4c47dfb54d9", FirstLBA: 8192, LastLBA: 262143},
	}
	if withBootStorage {
		parts = append(parts, gpttest.Part{Type: gpt.TypeBootStorage, UUID: "2d11fdb7-c9b6-475f-8336-ebdb5b830355", FirstLBA: 262144, LastLBA: 262144 + 2097152 - 1})
	}
	parts = append(parts, gpttest.Part{Type: gpt.TypeLinuxRoot, UUID: "fd202e72-0241-4c3b-953d-ffe506aefc6c", FirstLBA: 2359296, LastLBA: 8000000})
	d := gpttest.Image(parts, 8388608)
	fatBS := func(off int64, total uint32) {
		b := make([]byte, 512)
		binary.LittleEndian.PutUint16(b[11:], 512)
		b[13] = 8
		binary.LittleEndian.PutUint16(b[14:], 32)
		b[16] = 2
		binary.LittleEndian.PutUint32(b[32:], total)
		binary.LittleEndian.PutUint32(b[36:], (total/8)/128+1)
		binary.LittleEndian.PutUint32(b[67:], 0x1234ABCD)
		b[510], b[511] = 0x55, 0xAA
		d.WriteAt(b, off)
	}
	fatBS(8192*512, 253952)
	if withBootStorage {
		fatBS(262144*512, 2097152)
	}
	sb := make([]byte, 1024)
	binary.LittleEndian.PutUint16(sb[56:], 0xEF53)
	d.WriteAt(sb, 2359296*512+1024)
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func installPayload(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var lines []string
	for _, n := range append([]string{"VERSIONS"}, payload.Required...) {
		p := filepath.Join(dir, filepath.FromSlash(n))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(n), 0o644)
		s := sha256.Sum256([]byte(n))
		lines = append(lines, hex.EncodeToString(s[:])+"  ./"+n)
	}
	sort.Strings(lines)
	os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	return dir
}

func TestInstallCheckAcceptsAGoodLayout(t *testing.T) {
	var out, errb bytes.Buffer
	rc := runInstall([]string{"--check", "--disk", installDisk(t, true), "--firmware", "both", "--payload-dir", installPayload(t)}, &out, &errb)
	if rc != 0 {
		t.Fatalf("rc %d, stderr %q", rc, errb.String())
	}
	for _, want := range []string{"boot storage: partition 3, PARTUUID 2d11fdb7-c9b6-475f-8336-ebdb5b830355", "layout and payloads are accepted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q: %s", want, out.String())
		}
	}
}

func TestInstallCheckRefusesWithTheRule(t *testing.T) {
	var out, errb bytes.Buffer
	rc := runInstall([]string{"--check", "--disk", installDisk(t, false), "--firmware", "both", "--payload-dir", installPayload(t)}, &out, &errb)
	if rc != 1 {
		t.Fatalf("rc %d, want 1", rc)
	}
	if !strings.Contains(errb.String(), "refused: L2:") || strings.Contains(out.String(), "accepted") {
		t.Errorf("stderr %q stdout %q", errb.String(), out.String())
	}
}

func TestInstallCheckRefusesAnUnverifiedPayload(t *testing.T) {
	dir := installPayload(t)
	os.WriteFile(filepath.Join(dir, "uefi", "loader.conf"), []byte("edited"), 0o644)
	var out, errb bytes.Buffer
	rc := runInstall([]string{"--check", "--disk", installDisk(t, true), "--firmware", "both", "--payload-dir", dir}, &out, &errb)
	if rc != 1 || !strings.Contains(errb.String(), "differs from its MANIFEST hash") {
		t.Errorf("rc %d stderr %q", rc, errb.String())
	}
}

func TestInstallUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := runInstall([]string{"--check"}, &out, &errb); rc != 2 {
		t.Errorf("no --disk: rc %d", rc)
	}
	errb.Reset()
	if rc := runInstall([]string{"--disk", "x"}, &out, &errb); rc != 2 {
		t.Errorf("no --firmware: rc %d, stderr %q", rc, errb.String())
	}
}

// noStock reports that no stock loader package is installed.
type noStock struct{}

func (noStock) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("dpkg-query: no packages found"), os.ErrNotExist
}

func installTarget(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	os.MkdirAll(filepath.Join(target, "etc", "apt", "apt.conf.d"), 0o755)
	os.MkdirAll(filepath.Join(target, "usr/lib/systemd/boot/efi"), 0o755)
	os.MkdirAll(filepath.Join(target, "boot"), 0o755)
	os.MkdirAll(filepath.Join(target, "lib/modules/6.12.9-amd64"), 0o755)
	os.WriteFile(filepath.Join(target, "etc/machine-id"), []byte("3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f\n"), 0o644)
	os.WriteFile(filepath.Join(target, "etc/os-release"), []byte("PRETTY_NAME=\"Debian\"\nID=debian\n"), 0o644)
	os.WriteFile(filepath.Join(target, "usr/lib/systemd/boot/efi/linuxx64.efi.stub"), []byte("stub"), 0o644)
	os.WriteFile(filepath.Join(target, "boot/vmlinuz-6.12.9-amd64"), []byte("k"), 0o644)
	os.WriteFile(filepath.Join(target, "boot/initrd.img-6.12.9-amd64"), []byte("i"), 0o644)
	os.WriteFile(filepath.Join(target, "lib/modules/6.12.9-amd64/modules.dep"), []byte("#"), 0o644)
	return target
}

func installHook(lock string) func(*install.FullOptions) {
	return func(o *install.FullOptions) {
		o.UKI = publishtest.UKIBuilder{}
		o.Runner = noStock{}
		o.Publish = publish.Options{
			LockFile:         lock,
			PackageQuery:     publishtest.PackageQuery{Pkg: "linux-image-6.12.9-amd64", Ver: "6.12.9-1"},
			UKIBuilder:       publishtest.UKIBuilder{},
			DiskSpaceChecker: publishtest.DiskChecker{Free: 100 << 20},
			Clock:            publishtest.Clock{T: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)},
		}
	}
}

func TestInstallFull(t *testing.T) {
	bs := t.TempDir()
	installTestHook = installHook(filepath.Join(bs, "publish.lock"))
	defer func() { installTestHook = nil }()
	target, esp := installTarget(t), t.TempDir()
	args := []string{"--disk", installDisk(t, true), "--firmware", "uefi", "--payload-dir", installPayload(t),
		"--target", target, "--esp", esp, "--boot-storage", bs}

	var out, errb bytes.Buffer
	if rc := runInstall(args, &out, &errb); rc != 0 {
		t.Fatalf("rc %d, stderr %q, stdout %q", rc, errb.String(), out.String())
	}
	for _, p := range []string{filepath.Join(esp, "EFI/vintner/systemd-bootx64.efi"), filepath.Join(esp, "EFI/BOOT/BOOTX64.EFI"), filepath.Join(bs, "EFI/Linux/vintner-os-1+3-0.efi"), filepath.Join(bs, "EFI/Linux/vintner-recovery-b.efi")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	if !strings.Contains(out.String(), "generation 1 is TRYING") {
		t.Errorf("stdout %q", out.String())
	}

	// A second run finds an installed system and changes nothing.
	out.Reset()
	errb.Reset()
	if rc := runInstall(args, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "installed system") {
		t.Errorf("second run: rc %d, stderr %q", rc, errb.String())
	}
}

func TestInstallReportsAFailure(t *testing.T) {
	bs := t.TempDir()
	installTestHook = installHook(filepath.Join(bs, "publish.lock"))
	defer func() { installTestHook = nil }()
	target := installTarget(t)
	os.Remove(filepath.Join(target, "etc/machine-id"))
	var out, errb bytes.Buffer
	rc := runInstall([]string{"--disk", installDisk(t, true), "--firmware", "uefi", "--payload-dir", installPayload(t),
		"--target", target, "--esp", t.TempDir(), "--boot-storage", bs}, &out, &errb)
	if rc != 1 || !strings.Contains(errb.String(), "machine-id") || !strings.Contains(errb.String(), "everything that was changed") {
		t.Errorf("rc %d, stderr %q", rc, errb.String())
	}
}
