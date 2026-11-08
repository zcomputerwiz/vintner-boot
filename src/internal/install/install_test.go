// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/fsid"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt/gpttest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/layout"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload/payloadtest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

const (
	machineID = "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f"
	bsUUID    = "2d11fdb7-c9b6-475f-8336-ebdb5b830355"
	rootUUID  = "fd202e72-0241-4c3b-953d-ffe506aefc6c"
	espUUID   = "b558256e-e822-4787-9bd2-c4c47dfb54d9"
)

type fakeUKI struct{ reqs []publish.UKIRequest }

func (f *fakeUKI) Build(_ context.Context, r publish.UKIRequest) error {
	f.reqs = append(f.reqs, r)
	return os.WriteFile(r.OutputPath, []byte("UKI "+r.Cmdline), 0o644)
}

type fakeRunner struct {
	calls [][]string
	core  []byte
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return []byte("boom"), f.err
	}
	for i, a := range args {
		if a == "-o" {
			return nil, os.WriteFile(args[i+1], f.core, 0o644)
		}
	}
	return nil, errors.New("no -o")
}

type env struct {
	opts   Options
	esp    string
	bs     string
	target string
	disk   *gpttest.Disk
	uki    *fakeUKI
	run    *fakeRunner
	pl     string
}

func newEnv(t *testing.T, firmware string) *env {
	t.Helper()
	e := &env{esp: t.TempDir(), bs: t.TempDir(), target: t.TempDir(), uki: &fakeUKI{}}
	if err := os.MkdirAll(filepath.Join(e.target, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.target, "etc", "machine-id"), []byte(machineID+"\n"), 0o644)
	e.pl = payloadtest.Write(t, nil)
	p, err := payload.Open(e.pl)
	if err != nil {
		t.Fatal(err)
	}
	e.disk = gpttest.Image([]gpttest.Part{
		{Type: gpt.TypeBIOSBoot, UUID: "2b0f98d3-e62a-48b4-b260-dfdfda5b8b3f", FirstLBA: 2048, LastLBA: 8191},
	}, 8388608)
	core := bytes.Repeat([]byte{0xCC}, 1300)
	e.run = &fakeRunner{core: core}
	e.opts = Options{
		Firmware: firmware,
		Layout: &layout.Layout{
			Root:        &gpt.Partition{Number: 4, UUID: rootUUID},
			RootUUID:    "4e9f54f3-865b-43fa-8f1c-9e644d3a2b11",
			BootStorage: &gpt.Partition{Number: 3, UUID: bsUUID},
			BootFS:      &fsid.FAT{Type: "FAT32", Serial: 0x1234ABCD},
			ESP:         &gpt.Partition{Number: 2, UUID: espUUID},
			BIOSBoot:    &gpt.Partition{Number: 1, UUID: "2b0f98d3-e62a-48b4-b260-dfdfda5b8b3f", FirstLBA: 2048, LastLBA: 8191},
		},
		Payload: p, TargetDir: e.target, ESPDir: e.esp, BootStorageDir: e.bs, Disk: e.disk,
		Runner: e.run, UKI: e.uki,
		LookPath: func(s string) (string, error) { return "/usr/bin/" + s, nil },
	}
	return e
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestDeployUEFI(t *testing.T) {
	e := newEnv(t, "uefi")
	res, err := Deploy(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, filepath.Join(e.esp, "EFI/vintner/systemd-bootx64.efi")), payloadtest.Content(payload.SystemdBoot)) {
		t.Error("the loader on the ESP is not the payload's")
	}
	if !bytes.Equal(read(t, filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")), payloadtest.Content(payload.SystemdBoot)) {
		t.Error("BOOTX64.EFI is not the payload's loader")
	}
	if !bytes.Equal(read(t, filepath.Join(e.esp, "loader/loader.conf")), payloadtest.Content(payload.LoaderConf)) {
		t.Error("loader.conf is not the payload's")
	}
	for _, n := range []string{"vintner-recovery-a+3-0.efi", "vintner-recovery-b.efi"} {
		if !exists(filepath.Join(e.bs, "EFI/Linux", n)) {
			t.Errorf("%s is not on boot storage", n)
		}
	}
	if exists(filepath.Join(e.bs, "grub")) {
		t.Error("a uefi-only install wrote GRUB files")
	}
	if len(e.uki.reqs) != 2 {
		t.Fatalf("%d UKI builds", len(e.uki.reqs))
	}
	for i, slot := range []string{"a", "b"} {
		r := e.uki.reqs[i]
		if !strings.Contains(r.Cmdline, "vintner.recovery="+slot) || !strings.Contains(r.Cmdline, "vintner.bootstorage=PARTUUID="+bsUUID) {
			t.Errorf("recovery %s command line %q", slot, r.Cmdline)
		}
		if r.StubPath != e.opts.Payload.Path(payload.Stub) || r.LinuxPath != e.opts.Payload.Path(payload.RecoveryKern) {
			t.Errorf("recovery %s built from %s and %s", slot, r.StubPath, r.LinuxPath)
		}
		if r.Uname != "" {
			t.Errorf("uname %q", r.Uname)
		}
	}
	m, err := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Seq != 1 || m.MachineID != machineID || m.Firmware != "uefi" || m.SecureBootTier != "none" || m.NextGeneration != 1 || len(m.Images) != 0 {
		t.Errorf("manifest %+v", m)
	}
	if m.ESP == nil || m.ESP.PartUUID != espUUID || m.BootStorage.PartUUID != bsUUID || m.Root.PartUUID != rootUUID || m.Root.UUID != "4e9f54f3-865b-43fa-8f1c-9e644d3a2b11" {
		t.Errorf("identities %+v", m)
	}
	if len(m.Recovery) != 2 || m.Recovery[0].Version != "20261108-abcdef1" || m.Recovery[0].UEFI.Entry != "vintner-recovery-a" || m.Recovery[0].BIOS != nil {
		t.Errorf("recovery %+v", m.Recovery)
	}
	if want := sum(read(t, filepath.Join(e.bs, "EFI/Linux/vintner-recovery-a+3-0.efi"))); m.Recovery[0].UEFI.SHA256 != want {
		t.Errorf("recovery A hash %s, want %s", m.Recovery[0].UEFI.SHA256, want)
	}
	if len(res.Changed) == 0 || res.Manifest.InstallationID != m.InstallationID {
		t.Errorf("result %+v", res)
	}
}

func TestDeployBIOS(t *testing.T) {
	e := newEnv(t, "bios")
	// A protective MBR partition entry must survive.
	pre := make([]byte, 16)
	e.disk.ReadAt(pre, 446)
	if _, err := Deploy(context.Background(), e.opts); err != nil {
		t.Fatal(err)
	}
	if len(e.run.calls) != 1 || e.run.calls[0][0] != "grub-mkimage" {
		t.Fatalf("calls %v", e.run.calls)
	}
	args := strings.Join(e.run.calls[0], " ")
	for _, want := range []string{"-O i386-pc", "-d " + e.opts.Payload.Path(payload.GrubDir), "-p /grub", "biosdisk part_gpt fat search_fs_uuid normal"} {
		if !strings.Contains(args, want) {
			t.Errorf("grub-mkimage args lack %q: %s", want, args)
		}
	}
	// The boot sector: boot.img's jump and code, the partition table kept, the
	// sector of core.img in the field GRUB reads.
	mbr := make([]byte, 512)
	e.disk.ReadAt(mbr, 0)
	boot := payloadtest.Content("bios/i386-pc/boot.img")
	if !bytes.Equal(mbr[0:3], boot[0:3]) || !bytes.Equal(mbr[0x70:0x1b8], boot[0x70:0x1b8]) {
		t.Error("the boot sector does not carry boot.img's code")
	}
	if binary.LittleEndian.Uint64(mbr[0x5c:]) != 2048 || mbr[0x66] != 0x90 || mbr[0x67] != 0x90 {
		t.Errorf("boot sector fields: %x %x %x", mbr[0x5c:0x64], mbr[0x66], mbr[0x67])
	}
	post := make([]byte, 16)
	e.disk.ReadAt(post, 446)
	if !bytes.Equal(pre, post) || post[4] != 0xEE || mbr[510] != 0x55 || mbr[511] != 0xAA {
		t.Errorf("the partition table or signature changed: %x -> %x", pre, post)
	}
	// core.img: padded to sectors, the blocklist of its continuation patched in.
	core := make([]byte, 1536)
	e.disk.ReadAt(core, 2048*512)
	if !bytes.Equal(core[0x20:0x1f4], bytes.Repeat([]byte{0xCC}, 0x1f4-0x20)) || !bytes.Equal(core[0x1fe:1300], bytes.Repeat([]byte{0xCC}, 1300-0x1fe)) || core[1300] != 0 {
		t.Error("core.img is not at the start of the BIOS Boot Partition")
	}
	if binary.LittleEndian.Uint64(core[0x1f4:]) != 2049 || binary.LittleEndian.Uint16(core[0x1fc:]) != 2 {
		t.Errorf("blocklist: start %d count %d", binary.LittleEndian.Uint64(core[0x1f4:]), binary.LittleEndian.Uint16(core[0x1fc:]))
	}
	// boot storage.
	if !bytes.Equal(read(t, filepath.Join(e.bs, "grub/grub.cfg")), payloadtest.Content(payload.GrubCfg)) {
		t.Error("grub.cfg is not the payload's")
	}
	env, err := grubenv.Read(filepath.Join(e.bs, "grub/grubenv"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := env.Get("VINTNER_T_RA"); v != "xxx" {
		t.Errorf("VINTNER_T_RA=%q", v)
	}
	if _, ok := env.Get("VINTNER_ORDER"); ok {
		t.Error("VINTNER_ORDER is set before any image is published")
	}
	for _, slot := range []string{"a", "b"} {
		dir := filepath.Join(e.bs, "vintner/bios/recovery-"+slot)
		if !bytes.Equal(read(t, filepath.Join(dir, "vmlinuz")), payloadtest.Content(payload.RecoveryKern)) {
			t.Errorf("recovery %s kernel", slot)
		}
		cfg := string(read(t, filepath.Join(dir, "slot.cfg")))
		if !strings.HasPrefix(cfg, "set vintner_cmdline=\"console=tty0 console=ttyS0,115200 vintner.recovery="+slot+" vintner.bootstorage=PARTUUID="+bsUUID+"\"") {
			t.Errorf("slot.cfg of recovery %s: %q", slot, cfg)
		}
	}
	if exists(filepath.Join(e.esp, "EFI")) || exists(filepath.Join(e.bs, "EFI")) {
		t.Error("a bios-only install wrote UEFI files")
	}
	m, err := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Firmware != "bios" || m.ESP != nil || m.Recovery[0].BIOS == nil || m.Recovery[0].UEFI != nil {
		t.Errorf("manifest %+v", m)
	}
	if m.Recovery[1].BIOS.Files.SlotCfg != sum(read(t, filepath.Join(e.bs, "vintner/bios/recovery-b/slot.cfg"))) {
		t.Error("recovery B slot.cfg hash")
	}
}

func TestDeployBothFirmwares(t *testing.T) {
	e := newEnv(t, "both")
	if _, err := Deploy(context.Background(), e.opts); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(filepath.Join(e.bs, "vintner/state"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Firmware != "both" || m.Recovery[0].UEFI == nil || m.Recovery[0].BIOS == nil {
		t.Errorf("manifest %+v", m)
	}
}

func TestDeployWritesNothingWhenACheckFails(t *testing.T) {
	cases := []struct {
		name string
		fw   string
		mod  func(e *env)
		want string
	}{
		{"no machine-id", "uefi", func(e *env) { os.Remove(filepath.Join(e.target, "etc", "machine-id")) }, "machine-id"},
		{"a bad machine-id", "uefi", func(e *env) {
			os.WriteFile(filepath.Join(e.target, "etc", "machine-id"), []byte("short\n"), 0o644)
		}, "machine-id"},
		{"already installed", "uefi", func(e *env) {
			os.MkdirAll(filepath.Join(e.bs, "vintner/state"), 0o755)
			os.WriteFile(filepath.Join(e.bs, "vintner/state/manifest-a"), []byte("x"), 0o644)
		}, "installed system"},
		{"another loader.conf", "uefi", func(e *env) {
			os.MkdirAll(filepath.Join(e.esp, "loader"), 0o755)
			os.WriteFile(filepath.Join(e.esp, "loader/loader.conf"), []byte("default windows\n"), 0o644)
		}, "another boot manager"},
		{"no grub-mkimage", "bios", func(e *env) {
			e.opts.LookPath = func(string) (string, error) { return "", errors.New("not found") }
		}, "grub-common"},
		{"grub-mkimage fails", "bios", func(e *env) { e.run.err = errors.New("exit status 1") }, "grub-mkimage"},
		{"core.img does not fit", "bios", func(e *env) { e.run.core = make([]byte, 4<<20) }, "core.img is"},
	}
	for _, c := range cases {
		e := newEnv(t, c.fw)
		c.mod(e)
		sector0 := make([]byte, 512)
		e.disk.ReadAt(sector0, 0)
		_, err := Deploy(context.Background(), e.opts)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
			continue
		}
		var se *Error
		if !errors.As(err, &se) {
			t.Errorf("%s: %T is not *Error", c.name, err)
		}
		if exists(filepath.Join(e.esp, "EFI")) || exists(filepath.Join(e.bs, "grub")) || exists(filepath.Join(e.bs, "EFI")) {
			t.Errorf("%s: files were written", c.name)
		}
		after := make([]byte, 512)
		e.disk.ReadAt(after, 0)
		if !bytes.Equal(sector0, after) {
			t.Errorf("%s: the boot sector changed", c.name)
		}
		if len(se.Changed) != 0 {
			t.Errorf("%s: changed %v", c.name, se.Changed)
		}
	}
}

func TestDeployReportsWhatItChangedWhenAStepFails(t *testing.T) {
	e := newEnv(t, "both")
	e.opts.UKI = failingUKI{}
	sector0 := make([]byte, 512)
	e.disk.ReadAt(sector0, 0)
	_, err := Deploy(context.Background(), e.opts)
	var se *Error
	if !errors.As(err, &se) || se.Step != "recovery" {
		t.Fatalf("error %v", err)
	}
	if len(se.Changed) == 0 {
		t.Error("nothing reported as changed, but boot storage directories and recovery files were written")
	}
	after := make([]byte, 512)
	e.disk.ReadAt(after, 0)
	if !bytes.Equal(sector0, after) || exists(filepath.Join(e.esp, "EFI")) {
		t.Error("the BIOS boot sector or the ESP changed although the recovery step failed: the takeover comes last")
	}
}

type failingUKI struct{}

func (failingUKI) Build(context.Context, publish.UKIRequest) error {
	return errors.New("ukify exploded")
}

func TestOtherSystemOnTheESPKeepsBOOTX64(t *testing.T) {
	e := newEnv(t, "uefi")
	os.MkdirAll(filepath.Join(e.esp, "EFI/Microsoft/Boot"), 0o755)
	if _, err := Deploy(context.Background(), e.opts); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
		t.Error("BOOTX64.EFI was written although Windows uses the ESP")
	}
	if !exists(filepath.Join(e.esp, "EFI/vintner/systemd-bootx64.efi")) {
		t.Error("vintner's own loader was not written")
	}
}

func TestDebianStockDirectoryIsNotAnotherSystem(t *testing.T) {
	e := newEnv(t, "uefi")
	os.MkdirAll(filepath.Join(e.esp, "EFI/debian"), 0o755)
	if _, err := Deploy(context.Background(), e.opts); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(e.esp, "EFI/BOOT/BOOTX64.EFI")) {
		t.Error("BOOTX64.EFI was not written when only Debian's stock loader is there")
	}
}

func TestEmbedGRUBRefusesWhatCannotBeWrittenSafely(t *testing.T) {
	d := gpttest.Image(nil, 8388608)
	bp := gpt.Partition{FirstLBA: 2048, LastLBA: 2048 + 1} // 1 KiB
	if _, err := EmbedGRUB(d, bp, make([]byte, 512), make([]byte, 4096)); err == nil {
		t.Error("a core.img larger than the partition was written")
	}
	if _, err := EmbedGRUB(d, bp, make([]byte, 100), make([]byte, 600)); err == nil {
		t.Error("a boot.img of 100 bytes was accepted")
	}
}

func TestVerifyEmbeddedNoticesADifferentDisk(t *testing.T) {
	d := gpttest.Image(nil, 8388608)
	bp := gpt.Partition{FirstLBA: 2048, LastLBA: 8191}
	e, err := EmbedGRUB(d, bp, payloadtest.Content("bios/i386-pc/boot.img"), bytes.Repeat([]byte{1}, 700))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEmbedded(d, e); err != nil {
		t.Fatal(err)
	}
	d.WriteAt([]byte{0xFF}, 2048*512+300)
	if VerifyEmbedded(d, e) == nil {
		t.Error("a changed core.img read back as written")
	}
}
