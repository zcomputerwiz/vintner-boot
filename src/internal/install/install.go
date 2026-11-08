// SPDX-License-Identifier: GPL-2.0-or-later

// Package install deploys vintner onto a layout that internal/layout accepted:
// the loader, the BIOS boot code, grubenv, the recovery images and the
// manifest, all from the verified payloads of vintner-platform (WO-0010 3.2,
// step 1 and 2 of architecture §5). Publishing generation 1, signing and
// removing the stock loaders are separate steps of the command.
//
// Everything it writes lies in vintner's own places: \EFI\vintner and
// \loader\loader.conf on the ESP, boot storage, and for BIOS the boot sector
// and the BIOS Boot Partition. The one write that takes over booting from the
// stock loader is the BIOS boot sector, so it comes last.
package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/layout"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

// RecoveryCmdline is the command line of both recovery images, plus the slot
// and the boot storage identity (architecture §5: every vintner image names its
// boot storage).
const RecoveryCmdline = "console=tty0 console=ttyS0,115200"

// Runner runs an external program (grub-mkimage).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs programs with os/exec.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Options is everything Deploy needs. Layout comes from layout.Check with no
// findings, Payload from payload.Open.
type Options struct {
	Firmware       string // uefi, bios or both
	Layout         *layout.Layout
	Payload        *payload.Payload
	TargetDir      string // the root of the target system (for /etc/machine-id)
	ESPDir         string // the mounted ESP (uefi, both)
	BootStorageDir string // the mounted boot storage
	Disk           Disk   // the whole disk (bios, both)

	Runner   Runner
	UKI      publish.UKIBuilder
	LookPath func(string) (string, error)
	Now      func() time.Time
	Rand     io.Reader
	Log      io.Writer

	Tries  int // default 3
	Retain int // default 3
}

// Result says what Deploy changed.
type Result struct {
	Changed  []string
	Manifest *manifest.Manifest
}

// Error is a failed step, with everything that was changed before it.
type Error struct {
	Step    string
	Err     error
	Changed []string
}

func (e *Error) Error() string { return "install: " + e.Step + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

var (
	machineIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// Directories of the ESP that are not another operating system's: the removable
	// path, vintner's own, and Debian's stock loader, which the installer removes.
	ownESPDirs = map[string]bool{"boot": true, "vintner": true, "debian": true}
)

// Deploy deploys vintner onto the layout.
func Deploy(ctx context.Context, o Options) (res *Result, err error) {
	if o.Tries == 0 {
		o.Tries = 3
	}
	if o.Retain == 0 {
		o.Retain = 3
	}
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	if o.UKI == nil {
		o.UKI = publish.RealUKIBuilder{}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	d := &deployer{Options: o}
	defer func() {
		if err != nil {
			var se *Error
			if errors.As(err, &se) {
				se.Changed = d.changed
			}
		}
	}()
	if err := d.run(ctx); err != nil {
		return nil, err
	}
	return &Result{Changed: d.changed, Manifest: d.m}, nil
}

type deployer struct {
	Options
	changed []string
	m       *manifest.Manifest
	emb     *Embedded

	uefi, bios bool
	bs         string // PARTUUID of boot storage
	espUUID    string
	espFiles   map[string]string // path under the ESP to the payload file it must equal
	bsFiles    map[string]string // path under boot storage to the sha256 it must have
}

func (d *deployer) rec(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	d.changed = append(d.changed, s)
	fmt.Fprintln(d.Log, "  "+s)
}

func (d *deployer) fail(step string, err error) error { return &Error{Step: step, Err: err} }

func (d *deployer) run(ctx context.Context) error {
	d.uefi = d.Firmware == "uefi" || d.Firmware == "both"
	d.bios = d.Firmware == "bios" || d.Firmware == "both"
	l := d.Layout
	if l == nil || l.BootStorage == nil || l.Root == nil || d.Payload == nil {
		return d.fail("check", errors.New("a layout that internal/layout accepted and a verified payload are required"))
	}
	if d.uefi && (l.ESP == nil || d.ESPDir == "") {
		return d.fail("check", errors.New("an ESP and its mount directory are required for uefi"))
	}
	if d.bios && (l.BIOSBoot == nil || d.Disk == nil || l.BootFS == nil) {
		return d.fail("check", errors.New("a BIOS Boot Partition, the disk and boot storage's FAT identity are required for bios"))
	}
	d.bs = l.BootStorage.UUID
	d.espFiles = map[string]string{}
	d.bsFiles = map[string]string{}

	// Everything that can fail without writing is checked first.
	tools := []string{}
	if d.bios {
		tools = append(tools, "grub-mkimage")
	}
	for _, t := range tools {
		if _, err := d.LookPath(t); err != nil {
			return d.fail("check", fmt.Errorf("%s is not installed (package grub-common); it builds GRUB's core image", t))
		}
	}
	machineID, err := d.machineID()
	if err != nil {
		return d.fail("check", err)
	}
	for _, c := range []string{"manifest-a", "manifest-b"} {
		if _, err := os.Stat(filepath.Join(d.BootStorageDir, "vintner", "state", c)); err == nil {
			return d.fail("check", errors.New("boot storage already holds a vintner manifest: this is an installed system"))
		}
	}
	if d.uefi {
		if err := d.checkESP(); err != nil {
			return d.fail("check", err)
		}
	}
	var core []byte
	if d.bios {
		fmt.Fprintln(d.Log, "building GRUB's core image")
		if core, err = BuildCore(ctx, d.Runner, d.Payload, l.BootFS.UUID()); err != nil {
			return d.fail("core.img", err)
		}
		if int64(len(core)) > l.BIOSBoot.Size(512) {
			return d.fail("core.img", fmt.Errorf("core.img is %d bytes and the BIOS Boot Partition %d", len(core), l.BIOSBoot.Size(512)))
		}
	}
	version, err := os.ReadFile(d.Payload.Path(payload.RecoveryVer))
	if err != nil {
		return d.fail("check", err)
	}
	recVersion := strings.TrimSpace(string(version))
	if recVersion == "" {
		return d.fail("check", errors.New("the payload's recovery version is empty"))
	}

	// Boot storage first, then the ESP, then the BIOS boot sector.
	if err := d.bootStorage(ctx, recVersion); err != nil {
		return err
	}
	if d.bios {
		if err := d.grubFiles(); err != nil {
			return err
		}
	}
	if err := d.writeManifest(machineID, recVersion); err != nil {
		return err
	}
	if d.uefi {
		if err := d.deployESP(); err != nil {
			return err
		}
	}
	if d.bios {
		if d.emb, err = EmbedGRUB(d.Disk, *l.BIOSBoot, mustRead(d.Payload.Path(payload.GrubDir+"/boot.img")), core); err != nil {
			return d.fail("BIOS boot code", err)
		}
		d.rec("wrote GRUB boot code to the boot sector and core.img to the BIOS Boot Partition (partition %d)", l.BIOSBoot.Number)
	}
	return d.verify()
}

func mustRead(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

func (d *deployer) machineID() (string, error) {
	b, err := os.ReadFile(filepath.Join(d.TargetDir, "etc", "machine-id"))
	if err != nil {
		return "", fmt.Errorf("the target has no /etc/machine-id (run systemd-machine-id-setup in it first): %w", err)
	}
	id := strings.TrimSpace(string(b))
	if !machineIDRe.MatchString(id) {
		return "", fmt.Errorf("the target's /etc/machine-id %q is not 32 lower-case hex digits", id)
	}
	return id, nil
}

// checkESP refuses a loader.conf that is not ours: there is one per ESP, and
// another system's must not be replaced (threat model, shared namespace).
func (d *deployer) checkESP() error {
	want, err := os.ReadFile(d.Payload.Path(payload.LoaderConf))
	if err != nil {
		return err
	}
	have, err := os.ReadFile(filepath.Join(d.ESPDir, "loader", "loader.conf"))
	if err == nil && !bytes.Equal(have, want) {
		return errors.New("the ESP has a /loader/loader.conf that is not vintner's: another boot manager owns it")
	}
	return nil
}

// otherSystemOnESP reports whether \EFI holds a directory that is neither the
// removable path, vintner's, nor Debian's stock loader.
func otherSystemOnESP(esp string) (bool, error) {
	ents, err := os.ReadDir(filepath.Join(esp, "EFI"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.IsDir() && !ownESPDirs[strings.ToLower(e.Name())] {
			return true, nil
		}
	}
	return false, nil
}

func (d *deployer) mkdir(base string, rel ...string) error {
	for _, r := range rel {
		p := filepath.Join(base, filepath.FromSlash(r))
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
		d.rec("created %s", p)
	}
	return nil
}

func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// putBS writes a file on boot storage and remembers its hash for the check.
func (d *deployer) putBS(rel string, data []byte) error {
	p := filepath.Join(d.BootStorageDir, filepath.FromSlash(rel))
	if err := writeFileSync(p, data); err != nil {
		return err
	}
	d.bsFiles[rel] = sum(data)
	d.rec("wrote %s on boot storage", rel)
	return nil
}

func (d *deployer) bootStorage(ctx context.Context, recVersion string) error {
	dirs := []string{"vintner", "vintner/state"}
	if d.uefi {
		dirs = append(dirs, "EFI", "EFI/Linux")
	}
	if d.bios {
		dirs = append(dirs, "grub", "vintner/bios", "vintner/bios/recovery-a", "vintner/bios/recovery-b")
	}
	if err := d.mkdir(d.BootStorageDir, dirs...); err != nil {
		return d.fail("boot storage", err)
	}
	kern, err := os.ReadFile(d.Payload.Path(payload.RecoveryKern))
	if err != nil {
		return d.fail("recovery", err)
	}
	initrd, err := os.ReadFile(d.Payload.Path(payload.RecoveryInit))
	if err != nil {
		return d.fail("recovery", err)
	}
	for _, slot := range []string{"a", "b"} {
		cmdline := d.recoveryCmdline(slot)
		if d.bios {
			dir := "vintner/bios/recovery-" + slot
			slotCfg := "set vintner_cmdline=" + publish.GrubQuote(cmdline) + "\n"
			if err := d.putBS(dir+"/vmlinuz", kern); err != nil {
				return d.fail("recovery", err)
			}
			if err := d.putBS(dir+"/initrd.img", initrd); err != nil {
				return d.fail("recovery", err)
			}
			if err := d.putBS(dir+"/slot.cfg", []byte(slotCfg)); err != nil {
				return d.fail("recovery", err)
			}
		}
		if d.uefi {
			if err := d.recoveryUKI(ctx, slot, cmdline, recVersion); err != nil {
				return d.fail("recovery", err)
			}
		}
	}
	return nil
}

func (d *deployer) recoveryCmdline(slot string) string {
	return RecoveryCmdline + " vintner.recovery=" + slot + " vintner.bootstorage=PARTUUID=" + d.bs
}

// recoveryUKIName is the file name of a recovery image on boot storage: A is
// counted (and re-arms itself), B never carries a counter (ADR-0003).
func (d *deployer) recoveryUKIName(slot string) string {
	if slot == "a" {
		return fmt.Sprintf("vintner-recovery-a+%d-0.efi", d.Tries)
	}
	return "vintner-recovery-b.efi"
}

func (d *deployer) recoveryUKI(ctx context.Context, slot, cmdline, version string) error {
	tmp, err := os.MkdirTemp("", "vintner-recovery")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	osrel := filepath.Join(tmp, "osrel")
	content := fmt.Sprintf("ID=debian\nIMAGE_ID=vintner-recovery-%s\nIMAGE_VERSION=%s\nPRETTY_NAME=\"vintner recovery %s (%s)\"\n",
		slot, version, strings.ToUpper(slot), version)
	if err := os.WriteFile(osrel, []byte(content), 0o600); err != nil {
		return err
	}
	out := filepath.Join(tmp, "uki.efi")
	if err := d.UKI.Build(ctx, publish.UKIRequest{
		LinuxPath:     d.Payload.Path(payload.RecoveryKern),
		InitrdPath:    d.Payload.Path(payload.RecoveryInit),
		StubPath:      d.Payload.Path(payload.Stub),
		OSReleasePath: osrel,
		Cmdline:       cmdline,
		OutputPath:    out,
	}); err != nil {
		return err
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	return d.putBS("EFI/Linux/"+d.recoveryUKIName(slot), b)
}

// grubFiles writes grub.cfg and the initial grubenv on boot storage.
func (d *deployer) grubFiles() error {
	cfg, err := os.ReadFile(d.Payload.Path(payload.GrubCfg))
	if err != nil {
		return d.fail("grub", err)
	}
	if err := d.putBS("grub/grub.cfg", cfg); err != nil {
		return d.fail("grub", err)
	}
	env := &grubenv.Env{}
	env.Set("VINTNER_T_RA", strings.Repeat("x", d.Tries))
	if err := grubenv.Create(filepath.Join(d.BootStorageDir, "grub", "grubenv"), env); err != nil {
		return d.fail("grub", err)
	}
	d.rec("wrote grub/grubenv on boot storage (recovery A has %d tries)", d.Tries)
	return nil
}

func (d *deployer) writeManifest(machineID, recVersion string) error {
	id := make([]byte, 16)
	if _, err := io.ReadFull(d.Rand, id); err != nil {
		return d.fail("manifest", err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	hx := hex.EncodeToString(id)
	instID := hx[0:8] + "-" + hx[8:12] + "-" + hx[12:16] + "-" + hx[16:20] + "-" + hx[20:]

	m := &manifest.Manifest{
		Format:         1,
		InstallationID: instID,
		MachineID:      machineID,
		Firmware:       d.Firmware,
		SecureBootTier: "none",
		Root:           manifest.Root{PartUUID: d.Layout.Root.UUID, UUID: d.Layout.RootUUID},
		BootStorage:    manifest.BootStorage{PartUUID: d.bs},
		Tries:          d.Tries,
		Retain:         d.Retain,
		NextGeneration: 1,
		Images:         []manifest.Image{},
		Recovery:       []manifest.Recovery{},
	}
	if d.Layout.ESP != nil && d.uefi {
		m.ESP = &manifest.ESP{PartUUID: d.Layout.ESP.UUID}
	}
	for _, slot := range []string{"a", "b"} {
		r := manifest.Recovery{Slot: slot, Version: recVersion}
		if d.uefi {
			name := "EFI/Linux/" + d.recoveryUKIName(slot)
			b, err := os.ReadFile(filepath.Join(d.BootStorageDir, filepath.FromSlash(name)))
			if err != nil {
				return d.fail("manifest", err)
			}
			r.UEFI = &manifest.UEFIFile{Entry: "vintner-recovery-" + slot, SHA256: sum(b), Size: len(b)}
		}
		if d.bios {
			dir := "vintner/bios/recovery-" + slot
			r.BIOS = &manifest.BIOSFiles{Dir: "/" + dir, Files: manifest.BIOSFilesMap{
				Vmlinuz:   d.bsFiles[dir+"/vmlinuz"],
				InitrdImg: d.bsFiles[dir+"/initrd.img"],
				SlotCfg:   d.bsFiles[dir+"/slot.cfg"],
			}}
		}
		m.Recovery = append(m.Recovery, r)
	}
	m.Seq = 1
	if err := m.Validate(); err != nil {
		return d.fail("manifest", err)
	}
	// Save writes seq+1 to the copy that is not current, so seq 1 is what lands.
	m.Seq = 0
	stateDir := filepath.Join(d.BootStorageDir, "vintner", "state")
	if err := manifest.Save(stateDir, m); err != nil {
		return d.fail("manifest", err)
	}
	d.rec("wrote the manifest (installation %s) on boot storage", instID)
	m.Seq = 1
	d.m = m
	return nil
}

func (d *deployer) deployESP() error {
	copyPayload := func(rel, payloadRel string) error {
		b, err := os.ReadFile(d.Payload.Path(payloadRel))
		if err != nil {
			return err
		}
		p := filepath.Join(d.ESPDir, filepath.FromSlash(rel))
		if err := writeFileSync(p, b); err != nil {
			return err
		}
		d.espFiles[rel] = payloadRel
		d.rec("wrote %s on the ESP", rel)
		return nil
	}
	if err := d.mkdir(d.ESPDir, "EFI/vintner", "loader"); err != nil {
		return d.fail("ESP", err)
	}
	if err := copyPayload("EFI/vintner/systemd-bootx64.efi", payload.SystemdBoot); err != nil {
		return d.fail("ESP", err)
	}
	if err := copyPayload("loader/loader.conf", payload.LoaderConf); err != nil {
		return d.fail("ESP", err)
	}
	other, err := otherSystemOnESP(d.ESPDir)
	if err != nil {
		return d.fail("ESP", err)
	}
	if other {
		fmt.Fprintln(d.Log, "  another operating system uses the ESP: the removable path EFI/BOOT/BOOTX64.EFI is left alone")
		return nil
	}
	if err := d.mkdir(d.ESPDir, "EFI/BOOT"); err != nil {
		return d.fail("ESP", err)
	}
	if err := copyPayload("EFI/BOOT/BOOTX64.EFI", payload.SystemdBoot); err != nil {
		return d.fail("ESP", err)
	}
	return nil
}

// verify reads everything back: the ESP files against the payload, boot
// storage against the hashes written, the manifest through manifest.Load, and
// the BIOS boot code from the disk.
func (d *deployer) verify() error {
	for rel, pr := range d.espFiles {
		got, err := os.ReadFile(filepath.Join(d.ESPDir, filepath.FromSlash(rel)))
		if err != nil {
			return d.fail("verify", err)
		}
		if want := d.Payload.Files[pr]; sum(got) != want {
			return d.fail("verify", fmt.Errorf("%s on the ESP does not match the payload %s", rel, pr))
		}
	}
	for rel, want := range d.bsFiles {
		got, err := os.ReadFile(filepath.Join(d.BootStorageDir, filepath.FromSlash(rel)))
		if err != nil {
			return d.fail("verify", err)
		}
		if sum(got) != want {
			return d.fail("verify", fmt.Errorf("%s on boot storage does not read back as written", rel))
		}
	}
	m, err := manifest.Load(filepath.Join(d.BootStorageDir, "vintner", "state"))
	if err != nil {
		return d.fail("verify", err)
	}
	if m.InstallationID != d.m.InstallationID || m.Seq != 1 {
		return d.fail("verify", errors.New("the manifest does not read back as written"))
	}
	if d.bios {
		env, err := grubenv.Read(filepath.Join(d.BootStorageDir, "grub", "grubenv"))
		if err != nil {
			return d.fail("verify", err)
		}
		if v, _ := env.Get("VINTNER_T_RA"); v != strings.Repeat("x", d.Tries) {
			return d.fail("verify", errors.New("grubenv does not read back as written"))
		}
		if err := VerifyEmbedded(d.Disk, d.emb); err != nil {
			return d.fail("verify", err)
		}
	}
	return nil
}
