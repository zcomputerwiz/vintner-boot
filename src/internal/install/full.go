// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
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
//  4. remove \EFI\debian and purge the stock loader packages;
//  5. verify again.
//
// A failure before step 3 leaves the stock boot path as it was. A failure
// later leaves vintner booting; the Error says what was changed.
func Install(ctx context.Context, f FullOptions) (*Result, error) {
	f.Options.fill()
	dep, err := Stage(ctx, f.Options)
	if err != nil {
		return nil, err
	}
	fail := func(step string, err error) (*Result, error) {
		e := &Error{Step: step, Err: err, Changed: append([]string(nil), dep.Changed()...)}
		return nil, e
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

	if err := dep.Cutover(); err != nil {
		return nil, err
	}
	if err := dep.Verify(); err != nil {
		return nil, err
	}

	removed, err := RemoveStock(ctx, f.Runner, f.TargetDir, f.ESPDir)
	for _, r := range removed {
		dep.d.rec("%s", r)
	}
	if err != nil {
		return fail("stock loaders", err)
	}
	if err := dep.Verify(); err != nil {
		return nil, err
	}
	res := dep.Result()
	loaded, err := manifest.Load(filepath.Join(f.BootStorageDir, "vintner", "state"))
	if err != nil || len(loaded.Images) != 1 || loaded.Images[0].Generation != 1 {
		return fail("verify", fmt.Errorf("the manifest does not hold generation 1 after publishing (%v)", err))
	}
	res.Manifest = loaded
	return res, nil
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
	if err != nil && len(out) == 0 {
		// dpkg-query exits 1 when none of the names is known: nothing to remove.
		return removed, nil
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
