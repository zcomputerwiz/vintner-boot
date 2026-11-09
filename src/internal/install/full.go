// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

// StockLoaderPackages are the Debian boot loader packages whose maintainer
// scripts write to the ESP, the boot sector or NVRAM and so cannot stay beside
// vintner (architecture §5, ci/lifecycle/packages). vintner-platform conflicts
// with the same set, plus the signed GRUB package that apt removes with them.
var StockLoaderPackages = []string{
	"grub-cloud-amd64", "grub-pc", "grub-efi-amd64", "grub-efi-amd64-signed",
	"grub-efi-ia32", "grub-coreboot", "grub-xen", "grub-ieee1275",
	"shim-signed", "systemd-boot", "refind",
}

// FullOptions are the inputs of the whole installation.
type FullOptions struct {
	Options
	KernelABI   string // default: the newest kernel in <target>/boot
	CmdlineFile string // default: <target>/etc/kernel/cmdline
	// Publish carries test doubles and overrides for the publisher; install sets
	// the kernel, directories and command line itself.
	Publish publish.Options
}

// Install does the whole of WO-0010 3.2 in the order of architecture §5:
//
//  1. deploy vintner's files (Stage), without changing how the machine boots;
//  2. publish generation 1 with an explicit command line and root identity;
//  3. take booting over (Cutover) and read it all back;
//  4. remove \EFI\debian and purge the stock loader packages, but only when
//     booting really starts vintner now;
//  5. verify again.
//
// A failure before step 3 leaves the stock boot path as it was. A failure
// after step 2 is resumable: the Error says what was changed and Resume
// continues from step 3.
func Install(ctx context.Context, f FullOptions) (*Result, error) {
	f.Options.fill()
	dep, err := Stage(ctx, f.Options)
	if err != nil {
		return nil, err
	}
	fail := func(step string, err error) (*Result, error) {
		return nil, &Error{Step: step, Err: err, Changed: append([]string(nil), dep.Changed()...)}
	}

	abi := f.KernelABI
	if abi == "" {
		if abi, err = NewestKernel(f.TargetDir); err != nil {
			return fail("kernel", err)
		}
	}
	cmdlineFile := f.CmdlineFile
	if cmdlineFile == "" {
		cmdlineFile = filepath.Join(f.TargetDir, "etc", "kernel", "cmdline")
	}
	product, err := os.ReadFile(cmdlineFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail("command line", err)
	}
	m := dep.Manifest()
	cmdline, err := Cmdline(m.Root.UUID, m.BootStorage.PartUUID, string(product))
	if err != nil {
		return fail("command line", fmt.Errorf("%s: %w", cmdlineFile, err))
	}

	fmt.Fprintf(f.Log, "publishing generation 1 (kernel %s)\n", abi)
	po := f.Publish
	po.KernelABI = abi
	po.BootStorageDir = f.BootStorageDir
	po.RootDir = f.TargetDir
	po.CmdlineContent = cmdline
	po.InitialCmdline = cmdline
	po.Stdout, po.Stderr = f.Log, f.Log
	if po.UKIBuilder == nil {
		po.UKIBuilder = f.UKI
	}
	if _, err := publish.Publish(ctx, po); err != nil {
		return fail("publish", err)
	}
	// Nothing may be taken over unless the manifest really holds generation 1.
	if err := checkGenerationOne(f.BootStorageDir); err != nil {
		return fail("publish", err)
	}
	return finish(ctx, f, dep)
}

func checkGenerationOne(bootStorageDir string) error {
	m, err := manifest.Load(filepath.Join(bootStorageDir, "vintner", "state"))
	if err != nil {
		return fmt.Errorf("the manifest cannot be read after publishing: %w", err)
	}
	if len(m.Images) != 1 || m.Images[0].Generation != 1 {
		return fmt.Errorf("the manifest does not hold exactly generation 1 after publishing (%d images)", len(m.Images))
	}
	return nil
}

// finish is steps 3 to 5, shared by Install and Resume. A failure in it is
// resumable: generation 1 is published.
func finish(ctx context.Context, f FullOptions, dep *Deployment) (*Result, error) {
	fail := func(step string, err error) (*Result, error) {
		var se *Error
		if errors.As(err, &se) {
			se.Resumable = true
			return nil, se
		}
		return nil, &Error{Step: step, Err: err, Changed: append([]string(nil), dep.Changed()...), Resumable: true}
	}
	if err := dep.Cutover(); err != nil {
		return fail("takeover", err)
	}
	if err := dep.Verify(); err != nil {
		return fail("verify", err)
	}

	if took, why := dep.TookOver(); !took {
		dep.d.rec("the stock loaders were left in place: %s", why)
	} else {
		removed, err := RemoveStock(ctx, f.Runner, f.TargetDir, f.ESPDir)
		for _, r := range removed {
			dep.d.rec("%s", r)
		}
		if err != nil {
			return fail("stock loaders", err)
		}
		if err := dep.Verify(); err != nil {
			return fail("verify", err)
		}
	}
	res := dep.Result()
	if err := checkGenerationOne(f.BootStorageDir); err != nil {
		return fail("verify", err)
	}
	loaded, _ := manifest.Load(filepath.Join(f.BootStorageDir, "vintner", "state"))
	res.Manifest = loaded
	return res, nil
}

// Resume continues an installation that published generation 1 and failed
// afterwards: it takes booting over, removes the stock loaders when that is
// safe, and verifies. It refuses a boot storage that does not hold exactly
// generation 1 of this layout's installation.
func Resume(ctx context.Context, f FullOptions) (*Result, error) {
	f.Options.fill()
	o := f.Options
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	d := &deployer{Options: o, resumed: true}
	d.espFiles = map[string]string{}
	d.bsFiles = map[string]string{}
	dep := &Deployment{d}
	step := func(err error) (*Result, error) { return nil, d.wrap(err) }

	d.uefi = o.Firmware == "uefi" || o.Firmware == "both"
	d.bios = o.Firmware == "bios" || o.Firmware == "both"
	l := o.Layout
	if l == nil || l.BootStorage == nil || l.Root == nil || o.Payload == nil {
		return step(d.fail("check", errors.New("a layout that internal/layout accepted and a verified payload are required")))
	}
	d.bs = l.BootStorage.UUID
	m, err := manifest.Load(filepath.Join(o.BootStorageDir, "vintner", "state"))
	if err != nil {
		return step(d.fail("check", fmt.Errorf("nothing to resume: %w", err)))
	}
	if len(m.Images) != 1 || m.Images[0].Generation != 1 {
		return step(d.fail("check", fmt.Errorf("only an installation with exactly generation 1 can be resumed (this one has %d images)", len(m.Images))))
	}
	if !strings.EqualFold(m.BootStorage.PartUUID, l.BootStorage.UUID) || !strings.EqualFold(m.Root.PartUUID, l.Root.UUID) || m.Firmware != o.Firmware {
		return step(d.fail("check", errors.New("the manifest on boot storage belongs to another layout or firmware")))
	}
	d.m = m
	if d.uefi {
		if l.ESP == nil || o.ESPDir == "" {
			return step(d.fail("check", errors.New("an ESP and its mount directory are required for uefi")))
		}
		if err := checkWritable(o.ESPDir); err != nil {
			return step(d.fail("check", fmt.Errorf("the ESP: %w", err)))
		}
		d.espFiles["EFI/vintner/systemd-bootx64.efi"] = payload.SystemdBoot
		d.espFiles["loader/loader.conf"] = payload.LoaderConf
	}
	if d.bios {
		if l.BIOSBoot == nil || o.Disk == nil || l.BootFS == nil {
			return step(d.fail("check", errors.New("a BIOS Boot Partition, the disk and boot storage's FAT identity are required for bios")))
		}
		if _, err := d.LookPath("grub-mkimage"); err != nil {
			return step(d.fail("check", errors.New("grub-mkimage is not installed (package grub-common); it builds GRUB's core image")))
		}
		if d.bootImg, err = os.ReadFile(o.Payload.Path(payload.GrubDir + "/boot.img")); err != nil || len(d.bootImg) != 512 {
			return step(d.fail("check", fmt.Errorf("the payload's boot.img is not a 512-byte boot sector (%v)", err)))
		}
		if d.core, err = BuildCore(ctx, o.Runner, o.Payload, l.BootFS.UUID()); err != nil {
			return step(d.fail("core.img", err))
		}
		if int64(len(d.core)) > l.BIOSBoot.Size(512) {
			return step(d.fail("core.img", errors.New("core.img does not fit in the BIOS Boot Partition")))
		}
	}
	fmt.Fprintln(f.Log, "resuming: generation 1 is published")
	return finish(ctx, f, dep)
}

// fill sets the defaults Stage sets, so that Install can use them before it.
func (o *Options) fill() {
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	if o.UKI == nil {
		o.UKI = publish.RealUKIBuilder{}
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
}

var kernelRe = regexp.MustCompile(`^vmlinuz-(.+)$`)

// NewestKernel returns the ABI of the newest kernel in <target>/boot that has
// an initramfs, comparing the version numbers in it.
func NewestKernel(target string) (string, error) {
	ents, err := os.ReadDir(filepath.Join(target, "boot"))
	if err != nil {
		return "", err
	}
	var abis []string
	for _, e := range ents {
		m := kernelRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(target, "boot", "initrd.img-"+m[1])); err != nil {
			continue
		}
		abis = append(abis, m[1])
	}
	if len(abis) == 0 {
		return "", fmt.Errorf("no kernel with an initramfs (vmlinuz-<abi> and initrd.img-<abi>) in %s/boot", target)
	}
	sort.Slice(abis, func(i, j int) bool { return versionLess(abis[i], abis[j]) })
	return abis[len(abis)-1], nil
}

// versionLess compares two kernel ABI strings by their digit runs, then by the
// text between them (6.12.9-amd64 < 6.12.10-amd64).
func versionLess(a, b string) bool {
	numRe := regexp.MustCompile(`\d+|\D+`)
	pa, pb := numRe.FindAllString(a, -1), numRe.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea == nil && eb == nil {
			if na != nb {
				return na < nb
			}
			continue
		}
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// Cmdline builds an image's kernel command line: the root and read-only mount
// the installer knows, the product's own options from /etc/kernel/cmdline
// (decision D-B), and the boot storage identity. The product's options must not
// name the root or the boot storage: those two belong to vintner.
func Cmdline(rootFSUUID, bootStoragePartUUID, product string) (string, error) {
	var opts []string
	for _, line := range strings.Split(product, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, f := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(f, "root=") || strings.HasPrefix(f, "vintner.bootstorage="):
				return "", fmt.Errorf("the option %q belongs to vintner and must not be in the product's command line", f)
			case f == "ro" || f == "rw":
				return "", fmt.Errorf("the option %q is set by vintner (the root is mounted read-only first)", f)
			}
			opts = append(opts, f)
		}
	}
	out := []string{"root=UUID=" + rootFSUUID, "ro"}
	out = append(out, opts...)
	out = append(out, "vintner.bootstorage=PARTUUID="+bootStoragePartUUID)
	return strings.Join(out, " "), nil
}

// RemoveStock removes Debian's stock loader: \EFI\debian on the ESP first,
// because the removal script of shim-signed runs grub-install on an
// EFI-booted system while it exists, then the stock loader packages that are
// installed, with --allow-remove-essential (grub-efi-amd64-signed is
// Protected: yes). It returns what it removed.
func RemoveStock(ctx context.Context, r Runner, target, espDir string) ([]string, error) {
	var removed []string
	if espDir != "" {
		ents, err := os.ReadDir(filepath.Join(espDir, "EFI"))
		if err == nil {
			for _, e := range ents {
				if e.IsDir() && strings.EqualFold(e.Name(), "debian") {
					if err := os.RemoveAll(filepath.Join(espDir, "EFI", e.Name())); err != nil {
						return removed, err
					}
					removed = append(removed, "removed EFI/"+e.Name()+" from the ESP")
				}
			}
		}
	}
	run := func(name string, args ...string) ([]byte, error) {
		if target != "" && target != "/" {
			args = append([]string{target, name}, args...)
			name = "chroot"
		}
		return r.Run(ctx, name, args...)
	}
	out, err := run("dpkg-query", append([]string{"-W", "-f=${db:Status-Abbrev} ${Package}\\n"}, StockLoaderPackages...)...)
	if err != nil {
		// dpkg-query exits 1 when some name is not a known package, and still lists the
		// others; any other failure (no dpkg-query, no chroot) is not "nothing installed".
		var ec interface{ ExitCode() int }
		if !errors.As(err, &ec) || ec.ExitCode() != 1 {
			return removed, fmt.Errorf("dpkg-query: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	var installed []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "ii" {
			installed = append(installed, f[1])
		}
	}
	if len(installed) == 0 {
		return removed, nil
	}
	args := append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "--allow-remove-essential", "purge"}, installed...)
	if o, err := run("env", args...); err != nil {
		return removed, fmt.Errorf("apt-get purge %s: %w: %s", strings.Join(installed, " "), err, strings.TrimSpace(string(o)))
	}
	removed = append(removed, "purged "+strings.Join(installed, ", "))
	return removed, nil
}
