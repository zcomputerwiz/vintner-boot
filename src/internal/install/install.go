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

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
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
	EFIVarDir      string // efivarfs for the NVRAM boot entry (default efivar.DefaultDir)

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
	// Resumable is set when generation 1 is published and a later step failed: vintner install --resume continues.
	Resumable bool
	Step      string
	Err       error
	Changed   []string
}

func (e *Error) Error() string { return "install: " + e.Step + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

var (
	machineIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// Directories of the ESP that are not another operating system's: the removable
	// path, vintner's own, and Debian's stock loader, which the installer removes.
	ownESPDirs = map[string]bool{"boot": true, "vintner": true, "debian": true}
)

// Deployment is a staged installation. Stage writes everything that does not
// change how the machine boots; Cutover writes what does, after the caller has
// published the first generation; Verify reads it all back.
type Deployment struct{ d *deployer }

// Stage checks the layout and deploys vintner's files on boot storage and the
// ESP, without taking over booting: the loader in \EFI\vintner is not what the
// firmware starts yet, and the BIOS boot sector is not touched.
func Stage(ctx context.Context, o Options) (*Deployment, error) {
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
	if err := d.stage(ctx); err != nil {
		return nil, d.wrap(err)
	}
	return &Deployment{d}, nil
}

// Cutover takes booting over: \EFI\BOOT\BOOTX64.EFI (when it is safe) and, for
// BIOS, GRUB's boot code in the BIOS Boot Partition and the boot sector.
func (p *Deployment) Cutover() error { return p.d.wrap(p.d.cutover()) }

// Verify reads everything back.
func (p *Deployment) Verify() error { return p.d.wrap(p.d.verify()) }

// Result says what was changed so far.
func (p *Deployment) Result() *Result { return &Result{Changed: p.d.changed, Manifest: p.d.m} }

// Manifest is the manifest Stage wrote.
func (p *Deployment) Manifest() *manifest.Manifest { return p.d.m }

// Changed lists what has been changed so far, for a failure report that comes
// from outside this package.
func (p *Deployment) Changed() []string { return p.d.changed }

// Deploy stages, takes over and verifies in one go, without publishing an
// image: the machine then boots recovery A (the tests and a manual smoke test use it).
func Deploy(ctx context.Context, o Options) (*Result, error) {
	p, err := Stage(ctx, o)
	if err != nil {
		return nil, err
	}
	if err := p.Cutover(); err != nil {
		return nil, err
	}
	if err := p.Verify(); err != nil {
		return nil, err
	}
	return p.Result(), nil
}

type deployer struct {
	Options
	changed            []string
	m                  *manifest.Manifest
	emb                *Embedded
	tookUEFI, tookBIOS bool // cutover wrote what makes the firmware start vintner
	tookNVRAM          bool // the NVRAM entry for vintner's loader is first in BootOrder
	nvramEntry         int
	nvramDP            []byte
	resumed            bool // Resume: boot storage already holds generation 1
	core               []byte
	bootImg            []byte

	uefi, bios bool
	bs         string // PARTUUID of boot storage
	espUUID    string
	espFiles   map[string]string // path under the ESP to the payload file it must equal
	bsFiles    map[string]string // path under boot storage to the sha256 it must have
}

// wrap attaches what was changed so far to a failed step.
func (d *deployer) wrap(err error) error {
	var se *Error
	if err != nil && errors.As(err, &se) {
		se.Changed = append([]string(nil), d.changed...)
	}
	return err
}

func (d *deployer) rec(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	d.changed = append(d.changed, s)
	fmt.Fprintln(d.Log, "  "+s)
}

func (d *deployer) fail(step string, err error) error { return &Error{Step: step, Err: err} }

func (d *deployer) stage(ctx context.Context) error {
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
	if d.bios {
		if _, err := d.LookPath("grub-mkimage"); err != nil {
			return d.fail("check", errors.New("grub-mkimage is not installed (package grub-common); it builds GRUB's core image"))
		}
	}
	machineID, err := d.machineID()
	if err != nil {
		return d.fail("check", err)
	}
	if err := d.checkNoInstallation(); err != nil {
		return d.fail("check", err)
	}
	if err := checkWritable(d.BootStorageDir); err != nil {
		return d.fail("check", fmt.Errorf("boot storage: %w", err))
	}
	if d.bios {
		d.bootImg, err = os.ReadFile(d.Payload.Path(payload.GrubDir + "/boot.img"))
		if err != nil || len(d.bootImg) != 512 {
			return d.fail("check", fmt.Errorf("the payload's boot.img is not a 512-byte boot sector (%v)", err))
		}
	}
	if d.uefi {
		if err := d.checkESP(); err != nil {
			return d.fail("check", err)
		}
		if err := checkWritable(d.ESPDir); err != nil {
			return d.fail("check", fmt.Errorf("the ESP: %w", err))
		}
	}
	if d.bios {
		fmt.Fprintln(d.Log, "building GRUB's core image")
		if d.core, err = BuildCore(ctx, d.Runner, d.Payload, l.BootFS.UUID()); err != nil {
			return d.fail("core.img", err)
		}
		if int64(len(d.core)) > l.BIOSBoot.Size(512) {
			return d.fail("core.img", fmt.Errorf("core.img is %d bytes and the BIOS Boot Partition %d", len(d.core), l.BIOSBoot.Size(512)))
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

	// Boot storage first, then the ESP's own files.
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
		if err := d.stageESP(); err != nil {
			return err
		}
	}
	return nil
}

func (d *deployer) cutover() error {
	if d.uefi {
		if err := d.cutoverUEFI(); err != nil {
			return err
		}
	}
	if d.bios {
		l := d.Layout
		d.rec("writing core.img to the BIOS Boot Partition (partition %d) and GRUB boot code to the boot sector", l.BIOSBoot.Number)
		emb, err := EmbedGRUB(d.Disk, *l.BIOSBoot, d.bootImg, d.core)
		if err != nil {
			return d.fail("BIOS boot code", err)
		}
		d.emb = emb
		d.tookBIOS = true
		d.rec("wrote them, flushed")
	}
	return nil
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
	if err := f.Close(); err != nil {
		return err
	}
	// The new directory entry must be durable too, or the synced data is unreachable.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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
	grubenvPath := filepath.Join(d.BootStorageDir, "grub", "grubenv")
	if err := os.Remove(grubenvPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return d.fail("grub", err)
	}
	if err := grubenv.Create(grubenvPath, env); err != nil {
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
	saved, err := manifest.Load(stateDir)
	if err != nil {
		return d.fail("manifest", err)
	}
	d.rec("wrote the manifest (installation %s, seq %d) on boot storage", instID, saved.Seq)
	m.Seq = saved.Seq
	d.m = m
	return nil
}

func (d *deployer) copyToESP(rel, payloadRel string) error {
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

// stageESP writes vintner's own files. The firmware does not start them until
// cutoverESP (or an NVRAM entry) points at them.
func (d *deployer) stageESP() error {
	if err := d.mkdir(d.ESPDir, "EFI/vintner", "loader"); err != nil {
		return d.fail("ESP", err)
	}
	if err := d.copyToESP("EFI/vintner/systemd-bootx64.efi", payload.SystemdBoot); err != nil {
		return d.fail("ESP", err)
	}
	if err := d.copyToESP("loader/loader.conf", payload.LoaderConf); err != nil {
		return d.fail("ESP", err)
	}
	return nil
}

// cutoverESP puts the loader in the removable path, where firmware without a
// boot entry for vintner looks, unless another system could be using it.
func (d *deployer) cutoverESP() error {
	other, err := otherSystemOnESP(d.ESPDir)
	if err != nil {
		return d.fail("ESP", err)
	}
	if other {
		fmt.Fprintln(d.Log, "  another operating system uses the ESP: the removable path EFI/BOOT/BOOTX64.EFI is left alone")
		return nil
	}
	ok, err := d.removableOverwritable()
	if err != nil {
		return d.fail("ESP", err)
	}
	if !ok {
		fmt.Fprintln(d.Log, "  EFI/BOOT/BOOTX64.EFI holds a loader that is not vintner's or Debian's: left alone")
		return nil
	}
	if err := d.mkdir(d.ESPDir, "EFI/BOOT"); err != nil {
		return d.fail("ESP", err)
	}
	if err := d.copyToESP("EFI/BOOT/BOOTX64.EFI", payload.SystemdBoot); err != nil {
		return d.fail("ESP", err)
	}
	d.tookUEFI = true
	return nil
}

// cutoverUEFI is the UEFI half of the takeover: the NVRAM entry first (it names
// vintner's own file), then the removable path where that is safe.
func (d *deployer) cutoverUEFI() error {
	if err := d.cutoverNVRAM(); err != nil {
		return err
	}
	return d.cutoverESP()
}

// NVRAMDescription is the description of the boot entry vintner creates.
const NVRAMDescription = "Vintner Boot"

// nvramPath is the file the boot entry starts: vintner's own loader, never the
// removable path, so the entry works whatever the removable path holds.
const nvramPath = `\EFI\vintner\systemd-bootx64.efi`

// efiVarDir is the efivarfs directory to write the entry to. It is empty when
// the running system did not start through UEFI (a BIOS-booted installer, a
// container): there is no NVRAM to write.
func (d *deployer) efiVarDir() string {
	dir := d.EFIVarDir
	if dir == "" {
		dir = efivar.DefaultDir
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return dir
}

// cutoverNVRAM creates the Boot#### entry for vintner's loader and puts it first
// in BootOrder. The entry only names a file on the ESP, so it is written even
// when another system uses the removable path (R13). When the running system
// has no efivarfs the entry cannot be written; that is logged, and TookOver
// then depends on the removable path alone.
func (d *deployer) cutoverNVRAM() error {
	dir := d.efiVarDir()
	if dir == "" {
		d.rec("no UEFI variables here (the installer did not start through UEFI): no NVRAM boot entry is created; run vintner install --resume from the installed system, or rely on EFI/BOOT/BOOTX64.EFI")
		return nil
	}
	l := d.Layout
	if l.ESP == nil {
		return d.fail("NVRAM", errors.New("the layout has no ESP"))
	}
	p := *l.ESP
	dp, err := efivar.DevicePath(efivar.PartitionRef{
		Number: uint32(p.Number), FirstLBA: p.FirstLBA, Sectors: p.LastLBA - p.FirstLBA + 1, Signature: p.UUID,
	}, nvramPath)
	if err != nil {
		return d.fail("NVRAM", err)
	}
	num, created, err := efivar.SetFirstBootEntry(dir, NVRAMDescription, dp)
	if err != nil {
		return d.fail("NVRAM", err)
	}
	d.nvramEntry, d.nvramDP = int(num), dp
	if created {
		d.rec("created UEFI boot entry Boot%04X %q for %s and put it first in BootOrder", num, NVRAMDescription, nvramPath)
	} else {
		d.rec("UEFI boot entry Boot%04X %q is first in BootOrder", num, NVRAMDescription)
	}
	d.tookNVRAM = true
	return nil
}

// verifyNVRAM reads the entry and BootOrder back.
func (d *deployer) verifyNVRAM() error {
	if !d.tookNVRAM {
		return nil
	}
	dir := d.efiVarDir()
	order, err := efivar.BootOrder(dir)
	if err != nil {
		return d.fail("verify", err)
	}
	if len(order) == 0 || int(order[0]) != d.nvramEntry {
		return d.fail("verify", fmt.Errorf("BootOrder %v does not start with Boot%04X", order, d.nvramEntry))
	}
	entries, err := efivar.BootEntries(dir)
	if err != nil {
		return d.fail("verify", err)
	}
	for _, e := range entries {
		if int(e.Number) == d.nvramEntry && e.Description == NVRAMDescription && bytes.Equal(e.DevicePath, d.nvramDP) {
			return nil
		}
	}
	return d.fail("verify", fmt.Errorf("Boot%04X does not read back as written", d.nvramEntry))
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
	if m.InstallationID != d.m.InstallationID || m.Seq < d.m.Seq { // publishing generation 1 moves the manifest on
		return d.fail("verify", errors.New("the manifest does not read back as written"))
	}
	if err := d.verifyNVRAM(); err != nil {
		return err
	}
	if d.bios {
		env, err := grubenv.Read(filepath.Join(d.BootStorageDir, "grub", "grubenv"))
		if err != nil {
			return d.fail("verify", err)
		}
		if v, _ := env.Get("VINTNER_T_RA"); v == "" || (!d.resumed && v != strings.Repeat("x", d.Tries)) {
			return d.fail("verify", errors.New("grubenv does not read back as written"))
		}
		if err := VerifyEmbedded(d.Disk, d.emb); err != nil {
			return d.fail("verify", err)
		}
	}
	return nil
}

// checkNoInstallation refuses a boot storage that holds an installed system: a
// manifest with a published image, or one that cannot be read. A manifest that
// publishes nothing is what an earlier deployment left, and is replaced, so
// that a run that failed half-way can be run again.
func (d *deployer) checkNoInstallation() error {
	stateDir := filepath.Join(d.BootStorageDir, "vintner", "state")
	found := false
	for _, c := range []string{"manifest-a", "manifest-b"} {
		if _, err := os.Stat(filepath.Join(stateDir, c)); err == nil {
			found = true
		}
	}
	if !found {
		return nil
	}
	m, err := manifest.Load(stateDir)
	if err != nil {
		return fmt.Errorf("boot storage holds a vintner manifest that cannot be read (%v): not replacing it", err)
	}
	if len(m.Images) > 0 {
		return fmt.Errorf("boot storage already holds an installation with %d published image(s): this is an installed system", len(m.Images))
	}
	fmt.Fprintln(d.Log, "  an earlier deployment that published nothing is replaced")
	return nil
}

// checkWritable proves dir accepts a new file, before anything is written.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".vintner-write-check-")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// removableOverwritable says whether EFI/BOOT/BOOTX64.EFI may be replaced: it is
// absent, it already is vintner's loader, or Debian's stock loader is installed
// (its removable copy, which the installer removes anyway). Another loader that
// lives only in the removable path is left alone.
func (d *deployer) removableOverwritable() (bool, error) {
	cur, err := os.ReadFile(filepath.Join(d.ESPDir, "EFI", "BOOT", "BOOTX64.EFI"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if sum(cur) == d.Payload.Files[payload.SystemdBoot] {
		return true, nil
	}
	ents, err := os.ReadDir(filepath.Join(d.ESPDir, "EFI"))
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.IsDir() && strings.EqualFold(e.Name(), "debian") {
			return true, nil
		}
	}
	return false, nil
}

// TookOver reports whether booting now starts vintner on every firmware that was
// asked for: the BIOS boot code was written, and on UEFI the removable path
// holds vintner's loader. When it does not (another system uses the ESP, or the
// removable path holds another loader), the stock loaders must stay: nothing else
// makes the firmware start vintner until the NVRAM entry of WO-0010 3.3 exists.
func (p *Deployment) TookOver() (bool, string) {
	d := p.d
	if d.uefi && !d.tookUEFI && !d.tookNVRAM {
		return false, "the firmware does not start vintner on UEFI: EFI/BOOT/BOOTX64.EFI was left alone (another system uses the ESP or the removable path holds another loader) and no NVRAM boot entry could be written (no UEFI variables here)"
	}
	if d.bios && !d.tookBIOS {
		return false, "the BIOS boot code was not written"
	}
	return true, ""
}
